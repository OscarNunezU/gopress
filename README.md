# gopress

A minimal, production-ready HTML-to-PDF microservice in pure Go, powered by Chromium.

gopress does one thing: convert HTML to PDF using the Chrome DevTools Protocol. No external CDP libraries — the WebSocket client is implemented from scratch against [RFC 6455](https://datatracker.ietf.org/doc/html/rfc6455).

## Features

- **Single endpoint** — `POST /pdf` with multipart form data
- **Asset support** — serve CSS, images, and fonts alongside your HTML
- **Browser pool** — N Chromium instances with automatic restart after M conversions
- **Observability** — Prometheus metrics and OpenTelemetry tracing out of the box
- **Air-gapped Docker** — pinned Chrome for Testing binary ships in the image, no runtime downloads
- **Non-root** — runs as an unprivileged user inside the container

## Quick start

```bash
docker run --rm -p 3000:3000 ghcr.io/oscarnunezu/gopress:latest
```

Convert an HTML file:

```bash
curl -s -X POST http://localhost:3000/pdf \
  -F "index.html=@report.html" \
  -o report.pdf
```

With assets and PDF options:

```bash
curl -s -X POST http://localhost:3000/pdf \
  -F "index.html=@report.html" \
  -F "style.css=@style.css" \
  -F "logo.png=@logo.png" \
  -F 'options.json={"landscape":true,"printBackground":true,"paperWidth":11,"paperHeight":8.5}' \
  -o report.pdf
```

## API

### `POST /pdf`

Accepts `multipart/form-data`. Returns `application/pdf` on success.

| Field | Required | Description |
|-------|----------|-------------|
| `index.html` | yes | HTML document to render |
| `<any filename>` | no | Asset files (CSS, images, fonts). Referenced from HTML by filename. |
| `options.json` | no | PDF options (see below) |

**`options.json` fields** (all optional, Chromium defaults apply when omitted):

```json
{
  "landscape": false,
  "printBackground": false,
  "scale": 1.0,
  "paperWidth": 8.5,
  "paperHeight": 11.0,
  "marginTop": 0.4,
  "marginBottom": 0.4,
  "marginLeft": 0.4,
  "marginRight": 0.4,
  "preferCSSPageSize": false
}
```

Paper dimensions are in inches. Use `"preferCSSPageSize": true` to let `@page` CSS rules control the size.

### `GET /health`

Returns `200 {"status":"ok"}`. Use this for liveness probes.

### `GET /version`

Returns `200 {"version":"<semver>"}`.

### `GET /metrics`

Prometheus metrics endpoint.

| Metric | Type | Description |
|--------|------|-------------|
| `gopress_conversions_total` | counter | Total conversions, labelled `status={ok,error}` |
| `gopress_conversion_duration_seconds` | histogram | End-to-end conversion latency |
| `gopress_pool_queue_size` | gauge | Jobs waiting in the queue |
| `gopress_pool_free_instances` | gauge | Idle browser instances |
| `gopress_pool_restarts_total` | counter | Instance restarts, labelled `reason={max_conversions,crash}` |
| `gopress_rate_limited_total` | counter | Requests rejected with HTTP 429 |

## Configuration

All configuration is via environment variables.

| Variable | Default | Description |
|----------|---------|-------------|
| `GOPRESS_PORT` | `3000` | HTTP listen port |
| `GOPRESS_POOL_SIZE` | `4` | Number of Chromium instances |
| `GOPRESS_MAX_CONVERSIONS` | `500` | Conversions per instance before restart. 0 disables restarts. Tune down for very large documents; tune up (or disable) for small ones. |
| `GOPRESS_QUEUE_DEPTH` | `0 (auto)` | Pending-job buffer size. 0 = `GOPRESS_POOL_SIZE × 4` |
| `CHROME_BIN_PATH` | `/usr/bin/chrome` | Path to the Chrome/Chromium binary |
| `GOPRESS_API_KEY` | _(empty)_ | Bearer token for `POST /pdf`. Leave empty to disable auth. Minimum 16 characters when set. |
| `GOPRESS_RATE_LIMIT` | `0` | Maximum steady-state requests/second for `POST /pdf`. 0 disables rate limiting. |
| `GOPRESS_RATE_BURST` | `0` | Token-bucket burst size. 0 defaults to 1 when `GOPRESS_RATE_LIMIT > 0`. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | _(empty)_ | OTLP gRPC endpoint. Tracing is disabled when empty. |

Copy [`.env.example`](.env.example) to `.env` and adjust to your environment.

## Building from source

Requirements: Go 1.26+, a local Chrome/Chromium binary.

```bash
# Build binary
make build

# Run tests
make test

# Run with coverage report
make coverage

# Run locally (set CHROME_BIN_PATH to your local Chrome)
CHROME_BIN_PATH=/usr/bin/google-chrome make run
```

## Docker

```bash
# Build the Chrome base image (once, or when updating Chrome)
make docker-base

# Push base image to GHCR
make docker-push-base

# Build the gopress image
make docker-build VERSION=0.1.0

# Push
make docker-push VERSION=0.1.0

# Run
make docker-run VERSION=0.1.0
```

To update the pinned Chrome version:

```bash
make docker-base CHROME_VERSION=147.0.7727.56
make docker-push-base CHROME_VERSION=147.0.7727.56
make docker-build VERSION=0.2.0 CHROME_VERSION=147.0.7727.56
```

Latest stable Chrome for Testing versions: https://googlechromelabs.github.io/chrome-for-testing/

## Observability

### Prometheus

Metrics are available at `GET /metrics`. Example Prometheus scrape config:

```yaml
scrape_configs:
  - job_name: gopress
    static_configs:
      - targets: ["gopress:3000"]
```

### OpenTelemetry traces

Set `OTEL_EXPORTER_OTLP_ENDPOINT` to enable distributed tracing (OTLP/gRPC). When empty, the tracer is a noop with zero overhead.

```bash
OTEL_EXPORTER_OTLP_ENDPOINT=http://jaeger:4317 ./gopress
```

Span hierarchy per conversion:

```
conversion
  browser.convert
    browser.dial_cdp
    browser.load_html
    browser.print_pdf
```

## License

MIT
