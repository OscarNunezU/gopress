package browser

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

// fakeInstance is a controllable implementation of the instance interface.
type fakeInstance struct {
	convertFn    func(ctx context.Context, job *Job) ([]byte, error)
	needsRestart atomic.Bool
	closeCalled  atomic.Bool
}

func (f *fakeInstance) Convert(ctx context.Context, job *Job) ([]byte, error) {
	if f.convertFn != nil {
		return f.convertFn(ctx, job)
	}
	return []byte("%PDF-fake"), nil
}

func (f *fakeInstance) NeedsRestart() bool { return f.needsRestart.Load() }
func (f *fakeInstance) Close() error        { f.closeCalled.Store(true); return nil }

// newTestPool builds a Pool whose instances are replaced by the provided fakes.
// It patches newInstance on the returned pool before any workers start, so the
// factory is only used during restart() — the initial slots are set directly.
func newTestPool(t *testing.T, size int, fakes []*fakeInstance) *Pool {
	t.Helper()

	cfg := PoolConfig{
		Size:       size,
		QueueDepth: size * 4,
	}
	p := &Pool{
		cfg:    cfg,
		queue:  make(chan *pendingJob, cfg.QueueDepth),
		logger: noopLogger(t),
	}
	// Set up slots directly — no Chrome process needed.
	for i, fi := range fakes {
		s := &slot{index: i, inst: fi}
		p.slots = append(p.slots, s)
		go p.worker(s)
	}
	// Provide a factory for restart() tests.
	p.newInstance = func(ctx context.Context, port int) (instance, error) {
		return &fakeInstance{}, nil
	}
	return p
}

func TestPoolJobCompletion(t *testing.T) {
	fi := &fakeInstance{}
	p := newTestPool(t, 1, []*fakeInstance{fi})
	defer p.Close()

	pdf, err := p.Convert(context.Background(), &Job{HTML: "<h1>hi</h1>"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pdf) == 0 {
		t.Fatal("expected non-empty pdf")
	}
}

func TestPoolErrQueueFull(t *testing.T) {
	// Single instance that blocks until released.
	release := make(chan struct{})
	fi := &fakeInstance{
		convertFn: func(ctx context.Context, job *Job) ([]byte, error) {
			<-release
			return []byte("%PDF"), nil
		},
	}
	// QueueDepth=1 so the queue fills immediately after one job is accepted.
	cfg := PoolConfig{Size: 1, QueueDepth: 1}
	p := &Pool{
		cfg:    cfg,
		queue:  make(chan *pendingJob, cfg.QueueDepth),
		logger: noopLogger(t),
	}
	p.newInstance = func(ctx context.Context, port int) (instance, error) {
		return &fakeInstance{}, nil
	}
	s := &slot{index: 0, inst: fi}
	p.slots = append(p.slots, s)
	go p.worker(s)
	defer func() { close(release); p.Close() }()

	// Fill the worker with a blocking job.
	go p.Convert(context.Background(), &Job{HTML: "block"}) //nolint:errcheck

	// Give worker time to dequeue the first job.
	time.Sleep(20 * time.Millisecond)

	// Fill the queue buffer with a second job.
	go p.Convert(context.Background(), &Job{HTML: "fill"}) //nolint:errcheck

	// Give the queue buffer time to fill.
	time.Sleep(20 * time.Millisecond)

	// Third job must be rejected immediately.
	_, err := p.Convert(context.Background(), &Job{HTML: "overflow"})
	if !errors.Is(err, ErrQueueFull) {
		t.Fatalf("expected ErrQueueFull, got %v", err)
	}
}

func TestPoolContextCancelWhileWaiting(t *testing.T) {
	release := make(chan struct{})
	fi := &fakeInstance{
		convertFn: func(ctx context.Context, job *Job) ([]byte, error) {
			<-release
			return []byte("%PDF"), nil
		},
	}
	p := newTestPool(t, 1, []*fakeInstance{fi})
	defer func() { close(release); p.Close() }()

	// Keep the worker busy.
	go p.Convert(context.Background(), &Job{HTML: "block"}) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := p.Convert(ctx, &Job{HTML: "waiter"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context error, got %v", err)
	}
}

func TestPoolRestartAfterMaxConversions(t *testing.T) {
	restartCalled := make(chan struct{}, 1)
	fi := &fakeInstance{
		convertFn: func(ctx context.Context, job *Job) ([]byte, error) {
			return []byte("%PDF"), nil
		},
	}
	fi.needsRestart.Store(true)

	cfg := PoolConfig{Size: 1, QueueDepth: 4}
	p := &Pool{
		cfg:    cfg,
		queue:  make(chan *pendingJob, cfg.QueueDepth),
		logger: noopLogger(t),
	}
	p.newInstance = func(ctx context.Context, port int) (instance, error) {
		select {
		case restartCalled <- struct{}{}:
		default:
		}
		return &fakeInstance{}, nil
	}
	s := &slot{index: 0, inst: fi}
	p.slots = append(p.slots, s)
	go p.worker(s)
	defer p.Close()

	p.Convert(context.Background(), &Job{HTML: "<p>x</p>"}) //nolint:errcheck

	select {
	case <-restartCalled:
		// pass
	case <-time.After(2 * time.Second):
		t.Fatal("pool did not call newInstance for restart within 2s")
	}
}

// noopLogger returns a discard slog.Logger for tests.
func noopLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
