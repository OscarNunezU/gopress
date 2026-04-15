package converter

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/OscarNunezU/gopress/internal/browser"
)

// mockPool is a minimal poolIface implementation for testing.
type mockPool struct {
	fn func(ctx context.Context, job *browser.Job) ([]byte, error)
}

func (m *mockPool) Convert(ctx context.Context, job *browser.Job) ([]byte, error) {
	if m.fn != nil {
		return m.fn(ctx, job)
	}
	return []byte("%PDF-fake"), nil
}

func TestConversionStatus(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"ok", nil, "ok"},
		{"queue_full", browser.ErrQueueFull, "queue_full"},
		{"deadline", context.DeadlineExceeded, "timeout"},
		{"cancelled", context.Canceled, "timeout"},
		{"wrapped_deadline", fmt.Errorf("convert: %w", context.DeadlineExceeded), "timeout"},
		{"chrome", errors.New("cdp: connection lost"), "chrome_error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := conversionStatus(tt.err)
			if got != tt.want {
				t.Errorf("conversionStatus(%v) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}

func TestConverterConvert(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		c := New(&mockPool{fn: func(_ context.Context, _ *browser.Job) ([]byte, error) {
			return []byte("%PDF-1.4"), nil
		}})
		pdf, err := c.Convert(context.Background(), "<h1>hi</h1>", nil, browser.PDFOptions{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(pdf) == 0 {
			t.Fatal("expected non-empty pdf")
		}
	})

	t.Run("empty_html", func(t *testing.T) {
		c := New(&mockPool{})
		_, err := c.Convert(context.Background(), "", nil, browser.PDFOptions{})
		if err == nil {
			t.Fatal("expected error for empty html, got nil")
		}
	})

	t.Run("queue_full", func(t *testing.T) {
		c := New(&mockPool{fn: func(_ context.Context, _ *browser.Job) ([]byte, error) {
			return nil, browser.ErrQueueFull
		}})
		_, err := c.Convert(context.Background(), "<p>x</p>", nil, browser.PDFOptions{})
		if !errors.Is(err, browser.ErrQueueFull) {
			t.Fatalf("expected ErrQueueFull, got %v", err)
		}
	})
}
