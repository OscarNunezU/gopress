package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/OscarNunezU/gopress/internal/cdp"
)

// fakeBrowserClient implements browserClient without a real Chrome process.
type fakeBrowserClient struct {
	closed bool
	sendFn func(ctx context.Context, method string, params any, result any) error
}

func (f *fakeBrowserClient) Send(ctx context.Context, method string, params any, result any) error {
	if f.sendFn != nil {
		return f.sendFn(ctx, method, params, result)
	}
	return nil
}

func (f *fakeBrowserClient) Subscribe(_ string) <-chan cdp.Event {
	return make(chan cdp.Event, 1)
}

func (f *fakeBrowserClient) NewSession(_ string) *cdp.Session {
	return nil
}

func (f *fakeBrowserClient) IsClosed() bool { return f.closed }

func (f *fakeBrowserClient) Close() error {
	f.closed = true
	return nil
}

// --- Instance.NeedsRestart / HasCrashed ---

func TestInstanceNeedsRestartQuota(t *testing.T) {
	inst := &Instance{
		client:         &fakeBrowserClient{},
		maxConversions: 3,
		conversions:    3,
	}
	if !inst.NeedsRestart() {
		t.Error("NeedsRestart() = false, want true (quota reached)")
	}
}

func TestInstanceNeedsRestartNotYet(t *testing.T) {
	inst := &Instance{
		client:         &fakeBrowserClient{},
		maxConversions: 10,
		conversions:    3,
	}
	if inst.NeedsRestart() {
		t.Error("NeedsRestart() = true, want false")
	}
}

func TestInstanceNeedsRestartUnlimited(t *testing.T) {
	inst := &Instance{
		client:         &fakeBrowserClient{},
		maxConversions: 0, // 0 = unlimited
		conversions:    9999,
	}
	if inst.NeedsRestart() {
		t.Error("NeedsRestart() = true, want false (unlimited)")
	}
}

func TestInstanceHasCrashedFalse(t *testing.T) {
	inst := &Instance{client: &fakeBrowserClient{closed: false}}
	if inst.HasCrashed() {
		t.Error("HasCrashed() = true, want false")
	}
}

func TestInstanceHasCrashedTrue(t *testing.T) {
	inst := &Instance{client: &fakeBrowserClient{closed: true}}
	if !inst.HasCrashed() {
		t.Error("HasCrashed() = false, want true")
	}
}

func TestInstanceNeedsRestartAfterCrash(t *testing.T) {
	inst := &Instance{
		client:         &fakeBrowserClient{closed: true},
		maxConversions: 100,
		conversions:    0,
	}
	if !inst.NeedsRestart() {
		t.Error("NeedsRestart() = false, want true (crashed)")
	}
}

// --- openSession error paths ---

func TestOpenSessionCreateTargetError(t *testing.T) {
	wantErr := fmt.Errorf("chrome is gone")
	inst := &Instance{
		client: &fakeBrowserClient{
			sendFn: func(_ context.Context, method string, _ any, _ any) error {
				if method == "Target.createTarget" {
					return wantErr
				}
				return nil
			},
		},
		logger: noopLogger(t),
	}
	_, _, err := inst.openSession(context.Background())
	if err == nil {
		t.Fatal("expected error from openSession, got nil")
	}
}

func TestOpenSessionAttachError(t *testing.T) {
	wantErr := fmt.Errorf("attach refused")
	calls := 0
	inst := &Instance{
		client: &fakeBrowserClient{
			sendFn: func(_ context.Context, method string, _ any, result any) error {
				calls++
				if method == "Target.createTarget" {
					b, _ := json.Marshal(map[string]string{"targetId": "fake-target"})
					_ = json.Unmarshal(b, result)
					return nil
				}
				return wantErr
			},
		},
		logger: noopLogger(t),
	}
	_, _, err := inst.openSession(context.Background())
	if err == nil {
		t.Fatal("expected error from attachToTarget, got nil")
	}
	if calls < 2 {
		t.Errorf("expected at least 2 Send calls, got %d", calls)
	}
}

// --- process.waitReady ---

func TestProcessWaitReady(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	var port int
	fmt.Sscanf(srv.Listener.Addr().String(), "127.0.0.1:%d", &port)

	p := &Process{port: port, logger: noopLogger(t)}
	if err := p.waitReady(context.Background()); err != nil {
		t.Fatalf("waitReady() = %v, want nil", err)
	}
}

func TestProcessWaitReadyContextCancelled(t *testing.T) {
	p := &Process{port: 19999, logger: noopLogger(t)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.waitReady(ctx); err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
}

func TestProcessPort(t *testing.T) {
	p := &Process{port: 9222}
	if got := p.Port(); got != 9222 {
		t.Errorf("Port() = %d, want 9222", got)
	}
}
