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
	instances []*Instance
	queue     chan *pendingJob
	mu        sync.Mutex
	logger    *slog.Logger
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
		queue:  make(chan *pendingJob, cfg.Size*4),
		logger: logger,
	}

	for i := range cfg.Size {
		inst, err := NewInstance(ctx, cfg.BinPath, cfg.BasePort+i, cfg.MaxConversions, logger)
		if err != nil {
			p.Close()
			return nil, fmt.Errorf("init pool instance %d: %w", i, err)
		}
		p.instances = append(p.instances, inst)
		go p.worker(inst)
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
	for _, inst := range p.instances {
		if err := inst.Close(); err != nil {
			p.logger.Error("close instance", "err", err)
		}
	}
}

func (p *Pool) worker(inst *Instance) {
	for pj := range p.queue {
		pdf, err := inst.Convert(context.Background(), pj.job)
		pj.result <- jobResult{pdf: pdf, err: err}

		if inst.NeedsRestart() {
			// TODO: restart instance and replace in pool
			p.logger.Info("instance restart needed", "port", inst.process.Port())
		}
	}
}
