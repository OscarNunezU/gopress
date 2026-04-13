package browser

import (
	"fmt"
	"log/slog"
	"os/exec"
)

// chromeFlags are the Chromium flags required for headless PDF generation.
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

// Start launches a Chromium process on the given port.
func Start(binPath string, port int, logger *slog.Logger) (*Process, error) {
	flags := append(chromeFlags, fmt.Sprintf("--remote-debugging-port=%d", port))
	cmd := exec.Command(binPath, flags...)

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start chromium on port %d: %w", port, err)
	}

	logger.Info("chromium started", "pid", cmd.Process.Pid, "port", port)
	return &Process{cmd: cmd, port: port, logger: logger}, nil
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
