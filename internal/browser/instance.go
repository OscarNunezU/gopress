package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/OscarNunezU/gopress/internal/cdp"
	"github.com/OscarNunezU/gopress/internal/telemetry"
)

// Instance wraps a Chromium process and its CDP client.
// One instance handles one conversion at a time.
type Instance struct {
	process        *Process
	conversions    int
	maxConversions int
	logger         *slog.Logger
}

// NewInstance starts a Chromium process and returns a ready Instance.
func NewInstance(ctx context.Context, binPath string, port int, maxConversions int, logger *slog.Logger) (*Instance, error) {
	proc, err := Start(ctx, binPath, port, logger)
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
	ctx, span := telemetry.Tracer().Start(ctx, "browser.convert")
	defer span.End()

	// --- dial CDP ---
	dialCtx, dialSpan := telemetry.Tracer().Start(ctx, "browser.dial_cdp")
	client, targetID, err := i.dialCDP(dialCtx)
	dialSpan.End()
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		return nil, fmt.Errorf("dial cdp: %w", err)
	}
	defer func() {
		_ = cdp.CloseTarget(ctx, client, targetID)
		_ = client.Close()
	}()

	// Enable required CDP domains.
	if err := cdp.EnablePage(ctx, client); err != nil {
		span.SetStatus(codes.Error, err.Error())
		return nil, fmt.Errorf("enable Page domain: %w", err)
	}
	if err := cdp.EnableLifecycleEvents(ctx, client); err != nil {
		span.SetStatus(codes.Error, err.Error())
		return nil, fmt.Errorf("enable lifecycle events: %w", err)
	}
	if err := cdp.EnableNetwork(ctx, client); err != nil {
		span.SetStatus(codes.Error, err.Error())
		return nil, fmt.Errorf("enable Network domain: %w", err)
	}

	// --- load HTML ---
	// IMPORTANT: subscribe to page events BEFORE triggering any navigation or
	// content injection to eliminate the race where the event fires before we
	// start listening, which would cause the conversion to hang until timeout.
	loadCtx, loadSpan := telemetry.Tracer().Start(ctx, "browser.load_html")
	loadSpan.SetAttributes(attribute.Bool("has_assets", len(job.Assets) > 0))

	var loadErr error
	if len(job.Assets) > 0 {
		loadErr = i.convertWithAssets(loadCtx, client, job)
	} else {
		// Subscribe before injecting content.
		loadCh := client.Subscribe("Page.loadEventFired")
		if loadErr = cdp.SetDocumentContent(loadCtx, client, job.HTML); loadErr == nil {
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
	result, err := cdp.PrintToPDF(pdfCtx, client, params)
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

// NeedsRestart reports whether the instance has exceeded its conversion quota.
func (i *Instance) NeedsRestart() bool {
	return i.maxConversions > 0 && i.conversions >= i.maxConversions
}

// Close kills the underlying Chromium process.
func (i *Instance) Close() error {
	return i.process.Kill()
}

// dialCDP connects a CDP client to a new tab on this instance.
func (i *Instance) dialCDP(ctx context.Context) (*cdp.Client, string, error) {
	host := fmt.Sprintf("localhost:%d", i.process.Port())

	wsURL, targetID, err := newTarget(host)
	if err != nil {
		return nil, "", fmt.Errorf("new cdp target: %w", err)
	}

	client, err := cdp.Dial(ctx, wsURL, i.logger)
	if err != nil {
		return nil, "", fmt.Errorf("cdp dial %s: %w", wsURL, err)
	}

	return client, targetID, nil
}

// convertWithAssets starts a temporary HTTP server to serve assets, subscribes
// to lifecycle events, navigates Chromium, then waits for network idle.
//
// Subscribing BEFORE Navigate is mandatory: if the page loads before Subscribe
// is called, the networkIdle event is never received and the conversion hangs.
func (i *Instance) convertWithAssets(ctx context.Context, client *cdp.Client, job *Job) error {
	files := make(map[string][]byte, len(job.Assets)+1)
	files["index.html"] = []byte(job.HTML)
	for name, data := range job.Assets {
		files[name] = data
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("asset server listen: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

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
		w.Write(data) //nolint:errcheck
	})

	srv := &http.Server{Handler: mux}
	go srv.Serve(ln) //nolint:errcheck
	defer srv.Close()

	// Subscribe BEFORE triggering navigation — avoids the race condition.
	lifecycleCh := client.Subscribe("Page.lifecycleEvent")

	url := fmt.Sprintf("http://127.0.0.1:%d/", port)
	if err := cdp.Navigate(ctx, client, url); err != nil {
		return fmt.Errorf("navigate to asset server: %w", err)
	}

	return waitForNetworkIdle(ctx, lifecycleCh)
}

// waitForEvent blocks until one event arrives on ch or the context is cancelled.
// Used for Page.loadEventFired where the page has no external resources.
func waitForEvent(ctx context.Context, ch <-chan cdp.Event) error {
	select {
	case <-ctx.Done():
		return fmt.Errorf("wait for page load: %w", ctx.Err())
	case <-ch:
		return nil
	}
}

// waitForNetworkIdle blocks until Page.lifecycleEvent fires with name="networkIdle"
// or the context is cancelled. Using networkIdle instead of a fixed settle delay
// ensures all sub-resources (fonts, images, CSS) are fully loaded before printing.
func waitForNetworkIdle(ctx context.Context, ch <-chan cdp.Event) error {
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for network idle: %w", ctx.Err())
		case evt := <-ch:
			if name, _ := evt.Params["name"].(string); name == "networkIdle" {
				return nil
			}
		}
	}
}

// newTarget calls /json/new on the browser and returns (wsURL, targetID).
// Chrome 113+ requires PUT; GET was deprecated and returns a plain-text warning.
func newTarget(host string) (wsURL string, targetID string, err error) {
	req, err := http.NewRequest(http.MethodPut, "http://"+host+"/json/new", nil)
	if err != nil {
		return "", "", fmt.Errorf("build new-target request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req) //nolint:noctx
	if err != nil {
		return "", "", fmt.Errorf("create target: %w", err)
	}
	defer resp.Body.Close()

	var target struct {
		ID                   string `json:"id"`
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&target); err != nil {
		return "", "", fmt.Errorf("decode target: %w", err)
	}
	return target.WebSocketDebuggerURL, target.ID, nil
}
