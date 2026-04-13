# Base image that packages a pinned Chrome for Testing binary.
# Build once, push to your registry, reference from the main Dockerfile.
# This is what enables air-gapped operation.
#
# Usage:
#   docker build -f build/base.Dockerfile -t ghcr.io/oscarnunezu/gopress-base:130.0.6723.91 .
#   docker push ghcr.io/oscarnunezu/gopress-base:130.0.6723.91

FROM debian:13-slim

# Pinned Chrome for Testing version.
# Check latest stable: https://googlechromelabs.github.io/chrome-for-testing/
ARG CHROME_VERSION=130.0.6723.91

RUN apt-get update -qq \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends \
        curl ca-certificates unzip \
    && rm -rf /var/lib/apt/lists/*

RUN curl -fsSL \
    "https://storage.googleapis.com/chrome-for-testing-public/${CHROME_VERSION}/linux64/chrome-linux64.zip" \
    -o /tmp/chrome.zip \
    && unzip -q /tmp/chrome.zip -d /opt \
    && mv /opt/chrome-linux64 /opt/chrome \
    && ln -s /opt/chrome/chrome /usr/bin/chrome \
    && rm /tmp/chrome.zip \
    && test -x /opt/chrome/chrome
