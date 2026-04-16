# Pinned Chrome for Testing version and SHA256.
# Run `make chrome-checksum` to obtain the hash when upgrading.
ARG CHROME_VERSION=147.0.7727.56
ARG CHROME_SHA256=6019890a909bb6359ea40c2f0f82d40c25dae52aa6fcb9c7706739b8dcd6b28e
ARG GOLANG_VERSION=1.26.2

# Pin APT to a reproducible snapshot so package versions are fixed across builds.
# Update DEBIAN_SNAPSHOT intentionally when you want security patches.
ARG DEBIAN_SNAPSHOT=20260414T000000Z

# ── Go build stage ────────────────────────────────────────────────────────────
# Pin to an immutable digest — tag alone is mutable.
FROM golang:1.26.2@sha256:fcdb3e42c5544e9682a635771eac76a698b66de79b1b50ec5b9ce5c5f14ad775 AS build-stage

ARG VERSION=dev
ENV CGO_ENABLED=0

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY cmd ./cmd
COPY internal ./internal

RUN go build \
    -ldflags="-s -w -X github.com/OscarNunezU/gopress/internal/api.version=${VERSION}" \
    -o gopress \
    ./cmd/server

# ── Chrome download stage ─────────────────────────────────────────────────────
# Downloads and verifies Chrome for Testing from Google Storage.
# Runs in a throw-away layer; only /opt/chrome is copied to the final image.
FROM debian:13-slim@sha256:4ffb3a1511099754cddc70eb1b12e50ffdb67619aa0ab6c13fcd800a78ef7c7a AS chrome-stage

ARG CHROME_VERSION
ARG CHROME_SHA256
ARG DEBIAN_SNAPSHOT

RUN printf 'deb [check-valid-until=no] https://snapshot.debian.org/archive/debian/%s/ trixie main\n\
deb [check-valid-until=no] https://snapshot.debian.org/archive/debian-security/%s/ trixie-security main\n' \
        "${DEBIAN_SNAPSHOT}" "${DEBIAN_SNAPSHOT}" > /etc/apt/sources.list \
    && apt-get update -qq \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends \
        curl ca-certificates unzip \
    && rm -rf /var/lib/apt/lists/*

RUN curl -fsSL \
        "https://storage.googleapis.com/chrome-for-testing-public/${CHROME_VERSION}/linux64/chrome-linux64.zip" \
        -o /tmp/chrome.zip \
    && if [ -n "${CHROME_SHA256}" ]; then \
           echo "${CHROME_SHA256}  /tmp/chrome.zip" | sha256sum -c - || \
           { echo "ERROR: Chrome ZIP SHA256 mismatch — aborting build" >&2; exit 1; }; \
       fi \
    && unzip -q /tmp/chrome.zip -d /opt \
    && mv /opt/chrome-linux64 /opt/chrome \
    && rm /tmp/chrome.zip \
    && test -x /opt/chrome/chrome

# ── Final stage ───────────────────────────────────────────────────────────────
# Pin to an immutable digest — tag alone is mutable.
FROM debian:13-slim@sha256:4ffb3a1511099754cddc70eb1b12e50ffdb67619aa0ab6c13fcd800a78ef7c7a

ARG GOPRESS_USER_UID=1001
ARG GOPRESS_USER_GID=1001
ARG DEBIAN_SNAPSHOT

# Chrome runtime dependencies + fonts.
RUN printf 'deb [check-valid-until=no] https://snapshot.debian.org/archive/debian/%s/ trixie main\n\
deb [check-valid-until=no] https://snapshot.debian.org/archive/debian-security/%s/ trixie-security main\n' \
        "${DEBIAN_SNAPSHOT}" "${DEBIAN_SNAPSHOT}" > /etc/apt/sources.list \
    && apt-get update -qq \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends \
        tini \
        # Core fonts for reliable rendering.
        fonts-liberation \
        fonts-noto-cjk \
        fonts-noto-color-emoji \
        fonts-noto-core \
        # Chrome runtime libs (from /opt/chrome/deb.deps — verified via ldd).
        libasound2t64 \
        libatk1.0-0 \
        libatk-bridge2.0-0 \
        libcairo2 \
        libcups2 \
        libdrm2 \
        libgbm1 \
        libnss3 \
        libpango-1.0-0 \
        libxkbcommon0 \
        libxcomposite1 \
        libxdamage1 \
        libxfixes3 \
        libxrandr2 \
    && rm -rf /var/lib/apt/lists/* /tmp/* /var/tmp/*

# Non-root user.
RUN groupadd --gid ${GOPRESS_USER_GID} gopress \
    && useradd --uid ${GOPRESS_USER_UID} --gid gopress \
        --shell /bin/bash --home /home/gopress --no-create-home gopress \
    && mkdir /home/gopress \
    && chown gopress: /home/gopress

# Copy Chrome binary from the download stage.
COPY --from=chrome-stage /opt/chrome /opt/chrome
RUN ln -s /opt/chrome/chrome /usr/bin/chrome

# Copy gopress binary.
COPY --from=build-stage /app/gopress /usr/bin/gopress

# Font rendering config.
COPY build/fonts.conf /etc/fonts/conf.d/100-gopress.conf

ENV CHROME_BIN_PATH=/usr/bin/chrome
ENV GOPRESS_PORT=3000
ENV GOPRESS_POOL_SIZE=4
ENV GOPRESS_MAX_CONVERSIONS=500

USER gopress
WORKDIR /home/gopress

EXPOSE 3000

# OCI image labels — static metadata baked at build time.
# Dynamic fields (version, revision, created) are injected by docker/metadata-action in CI.
LABEL org.opencontainers.image.title="gopress" \
      org.opencontainers.image.description="HTML to PDF microservice powered by headless Chrome and CDP" \
      org.opencontainers.image.url="https://github.com/OscarNunezU/gopress" \
      org.opencontainers.image.source="https://github.com/OscarNunezU/gopress" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.vendor="gopress"

# Uses the gopress binary itself as the health probe — no extra tooling needed.
HEALTHCHECK --interval=15s --timeout=5s --start-period=30s --retries=3 \
    CMD ["/usr/bin/gopress", "--healthcheck"]

ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["/usr/bin/gopress"]
