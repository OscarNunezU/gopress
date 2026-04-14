package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/OscarNunezU/gopress/internal/api"
	"github.com/OscarNunezU/gopress/internal/browser"
	"github.com/OscarNunezU/gopress/internal/converter"
	"github.com/OscarNunezU/gopress/internal/telemetry"
)

func main() {
	// Fast-path: healthcheck mode — invoked by the Docker HEALTHCHECK instruction.
	// Using the binary itself avoids shipping curl/wget in the production image.
	if len(os.Args) > 1 && os.Args[1] == "--healthcheck" {
		port := envInt("GOPRESS_PORT", 3000)
		resp, err := http.Get(fmt.Sprintf("http://localhost:%d/health", port)) //nolint:noctx
		if err != nil {
			os.Exit(1)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		os.Exit(0)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	cfg := loadConfig()
	if err := validateConfig(cfg); err != nil {
		logger.Error("invalid configuration", "err", err)
		os.Exit(1)
	}

	// Telemetry.
	telemetry.Register()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	shutdownTrace, err := telemetry.InitTracing(ctx, cfg.otlpEndpoint)
	if err != nil {
		logger.Error("init tracing", "err", err)
		os.Exit(1)
	}

	// Browser pool.
	pool, err := browser.NewPool(ctx, browser.PoolConfig{
		BinPath:        cfg.chromeBin,
		Size:           cfg.poolSize,
		BasePort:       9222,
		MaxConversions: cfg.maxConversions,
		QueueDepth:     cfg.queueDepth,
	}, logger)
	if err != nil {
		logger.Error("init browser pool", "err", err)
		os.Exit(1)
	}

	// HTTP server.
	conv := converter.New(pool)
	srv := api.New(api.Config{
		Port:         cfg.port,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  60 * time.Second,
		APIKey:       cfg.apiKey,
		RateLimit:    cfg.rateLimit,
		RateBurst:    cfg.rateBurst,
	}, conv, logger)

	// Start server in background, block until signal.
	srvErr := make(chan error, 1)
	go func() { srvErr <- srv.Start() }()

	logger.Info("gopress started", "port", cfg.port, "pool_size", cfg.poolSize)
	if cfg.apiKey != "" {
		logger.Info("API key authentication enabled")
	} else {
		logger.Warn("API key authentication disabled — POST /pdf is open to all callers")
	}

	select {
	case err := <-srvErr:
		logger.Error("server error", "err", err)
	case <-ctx.Done():
		logger.Info("shutting down")
	}

	// Graceful shutdown.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Stop accepting new requests first.
	_ = srv.Shutdown(shutdownCtx)

	// Let in-flight conversions finish before killing Chrome.
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer drainCancel()
	pool.Drain(drainCtx)

	pool.Close()
	_ = shutdownTrace(shutdownCtx)

	logger.Info("gopress stopped")
}

type config struct {
	port           int
	chromeBin      string
	poolSize       int
	maxConversions int
	queueDepth     int
	otlpEndpoint   string
	apiKey         string
	rateLimit      float64
	rateBurst      int
}

func loadConfig() config {
	return config{
		port:           envInt("GOPRESS_PORT", 3000),
		chromeBin:      envStr("CHROME_BIN_PATH", "/usr/bin/chrome"),
		poolSize:       envInt("GOPRESS_POOL_SIZE", 4),
		maxConversions: envInt("GOPRESS_MAX_CONVERSIONS", 100),
		queueDepth:     envInt("GOPRESS_QUEUE_DEPTH", 0),
		otlpEndpoint:   envStr("OTEL_EXPORTER_OTLP_ENDPOINT", ""),
		apiKey:         envStr("GOPRESS_API_KEY", ""),
		rateLimit:      envFloat("GOPRESS_RATE_LIMIT", 0),
		rateBurst:      envInt("GOPRESS_RATE_BURST", 0),
	}
}

func envStr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func envFloat(key string, fallback float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return fallback
}

func validateConfig(cfg config) error {
	if cfg.poolSize <= 0 {
		return fmt.Errorf("GOPRESS_POOL_SIZE must be > 0, got %d", cfg.poolSize)
	}
	if cfg.maxConversions < 0 {
		return fmt.Errorf("GOPRESS_MAX_CONVERSIONS must be >= 0, got %d", cfg.maxConversions)
	}
	if cfg.port <= 0 || cfg.port > 65535 {
		return fmt.Errorf("GOPRESS_PORT must be 1–65535, got %d", cfg.port)
	}
	if cfg.chromeBin == "" {
		return fmt.Errorf("CHROME_BIN_PATH must not be empty")
	}
	if cfg.apiKey != "" && len(cfg.apiKey) < 16 {
		return fmt.Errorf("GOPRESS_API_KEY must be at least 16 characters when set, got %d", len(cfg.apiKey))
	}
	return nil
}
