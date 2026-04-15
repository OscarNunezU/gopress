package browser

import (
	"context"
	"testing"

	"github.com/OscarNunezU/gopress/internal/cdp"
)

func TestWaitForEvent(t *testing.T) {
	t.Run("fires", func(t *testing.T) {
		ch := make(chan cdp.Event, 1)
		ch <- cdp.Event{Method: "Page.loadEventFired"}
		if err := waitForEvent(context.Background(), ch); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("context_cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		ch := make(chan cdp.Event)
		if err := waitForEvent(ctx, ch); err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}

