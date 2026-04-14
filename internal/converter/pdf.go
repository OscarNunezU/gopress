// Package converter provides the high-level HTML→PDF conversion logic.
package converter

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/OscarNunezU/gopress/internal/browser"
	"github.com/OscarNunezU/gopress/internal/telemetry"
)

// Converter converts HTML documents to PDF using a browser pool.
type Converter struct {
	pool *browser.Pool
}

// New creates a Converter backed by the given pool.
func New(pool *browser.Pool) *Converter {
	return &Converter{pool: pool}
}

// Convert transforms HTML (with optional assets) into a PDF byte slice.
func (c *Converter) Convert(ctx context.Context, html string, assets map[string][]byte, opts browser.PDFOptions) ([]byte, error) {
	if html == "" {
		return nil, fmt.Errorf("html content is required")
	}

	ctx, span := telemetry.Tracer().Start(ctx, "conversion")
	defer span.End()

	span.SetAttributes(
		attribute.Int("html.length", len(html)),
		attribute.Int("assets.count", len(assets)),
	)

	start := time.Now()
	job := &browser.Job{
		HTML:    html,
		Assets:  assets,
		Options: opts,
	}

	pdf, err := c.pool.Convert(ctx, job)
	duration := time.Since(start).Seconds()

	status := conversionStatus(err)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
	}

	telemetry.ConversionsTotal.WithLabelValues(status).Inc()
	telemetry.ConversionDuration.WithLabelValues(status).Observe(duration)

	if err != nil {
		return nil, fmt.Errorf("convert html to pdf: %w", err)
	}

	telemetry.ConversionSizeBytes.Observe(float64(len(pdf)))
	span.SetAttributes(attribute.Int("pdf.size_bytes", len(pdf)))
	return pdf, nil
}

// conversionStatus maps an error to a metric label.
//
//   - "ok"           — no error
//   - "queue_full"   — pool queue was at capacity (ErrQueueFull)
//   - "timeout"      — request context exceeded its deadline or was cancelled
//   - "chrome_error" — any other Chrome/CDP failure
func conversionStatus(err error) string {
	if err == nil {
		return "ok"
	}
	if errors.Is(err, browser.ErrQueueFull) {
		return "queue_full"
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "timeout"
	}
	return "chrome_error"
}
