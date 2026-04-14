package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/OscarNunezU/gopress/internal/browser"
)

// maxUploadBytes is the hard limit on multipart request body size (64 MiB).
const maxUploadBytes = 64 << 20

// errMissingHTML is returned when the multipart form has no index.html field.
var errMissingHTML = errors.New("index.html is required")

// convertHandler handles POST /pdf.
// Accepts multipart/form-data with:
//   - index.html (required)
//   - any number of asset files (CSS, images, fonts, etc.)
//   - options.json (optional PDF options)
func convertHandler(conv converterIface, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "invalid multipart form", http.StatusBadRequest)
			return
		}

		html, assets, opts, err := parseForm(r)
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
				logger.Error("conversion failed", "err", err)
				http.Error(w, "conversion failed", http.StatusInternalServerError)
			}
			return
		}

		w.Header().Set("Content-Type", "application/pdf")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(pdf)
	})
}

func parseForm(r *http.Request) (html string, assets map[string][]byte, opts browser.PDFOptions, err error) {
	assets = make(map[string][]byte)

	for name, headers := range r.MultipartForm.File {
		f, ferr := headers[0].Open()
		if ferr != nil {
			return "", nil, opts, ferr
		}
		data, ferr := io.ReadAll(f)
		f.Close()
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
			assets[name] = data
		}
	}

	if html == "" {
		return "", nil, opts, errMissingHTML
	}
	return html, assets, opts, nil
}
