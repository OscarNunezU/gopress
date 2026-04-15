ARG CHROME_BASE_IMAGE=ghcr.io/oscarnunezu/gopress-base:147.0.7727.56
ARG GOLANG_VERSION=1.26.2

# Pull the Chrome base as a named stage so COPY --from works with an ARG value.
FROM ${CHROME_BASE_IMAGE} AS chrome-base

# ----------------------------------------------
# Go build stage
# Pin to an immutable digest — tag alone is mutable.
# ----------------------------------------------
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

# ----------------------------------------------
# Final stage
# Pin to an immutable digest — tag alone is mutable.
# ----------------------------------------------
FROM debian:13-slim@sha256:4ffb3a1511099754cddc70eb1b12e50ffdb67619aa0ab6c13fcd800a78ef7c7a

ARG GOPRESS_USER_UID=1001
ARG GOPRESS_USER_GID=1001

# Pin APT to a reproducible snapshot so package versions are fixed across builds.
# Update DEBIAN_SNAPSHOT intentionally when you want security patches.
# Enterprise alternative: replace the snapshot URL with your internal APT mirror.
ARG DEBIAN_SNAPSHOT=20260414T000000Z

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

# Copy Chrome binary from base image stage.
COPY --from=chrome-base /opt/chrome /opt/chrome
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

# Uses the gopress binary itself as the health probe — no extra tooling (curl/wget)
# needed in the image. The binary reads GOPRESS_PORT from the environment.
HEALTHCHECK --interval=15s --timeout=5s --start-period=30s --retries=3 \
    CMD ["/usr/bin/gopress", "--healthcheck"]

ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["/usr/bin/gopress"]
