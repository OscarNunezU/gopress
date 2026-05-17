package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/OscarNunezU/gopress/internal/browser"
)

// maxUploadBytes is the hard limit on request body size (64 MiB).
const maxUploadBytes = 64 << 20

// Output format values for the "format" request field.
const (
	formatPDF  = "pdf"
	formatPDFA = "pdf-a"
)

// errMissingHTML is returned when the request has no HTML content.
var errMissingHTML = errors.New("html is required")

// jsonRequest is the body schema for application/json requests.
type jsonRequest struct {
	HTML    string              `json:"html"`
	Format  string              `json:"format,omitempty"`
	Options *browser.PDFOptions `json:"options,omitempty"`
}

// convertHandler handles POST /pdf.
//
// Accepts two content types:
//
//  1. application/json:
//     {"html": "...", "format": "pdf|pdf-a", "options": {...}}
//
//  2. multipart/form-data:
//     index.html (required), format (optional text field), any asset files, options.json (optional)
//
// The "format" field selects the output:
//   - "pdf"   (default) — standard PDF via Chromium
//   - "pdf-a" — PDF/A-2b via Chromium + pure-Go incremental stamp
func convertHandler(conv converterIface, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)

		var (
			html   string
			assets map[string][]byte
			format string
			opts   browser.PDFOptions
			err    error
		)

		ct := r.Header.Get("Content-Type")
		switch {
		case strings.HasPrefix(ct, "application/json"):
			html, format, opts, err = parseJSON(r)
			assets = map[string][]byte{}
		default:
			if err = r.ParseMultipartForm(32 << 20); err != nil {
				var maxErr *http.MaxBytesError
				if errors.As(err, &maxErr) {
					http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
					return
				}
				http.Error(w, "invalid multipart form", http.StatusBadRequest)
				return
			}
			html, assets, format, opts, err = parseForm(r)
		}

		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		var pdf []byte
		switch format {
		case formatPDFA:
			pdf, err = conv.ConvertPDFA(r.Context(), html, assets, opts)
		default:
			pdf, err = conv.Convert(r.Context(), html, assets, opts)
		}

		if err != nil {
			switch {
			case errors.Is(err, browser.ErrQueueFull):
				http.Error(w, "server overloaded, try again later", http.StatusServiceUnavailable)
			case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
				http.Error(w, "conversion timeout", http.StatusGatewayTimeout)
			default:
				logger.Error("conversion failed", "err", err, "request_id", requestIDFromContext(r.Context()))
				http.Error(w, "conversion failed", http.StatusInternalServerError)
			}
			return
		}

		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Disposition", `attachment; filename="document.pdf"`)
		w.Header().Set("Content-Length", strconv.Itoa(len(pdf)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(pdf)
	})
}

func parseJSON(r *http.Request) (html, format string, opts browser.PDFOptions, err error) {
	var req jsonRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return "", "", opts, fmt.Errorf("invalid JSON: %w", err)
	}
	if req.HTML == "" {
		return "", "", opts, errMissingHTML
	}
	if err := validateFormat(req.Format); err != nil {
		return "", "", opts, err
	}
	if req.Options != nil {
		opts = *req.Options
	}
	return req.HTML, req.Format, opts, nil
}

func parseForm(r *http.Request) (html string, assets map[string][]byte, format string, opts browser.PDFOptions, err error) {
	assets = make(map[string][]byte)

	// Text fields (non-file form values).
	if vals := r.MultipartForm.Value["format"]; len(vals) > 0 {
		format = vals[0]
	}
	if err := validateFormat(format); err != nil {
		return "", nil, "", opts, err
	}

	for name, headers := range r.MultipartForm.File {
		f, ferr := headers[0].Open()
		if ferr != nil {
			return "", nil, "", opts, ferr
		}
		data, ferr := io.ReadAll(f)
		_ = f.Close()
		if ferr != nil {
			return "", nil, "", opts, ferr
		}

		switch name {
		case "index.html":
			html = string(data)
		case "options.json":
			if jerr := json.Unmarshal(data, &opts); jerr != nil {
				return "", nil, "", opts, jerr
			}
		default:
			if err := validateAssetName(name); err != nil {
				return "", nil, "", opts, err
			}
			assets[name] = data
		}
	}

	if html == "" {
		return "", nil, "", opts, errMissingHTML
	}
	return html, assets, format, opts, nil
}

// validateFormat returns an error if format is not a recognised value.
// An empty string is accepted and means the default (pdf).
func validateFormat(format string) error {
	switch format {
	case "", formatPDF, formatPDFA:
		return nil
	default:
		return fmt.Errorf("unsupported format %q: must be %q or %q", format, formatPDF, formatPDFA)
	}
}

// validateAssetName rejects asset filenames that are empty, too long,
// contain path traversal components, start with an absolute path separator,
// or contain control characters.
// Subdirectory paths like "images/logo.png" are allowed — the in-memory
// asset server resolves them correctly.
func validateAssetName(name string) error {
	if name == "" {
		return fmt.Errorf("asset name must not be empty")
	}
	if len(name) > 255 {
		return fmt.Errorf("asset name too long: %q", name)
	}
	if strings.HasPrefix(name, "/") || strings.HasPrefix(name, "\\") {
		return fmt.Errorf("asset name must not be an absolute path: %q", name)
	}
	for _, seg := range strings.FieldsFunc(name, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			return fmt.Errorf("asset name must not contain path traversal: %q", name)
		}
	}
	for _, r := range name {
		if r < 0x20 {
			return fmt.Errorf("asset name contains invalid character: %q", name)
		}
	}
	return nil
}
