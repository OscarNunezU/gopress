package browser

import (
	"context"
	"testing"
	"time"

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

func TestWaitForNetworkIdle(t *testing.T) {
	t.Run("fires_on_network_idle", func(t *testing.T) {
		ch := make(chan cdp.Event, 2)
		ch <- cdp.Event{Method: "Page.lifecycleEvent", Params: map[string]any{"name": "DOMContentLoaded"}}
		ch <- cdp.Event{Method: "Page.lifecycleEvent", Params: map[string]any{"name": "networkIdle"}}
		if err := waitForNetworkIdle(context.Background(), ch); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("skips_non_idle_events_then_times_out", func(t *testing.T) {
		ch := make(chan cdp.Event, 1)
		ch <- cdp.Event{Method: "Page.lifecycleEvent", Params: map[string]any{"name": "DOMContentLoaded"}}
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		if err := waitForNetworkIdle(ctx, ch); err == nil {
			t.Fatal("expected error (timeout), got nil")
		}
	})

	t.Run("context_cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		ch := make(chan cdp.Event)
		if err := waitForNetworkIdle(ctx, ch); err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}
