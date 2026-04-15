package browser

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/OscarNunezU/gopress/internal/telemetry"
)

// ErrQueueFull is returned by Convert when all queue slots are occupied.
// API handlers should map this to 503 Service Unavailable.
var ErrQueueFull = errors.New("conversion queue is full")

// instance is the interface Pool requires from a Chromium instance.
// *Instance satisfies it; tests can substitute a fake.
type instance interface {
	Convert(ctx context.Context, job *Job) ([]byte, error)
	NeedsRestart() bool
	HasCrashed() bool
	Close() error
}

// Job holds the input and options for a single HTML→PDF conversion.
type Job struct {
	HTML    string
	Assets  map[string][]byte
	Options PDFOptions
}

// PDFOptions maps to CDP Page.printToPDF parameters exposed via the API.
type PDFOptions struct {
	Landscape           bool
	PrintBackground     bool
	Scale               float64
	PaperWidth          float64
	PaperHeight         float64
	MarginTop           float64
	MarginBottom        float64
	MarginLeft          float64
	MarginRight         float64
	PreferCSSPageSize   bool
	DisplayHeaderFooter bool
	// HeaderTemplate and FooterTemplate are HTML strings rendered by Chrome.
	// Use <span class="date">, <span class="title">, <span class="url">,
	// <span class="pageNumber">, <span class="totalPages"> as placeholders.
	HeaderTemplate string
	FooterTemplate string
	// PageRanges restricts printing to specific pages, e.g. "1-5, 8, 11-13".
	// Empty string prints all pages.
	PageRanges string
}

// Pool manages a fixed set of Chromium instances and queues conversion jobs.
type Pool struct {
	cfg         PoolConfig
	slots       []*slot
	queue       chan *pendingJob
	done        chan struct{} // closed by Close(); lets workers exit backoff sleeps early
	mu          sync.Mutex
	closeOnce   sync.Once      // ensures Close is idempotent
	wg          sync.WaitGroup // tracks in-flight conversions for Drain
	shutdown    atomic.Bool    // set during Close to stop restart attempts
	backoffUnit time.Duration  // unit for restart backoff (default: time.Second; tests use time.Millisecond)
	logger      *slog.Logger
	newInstance func(ctx context.Context, port int) (instance, error)
}

type slot struct {
	index int
	inst  instance
}

type pendingJob struct {
	ctx    context.Context
	job    *Job
	result chan<- jobResult
}

type jobResult struct {
	pdf []byte
	err error
}

// PoolConfig holds configuration for the instance pool.
type PoolConfig struct {
	BinPath        string
	Size           int
	BasePort       int
	MaxConversions int
	// QueueDepth sets the pending-job buffer size. 0 means auto (Size*4).
	QueueDepth int
}

// NewPool creates and starts a pool of Chromium instances.
func NewPool(ctx context.Context, cfg PoolConfig, logger *slog.Logger) (*Pool, error) {
	qd := cfg.QueueDepth
	if qd <= 0 {
		qd = cfg.Size * 4
	}

	p := &Pool{
		cfg:    cfg,
		queue:  make(chan *pendingJob, qd),
		done:   make(chan struct{}),
		logger: logger,
	}
	p.newInstance = func(ctx context.Context, port int) (instance, error) {
		return NewInstance(ctx, cfg.BinPath, port, cfg.MaxConversions, logger)
	}

	for i := range cfg.Size {
		inst, err := p.newInstance(ctx, cfg.BasePort+i)
		if err != nil {
			p.Close()
			return nil, fmt.Errorf("init pool instance %d: %w", i, err)
		}
		s := &slot{index: i, inst: inst}
		p.slots = append(p.slots, s)
		go p.worker(s)
	}

	// All instances are idle at startup.
	telemetry.PoolFreeInstances.Set(float64(cfg.Size))

	return p, nil
}

// Convert submits a job to the pool and waits for the result.
// Returns ErrQueueFull immediately if the queue buffer is at capacity,
// so callers can shed load with 503 instead of accumulating goroutines.
func (p *Pool) Convert(ctx context.Context, job *Job) ([]byte, error) {
	result := make(chan jobResult, 1)
	pj := &pendingJob{ctx: ctx, job: job, result: result}

	// wg.Add must happen before the job enters the queue so Drain() cannot
	// return before the worker has had a chance to call wg.Done().
	p.wg.Add(1)
	// Fast-path: reject immediately if the pool is already shutting down so we
	// never attempt to send to a queue whose workers may have exited.
	if p.shutdown.Load() {
		p.wg.Done()
		return nil, ErrQueueFull
	}
	select {
	case p.queue <- pj:
		telemetry.PoolQueueSize.Inc()
	case <-p.done:
		// Pool was closed between the shutdown check above and this select.
		p.wg.Done()
		return nil, ErrQueueFull
	case <-ctx.Done():
		p.wg.Done()
		return nil, ctx.Err()
	default:
		p.wg.Done()
		return nil, ErrQueueFull
	}

	select {
	case r := <-result:
		return r.pdf, r.err
	case <-ctx.Done():
		// The job is already queued; the worker will still process it and
		// call wg.Done(). We return the context error to the caller now.
		return nil, ctx.Err()
	}
}

// Drain waits for all in-flight conversions to finish or ctx to expire.
// Call this after srv.Shutdown() and before pool.Close() so no PDF bytes
// are lost mid-write when Chrome is killed.
func (p *Pool) Drain(ctx context.Context) {
	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// Close shuts down all Chromium instances. Safe to call multiple times.
func (p *Pool) Close() {
	p.shutdown.Store(true)
	p.closeOnce.Do(func() {
		// Closing p.done signals both workers (exit their for-select) and any
		// Convert() caller that is mid-select on the enqueue case. The queue
		// itself is intentionally NOT closed here: closing a channel while a
		// concurrent goroutine may be sending to it causes a panic. Workers
		// exit via the p.done case in their for-select loop instead.
		close(p.done)
		p.mu.Lock()
		defer p.mu.Unlock()
		for _, s := range p.slots {
			if err := s.inst.Close(); err != nil {
				p.logger.Error("close instance", "err", err)
			}
		}
	})
}

func (p *Pool) worker(s *slot) {
	// consecutiveFails tracks back-to-back restart failures for this slot.
	// It resets to 0 on any successful restart and drives the backoff duration.
	consecutiveFails := 0

	for {
		// Block until a job arrives or the pool is closed.
		var pj *pendingJob
		select {
		case pj = <-p.queue:
		case <-p.done:
			return
		}

		telemetry.PoolQueueSize.Dec()
		telemetry.PoolFreeInstances.Dec()

		pdf, err := s.inst.Convert(pj.ctx, pj.job)
		pj.result <- jobResult{pdf: pdf, err: err}
		p.wg.Done()

		telemetry.PoolFreeInstances.Inc()

		if s.inst.NeedsRestart() && !p.shutdown.Load() {
			reason := "max_conversions"
			if s.inst.HasCrashed() {
				reason = "crash"
			}
			telemetry.PoolRestarts.WithLabelValues(reason).Inc()
			p.logger.Info("restarting instance", "port", p.cfg.BasePort+s.index, "reason", reason)

			if err := p.restart(s); err != nil {
				p.logger.Error("instance restart failed", "err", err, "port", p.cfg.BasePort+s.index)
				// Slot is dead — correct the gauge so it doesn't appear free.
				telemetry.PoolFreeInstances.Dec()

				// Exponential backoff: 1×unit, 2×unit, 4×unit … capped at 30×unit.
				// Prevents a tight loop of failed restarts from burning CPU and
				// flooding logs when Chrome can't start (OOM, binary missing, etc.).
				// backoffUnit defaults to time.Second; tests override it to
				// time.Millisecond so the backoff path executes in microseconds.
				consecutiveFails++
				unit := p.backoffUnit
				if unit <= 0 {
					unit = time.Second
				}
				backoff := time.Duration(1<<uint(consecutiveFails-1)) * unit
				if backoff > 30*unit {
					backoff = 30 * unit
				}
				p.logger.Warn("backing off before restart retry",
					"backoff", backoff,
					"consecutive_fails", consecutiveFails,
					"port", p.cfg.BasePort+s.index,
				)
				select {
				case <-time.After(backoff):
				case <-p.done:
					return
				}
			} else {
				consecutiveFails = 0
			}
		}
	}
}

// restart kills the current instance and starts a fresh one in the same slot.
func (p *Pool) restart(s *slot) error {
	if err := s.inst.Close(); err != nil {
		p.logger.Warn("kill old instance during restart", "err", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	inst, err := p.newInstance(ctx, p.cfg.BasePort+s.index)
	if err != nil {
		return fmt.Errorf("restart instance at port %d: %w", p.cfg.BasePort+s.index, err)
	}

	p.mu.Lock()
	s.inst = inst
	p.mu.Unlock()
	return nil
}
