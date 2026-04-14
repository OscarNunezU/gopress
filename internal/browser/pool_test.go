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
func (f *fakeInstance) HasCrashed() bool   { return false }
func (f *fakeInstance) Close() error       { f.closeCalled.Store(true); return nil }

// newTestPool builds a Pool backed by the given fakes (no Chrome process).
func newTestPool(t *testing.T, size int, fakes []*fakeInstance) *Pool {
	t.Helper()

	cfg := PoolConfig{Size: size, QueueDepth: size * 4}
	p := &Pool{
		cfg:    cfg,
		queue:  make(chan *pendingJob, cfg.QueueDepth),
		logger: noopLogger(t),
	}
	p.newInstance = func(ctx context.Context, port int) (instance, error) {
		return &fakeInstance{}, nil
	}
	for i, fi := range fakes {
		s := &slot{index: i, inst: fi}
		p.slots = append(p.slots, s)
		go p.worker(s)
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
	started := make(chan struct{})
	release := make(chan struct{})

	fi := &fakeInstance{
		convertFn: func(ctx context.Context, job *Job) ([]byte, error) {
			// Signal exactly once that the worker has started.
			select {
			case started <- struct{}{}:
			default:
			}
			<-release
			return []byte("%PDF"), nil
		},
	}

	// QueueDepth=1: one slot in the buffer, one worker that will be busy.
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

	// Submit the blocking job and wait until the worker has dequeued it.
	go p.Convert(context.Background(), &Job{HTML: "block"}) //nolint:errcheck
	<-started

	// Worker is busy. Fill the 1-slot queue buffer directly (same package).
	// wg.Add must mirror what Convert() does so the worker's wg.Done() doesn't underflow.
	dummy := make(chan jobResult, 1)
	p.wg.Add(1)
	p.queue <- &pendingJob{ctx: context.Background(), job: &Job{HTML: "fill"}, result: dummy}

	// Queue is now at capacity — next Convert must return ErrQueueFull immediately.
	_, err := p.Convert(context.Background(), &Job{HTML: "overflow"})
	if !errors.Is(err, ErrQueueFull) {
		t.Fatalf("expected ErrQueueFull, got %v", err)
	}
}

func TestPoolContextCancelWhileWaiting(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})

	fi := &fakeInstance{
		convertFn: func(ctx context.Context, job *Job) ([]byte, error) {
			select {
			case started <- struct{}{}:
			default:
			}
			<-release
			return []byte("%PDF"), nil
		},
	}

	p := newTestPool(t, 1, []*fakeInstance{fi})
	defer func() { close(release); p.Close() }()

	// Keep the worker busy.
	go p.Convert(context.Background(), &Job{HTML: "block"}) //nolint:errcheck
	<-started

	// Submit a waiter whose context times out while queued.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
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

func TestPoolCloseIdempotent(t *testing.T) {
	p := newTestPool(t, 1, []*fakeInstance{{}})

	// Should not panic on double close.
	p.Close()
	p.Close()
}

// noopLogger returns a discard slog.Logger for tests.
func noopLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
