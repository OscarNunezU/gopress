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

// errMissingHTML is returned when the request has no HTML content.
var errMissingHTML = errors.New("html is required")

// jsonRequest is the body schema for application/json requests.
type jsonRequest struct {
	HTML    string            `json:"html"`
	Options *browser.PDFOptions `json:"options,omitempty"`
}

// convertHandler handles POST /pdf.
//
// Accepts two content types:
//
//  1. application/json — simple HTML-only requests:
//     {"html": "<h1>Hello</h1>", "options": {...}}
//
//  2. multipart/form-data — HTML with assets (CSS, images, fonts):
//     index.html (required), any asset files, options.json (optional)
func convertHandler(conv converterIface, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)

		var (
			html   string
			assets map[string][]byte
			opts   browser.PDFOptions
			err    error
		)

		ct := r.Header.Get("Content-Type")
		switch {
		case strings.HasPrefix(ct, "application/json"):
			html, opts, err = parseJSON(r)
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
			html, assets, opts, err = parseForm(r)
		}

		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		pdf, err := conv.Convert(r.Context(), html, assets, opts)
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

func parseJSON(r *http.Request) (html string, opts browser.PDFOptions, err error) {
	var req jsonRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return "", opts, fmt.Errorf("invalid JSON: %w", err)
	}
	if req.HTML == "" {
		return "", opts, errMissingHTML
	}
	if req.Options != nil {
		opts = *req.Options
	}
	return req.HTML, opts, nil
}

func parseForm(r *http.Request) (html string, assets map[string][]byte, opts browser.PDFOptions, err error) {
	assets = make(map[string][]byte)

	for name, headers := range r.MultipartForm.File {
		f, ferr := headers[0].Open()
		if ferr != nil {
			return "", nil, opts, ferr
		}
		data, ferr := io.ReadAll(f)
		_ = f.Close() // read is already done; close error is not actionable here
		if ferr != nil {
			return "", nil, opts, ferr
		}

		switch name {
		case "index.html":
			html = string(data)
		case "options.json":
			if jerr := json.Unmarshal(data, &opts); jerr != nil {
				return "", nil, opts, jerr
			}
		default:
			if err := validateAssetName(name); err != nil {
				return "", nil, opts, err
			}
			assets[name] = data
		}
	}

	if html == "" {
		return "", nil, opts, errMissingHTML
	}
	return html, assets, opts, nil
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
