// Package pdfa converts a regular PDF to PDF/A-2b via incremental update.
// It appends XMP conformance metadata and an sRGB output intent to the
// document catalog, leaving the original bytes untouched.
package pdfa

import (
	"bytes"
	_ "embed"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

//go:embed srgb.icc
var sRGBProfile []byte

// xmpMetadata is the XMP packet declaring PDF/A-2b conformance.
// The BOM (\xef\xbb\xbf) is required by the XMP spec.
const xmpMetadata = "<?xpacket begin=\"\xef\xbb\xbf\" id=\"W5M0MpCehiHzreSzNTczkc9d\"?>\n" +
	"<x:xmpmeta xmlns:x=\"adobe:ns:meta/\">\n" +
	"  <rdf:RDF xmlns:rdf=\"http://www.w3.org/1999/02/22-rdf-syntax-ns#\">\n" +
	"    <rdf:Description rdf:about=\"\"\n" +
	"        xmlns:pdfaid=\"http://www.aiim.org/pdfa/ns/id/\">\n" +
	"      <pdfaid:part>2</pdfaid:part>\n" +
	"      <pdfaid:conformance>B</pdfaid:conformance>\n" +
	"    </rdf:Description>\n" +
	"  </rdf:RDF>\n" +
	"</x:xmpmeta>\n" +
	"<?xpacket end=\"w\"?>"

var (
	reStartXRef = regexp.MustCompile(`startxref\s+(\d+)\s+%%EOF`)
	reDictSize  = regexp.MustCompile(`/Size\s+(\d+)`)
	reDictRoot  = regexp.MustCompile(`/Root\s+(\d+)\s+\d+\s+R`)
)

// Stamp adds PDF/A-2b conformance markers to pdf using a PDF incremental update.
// The original bytes are preserved; new objects and a new xref/trailer are appended.
// Works with Chrome/Chromium PDF output that uses traditional (non-stream) xref tables.
func Stamp(pdf []byte) ([]byte, error) {
	trailerDict, xrefStart, err := findTrailer(pdf)
	if err != nil {
		return nil, fmt.Errorf("pdfa: find trailer: %w", err)
	}

	size, err := matchInt(reDictSize, trailerDict)
	if err != nil {
		return nil, fmt.Errorf("pdfa: trailer /Size: %w", err)
	}

	rootNum, err := matchInt(reDictRoot, trailerDict)
	if err != nil {
		return nil, fmt.Errorf("pdfa: trailer /Root: %w", err)
	}

	xrefMap, err := parseXref(pdf, xrefStart)
	if err != nil {
		return nil, fmt.Errorf("pdfa: parse xref: %w", err)
	}

	catalogOffset, ok := xrefMap[rootNum]
	if !ok {
		return nil, fmt.Errorf("pdfa: catalog object %d not in xref", rootNum)
	}

	catalogBody, err := readObjectBody(pdf, catalogOffset, rootNum)
	if err != nil {
		return nil, fmt.Errorf("pdfa: read catalog: %w", err)
	}

	// New object numbers: xmpNum and iccNum are always consecutive,
	// allocated starting from the current /Size.
	xmpNum := size
	iccNum := size + 1
	newSize := size + 2

	baseOffset := int64(len(pdf))
	newOffsets := make(map[int]int64, 3)

	var update bytes.Buffer

	appendStream := func(num int, dict string, data []byte) {
		newOffsets[num] = baseOffset + int64(update.Len())
		fmt.Fprintf(&update, "%d 0 obj\n%s\nstream\n", num, dict)
		update.Write(data)
		// The newline before endstream is NOT counted in /Length per PDF spec.
		update.WriteString("\nendstream\nendobj\n")
	}

	appendDict := func(num int, content string) {
		newOffsets[num] = baseOffset + int64(update.Len())
		fmt.Fprintf(&update, "%d 0 obj\n%s\nendobj\n", num, content)
	}

	// XMP metadata stream
	xmpData := []byte(xmpMetadata)
	appendStream(xmpNum,
		fmt.Sprintf("<</Type /Metadata /Subtype /XML /Length %d>>", len(xmpData)),
		xmpData)

	// sRGB ICC profile stream (/N 3 = 3-channel RGB color space)
	appendStream(iccNum,
		fmt.Sprintf("<</Type /ICCBased /N 3 /Length %d>>", len(sRGBProfile)),
		sRGBProfile)

	// Updated catalog (same object number as original, new revision)
	appendDict(rootNum, patchCatalog(catalogBody, xmpNum, iccNum))

	// Write new xref section.
	// xmpNum and iccNum are consecutive; rootNum is separate — emit two subsections.
	xrefOffset := baseOffset + int64(update.Len())
	update.WriteString("xref\n")
	emitXrefSubsection(&update, rootNum, []int64{newOffsets[rootNum]})
	emitXrefSubsection(&update, xmpNum, []int64{newOffsets[xmpNum], newOffsets[iccNum]})

	// New trailer chains to previous via /Prev.
	fmt.Fprintf(&update, "trailer\n<</Size %d /Root %d 0 R /Prev %d>>\n",
		newSize, rootNum, xrefStart)
	fmt.Fprintf(&update, "startxref\n%d\n%%%%EOF\n", xrefOffset)

	return append(pdf, update.Bytes()...), nil
}

// patchCatalog removes any existing /Metadata and /OutputIntents from the catalog
// dict and adds new ones referencing xmpNum and iccNum.
func patchCatalog(body string, xmpNum, iccNum int) string {
	body = removeDictKey(body, "Metadata")
	body = removeDictKey(body, "OutputIntents")

	// Strip outer << >> to inject new entries.
	inner := strings.TrimSpace(body)
	inner = strings.TrimPrefix(inner, "<<")
	inner = strings.TrimSuffix(inner, ">>")
	inner = strings.TrimSpace(inner)

	outputIntent := fmt.Sprintf(
		"<</Type /OutputIntent /S /GTS_PDFA1 /OutputConditionIdentifier (sRGB IEC61966-2.1) /DestOutputProfile %d 0 R>>",
		iccNum)

	return fmt.Sprintf("<<%s\n/Metadata %d 0 R\n/OutputIntents [%s]\n>>",
		inner, xmpNum, outputIntent)
}

// removeDictKey removes a single-line /KEY ... entry from a PDF dict string.
var reRemoveKey = func(key string) *regexp.Regexp {
	return regexp.MustCompile(`(?m)^\s*/` + regexp.QuoteMeta(key) + `[^\n]*\n?`)
}

func removeDictKey(dict, key string) string {
	return reRemoveKey(key).ReplaceAllString(dict, "")
}

// emitXrefSubsection writes a traditional xref subsection starting at firstObj.
func emitXrefSubsection(buf *bytes.Buffer, firstObj int, offsets []int64) {
	fmt.Fprintf(buf, "%d %d\n", firstObj, len(offsets))
	for _, off := range offsets {
		// 20 bytes exactly: 10-digit offset + space + 5-digit gen + space + 'n' + \r\n
		fmt.Fprintf(buf, "%010d 00000 n \r\n", off)
	}
}

// findTrailer locates the last trailer dict and the startxref offset.
func findTrailer(pdf []byte) (dict string, xrefOffset int64, err error) {
	tail := pdf
	if len(tail) > 2048 {
		tail = pdf[len(pdf)-2048:]
	}

	m := reStartXRef.FindSubmatch(tail)
	if m == nil {
		return "", 0, fmt.Errorf("startxref not found")
	}
	xrefOffset, _ = strconv.ParseInt(string(m[1]), 10, 64)

	trailerIdx := bytes.LastIndex(pdf, []byte("trailer"))
	if trailerIdx < 0 {
		return "", 0, fmt.Errorf("trailer keyword not found")
	}

	after := pdf[trailerIdx+len("trailer"):]
	start := bytes.Index(after, []byte("<<"))
	if start < 0 {
		return "", 0, fmt.Errorf("trailer dict opening << not found")
	}

	end := matchingDictClose(after[start:])
	if end < 0 {
		return "", 0, fmt.Errorf("trailer dict closing >> not found")
	}

	return string(after[start : start+end+2]), xrefOffset, nil
}

// matchingDictClose returns the index of the >> that closes the << at data[0].
// Counts nesting depth to handle dicts-within-dicts.
func matchingDictClose(data []byte) int {
	depth := 0
	for i := 0; i < len(data)-1; i++ {
		switch {
		case data[i] == '<' && data[i+1] == '<':
			depth++
			i++
		case data[i] == '>' && data[i+1] == '>':
			depth--
			if depth == 0 {
				return i
			}
			i++
		}
	}
	return -1
}

// parseXref parses the traditional (non-stream) cross-reference table at offset.
// Returns a map of object number → byte offset for all in-use entries.
func parseXref(pdf []byte, offset int64) (map[int]int64, error) {
	if offset < 0 || offset >= int64(len(pdf)) {
		return nil, fmt.Errorf("xref offset %d out of file bounds", offset)
	}

	data := pdf[offset:]
	if !bytes.HasPrefix(data, []byte("xref")) {
		// Chrome 90+ can produce cross-reference streams — detect and report clearly.
		if bytes.HasPrefix(data, []byte("\r\nxref")) || bytes.HasPrefix(data, []byte("\r xref")) {
			// trimmed whitespace variant — normalise and retry
		} else {
			return nil, fmt.Errorf("expected 'xref' at offset %d (cross-reference streams not supported)", offset)
		}
	}

	result := make(map[int]int64)
	lines := strings.Split(string(data), "\n")
	i := 1 // skip "xref"

	for i < len(lines) {
		line := strings.TrimRight(lines[i], "\r ")
		if line == "trailer" || line == "" {
			break
		}

		var firstObj, count int
		if _, serr := fmt.Sscanf(line, "%d %d", &firstObj, &count); serr != nil {
			break
		}
		i++

		for j := 0; j < count && i < len(lines); j++ {
			entry := lines[i]
			i++
			if len(entry) < 18 {
				continue
			}
			off, perr := strconv.ParseInt(strings.TrimSpace(entry[:10]), 10, 64)
			if perr != nil {
				continue
			}
			// entry[17] is 'n' (in-use) or 'f' (free); may be at index 17 or 16 depending on spacing
			status := entry[17]
			if len(entry) > 17 && (entry[17] == 'n' || entry[17] == 'f') {
				status = entry[17]
			} else if len(entry) > 16 {
				status = entry[16]
			}
			if status == 'n' {
				result[firstObj+j] = off
			}
		}
	}

	return result, nil
}

// readObjectBody reads the body of object num at the given byte offset.
// Returns the text between "N 0 obj" and "endobj", trimmed.
func readObjectBody(pdf []byte, offset int64, num int) (string, error) {
	if offset < 0 || offset >= int64(len(pdf)) {
		return "", fmt.Errorf("object %d: offset %d out of bounds", num, offset)
	}

	data := string(pdf[offset:])
	marker := fmt.Sprintf("%d 0 obj", num)
	idx := strings.Index(data, marker)
	if idx < 0 {
		return "", fmt.Errorf("object %d: marker not found at offset %d", num, offset)
	}

	after := data[idx+len(marker):]
	end := strings.Index(after, "endobj")
	if end < 0 {
		return "", fmt.Errorf("object %d: endobj not found", num)
	}

	return strings.TrimSpace(after[:end]), nil
}

func matchInt(re *regexp.Regexp, s string) (int, error) {
	m := re.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("pattern %q not found in: %s", re, s)
	}
	return strconv.Atoi(m[1])
}
