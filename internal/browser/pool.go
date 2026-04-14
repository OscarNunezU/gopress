package browser

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
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
	mu          sync.Mutex
	closeOnce   sync.Once // ensures Close is idempotent
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

	select {
	case p.queue <- pj:
		telemetry.PoolQueueSize.Inc()
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
		return nil, ErrQueueFull
	}

	select {
	case r := <-result:
		return r.pdf, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Close shuts down all Chromium instances. Safe to call multiple times.
func (p *Pool) Close() {
	p.closeOnce.Do(func() {
		close(p.queue)
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
	for pj := range p.queue {
		telemetry.PoolQueueSize.Dec()
		telemetry.PoolFreeInstances.Dec()

		pdf, err := s.inst.Convert(pj.ctx, pj.job)
		pj.result <- jobResult{pdf: pdf, err: err}

		telemetry.PoolFreeInstances.Inc()

		if s.inst.NeedsRestart() {
			telemetry.PoolRestarts.Inc()
			p.logger.Info("restarting instance", "port", p.cfg.BasePort+s.index)
			if err := p.restart(s); err != nil {
				p.logger.Error("instance restart failed", "err", err, "port", p.cfg.BasePort+s.index)
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
