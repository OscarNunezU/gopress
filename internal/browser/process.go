package browser

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os/exec"
	"time"
)

const (
	waitBackoffInit = 10 * time.Millisecond
	waitBackoffMax  = 200 * time.Millisecond
	waitBackoffMult = 2
)

// chromeFlags are the flags required for headless PDF generation.
var chromeFlags = []string{
	"--headless=new",
	// --no-sandbox disables Chrome's internal process sandbox.
	// This is the standard practice for containerised Chrome (used by Puppeteer,
	// Playwright, and chromedp). The container itself — running as non-root UID 1001
	// with its own Linux namespaces — provides equivalent isolation. Chrome's sandbox
	// requires CLONE_NEWPID/CLONE_NEWUSER, which are typically unavailable inside a
	// container without elevated privileges.
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
	pid := p.cmd.Process.Pid
	if err := p.cmd.Process.Kill(); err != nil {
		return fmt.Errorf("kill chromium pid %d: %w", pid, err)
	}
	// Wait collects the exit status so the OS can reclaim the process table entry.
	_ = p.cmd.Wait()
	p.logger.Info("chromium stopped", "pid", pid)
	return nil
}

// waitReady polls the /json/version endpoint until Chromium is accepting
// connections or the context is cancelled.
// It uses exponential backoff (10ms→200ms) to avoid spinning during startup.
func (p *Process) waitReady(ctx context.Context) error {
	url := fmt.Sprintf("http://localhost:%d/json/version", p.port)
	delay := waitBackoffInit

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("chromium port %d not ready: %w", p.port, ctx.Err())
		case <-time.After(delay):
			resp, err := http.Get(url) //nolint:noctx
			if err == nil {
				resp.Body.Close()
				p.logger.Debug("chromium ready", "port", p.port)
				return nil
			}
			delay *= waitBackoffMult
			if delay > waitBackoffMax {
				delay = waitBackoffMax
			}
		}
	}
}
