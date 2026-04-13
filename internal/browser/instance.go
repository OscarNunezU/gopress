package browser

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net"
	"encoding/json"
	"net/http"
	"time"

	"github.com/OscarNunezU/gopress/internal/cdp"
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
	client, targetID, err := i.dialCDP(ctx)
	if err != nil {
		return nil, fmt.Errorf("dial cdp: %w", err)
	}
	defer func() {
		_ = cdp.CloseTarget(ctx, client, targetID)
		_ = client.Close()
	}()

	// Enable required CDP domains.
	if err := cdp.EnablePage(ctx, client); err != nil {
		return nil, fmt.Errorf("enable Page domain: %w", err)
	}
	if err := cdp.EnableNetwork(ctx, client); err != nil {
		return nil, fmt.Errorf("enable Network domain: %w", err)
	}

	// Serve assets and HTML from a local HTTP server if there are assets,
	// otherwise inject HTML directly via Page.setDocumentContent.
	if len(job.Assets) > 0 {
		if err := i.convertWithAssets(ctx, client, job); err != nil {
			return nil, err
		}
	} else {
		if err := cdp.SetDocumentContent(ctx, client, job.HTML); err != nil {
			return nil, fmt.Errorf("set document content: %w", err)
		}
		if err := waitForLoad(ctx, client); err != nil {
			return nil, err
		}
	}

	// Generate PDF.
	params := cdp.PrintToPDFParams{
		Landscape:         job.Options.Landscape,
		PrintBackground:   job.Options.PrintBackground,
		Scale:             job.Options.Scale,
		PaperWidth:        job.Options.PaperWidth,
		PaperHeight:       job.Options.PaperHeight,
		MarginTop:         job.Options.MarginTop,
		MarginBottom:      job.Options.MarginBottom,
		MarginLeft:        job.Options.MarginLeft,
		MarginRight:       job.Options.MarginRight,
		PreferCSSPageSize: job.Options.PreferCSSPageSize,
		TransferMode:      "ReturnAsBase64",
	}

	result, err := cdp.PrintToPDF(ctx, client, params)
	if err != nil {
		return nil, fmt.Errorf("print to pdf: %w", err)
	}

	pdf, err := base64.StdEncoding.DecodeString(result.Data)
	if err != nil {
		return nil, fmt.Errorf("decode pdf base64: %w", err)
	}

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
// Returns the client, the target ID (for closing the tab later), and any error.
func (i *Instance) dialCDP(ctx context.Context) (*cdp.Client, string, error) {
	host := fmt.Sprintf("localhost:%d", i.process.Port())

	// Open a new target (tab) and get its WebSocket URL.
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

// convertWithAssets starts a temporary HTTP server to serve assets, navigates
// Chromium to it, and waits for the page to fully load.
func (i *Instance) convertWithAssets(ctx context.Context, client *cdp.Client, job *Job) error {
	// Build an in-memory file map: inject index.html + all assets.
	files := make(map[string][]byte, len(job.Assets)+1)
	files["index.html"] = []byte(job.HTML)
	for name, data := range job.Assets {
		files[name] = data
	}

	// Pick a free local port for the asset server.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("asset server listen: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Path[1:] // strip leading "/"
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

	url := fmt.Sprintf("http://127.0.0.1:%d/", port)
	if err := cdp.Navigate(ctx, client, url); err != nil {
		return fmt.Errorf("navigate to asset server: %w", err)
	}
	return waitForLoad(ctx, client)
}

// waitForLoad blocks until Page.loadEventFired or context cancellation.
func waitForLoad(ctx context.Context, client *cdp.Client) error {
	ch := client.Subscribe("Page.loadEventFired")
	select {
	case <-ctx.Done():
		return fmt.Errorf("wait for page load: %w", ctx.Err())
	case <-ch:
		// Small settle delay to let late-loading resources finish.
		select {
		case <-ctx.Done():
		case <-time.After(50 * time.Millisecond):
		}
		return nil
	}
}

// newTarget calls /json/new on the browser and returns (wsURL, targetID).
func newTarget(host string) (wsURL string, targetID string, err error) {
	resp, err := http.Get("http://" + host + "/json/new") //nolint:noctx
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
