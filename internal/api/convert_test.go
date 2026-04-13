package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/OscarNunezU/gopress/internal/browser"
)

// mockConverter is a test stub for converterIface.
type mockConverter struct {
	pdfData []byte
	err     error
}

func (m *mockConverter) Convert(_ context.Context, _ string, _ map[string][]byte, _ browser.PDFOptions) ([]byte, error) {
	return m.pdfData, m.err
}

// buildMultipart creates a multipart/form-data body from a map of filename → content.
func buildMultipart(t *testing.T, files map[string][]byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for name, data := range files {
		fw, err := w.CreateFormFile(name, name)
		if err != nil {
			t.Fatalf("create form file %q: %v", name, err)
		}
		if _, err := io.Copy(fw, bytes.NewReader(data)); err != nil {
			t.Fatalf("write form file %q: %v", name, err)
		}
	}
	w.Close()
	return &buf, w.FormDataContentType()
}

func TestParseFormMissingHTML(t *testing.T) {
	body, ct := buildMultipart(t, map[string][]byte{
		"style.css": []byte("body{}"),
	})
	r := httptest.NewRequest(http.MethodPost, "/pdf", body)
	r.Header.Set("Content-Type", ct)
	_ = r.ParseMultipartForm(32 << 20)

	_, _, _, err := parseForm(r)
	if !errors.Is(err, errMissingHTML) {
		t.Errorf("error = %v, want errMissingHTML", err)
	}
}

func TestParseFormOnlyHTML(t *testing.T) {
	const want = "<html><body>hello</body></html>"
	body, ct := buildMultipart(t, map[string][]byte{
		"index.html": []byte(want),
	})
	r := httptest.NewRequest(http.MethodPost, "/pdf", body)
	r.Header.Set("Content-Type", ct)
	_ = r.ParseMultipartForm(32 << 20)

	html, assets, _, err := parseForm(r)
	if err != nil {
		t.Fatalf("parseForm: %v", err)
	}
	if html != want {
		t.Errorf("html = %q, want %q", html, want)
	}
	if len(assets) != 0 {
		t.Errorf("assets len = %d, want 0", len(assets))
	}
}

func TestParseFormWithAssets(t *testing.T) {
	body, ct := buildMultipart(t, map[string][]byte{
		"index.html": []byte("<html/>"),
		"style.css":  []byte("body{}"),
		"logo.png":   []byte("\x89PNG"),
	})
	r := httptest.NewRequest(http.MethodPost, "/pdf", body)
	r.Header.Set("Content-Type", ct)
	_ = r.ParseMultipartForm(32 << 20)

	_, assets, _, err := parseForm(r)
	if err != nil {
		t.Fatalf("parseForm: %v", err)
	}
	if len(assets) != 2 {
		t.Errorf("assets len = %d, want 2", len(assets))
	}
	if string(assets["style.css"]) != "body{}" {
		t.Errorf("style.css = %q, want \"body{}\"", assets["style.css"])
	}
}

func TestParseFormWithOptions(t *testing.T) {
	body, ct := buildMultipart(t, map[string][]byte{
		"index.html":   []byte("<html/>"),
		"options.json": []byte(`{"landscape":true,"paperWidth":8.5,"paperHeight":11}`),
	})
	r := httptest.NewRequest(http.MethodPost, "/pdf", body)
	r.Header.Set("Content-Type", ct)
	_ = r.ParseMultipartForm(32 << 20)

	_, _, opts, err := parseForm(r)
	if err != nil {
		t.Fatalf("parseForm: %v", err)
	}
	if !opts.Landscape {
		t.Error("opts.Landscape = false, want true")
	}
	if opts.PaperWidth != 8.5 {
		t.Errorf("opts.PaperWidth = %v, want 8.5", opts.PaperWidth)
	}
	if opts.PaperHeight != 11 {
		t.Errorf("opts.PaperHeight = %v, want 11", opts.PaperHeight)
	}
}

func TestConvertHandlerSuccess(t *testing.T) {
	fakePDF := []byte("%PDF-1.4 fake")
	conv := &mockConverter{pdfData: fakePDF}

	body, ct := buildMultipart(t, map[string][]byte{
		"index.html": []byte("<html><body>hi</body></html>"),
	})
	r := httptest.NewRequest(http.MethodPost, "/pdf", body)
	r.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()

	convertHandler(conv, slog.Default()).ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/pdf" {
		t.Errorf("Content-Type = %q, want application/pdf", ct)
	}
	if !bytes.Equal(w.Body.Bytes(), fakePDF) {
		t.Error("response body does not match expected PDF bytes")
	}
}

func TestConvertHandlerMissingHTML(t *testing.T) {
	conv := &mockConverter{}

	body, ct := buildMultipart(t, map[string][]byte{
		"style.css": []byte("body{}"),
	})
	r := httptest.NewRequest(http.MethodPost, "/pdf", body)
	r.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()

	convertHandler(conv, slog.Default()).ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestConvertHandlerConverterError(t *testing.T) {
	conv := &mockConverter{err: errors.New("chrome crashed")}

	body, ct := buildMultipart(t, map[string][]byte{
		"index.html": []byte("<html/>"),
	})
	r := httptest.NewRequest(http.MethodPost, "/pdf", body)
	r.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()

	convertHandler(conv, slog.Default()).ServeHTTP(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
}

func TestConvertHandlerQueueFull(t *testing.T) {
	conv := &mockConverter{err: browser.ErrQueueFull}

	body, ct := buildMultipart(t, map[string][]byte{
		"index.html": []byte("<html/>"),
	})
	r := httptest.NewRequest(http.MethodPost, "/pdf", body)
	r.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()

	convertHandler(conv, slog.Default()).ServeHTTP(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
}

func TestConvertHandlerTimeout(t *testing.T) {
	conv := &mockConverter{err: context.DeadlineExceeded}

	body, ct := buildMultipart(t, map[string][]byte{
		"index.html": []byte("<html/>"),
	})
	r := httptest.NewRequest(http.MethodPost, "/pdf", body)
	r.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()

	convertHandler(conv, slog.Default()).ServeHTTP(w, r)

	if w.Code != http.StatusGatewayTimeout {
		t.Errorf("status = %d, want %d", w.Code, http.StatusGatewayTimeout)
	}
}

func TestConvertHandlerInvalidMultipart(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/pdf", bytes.NewBufferString("not multipart"))
	r.Header.Set("Content-Type", "multipart/form-data; boundary=missing")
	w := httptest.NewRecorder()

	convertHandler(&mockConverter{}, slog.Default()).ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}
