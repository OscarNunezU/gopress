package converter

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/OscarNunezU/gopress/internal/browser"
	"github.com/OscarNunezU/gopress/internal/telemetry"
)

// ConvertPDFA converts HTML to PDF/A-2b: generates a regular PDF via Chrome,
// then post-processes it with ghostscript.
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

	pdf, err := c.pool.Convert(ctx, job)
	if err != nil {
		duration := time.Since(start).Seconds()
		status := conversionStatus(err)
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		telemetry.ConversionsTotal.WithLabelValues(status).Inc()
		telemetry.ConversionDuration.WithLabelValues(status).Observe(duration)
		return nil, fmt.Errorf("convert html to pdf: %w", err)
	}

	pdfa, err := runGhostscript(ctx, pdf)
	duration := time.Since(start).Seconds()

	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		telemetry.ConversionsTotal.WithLabelValues("pdfa_error").Inc()
		telemetry.ConversionDuration.WithLabelValues("pdfa_error").Observe(duration)
		return nil, fmt.Errorf("ghostscript pdfa conversion: %w", err)
	}

	telemetry.ConversionsTotal.WithLabelValues("ok").Inc()
	telemetry.ConversionDuration.WithLabelValues("ok").Observe(duration)
	telemetry.ConversionSizeBytes.Observe(float64(len(pdfa)))
	span.SetAttributes(attribute.Int("pdfa.size_bytes", len(pdfa)))
	return pdfa, nil
}

// runGhostscript converts raw PDF bytes to PDF/A-2b using ghostscript.
// Uses temp files because gs does not support stdin/stdout for PDF/A output.
func runGhostscript(ctx context.Context, pdf []byte) ([]byte, error) {
	in, err := os.CreateTemp("", "gopress-in-*.pdf")
	if err != nil {
		return nil, fmt.Errorf("create temp input: %w", err)
	}
	defer os.Remove(in.Name()) //nolint:errcheck

	out, err := os.CreateTemp("", "gopress-out-*.pdf")
	if err != nil {
		in.Close()
		return nil, fmt.Errorf("create temp output: %w", err)
	}
	outName := out.Name()
	out.Close()
	defer os.Remove(outName) //nolint:errcheck

	if _, err := in.Write(pdf); err != nil {
		in.Close()
		return nil, fmt.Errorf("write temp input: %w", err)
	}
	in.Close()

	//nolint:gosec // arguments are controlled by this service, not user input
	cmd := exec.CommandContext(ctx, "gs",
		"-dBATCH", "-dNOPAUSE", "-dNOSAFER",
		"-sDEVICE=pdfwrite",
		"-dPDFA=2",
		"-dPDFACompatibilityPolicy=1",
		"-sColorConversionStrategy=RGB",
		"-dCompatibilityLevel=1.7",
		"-sOutputFile="+outName,
		in.Name(),
	)

	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("gs failed: %w: %s", err, out)
	}

	return os.ReadFile(outName)
}
