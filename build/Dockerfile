ARG CHROME_BASE_IMAGE=ghcr.io/oscarnunezu/gopress-base:130.0.6723.91
ARG GOLANG_VERSION=1.23

# ----------------------------------------------
# Go build stage
# ----------------------------------------------
FROM golang:${GOLANG_VERSION} AS build-stage

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
# ----------------------------------------------
FROM debian:13-slim

ARG GOPRESS_USER_UID=1001
ARG GOPRESS_USER_GID=1001

# Chrome runtime dependencies + fonts.
RUN apt-get update -qq \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends \
        tini \
        curl \
        # Core fonts for reliable rendering.
        fonts-liberation \
        fonts-dejavu \
        fonts-noto-cjk \
        fonts-noto-color-emoji \
        fonts-noto-core \
        # Chrome runtime libs.
        libatk1.0-0 \
        libatk-bridge2.0-0 \
        libcups2 \
        libdrm2 \
        libgbm1 \
        libnss3 \
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

# Copy Chrome binary from base image.
COPY --from=${CHROME_BASE_IMAGE} /opt/chrome /opt/chrome
RUN ln -s /opt/chrome/chrome /usr/bin/chrome

# Copy gopress binary.
COPY --from=build-stage /app/gopress /usr/bin/gopress

# Font rendering config.
COPY build/fonts.conf /etc/fonts/conf.d/100-gopress.conf

ENV CHROME_BIN_PATH=/usr/bin/chrome
ENV GOPRESS_PORT=3000
ENV GOPRESS_POOL_SIZE=4
ENV GOPRESS_MAX_CONVERSIONS=100

USER gopress
WORKDIR /home/gopress

EXPOSE 3000

ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["gopress"]
