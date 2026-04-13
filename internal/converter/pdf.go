// Package converter provides the high-level HTML→PDF conversion logic.
package converter

import (
	"context"
	"fmt"

	"github.com/OscarNunezU/gopress/internal/browser"
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

	job := &browser.Job{
		HTML:    html,
		Assets:  assets,
		Options: opts,
	}

	pdf, err := c.pool.Convert(ctx, job)
	if err != nil {
		return nil, fmt.Errorf("convert html to pdf: %w", err)
	}

	return pdf, nil
}
