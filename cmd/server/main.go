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
	}, conv, logger)

	// Start server in background, block until signal.
	srvErr := make(chan error, 1)
	go func() { srvErr <- srv.Start() }()

	logger.Info("gopress started", "port", cfg.port, "pool_size", cfg.poolSize)

	select {
	case err := <-srvErr:
		logger.Error("server error", "err", err)
	case <-ctx.Done():
		logger.Info("shutting down")
	}

	// Graceful shutdown.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_ = srv.Shutdown(shutdownCtx)
	pool.Close()
	_ = shutdownTrace(shutdownCtx)

	logger.Info("gopress stopped")
}

type config struct {
	port           int
	chromeBin      string
	poolSize       int
	maxConversions int
	otlpEndpoint   string
}

func loadConfig() config {
	return config{
		port:           envInt("GOPRESS_PORT", 3000),
		chromeBin:      envStr("CHROME_BIN_PATH", "/usr/bin/chrome"),
		poolSize:       envInt("GOPRESS_POOL_SIZE", 4),
		maxConversions: envInt("GOPRESS_MAX_CONVERSIONS", 100),
		otlpEndpoint:   envStr("OTEL_EXPORTER_OTLP_ENDPOINT", ""),
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
