package browser

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/OscarNunezU/gopress/internal/cdp"
)

// Instance wraps a Chromium process and its CDP client.
// One instance handles one conversion at a time.
type Instance struct {
	process      *Process
	conversions  int
	maxConversions int
	logger       *slog.Logger
}

// NewInstance starts a Chromium process and returns a ready Instance.
func NewInstance(binPath string, port int, maxConversions int, logger *slog.Logger) (*Instance, error) {
	proc, err := Start(binPath, port, logger)
	if err != nil {
		return nil, fmt.Errorf("new instance: %w", err)
	}
	return &Instance{
		process:        proc,
		maxConversions: maxConversions,
		logger:         logger,
	}, nil
}

// Convert opens a new tab, runs the conversion job, and closes the tab.
func (i *Instance) Convert(ctx context.Context, job *Job) ([]byte, error) {
	// TODO: dial CDP, enable domains, load HTML, print PDF, close tab
	_ = ctx
	_ = job
	i.conversions++
	return nil, fmt.Errorf("not implemented")
}

// NeedsRestart reports whether the instance has exceeded its conversion quota.
func (i *Instance) NeedsRestart() bool {
	return i.maxConversions > 0 && i.conversions >= i.maxConversions
}

// Close kills the underlying Chromium process.
func (i *Instance) Close() error {
	return i.process.Kill()
}

// dialCDP connects a CDP client to a new tab on this instance.
func (i *Instance) dialCDP(ctx context.Context) (*cdp.Client, error) {
	host := fmt.Sprintf("localhost:%d", i.process.Port())
	// TODO: wait for port to be ready (retry loop)
	_ = host
	return nil, fmt.Errorf("not implemented")
}
