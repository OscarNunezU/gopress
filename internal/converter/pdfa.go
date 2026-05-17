package converter

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/OscarNunezU/gopress/internal/browser"
	"github.com/OscarNunezU/gopress/internal/pdfa"
	"github.com/OscarNunezU/gopress/internal/telemetry"
)

// ConvertPDFA converts HTML to PDF/A-2b: generates a regular PDF via Chrome,
// then stamps it with PDF/A-2b conformance markers in pure Go.
func (c *Converter) ConvertPDFA(ctx context.Context, html string, assets map[string][]byte, opts browser.PDFOptions) ([]byte, error) {
	if html == "" {
		return nil, fmt.Errorf("html content is required")
	}

	ctx, span := telemetry.Tracer().Start(ctx, "conversion.pdfa")
	defer span.End()

	span.SetAttributes(
		attribute.Int("html.length", len(html)),
		attribute.Int("assets.count", len(assets)),
	)

	start := time.Now()
	job := &browser.Job{HTML: html, Assets: assets, Options: opts}

	raw, err := c.pool.Convert(ctx, job)
	if err != nil {
		duration := time.Since(start).Seconds()
		status := conversionStatus(err)
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		telemetry.ConversionsTotal.WithLabelValues(status).Inc()
		telemetry.ConversionDuration.WithLabelValues(status).Observe(duration)
		return nil, fmt.Errorf("convert html to pdf: %w", err)
	}

	stamped, err := pdfa.Stamp(raw)
	duration := time.Since(start).Seconds()

	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		telemetry.ConversionsTotal.WithLabelValues("pdfa_error").Inc()
		telemetry.ConversionDuration.WithLabelValues("pdfa_error").Observe(duration)
		return nil, fmt.Errorf("pdfa stamp: %w", err)
	}

	telemetry.ConversionsTotal.WithLabelValues("ok").Inc()
	telemetry.ConversionDuration.WithLabelValues("ok").Observe(duration)
	telemetry.ConversionSizeBytes.Observe(float64(len(stamped)))
	span.SetAttributes(attribute.Int("pdfa.size_bytes", len(stamped)))
	return stamped, nil
}
