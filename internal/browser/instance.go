package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/OscarNunezU/gopress/internal/cdp"
	"github.com/OscarNunezU/gopress/internal/telemetry"
)

// browserClient is the subset of cdp.Client used by Instance.
// Defined as an interface to allow testing without a real Chrome process.
type browserClient interface {
	Send(ctx context.Context, method string, params any, result any) error
	Subscribe(method string) <-chan cdp.Event
	NewSession(sessionID string) *cdp.Session
	IsClosed() bool
	Close() error
}

// Instance wraps a Chromium process and a single persistent CDP connection.
// One Instance handles one conversion at a time; each conversion opens and
// closes a tab session over the shared WebSocket connection — no new TCP
// connections or WebSocket handshakes per conversion.
type Instance struct {
	process        *Process
	client         browserClient // persistent browser-level connection
	conversions    int
	maxConversions int
	logger         *slog.Logger
}

// NewInstance starts a Chromium process, dials the browser-level CDP WebSocket,
// and returns a ready Instance.
func NewInstance(ctx context.Context, binPath string, port int, maxConversions int, logger *slog.Logger) (*Instance, error) {
	proc, err := Start(ctx, binPath, port, logger)
	if err != nil {
		return nil, fmt.Errorf("new instance: %w", err)
	}

	client, err := dialBrowser(ctx, port, logger)
	if err != nil {
		_ = proc.Kill()
		return nil, fmt.Errorf("dial browser: %w", err)
	}

	return &Instance{
		process:        proc,
		client:         client,
		maxConversions: maxConversions,
		logger:         logger,
	}, nil
}

// Convert opens a new tab session, runs the conversion job, and closes the session.
// The underlying Chrome process and WebSocket connection are reused across conversions.
func (i *Instance) Convert(ctx context.Context, job *Job) ([]byte, error) {
	ctx, span := telemetry.Tracer().Start(ctx, "browser.convert")
	defer span.End()

	// --- open tab session ---
	dialCtx, dialSpan := telemetry.Tracer().Start(ctx, "browser.dial_cdp")
	session, targetID, err := i.openSession(dialCtx)
	dialSpan.End()
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		return nil, fmt.Errorf("open session: %w", err)
	}
	defer func() {
		_ = cdp.CloseTarget(ctx, i.client, targetID)
		session.Close()
	}()

	// Enable required CDP domains on the session (tab-scoped).
	if err := cdp.EnablePage(ctx, session); err != nil {
		span.SetStatus(codes.Error, err.Error())
		return nil, fmt.Errorf("enable Page domain: %w", err)
	}
	if err := cdp.EnableNetwork(ctx, session); err != nil {
		span.SetStatus(codes.Error, err.Error())
		return nil, fmt.Errorf("enable Network domain: %w", err)
	}

	// --- load HTML ---
	// Subscribe BEFORE triggering navigation or content injection to eliminate
	// the race where the event fires before we start listening.
	loadCtx, loadSpan := telemetry.Tracer().Start(ctx, "browser.load_html")
	loadSpan.SetAttributes(attribute.Bool("has_assets", len(job.Assets) > 0))

	var loadErr error
	if len(job.Assets) > 0 {
		loadErr = i.convertWithAssets(loadCtx, session, job)
	} else {
		loadCh := session.Subscribe("Page.loadEventFired")
		if loadErr = cdp.SetDocumentContent(loadCtx, session, job.HTML); loadErr == nil {
			loadErr = waitForEvent(loadCtx, loadCh)
		}
	}

	if loadErr != nil {
		loadSpan.SetStatus(codes.Error, loadErr.Error())
		loadSpan.RecordError(loadErr)
		loadSpan.End()
		span.SetStatus(codes.Error, loadErr.Error())
		return nil, loadErr
	}
	loadSpan.End()

	// --- print to PDF ---
	pdfCtx, pdfSpan := telemetry.Tracer().Start(ctx, "browser.print_pdf")
	params := cdp.PrintToPDFParams{
		Landscape:           job.Options.Landscape,
		PrintBackground:     job.Options.PrintBackground,
		Scale:               job.Options.Scale,
		PaperWidth:          job.Options.PaperWidth,
		PaperHeight:         job.Options.PaperHeight,
		MarginTop:           job.Options.MarginTop,
		MarginBottom:        job.Options.MarginBottom,
		MarginLeft:          job.Options.MarginLeft,
		MarginRight:         job.Options.MarginRight,
		PreferCSSPageSize:   job.Options.PreferCSSPageSize,
		DisplayHeaderFooter: job.Options.DisplayHeaderFooter,
		HeaderTemplate:      job.Options.HeaderTemplate,
		FooterTemplate:      job.Options.FooterTemplate,
		PageRanges:          job.Options.PageRanges,
		TransferMode:        "ReturnAsBase64",
	}
	result, err := cdp.PrintToPDF(pdfCtx, session, params)
	pdfSpan.End()
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		return nil, fmt.Errorf("print to pdf: %w", err)
	}

	pdf, err := base64.StdEncoding.DecodeString(result.Data)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return nil, fmt.Errorf("decode pdf base64: %w", err)
	}

	span.SetAttributes(attribute.Int("pdf.size_bytes", len(pdf)))
	i.conversions++
	return pdf, nil
}

// NeedsRestart reports whether the instance should be replaced. This is true
// when Chrome has crashed (detected via a closed CDP connection) or when the
// conversion quota has been reached (planned memory-hygiene restart).
func (i *Instance) NeedsRestart() bool {
	return i.HasCrashed() || (i.maxConversions > 0 && i.conversions >= i.maxConversions)
}

// HasCrashed reports whether the underlying Chrome process or CDP connection
// died unexpectedly. The pool uses this to distinguish a crash restart
// ("crash") from a quota restart ("max_conversions") in metrics.
func (i *Instance) HasCrashed() bool {
	return i.client.IsClosed()
}

// Close kills the Chromium process and closes the persistent CDP connection.
func (i *Instance) Close() error {
	_ = i.client.Close()
	return i.process.Kill()
}

// dialBrowser dials the browser-level CDP WebSocket obtained from /json/version.
// This connection is reused across all conversions in the lifetime of the Instance.
func dialBrowser(ctx context.Context, port int, logger *slog.Logger) (*cdp.Client, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://localhost:%d/json/version", port), nil)
	if err != nil {
		return nil, fmt.Errorf("build /json/version request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get /json/version: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	var info struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("decode /json/version: %w", err)
	}
	if info.WebSocketDebuggerURL == "" {
		return nil, fmt.Errorf("webSocketDebuggerUrl is empty (is --remote-debugging-port set?)")
	}

	return cdp.Dial(ctx, info.WebSocketDebuggerURL, logger)
}

// openSession creates a new browser tab and attaches a flat-mode CDP session to it.
// Returns the session and the target ID needed to close the tab later.
//
// Flat-mode (flatten:true) means all session messages use the sessionId field
// directly on the CDP message envelope — no Target.sendMessageToTarget wrapping.
func (i *Instance) openSession(ctx context.Context) (*cdp.Session, string, error) {
	var createResult struct {
		TargetID string `json:"targetId"`
	}
	if err := i.client.Send(ctx, "Target.createTarget", map[string]any{"url": "about:blank"}, &createResult); err != nil {
		return nil, "", fmt.Errorf("create target: %w", err)
	}

	var attachResult struct {
		SessionID string `json:"sessionId"`
	}
	if err := i.client.Send(ctx, "Target.attachToTarget", map[string]any{
		"targetId": createResult.TargetID,
		"flatten":  true,
	}, &attachResult); err != nil {
		return nil, "", fmt.Errorf("attach to target: %w", err)
	}

	return i.client.NewSession(attachResult.SessionID), createResult.TargetID, nil
}

// convertWithAssets starts a temporary HTTP server, subscribes to lifecycle events
// BEFORE navigating, then waits for network idle.
func (i *Instance) convertWithAssets(ctx context.Context, session cdp.Sender, job *Job) error {
	files := make(map[string][]byte, len(job.Assets)+1)
	files["index.html"] = []byte(job.HTML)
	for name, data := range job.Assets {
		files[name] = data
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0") //nolint:noctx
	if err != nil {
		return fmt.Errorf("asset server listen: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port //nolint:errcheck

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Path[1:]
		if name == "" {
			name = "index.html"
		}
		data, ok := files[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		ct := mime.TypeByExtension(filepath.Ext(name))
		if ct == "" {
			ct = "application/octet-stream"
		}
		w.Header().Set("Content-Type", ct)
		w.Write(data) //nolint:errcheck
	})

	srv := &http.Server{
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	go srv.Serve(ln) //nolint:errcheck
	defer srv.Close() //nolint:errcheck

	// Subscribe BEFORE navigating — avoids the race condition.
	loadCh := session.Subscribe("Page.loadEventFired")

	url := fmt.Sprintf("http://127.0.0.1:%d/", port)
	if err := cdp.Navigate(ctx, session, url); err != nil {
		return fmt.Errorf("navigate to asset server: %w", err)
	}

	return waitForEvent(ctx, loadCh)
}

// waitForEvent blocks until one event arrives on ch or the context is cancelled.
func waitForEvent(ctx context.Context, ch <-chan cdp.Event) error {
	select {
	case <-ctx.Done():
		return fmt.Errorf("wait for page load: %w", ctx.Err())
	case <-ch:
		return nil
	}
}

