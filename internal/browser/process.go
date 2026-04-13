package browser

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os/exec"
	"time"
)

// chromeFlags are the flags required for headless PDF generation.
var chromeFlags = []string{
	"--headless=new",
	"--no-sandbox",
	"--disable-gpu",
	"--disable-dev-shm-usage",
	"--disable-extensions",
	"--disable-background-networking",
	"--disable-sync",
	"--no-first-run",
	"--no-default-browser-check",
}

// Process represents a running Chromium process bound to a debug port.
type Process struct {
	cmd    *exec.Cmd
	port   int
	logger *slog.Logger
}

// Start launches a Chromium process on the given port and waits until the
// remote debugging endpoint is ready to accept connections.
func Start(ctx context.Context, binPath string, port int, logger *slog.Logger) (*Process, error) {
	flags := append(chromeFlags, fmt.Sprintf("--remote-debugging-port=%d", port))
	cmd := exec.CommandContext(ctx, binPath, flags...)

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start chromium on port %d: %w", port, err)
	}

	p := &Process{cmd: cmd, port: port, logger: logger}
	logger.Info("chromium started", "pid", cmd.Process.Pid, "port", port)

	if err := p.waitReady(ctx); err != nil {
		_ = p.Kill()
		return nil, err
	}

	return p, nil
}

// Port returns the remote debugging port of this process.
func (p *Process) Port() int {
	return p.port
}

// Kill terminates the Chromium process.
func (p *Process) Kill() error {
	if p.cmd.Process == nil {
		return nil
	}
	if err := p.cmd.Process.Kill(); err != nil {
		return fmt.Errorf("kill chromium pid %d: %w", p.cmd.Process.Pid, err)
	}
	p.logger.Info("chromium stopped", "pid", p.cmd.Process.Pid)
	return nil
}

// waitReady polls the /json/version endpoint until Chromium is accepting
// connections or the context is cancelled.
func (p *Process) waitReady(ctx context.Context) error {
	url := fmt.Sprintf("http://localhost:%d/json/version", p.port)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("chromium port %d not ready: %w", p.port, ctx.Err())
		case <-ticker.C:
			resp, err := http.Get(url) //nolint:noctx
			if err == nil {
				resp.Body.Close()
				p.logger.Debug("chromium ready", "port", p.port)
				return nil
			}
		}
	}
}
