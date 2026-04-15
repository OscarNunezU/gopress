// bench/bench.go — gopress load benchmark
//
// Measures HTML→PDF conversion latency and throughput against any
// multipart/form-data PDF service (gopress, Gotenberg, etc.).
//
// Usage:
//
//	go run ./bench                          # simple HTML, 4 VUs, 30s, gopress
//	go run ./bench -doc=complex             # 50-row invoice table
//	go run ./bench -all                     # simple + complex back to back
//	go run ./bench -vus=8 -duration=60s     # custom load
//
//	# Compare with Gotenberg (requires Gotenberg running on :3010)
//	go run ./bench -target=http://localhost:3010 \
//	               -endpoint=/forms/chromium/convert/html \
//	               -field=files -all
package main

import (
	"bytes"
	"flag"
	"fmt"
	"mime/multipart"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ---------------------------------------------------------------------------
// HTML documents
// ---------------------------------------------------------------------------

// simpleHTML is a minimal one-page invoice — equivalent to the "simple"
// document used in the pdf4.dev 2026 benchmark.
const simpleHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<style>
  body   { font-family: Arial, sans-serif; margin: 40px; color: #333; }
  h1     { font-size: 24px; margin-bottom: 8px; }
  .meta  { color: #666; font-size: 14px; margin-bottom: 24px; }
  .amount{ font-size: 32px; font-weight: bold; color: #111; }
  .badge { display:inline-block; background:#22c55e; color:#fff;
           padding:4px 12px; border-radius:4px; font-size:12px; }
</style>
</head>
<body>
  <h1>Invoice #1234</h1>
  <p class="meta">Issued: 2026-04-15 &bull; Due: 2026-05-15 &bull; Customer: ACME Corp</p>
  <p class="amount">$1,000.00</p>
  <span class="badge">PAID</span>
</body>
</html>`

// complexHTMLDoc generates a 50-row invoice table — equivalent to the
// "complex" document used in the pdf4.dev 2026 benchmark.
func complexHTMLDoc() string {
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<style>
  body { font-family: Arial, sans-serif; margin: 40px; color: #333; font-size: 13px; }
  h1   { font-size: 22px; margin-bottom: 4px; }
  .meta{ color: #666; font-size: 13px; margin-bottom: 20px; }
  table{ width: 100%; border-collapse: collapse; margin-top: 16px; }
  th   { background: #1e40af; color: #fff; padding: 8px 12px; text-align: left; }
  td   { padding: 7px 12px; border-bottom: 1px solid #e5e7eb; }
  tr.alt td { background: #f8fafc; }
  .total { text-align: right; font-weight: bold; font-size: 16px; margin-top: 16px; }
</style>
</head>
<body>
  <h1>Invoice #1234</h1>
  <p class="meta">Issued: 2026-04-15 &bull; Due: 2026-05-15 &bull; Customer: ACME Corp &bull; PO: PO-9876</p>
  <table>
    <thead><tr><th>#</th><th>Description</th><th>Qty</th><th>Unit Price</th><th>Total</th></tr></thead>
    <tbody>
`)
	var total float64
	for i := 1; i <= 50; i++ {
		qty := i%5 + 1
		unit := float64(10 + i*3)
		line := float64(qty) * unit
		total += line
		alt := ""
		if i%2 == 0 {
			alt = ` class="alt"`
		}
		fmt.Fprintf(&b, "      <tr%s><td>%d</td><td>Widget Model %02d &mdash; Premium Edition</td><td>%d</td><td>$%.2f</td><td>$%.2f</td></tr>\n",
			alt, i, i, qty, unit, line)
	}
	fmt.Fprintf(&b, `    </tbody>
  </table>
  <p class="total">Grand Total: $%.2f</p>
</body>
</html>`, total)
	return b.String()
}

// ---------------------------------------------------------------------------
// HTTP helpers
// ---------------------------------------------------------------------------

func buildBody(fieldName, html string) ([]byte, string) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, _ := w.CreateFormFile(fieldName, "index.html")
	fw.Write([]byte(html)) //nolint:errcheck
	w.Close()
	return buf.Bytes(), w.FormDataContentType()
}

// ---------------------------------------------------------------------------
// Benchmark runner
// ---------------------------------------------------------------------------

type sample struct {
	ms  float64
	err bool
}

type cfg struct {
	target   string
	endpoint string
	field    string
	vus      int
	duration time.Duration
	warmup   time.Duration
	doc      string
}

func run(c cfg) {
	html := simpleHTML
	if c.doc == "complex" {
		html = complexHTMLDoc()
	}

	body, ct := buildBody(c.field, html)
	url := c.target + c.endpoint
	client := &http.Client{Timeout: 30 * time.Second}

	doReq := func() (time.Duration, error) {
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return 0, err
		}
		req.Header.Set("Content-Type", ct)
		t0 := time.Now()
		resp, err := client.Do(req)
		elapsed := time.Since(t0)
		if err != nil {
			return elapsed, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return elapsed, fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		return elapsed, nil
	}

	// warmup — excluded from results
	fmt.Printf("  warming up (%s, %d VU) ...", c.warmup, c.vus)
	var wg sync.WaitGroup
	wDeadline := time.Now().Add(c.warmup)
	for range c.vus {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(wDeadline) {
				doReq() //nolint:errcheck
			}
		}()
	}
	wg.Wait()
	fmt.Println(" done")

	// load test
	fmt.Printf("  load test  (%s, %d VUs) ...", c.duration, c.vus)
	ch := make(chan sample, 50000)
	var okCount atomic.Int64
	start := time.Now()
	deadline := start.Add(c.duration)

	for range c.vus {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(deadline) {
				d, err := doReq()
				if err != nil {
					ch <- sample{ms: float64(d.Milliseconds()), err: true}
				} else {
					ch <- sample{ms: float64(d.Milliseconds())}
					okCount.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)
	close(ch)
	fmt.Println(" done")

	// collect
	var ms []float64
	var errCount int
	for s := range ch {
		if s.err {
			errCount++
		} else {
			ms = append(ms, s.ms)
		}
	}

	n := len(ms)
	if n == 0 {
		fmt.Println("\n  ✗  no successful requests — is the service running?")
		return
	}
	sort.Float64s(ms)

	pct := func(p float64) float64 {
		idx := int(float64(n)*p/100 + 0.5)
		if idx >= n {
			idx = n - 1
		}
		return ms[idx]
	}
	rps := float64(n) / elapsed.Seconds()

	fmt.Printf("\n  %-9s  %7s  %7s  %7s  %7s   %8s  errors\n", "document", "p50", "p95", "p99", "max", "RPS")
	fmt.Printf("  %-9s  %6.0fms  %6.0fms  %6.0fms  %6.0fms   %8.1f  %d\n\n",
		c.doc,
		pct(50), pct(95), pct(99), ms[n-1],
		rps, errCount,
	)
}

// ---------------------------------------------------------------------------
// main
// ---------------------------------------------------------------------------

func main() {
	target := flag.String("target", "http://localhost:3000", "Service base URL")
	endpoint := flag.String("endpoint", "/pdf", "HTTP endpoint path (gopress: /pdf  Gotenberg: /forms/chromium/convert/html)")
	field := flag.String("field", "index.html", "Multipart form field name (gopress: index.html  Gotenberg: files)")
	vus := flag.Int("vus", 4, "Concurrent virtual users (match GOPRESS_POOL_SIZE for a fair test)")
	duration := flag.Duration("duration", 30*time.Second, "Load-test duration (warmup excluded)")
	warmup := flag.Duration("warmup", 5*time.Second, "Warmup duration — lets Chrome pool reach steady state")
	doc := flag.String("doc", "simple", "Document type: simple | complex")
	all := flag.Bool("all", false, "Run simple and complex documents back to back")
	flag.Parse()

	fmt.Printf("\n╔══════════════════════════════════════════════╗\n")
	fmt.Printf("║            gopress  benchmark                ║\n")
	fmt.Printf("╚══════════════════════════════════════════════╝\n")
	fmt.Printf("  target   %s%s\n", *target, *endpoint)
	fmt.Printf("  vus      %d\n", *vus)
	fmt.Printf("  duration %s  (+ %s warmup)\n\n", *duration, *warmup)

	base := cfg{
		target:   *target,
		endpoint: *endpoint,
		field:    *field,
		vus:      *vus,
		duration: *duration,
		warmup:   *warmup,
	}

	if *all {
		for _, d := range []string{"simple", "complex"} {
			fmt.Printf("── %s document ─────────────────────────────\n", d)
			c := base
			c.doc = d
			run(c)
		}
		return
	}

	fmt.Printf("── %s document ─────────────────────────────\n", *doc)
	c := base
	c.doc = *doc
	run(c)

	_ = os.Args // satisfy linter
}
