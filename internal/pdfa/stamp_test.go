package pdfa

import (
	"bytes"
	"strings"
	"testing"
)

// minimalPDF builds a minimal but structurally valid PDF with a single catalog
// object and a simple xref table. It matches the pattern Chrome produces.
func minimalPDF(t *testing.T) []byte {
	t.Helper()

	// Object 1: catalog
	catalog := "1 0 obj\n<</Type /Catalog /Pages 2 0 R>>\nendobj\n"

	// Object 2: empty page tree (not strictly needed but keeps Size honest)
	pages := "2 0 obj\n<</Type /Pages /Kids [] /Count 0>>\nendobj\n"

	header := "%PDF-1.7\n"
	body := catalog + pages

	// Build xref
	off1 := len(header)
	off2 := off1 + len(catalog)

	xref := "xref\n0 3\n"
	xref += "0000000000 65535 f \r\n"
	xref += fmtXrefEntry(int64(off1))
	xref += fmtXrefEntry(int64(off2))

	startXRefOffset := len(header) + len(body)

	trailer := "trailer\n<</Size 3 /Root 1 0 R>>\n"
	trailer += "startxref\n"
	trailer += string([]byte(strings.Repeat("0", 10-len(itoa(startXRefOffset))) + itoa(startXRefOffset)))
	trailer += "\n%%EOF\n"

	// Re-compute correctly
	var buf bytes.Buffer
	buf.WriteString(header)
	buf.WriteString(body)
	xrefStart := buf.Len()
	buf.WriteString(xref)
	buf.WriteString("trailer\n<</Size 3 /Root 1 0 R>>\n")
	buf.WriteString("startxref\n")
	buf.WriteString(itoa(xrefStart))
	buf.WriteString("\n%%EOF\n")
	_ = trailer // replaced above

	return buf.Bytes()
}

func fmtXrefEntry(off int64) string {
	s := ""
	for i := 0; i < 10-len(itoa(int(off))); i++ {
		s += "0"
	}
	s += itoa(int(off)) + " 00000 n \r\n"
	return s
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	b := make([]byte, 0, 10)
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestStampAddsXMPMetadata(t *testing.T) {
	pdf := minimalPDF(t)
	stamped, err := Stamp(pdf)
	if err != nil {
		t.Fatalf("Stamp() error: %v", err)
	}
	if !bytes.Contains(stamped, []byte("pdfaid:part>2</pdfaid:part")) {
		t.Error("stamped PDF missing pdfaid:part=2 XMP marker")
	}
	if !bytes.Contains(stamped, []byte("pdfaid:conformance>B</pdfaid:conformance")) {
		t.Error("stamped PDF missing pdfaid:conformance=B XMP marker")
	}
}

func TestStampAddsOutputIntent(t *testing.T) {
	pdf := minimalPDF(t)
	stamped, err := Stamp(pdf)
	if err != nil {
		t.Fatalf("Stamp() error: %v", err)
	}
	if !bytes.Contains(stamped, []byte("/OutputIntents")) {
		t.Error("stamped PDF missing /OutputIntents")
	}
	if !bytes.Contains(stamped, []byte("GTS_PDFA1")) {
		t.Error("stamped PDF missing GTS_PDFA1 output intent type")
	}
	if !bytes.Contains(stamped, []byte("sRGB IEC61966-2.1")) {
		t.Error("stamped PDF missing sRGB output condition identifier")
	}
}

func TestStampAddsICCProfile(t *testing.T) {
	pdf := minimalPDF(t)
	stamped, err := Stamp(pdf)
	if err != nil {
		t.Fatalf("Stamp() error: %v", err)
	}
	// The sRGB ICC profile bytes must be present in the stamped output.
	if !bytes.Contains(stamped, sRGBProfile) {
		t.Error("stamped PDF does not contain the embedded sRGB ICC profile")
	}
}

func TestStampPreservesOriginal(t *testing.T) {
	pdf := minimalPDF(t)
	original := make([]byte, len(pdf))
	copy(original, pdf)

	stamped, err := Stamp(pdf)
	if err != nil {
		t.Fatalf("Stamp() error: %v", err)
	}
	if !bytes.HasPrefix(stamped, original) {
		t.Error("Stamp() did not preserve original PDF bytes as prefix")
	}
	if len(stamped) <= len(original) {
		t.Errorf("stamped size %d <= original size %d", len(stamped), len(original))
	}
}

func TestStampContainsValidXref(t *testing.T) {
	pdf := minimalPDF(t)
	stamped, err := Stamp(pdf)
	if err != nil {
		t.Fatalf("Stamp() error: %v", err)
	}

	// The new trailer must chain back to the original xref via /Prev.
	if !bytes.Contains(stamped, []byte("/Prev")) {
		t.Error("new trailer is missing /Prev for xref chaining")
	}
	// Must have a valid %%EOF at the very end.
	if !bytes.Contains(stamped[len(pdf):], []byte("%%EOF")) {
		t.Error("incremental update is missing end-of-file marker")
	}
}

func TestStampIdempotentStructure(t *testing.T) {
	// Stamping twice should not panic and should still contain valid markers.
	pdf := minimalPDF(t)
	once, err := Stamp(pdf)
	if err != nil {
		t.Fatalf("first Stamp() error: %v", err)
	}
	twice, err := Stamp(once)
	if err != nil {
		t.Fatalf("second Stamp() error: %v", err)
	}
	if !bytes.Contains(twice, []byte("pdfaid:part>2</pdfaid:part")) {
		t.Error("double-stamped PDF missing pdfaid markers")
	}
}
