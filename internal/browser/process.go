package browser

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	// readyTimeout acota cuánto se espera a que Chromium acepte conexiones.
	//
	// Va acá dentro y no en el contexto que recibe Start por una razón que costó
	// un servicio caído: ese contexto se le pasa a exec.CommandContext, que MATA
	// el proceso cuando se cancela. Un contexto con deadline para «esperar el
	// arranque» es, sin querer, un contexto que le pone fecha de muerte al
	// navegador.
	readyTimeout = 30 * time.Second

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
	stderr *tailBuffer
}

// tailBuffer guarda los últimos bytes de un flujo, descartando lo anterior.
//
// Chromium escribe mucho en stderr y casi todo es ruido; lo que sirve es lo
// último que dijo antes de morir. Sin esto `cmd.Stderr` quedaba en nil, o sea
// en /dev/null, y un navegador que no arrancaba no dejaba ninguna pista — el
// mismo problema que tenía el API al descartar el error de gopress.
type tailBuffer struct {
	mu   sync.Mutex
	buf  []byte
	size int
}

func newTailBuffer(size int) *tailBuffer { return &tailBuffer{size: size} }

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.size {
		b.buf = b.buf[len(b.buf)-b.size:]
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(string(b.buf))
}

// Start launches a Chromium process on the given port and waits until the
// remote debugging endpoint is ready to accept connections.
//
// `ctx` gobierna la VIDA del proceso, no el arranque: exec.CommandContext mata
// a Chromium en cuanto se cancela. Quien llame tiene que pasar un contexto que
// dure lo que deba durar el navegador —el del proceso servidor—, nunca uno con
// deadline. La espera del arranque se acota acá dentro, en readyTimeout.
func Start(ctx context.Context, binPath string, port int, logger *slog.Logger) (*Process, error) {
	flags := append(chromeFlags, fmt.Sprintf("--remote-debugging-port=%d", port))
	cmd := exec.CommandContext(ctx, binPath, flags...)
	stderr := newTailBuffer(4096)
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start chromium on port %d: %w", port, err)
	}

	p := &Process{cmd: cmd, port: port, logger: logger, stderr: stderr}
	logger.Info("chromium started", "pid", cmd.Process.Pid, "port", port)

	readyCtx, cancel := context.WithTimeout(ctx, readyTimeout)
	defer cancel()
	if err := p.waitReady(readyCtx); err != nil {
		// Lo último que dijo Chromium va en el error. Es la diferencia entre
		// «no arrancó» y saber por qué no arrancó.
		if last := stderr.String(); last != "" {
			err = fmt.Errorf("%w (chromium dijo: %s)", err, last)
		}
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
// A single Timer is reused across iterations to avoid the GC-unsafe pattern
// of creating a new time.After channel on every loop iteration.
func (p *Process) waitReady(ctx context.Context) error {
	url := fmt.Sprintf("http://localhost:%d/json/version", p.port)
	delay := waitBackoffInit

	t := time.NewTimer(delay)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("chromium port %d not ready: %w", p.port, ctx.Err())
		case <-t.C:
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				return fmt.Errorf("build readiness request: %w", err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err == nil {
				_ = resp.Body.Close()
				p.logger.Debug("chromium ready", "port", p.port)
				return nil
			}
			delay *= waitBackoffMult
			if delay > waitBackoffMax {
				delay = waitBackoffMax
			}
			t.Reset(delay)
		}
	}
}
