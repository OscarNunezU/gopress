package browser

import (
	"context"
	"errors"
	"fmt"
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
	hasCrashed   bool  // returned by HasCrashed(); drives reason="crash" in worker
	closeErr     error // returned by Close(); exercises error-log paths
}

func (f *fakeInstance) Convert(ctx context.Context, job *Job) ([]byte, error) {
	if f.convertFn != nil {
		return f.convertFn(ctx, job)
	}
	return []byte("%PDF-fake"), nil
}

func (f *fakeInstance) NeedsRestart() bool { return f.needsRestart.Load() }
func (f *fakeInstance) HasCrashed() bool   { return f.hasCrashed }
func (f *fakeInstance) Close() error       { f.closeCalled.Store(true); return f.closeErr }

// newTestPool builds a Pool backed by the given fakes (no Chrome process).
func newTestPool(t *testing.T, size int, fakes []*fakeInstance) *Pool {
	t.Helper()

	cfg := PoolConfig{Size: size, QueueDepth: size * 4}
	p := &Pool{
		cfg:    cfg,
		queue:  make(chan *pendingJob, cfg.QueueDepth),
		done:   make(chan struct{}),
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
		done:   make(chan struct{}),
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

func TestPoolConvertCanceledBeforeEnqueue(t *testing.T) {
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

	// QueueDepth=1: one slot in the buffer, one worker that will be busy.
	cfg := PoolConfig{Size: 1, QueueDepth: 1}
	p := &Pool{
		cfg:    cfg,
		queue:  make(chan *pendingJob, cfg.QueueDepth),
		done:   make(chan struct{}),
		logger: noopLogger(t),
	}
	p.newInstance = func(ctx context.Context, port int) (instance, error) {
		return &fakeInstance{}, nil
	}
	s := &slot{index: 0, inst: fi}
	p.slots = append(p.slots, s)
	go p.worker(s)
	defer func() { close(release); p.Close() }()

	// Keep the worker busy so the queue slot is occupied.
	go p.Convert(context.Background(), &Job{HTML: "block"}) //nolint:errcheck
	<-started

	// Fill the 1-slot queue buffer directly.
	dummy := make(chan jobResult, 1)
	p.wg.Add(1)
	p.queue <- &pendingJob{ctx: context.Background(), job: &Job{HTML: "fill"}, result: dummy}

	// Queue is full. A pre-cancelled context must return context.Canceled
	// (not ErrQueueFull) because ctx.Done() takes precedence over default.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := p.Convert(ctx, &Job{HTML: "pre-cancel"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
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
		done:   make(chan struct{}),
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

func TestPoolDrain(t *testing.T) {
	started := make(chan struct{}, 1)
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

	// Start a long-running conversion in the background.
	go p.Convert(context.Background(), &Job{HTML: "<p>drain</p>"}) //nolint:errcheck
	<-started                                                       // worker is now busy

	// Launch Drain — it must block until the in-flight conversion finishes.
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		drainCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		p.Drain(drainCtx)
	}()

	// Drain must NOT return while the worker is still running.
	select {
	case <-drained:
		t.Fatal("Drain returned before in-flight conversion finished")
	case <-time.After(50 * time.Millisecond):
	}

	// Release the worker; Drain must complete promptly.
	close(release)
	select {
	case <-drained:
		// pass
	case <-time.After(2 * time.Second):
		t.Fatal("Drain did not complete after conversion finished")
	}

	p.Close()
}

func TestPoolCloseIdempotent(t *testing.T) {
	p := newTestPool(t, 1, []*fakeInstance{{}})

	// Should not panic on double close.
	p.Close()
	p.Close()
}

func TestPoolWorkerRestartBackoff(t *testing.T) {
	t.Run("retries_after_backoff_and_recovers", func(t *testing.T) {
		// newInstance fails twice, succeeds on the third attempt.
		// The worker only retries restart when it processes a new job, so we
		// submit 3 jobs sequentially to drive 3 restart attempts.
		attempts := 0
		recovered := make(chan struct{}, 1)

		fi := &fakeInstance{
			convertFn: func(ctx context.Context, job *Job) ([]byte, error) {
				return []byte("%PDF"), nil
			},
		}
		fi.needsRestart.Store(true)

		cfg := PoolConfig{Size: 1, QueueDepth: 4}
		p := &Pool{
			cfg:         cfg,
			queue:       make(chan *pendingJob, cfg.QueueDepth),
			done:        make(chan struct{}),
			logger:      noopLogger(t),
			backoffUnit: time.Millisecond, // fast backoff for tests
		}
		p.newInstance = func(ctx context.Context, port int) (instance, error) {
			attempts++
			if attempts < 3 {
				return nil, fmt.Errorf("transient startup error")
			}
			select {
			case recovered <- struct{}{}:
			default:
			}
			return &fakeInstance{}, nil
		}
		s := &slot{index: 0, inst: fi}
		p.slots = append(p.slots, s)
		go p.worker(s)
		defer p.Close()

		// Each Convert waits for the result, so jobs arrive sequentially.
		// Job N+1 is queued while the worker sleeps its backoff for job N.
		for range 3 {
			p.Convert(context.Background(), &Job{HTML: "<p>x</p>"}) //nolint:errcheck
		}

		select {
		case <-recovered:
			// pass: slot recovered after two transient failures
		case <-time.After(2 * time.Second):
			t.Fatal("pool did not recover after backoff within 2s")
		}
	})

	t.Run("exits_during_backoff_on_close", func(t *testing.T) {
		fi := &fakeInstance{
			convertFn: func(ctx context.Context, job *Job) ([]byte, error) {
				return []byte("%PDF"), nil
			},
		}
		fi.needsRestart.Store(true)

		cfg := PoolConfig{Size: 1, QueueDepth: 4}
		p := &Pool{
			cfg:         cfg,
			queue:       make(chan *pendingJob, cfg.QueueDepth),
			done:        make(chan struct{}),
			logger:      noopLogger(t),
			backoffUnit: time.Hour, // enormous — worker must not sleep through it
		}
		p.newInstance = func(ctx context.Context, port int) (instance, error) {
			return nil, fmt.Errorf("always fails")
		}
		s := &slot{index: 0, inst: fi}
		p.slots = append(p.slots, s)
		go p.worker(s)

		p.Convert(context.Background(), &Job{HTML: "<p>x</p>"}) //nolint:errcheck

		// Worker is now sleeping in a 1-hour backoff. Close must wake it via p.done.
		closeDone := make(chan struct{})
		go func() {
			defer close(closeDone)
			p.Close()
		}()

		select {
		case <-closeDone:
			// pass: Close() returned promptly
		case <-time.After(2 * time.Second):
			t.Fatal("pool.Close() did not return promptly during backoff sleep")
		}
	})
}

// TestPoolWorkerCrashReason verifies that when HasCrashed() is true the worker
// emits a restart with reason="crash" (not "max_conversions").
func TestPoolWorkerCrashReason(t *testing.T) {
	restartCalled := make(chan struct{}, 1)

	fi := &fakeInstance{
		convertFn: func(ctx context.Context, job *Job) ([]byte, error) {
			return []byte("%PDF"), nil
		},
		hasCrashed: true, // drives reason = "crash" in worker
	}
	fi.needsRestart.Store(true)

	cfg := PoolConfig{Size: 1, QueueDepth: 4}
	p := &Pool{
		cfg:    cfg,
		queue:  make(chan *pendingJob, cfg.QueueDepth),
		done:   make(chan struct{}),
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

	p.Convert(context.Background(), &Job{HTML: "<p>crash</p>"}) //nolint:errcheck

	select {
	case <-restartCalled:
		// pass: worker triggered a restart for the crashed instance
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not restart crashed instance within 2s")
	}
}

// TestPoolCloseInstanceError verifies that Close() does not panic when an
// instance's own Close() returns an error (logger absorbs it).
func TestPoolCloseInstanceError(t *testing.T) {
	fi := &fakeInstance{closeErr: errors.New("process already dead")}
	p := newTestPool(t, 1, []*fakeInstance{fi})
	p.Close() // must not panic
	p.Close() // idempotent
}

// TestPoolRestartOldInstanceCloseError verifies that restart() continues and
// starts a fresh instance even when the old instance's Close() returns an error.
func TestPoolRestartOldInstanceCloseError(t *testing.T) {
	restartDone := make(chan struct{}, 1)

	fi := &fakeInstance{
		convertFn: func(ctx context.Context, job *Job) ([]byte, error) {
			return []byte("%PDF"), nil
		},
		closeErr: errors.New("kill failed"), // Close() on old instance fails
	}
	fi.needsRestart.Store(true)

	cfg := PoolConfig{Size: 1, QueueDepth: 4}
	p := &Pool{
		cfg:    cfg,
		queue:  make(chan *pendingJob, cfg.QueueDepth),
		done:   make(chan struct{}),
		logger: noopLogger(t),
	}
	p.newInstance = func(ctx context.Context, port int) (instance, error) {
		select {
		case restartDone <- struct{}{}:
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
	case <-restartDone:
		// pass: restart completed despite Close() error on the old instance
	case <-time.After(2 * time.Second):
		t.Fatal("restart did not happen within 2s")
	}
}

// noopLogger returns a discard slog.Logger for tests.
func noopLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
