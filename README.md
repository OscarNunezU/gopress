# gopress

Minimal HTML to PDF conversion service in pure Go.

Uses Chromium headless via a hand-rolled [Chrome DevTools Protocol](https://chromedevtools.github.io/devtools-protocol/) client — no `chromedp`, no external Go CDP libraries.

## Features

- `POST /pdf` — convert HTML (+ assets) to PDF
- Chromium instance pool with automatic restart
- Prometheus metrics at `GET /metrics`
- OpenTelemetry tracing (OTLP/gRPC)
- Single static binary, minimal Docker image (~300MB)
- Air-gapped friendly: Chrome binary baked into base image

## API

```
POST /pdf
Content-Type: multipart/form-data

Fields:
  index.html   (required) — HTML document
  *.css/js/png (optional) — assets referenced by the HTML
  options.json (optional) — PDF options (see below)

Response: application/pdf
```

### options.json

```json
{
  "paperWidth":        8.27,
  "paperHeight":       11.69,
  "marginTop":         0,
  "marginBottom":      0,
  "marginLeft":        0,
  "marginRight":       0,
  "printBackground":   true,
  "landscape":         false,
  "scale":             1.0,
  "preferCSSPageSize": false
}
```

## Quick start

```bash
make build
./gopress
curl -F index.html=@doc.html http://localhost:3000/pdf -o output.pdf
```

## Docker

```bash
# Build the Chrome base image once (or pull from your registry)
make docker-base

# Build gopress image
make docker-build

# Run
make docker-run
```

## Configuration

| Env var | Default | Description |
|---|---|---|
| `GOPRESS_PORT` | `3000` | HTTP listen port |
| `GOPRESS_POOL_SIZE` | `4` | Number of Chromium instances |
| `GOPRESS_MAX_CONVERSIONS` | `100` | Conversions per instance before restart |
| `CHROME_BIN_PATH` | `/usr/bin/chrome` | Path to Chrome binary |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | _(empty)_ | OTLP endpoint (disabled if empty) |

## License

MIT
