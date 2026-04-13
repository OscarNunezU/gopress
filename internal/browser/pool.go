package browser

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
)

// Job holds the input and options for a single HTML→PDF conversion.
type Job struct {
	// HTML is the raw HTML content to convert.
	HTML string
	// Assets maps filename to content (CSS, images, fonts, etc.)
	Assets map[string][]byte
	// Options controls PDF output parameters.
	Options PDFOptions
}

// PDFOptions maps to CDP Page.printToPDF parameters exposed via the API.
type PDFOptions struct {
	Landscape         bool
	PrintBackground   bool
	Scale             float64
	PaperWidth        float64
	PaperHeight       float64
	MarginTop         float64
	MarginBottom      float64
	MarginLeft        float64
	MarginRight       float64
	PreferCSSPageSize bool
}

// Pool manages a fixed set of Chromium instances and queues conversion jobs.
type Pool struct {
	cfg    PoolConfig
	slots  []*slot
	queue  chan *pendingJob
	mu     sync.Mutex
	logger *slog.Logger
}

// slot tracks one instance position in the pool (index + current instance).
type slot struct {
	index int
	inst  *Instance
}

type pendingJob struct {
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

	return p, nil
}

// Convert submits a job to the pool and waits for the result.
func (p *Pool) Convert(ctx context.Context, job *Job) ([]byte, error) {
	result := make(chan jobResult, 1)
	select {
	case p.queue <- &pendingJob{job: job, result: result}:
	case <-ctx.Done():
		return nil, ctx.Err()
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
		pdf, err := s.inst.Convert(context.Background(), pj.job)
		pj.result <- jobResult{pdf: pdf, err: err}

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
