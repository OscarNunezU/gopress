package api

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/OscarNunezU/gopress/internal/browser"
	"github.com/OscarNunezU/gopress/internal/telemetry"
)

// Server is the gopress HTTP server.
type Server struct {
	http   *http.Server
	logger *slog.Logger
}

// Config holds HTTP server configuration.
type Config struct {
	Port         int
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	IdleTimeout  time.Duration
}

// converterIface allows injecting the converter without a circular import.
type converterIface interface {
	Convert(ctx context.Context, html string, assets map[string][]byte, opts browser.PDFOptions) ([]byte, error)
}

// New creates a configured Server with all routes registered.
func New(cfg Config, converter converterIface, logger *slog.Logger) *Server {
	mux := http.NewServeMux()

	s := &Server{logger: logger}
	mux.Handle("POST /pdf", convertHandler(converter, logger))
	mux.Handle("GET /health", healthHandler())
	mux.Handle("GET /version", versionHandler())
	mux.Handle("GET /metrics", telemetry.Handler())

	s.http = &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      mux,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		IdleTimeout:  cfg.IdleTimeout,
	}
	return s
}

// Start begins listening and serving requests.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.http.Addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.http.Addr, err)
	}
	s.logger.Info("gopress listening", "addr", s.http.Addr)
	return s.http.Serve(ln)
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}
