package browser

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/OscarNunezU/gopress/internal/telemetry"
)

// ErrQueueFull is returned by Convert when all queue slots are occupied.
// API handlers should map this to 503 Service Unavailable.
var ErrQueueFull = errors.New("conversion queue is full")

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
	cfg    PoolConfig
	slots  []*slot
	queue  chan *pendingJob
	mu     sync.Mutex
	logger *slog.Logger
}

type slot struct {
	index int
	inst  *Instance
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
}

// NewPool creates and starts a pool of Chromium instances.
func NewPool(ctx context.Context, cfg PoolConfig, logger *slog.Logger) (*Pool, error) {
	p := &Pool{
		cfg:    cfg,
		queue:  make(chan *pendingJob, cfg.Size*4),
		logger: logger,
	}

	for i := range cfg.Size {
		inst, err := NewInstance(ctx, cfg.BinPath, cfg.BasePort+i, cfg.MaxConversions, logger)
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

// Close shuts down all Chromium instances.
func (p *Pool) Close() {
	close(p.queue)
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, s := range p.slots {
		if err := s.inst.Close(); err != nil {
			p.logger.Error("close instance", "err", err)
		}
	}
}

func (p *Pool) worker(s *slot) {
	for pj := range p.queue {
		telemetry.PoolQueueSize.Dec()
		telemetry.PoolFreeInstances.Dec()

		pdf, err := s.inst.Convert(pj.ctx, pj.job)
		pj.result <- jobResult{pdf: pdf, err: err}

		telemetry.PoolFreeInstances.Inc()

		if s.inst.NeedsRestart() {
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

	inst, err := NewInstance(
		context.Background(),
		p.cfg.BinPath,
		p.cfg.BasePort+s.index,
		p.cfg.MaxConversions,
		p.logger,
	)
	if err != nil {
		return fmt.Errorf("restart instance at port %d: %w", p.cfg.BasePort+s.index, err)
	}

	p.mu.Lock()
	s.inst = inst
	p.mu.Unlock()
	return nil
}
