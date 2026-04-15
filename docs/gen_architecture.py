"""Generate docs/architecture.png — gopress architecture diagram."""
from PIL import Image, ImageDraw, ImageFont
import math, os

W, H = 1400, 1000

# ---------------------------------------------------------------------------
# Colors
# ---------------------------------------------------------------------------
BG       = (13,  17,  23)
SURF     = (22,  27,  39)
SURF2    = (30,  45,  61)
BORDER   = (45,  58,  75)
TEXT     = (226, 232, 240)
DIM      = (148, 163, 184)
DIMMER   = (71,  85,  105)
BLUE     = (79,  143, 247)
BLUE_BG  = (20,  35,  65)
BLUE_HDR = (24,  52,  90)
CYAN     = (34,  211, 238)
GREEN    = (134, 239, 172)
GREEN_BG = (15,  65,  35)
GREEN_HDR= (18,  75,  40)
ORANGE   = (253, 186, 116)
ORG_BG   = (70,  28,  10)
ORG_HDR  = (120, 45,  15)
PURPLE   = (196, 181, 253)
PRP_BG   = (40,  18,  85)
PRP_HDR  = (65,  28,  130)
YELLOW   = (251, 191, 36)
RED      = (248, 113, 113)
LIME     = (163, 230, 53)
PINK     = (232, 121, 249)
SECTION_LINE = (30, 40, 58)

img = Image.new("RGB", (W, H), BG)
d   = ImageDraw.Draw(img)

# ---------------------------------------------------------------------------
# Fonts
# ---------------------------------------------------------------------------
_font_cache = {}
def F(size, bold=False, mono=False):
    key = (size, bold, mono)
    if key not in _font_cache:
        candidates = []
        if mono:
            candidates = ["C:/Windows/Fonts/consola.ttf", "C:/Windows/Fonts/cour.ttf"]
        elif bold:
            candidates = ["C:/Windows/Fonts/segoeuib.ttf", "C:/Windows/Fonts/arialbd.ttf"]
        else:
            candidates = ["C:/Windows/Fonts/segoeui.ttf", "C:/Windows/Fonts/arial.ttf"]
        f = None
        for p in candidates:
            try:
                f = ImageFont.truetype(p, size)
                break
            except Exception:
                pass
        _font_cache[key] = f or ImageFont.load_default()
    return _font_cache[key]

def T(x, y, s, color=TEXT, size=11, bold=False, mono=False, anchor="lt"):
    d.text((x, y), str(s), fill=color, font=F(size, bold, mono), anchor=anchor)

# ---------------------------------------------------------------------------
# Drawing helpers
# ---------------------------------------------------------------------------
def RR(x, y, w, h, r=8, fill=SURF2, outline=BORDER, lw=1):
    d.rounded_rectangle([x, y, x+w, y+h], radius=r, fill=fill, outline=outline, width=lw)

def box(x, y, w, h, title, title_col, hdr_fill, border_col,
        body_fill=None, r=8):
    """Rounded box with coloured header strip."""
    bf = body_fill or SURF2
    RR(x, y, w, h, r=r, fill=bf, outline=border_col, lw=1)
    # Header strip (rounded top only)
    d.rounded_rectangle([x, y, x+w, y+26], radius=r, fill=hdr_fill, outline=border_col, width=1)
    d.rectangle([x+1, y+18, x+w-1, y+26], fill=hdr_fill)   # fill bottom corners
    T(x + w//2, y+13, title, color=title_col, size=11, bold=True, anchor="mm")

def arrow(x1, y1, x2, y2, color=DIM, lw=2, dashed=False):
    if dashed:
        # Draw dashed line manually
        dx, dy_ = x2-x1, y2-y1
        length = math.hypot(dx, dy_)
        if length == 0:
            return
        ux, uy = dx/length, dy_/length
        pos, dash_on = 0.0, True
        dash_len = 6
        while pos < length:
            seg = min(dash_len, length - pos)
            sx, sy = x1 + ux*pos, y1 + uy*pos
            ex2, ey2 = x1 + ux*(pos+seg), y1 + uy*(pos+seg)
            if dash_on:
                d.line([(int(sx), int(sy)), (int(ex2), int(ey2))], fill=color, width=lw)
            pos += dash_len
            dash_on = not dash_on
    else:
        d.line([(x1, y1), (x2, y2)], fill=color, width=lw)
    # Arrowhead
    angle = math.atan2(y2-y1, x2-x1)
    sz = 9
    for da in [-0.4, 0.4]:
        ex = x2 - sz * math.cos(angle + da)
        ey = y2 - sz * math.sin(angle + da)
        d.line([(x2, y2), (int(ex), int(ey))], fill=color, width=lw)

def section_hdr(y, label):
    T(700, y, label, color=DIMMER, size=10, bold=True, anchor="mm")

# ===========================================================================
# TITLE
# ===========================================================================
T(700, 12, "gopress  —  Architecture Overview", color=TEXT, size=16, bold=True, anchor="mt")

# ===========================================================================
# SECTION 1 — BUILD PIPELINE  (y: 38 → 358)
# ===========================================================================
section_hdr(33, "BUILD PIPELINE")
RR(15, 38, 1370, 322, r=12, fill=SURF, outline=SECTION_LINE, lw=1)

# --- base.Dockerfile ---
box(30, 52, 305, 118, "base.Dockerfile", BLUE, BLUE_HDR, (45, 80, 130), BLUE_BG)
T(42, 83,  "FROM debian:13-slim@sha256:4ffb…",     DIM,    9, mono=True)
T(42, 97,  "ARG CHROME_VERSION=147.0.7727.56",      DIM,    9, mono=True)
T(42, 111, "ARG CHROME_SHA256  ← sha256sum verified", YELLOW, 9, mono=True)
T(42, 125, "ARG DEBIAN_SNAPSHOT=20260414",           DIM,    9, mono=True)
T(42, 139, "test -x /opt/chrome/chrome",             LIME,   9, mono=True)
T(42, 152, "run: make chrome-checksum",              DIMMER, 8.5)

arrow(335, 111, 405, 111, color=DIMMER)

# --- base image ---
box(407, 52, 275, 118, "ghcr.io/gopress-base", ORANGE, ORG_HDR, (90, 45, 15), ORG_BG)
T(419, 83,  ":147.0.7727.56",                        ORANGE, 10, bold=True)
T(419, 99,  "/opt/chrome/chrome  (locked binary)",   DIM,    9.5)
T(419, 113, "Air-gapped · build once, deploy anywhere", LIME, 9.5)
T(419, 127, "pinned digest + DEBIAN_SNAPSHOT APT",   DIM,    9.5)
T(419, 142, "push once per Chrome version bump",     DIMMER, 9)

# vertical arrow base image → Dockerfile
arrow(544, 170, 544, 194, color=DIMMER, dashed=True)
arrow(544, 198, 340, 246, color=DIMMER, dashed=True)

# --- Dockerfile ---
box(30, 200, 305, 152, "Dockerfile  (multi-stage)", GREEN, GREEN_HDR, (25, 75, 45), GREEN_BG)
T(42, 232, "Stage 1 — Go build",                    DIMMER, 8.5, bold=True)
T(42, 245, "  FROM golang:1.26.2@sha256:fcdb…",     DIM,    8.5, mono=True)
T(42, 258, "  CGO_ENABLED=0 · go build -s -w",      DIM,    8.5, mono=True)
T(42, 273, "Stage 2 — Runtime",                     DIMMER, 8.5, bold=True)
T(42, 286, "  debian:13-slim@sha256:4ffb…",         DIM,    8.5, mono=True)
T(42, 299, "  DEBIAN_SNAPSHOT=20260414 (APT pinned)",DIM,   8.5, mono=True)
T(42, 312, "  tini PID1 · UID 1001 · non-root",     YELLOW, 9)
T(42, 326, "  HEALTHCHECK gopress --healthcheck",   LIME,   9)

arrow(335, 276, 405, 276, color=DIMMER)

# --- final image ---
box(407, 200, 275, 152, "ghcr.io/gopress", GREEN, GREEN_HDR, (25, 75, 45), GREEN_BG)
T(419, 232, ":version  /  :sha-xxxxxxx",             GREEN,  10, bold=True)
T(419, 248, "Chrome binary + gopress binary",        DIM,    9.5)
T(419, 262, "seccomp: chrome.seccomp.json",          YELLOW, 9.5)
T(419, 276, "non-root UID 1001  ·  tini PID 1",     LIME,   9.5)
T(419, 290, "base images pinned by SHA256",          DIM,    9.5)
T(419, 304, "DEBIAN_SNAPSHOT APT for reproducibility",DIM,  9.5)
T(419, 323, "debian:13-slim@sha256:4ffb…",          DIMMER, 8.5, mono=True)

arrow(682, 276, 752, 276, color=DIMMER)

# --- CI Pipeline ---
box(754, 52, 616, 300, "CI PIPELINE  ·  GitHub Actions", PURPLE, PRP_HDR, (60, 35, 120), PRP_BG)

# test step
RR(766, 78, 125, 52, r=6, fill=BLUE_BG, outline=(45, 80, 140), lw=1)
T(828, 98,  "test",           BLUE,   11, bold=True, anchor="mm")
T(828, 114, "go test -race",  DIMMER, 9,  anchor="mm")
T(828, 124, "./...",          DIMMER, 9,  anchor="mm")
arrow(891, 104, 909, 104, color=DIMMER)

# lint step
RR(911, 78, 125, 52, r=6, fill=BLUE_BG, outline=(45, 80, 140), lw=1)
T(973, 98,  "lint",           BLUE,   11, bold=True, anchor="mm")
T(973, 114, "golangci-lint",  DIMMER, 9,  anchor="mm")
arrow(1036, 104, 1054, 104, color=DIMMER)

# publish step
RR(1056, 78, 295, 52, r=6, fill=BLUE_BG, outline=(45, 80, 140), lw=1)
T(1203, 96,  "publish  (main / tags only)",             BLUE,   10, bold=True, anchor="mm")
T(1203, 110, "build+push · provenance · SBOM",          DIMMER, 9,  anchor="mm")
T(1203, 122, "needs: [test, lint]  ·  id-token: write", DIMMER, 8.5,anchor="mm")

arrow(1203, 130, 1203, 148, color=DIMMER)

# cosign
RR(766, 150, 270, 52, r=6, fill=(28, 12, 50), outline=(80, 35, 140), lw=1)
T(901, 170, "cosign  (keyless signing)",                PINK,   10, bold=True, anchor="mm")
T(901, 184, "GitHub OIDC  ·  no key material",         DIMMER, 9,  anchor="mm")
T(901, 195, "cosign sign --yes IMAGE@digest",           DIMMER, 8.5,mono=True, anchor="mm")
arrow(1036, 176, 1054, 176, color=DIMMER)

# syft
RR(1056, 150, 295, 52, r=6, fill=(12, 30, 18), outline=(28, 95, 48), lw=1)
T(1203, 170, "syft SBOM",                              GREEN,  10, bold=True, anchor="mm")
T(1203, 184, "SPDX-JSON  ·  cosign attach sbom",       DIMMER, 9,  anchor="mm")
T(1203, 195, "90-day artifact  ·  uploaded to run",    DIMMER, 8.5,anchor="mm")

# observability note
RR(766, 215, 585, 50, r=6, fill=SURF, outline=(40, 52, 68), lw=1)
T(1058, 230, "Observability", DIM, 9.5, bold=True, anchor="mm")
T(1058, 244, "Prometheus /metrics  ·  OTel spans (dial_cdp, load_html, print_pdf)  ·  Jaeger traces", DIMMER, 9, anchor="mm")
T(1058, 257, "Gauges: gopress_pool_free_instances  ·  gopress_pool_queue_size", DIMMER, 8.5, mono=True, anchor="mm")

# verify note
RR(766, 275, 585, 38, r=6, fill=SURF, outline=(40, 52, 68), lw=1)
T(766+10, 282, "Verify:", DIM, 9, bold=True)
T(766+10, 296, 'cosign verify --certificate-identity-regexp="…/gopress" --certificate-oidc-issuer="https://token.actions.githubusercontent.com" IMAGE', DIMMER, 8, mono=True)

# ===========================================================================
# SECTION 2 — RUNTIME ARCHITECTURE  (y: 372 → 620)
# ===========================================================================
section_hdr(368, "RUNTIME ARCHITECTURE")
RR(15, 374, 1370, 248, r=12, fill=SURF, outline=SECTION_LINE, lw=1)

T(700, 384, "Docker Container  ·  --security-opt seccomp=chrome.seccomp.json  ·  UID 1001 non-root  ·  tini PID 1",
  DIMMER, 9.5, anchor="mt")

# HTTP server box
box(28, 396, 175, 218, "HTTP Server", BLUE, BLUE_HDR, (40, 75, 130), BLUE_BG)
T(115, 428, ":3000",           TEXT,   13, bold=True, anchor="mm")
T(115, 448, "read    30s",     DIM,    9.5, anchor="mm")
T(115, 462, "write  120s",     DIM,    9.5, anchor="mm")
T(115, 476, "idle     60s",    DIM,    9.5, anchor="mm")
T(115, 496, "GET  /health",    DIM,    9.5, anchor="mm")
T(115, 510, "GET  /metrics",   DIM,    9.5, anchor="mm")
T(115, 524, "POST /pdf",       YELLOW, 10, bold=True, anchor="mm")
T(115, 540, "graceful 30s",    DIM,    9, anchor="mm")
T(115, 558, "JSON structured", DIMMER, 9, anchor="mm")
T(115, 572, "logs to stdout",  DIMMER, 9, anchor="mm")
T(115, 590, "GET /version",    DIM,    9.5, anchor="mm")

arrow(203, 510, 224, 510, color=BLUE, lw=2)

# Pool box
RR(226, 396, 1145, 218, r=7, fill=(17, 22, 32), outline=(38, 52, 70), lw=1)
T(798, 407, "Browser Pool  ·  worker goroutines  ·  buffered job channel  ·  ErrQueueFull → HTTP 503  ·  ctx from caller",
  DIMMER, 9, anchor="mt")

# 4 Instance boxes
inst_titles = ["Instance 1", "Instance 2", "Instance 3", "Instance 4"]
inst_ports  = [9222, 9223, 9224, 9225]
for i in range(4):
    ix = 234 + i * 277
    iy = 420
    iw = 262
    ih = 186
    box(ix, iy, iw, ih, inst_titles[i], BLUE, BLUE_HDR, (40, 75, 130), BLUE_BG)
    T(ix + iw//2, iy+40, f"Chrome PID  ·  port {inst_ports[i]}", DIM,  9.5, anchor="mm")
    T(ix + iw//2, iy+57, "WebSocket  ↕  persistent",            CYAN, 9.5, anchor="mm")
    T(ix + iw//2, iy+72, "↑ dialBrowser() at startup",         DIMMER,9,   anchor="mm")
    T(ix + iw//2, iy+88, "CDP flat-mode sessions",              DIM,  9.5, anchor="mm")
    T(ix + iw//2, iy+104,"Target.createTarget per job",         DIM,  9.5, anchor="mm")
    T(ix + iw//2, iy+118,"Target.attachToTarget flatten:true",  DIM,  9.5, anchor="mm")
    T(ix + iw//2, iy+134,"0 / 100 conversions",                 LIME, 9.5, anchor="mm")
    T(ix + iw//2, iy+150,"NeedsRestart() → recycle",            LIME, 9.5, anchor="mm")
    T(ix + iw//2, iy+166,"PoolFreeInstances gauge",             DIMMER,9,  anchor="mm")

# ===========================================================================
# SECTION 3 — REQUEST FLOW  (y: 635 → 985)
# ===========================================================================
section_hdr(630, "REQUEST FLOW")
RR(15, 636, 1370, 350, r=12, fill=SURF, outline=SECTION_LINE, lw=1)

STEP_W = 200

# helper to draw a step box and return center x
def step(x, y, w, h, num, title, lines, title_col=BLUE, fill=BLUE_BG, border=(40,75,130)):
    RR(x, y, w, h, r=7, fill=fill, outline=border, lw=1)
    T(x + w//2, y+7,  f"{'⓪①②③④⑤⑥⑦⑧⑨⑩'[num]}", color=title_col, size=14, bold=True, anchor="mt")
    T(x + w//2, y+27, title,   color=title_col,  size=10, bold=True, anchor="mt")
    for j, line in enumerate(lines):
        col = line[1] if isinstance(line, tuple) else DIM
        txt2 = line[0] if isinstance(line, tuple) else line
        T(x + w//2, y+41+j*14, txt2, color=col, size=8.5, mono=isinstance(line,tuple) and line[2] if isinstance(line,tuple) and len(line)>2 else False, anchor="mt")
    return x + w//2

# Row 1 — steps 1-6 (left to right)
ROW1_Y = 648
ROW1_H = 112

# ① Client
x1 = step(22,  ROW1_Y, 145, ROW1_H, 1, "Client",
          [("POST /pdf", DIM), ("multipart/form-data", DIM), ("HTML + assets + opts", DIM)])
arrow(167, ROW1_Y+56, 184, ROW1_Y+56, color=BLUE, lw=2)

# ② parseForm
x2 = step(186, ROW1_Y, 175, ROW1_H, 2, "parseForm",
          [("index.html (required)", DIM), ("assets → map[string][]byte", DIM), ("options.json → PDFOptions", DIM), ("32 MB limit", DIMMER)])
arrow(361, ROW1_Y+56, 378, ROW1_Y+56, color=BLUE, lw=2)

# ③ Pool.Convert
x3 = step(380, ROW1_Y, 185, ROW1_H, 3, "Pool.Convert",
          [("non-blocking chan send", DIM), ("queue full → ErrQueueFull", RED), ("→ HTTP 503", RED), ("timeout → HTTP 504", RED)])
arrow(565, ROW1_Y+56, 582, ROW1_Y+56, color=BLUE, lw=2)

# ④ openSession
x4 = step(584, ROW1_Y, 210, ROW1_H, 4, "openSession",
          [("Target.createTarget", CYAN, True), ("Target.attachToTarget", CYAN, True), ("  flatten: true", CYAN, True), ("→ sessionId (CDP flat)", DIM)])
arrow(794, ROW1_Y+56, 811, ROW1_Y+56, color=CYAN, lw=2)

# ⑤ Enable domains
x5 = step(813, ROW1_Y, 200, ROW1_H, 5, "Enable domains",
          [("Page.enable", CYAN, True), ("Network.enable", CYAN, True), ("setLifecycleEventsEnabled", CYAN, True), ("Subscribe BEFORE navigate", YELLOW)])
arrow(1013, ROW1_Y+56, 1030, ROW1_Y+56, color=CYAN, lw=2)

# ⑥ Load HTML
x6 = step(1032, ROW1_Y, 340, ROW1_H, 6, "Load HTML",
          [("[HTML only]  setDocumentContent", CYAN), ("  → waitForEvent(loadEventFired)", DIMMER), ("[+assets]  local HTTP srv → Navigate", CYAN), ("  → waitForNetworkIdle  ·  no race", YELLOW)])

# Vertical arrow row1 → row2
arrow(1365, ROW1_Y+ROW1_H, 1365, ROW1_Y+ROW1_H+48, color=CYAN, lw=2)

# Row 2 — steps 7-10 (right to left)
ROW2_Y = ROW1_Y + ROW1_H + 50
ROW2_H = 112

# ⑦ printToPDF
x7 = step(1032, ROW2_Y, 340, ROW2_H, 7, "Page.printToPDF",
          [("TransferMode: ReturnAsBase64", CYAN, True), ("landscape · scale · margins", DIM), ("paperWidth/Height · pageRanges", DIM), ("displayHeader/FooterTemplate", DIM)])
arrow(1032, ROW2_Y+56, 1015, ROW2_Y+56, color=CYAN, lw=2)

# ⑧ Decode + Close
x8 = step(800, ROW2_Y, 210, ROW2_H, 8, "Decode + Close",
          [("base64.Decode → []byte", DIM), ("pdf.size_bytes attr", DIMMER), ("CloseTarget(targetId)", DIM), ("session.Close() → cleanup", DIM)])
arrow(800, ROW2_Y+56, 783, ROW2_Y+56, color=BLUE, lw=2)

# ⑨ Metrics + Tracing
x9 = step(560, ROW2_Y, 218, ROW2_H, 9, "Metrics + Tracing",
          [("conversions++", DIM), ("PoolFreeInstances.Inc()", DIMMER), ("OTel span end", PURPLE), ("NeedsRestart() → recycle", LIME)],
          title_col=PURPLE, fill=(25, 12, 45), border=(70, 35, 130))
arrow(560, ROW2_Y+56, 543, ROW2_Y+56, color=BLUE, lw=2)

# ⑩ Response
x10 = step(22, ROW2_Y, 516, ROW2_H, 10, "HTTP Response",
           [("200 application/pdf", GREEN), ("Content-Disposition: attachment; filename=document.pdf", DIM), ("body = raw PDF bytes  ·  ~260ms avg (8 concurrent)", DIMMER)],
           title_col=GREEN, fill=GREEN_BG, border=(28, 90, 48))

# ===========================================================================
# Save
# ===========================================================================
out = os.path.join(os.path.dirname(__file__), "architecture.png")
img.save(out, "PNG")
print(f"Saved: {out}  ({W}x{H})")
