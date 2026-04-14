# Base image that packages a pinned Chrome for Testing binary.
# Build once, push to your registry, reference from the main Dockerfile.
# This enables air-gapped operation: the final runtime image pulls Chrome
# from this pre-built base instead of downloading it at every build.
#
# Usage:
#   make chrome-checksum          # compute CHROME_SHA256 for the pinned version
#   make docker-base CHROME_SHA256=<hash>
#   make docker-push-base

# Pin the base to an immutable digest — tag alone is mutable.
FROM debian:13-slim@sha256:4ffb3a1511099754cddc70eb1b12e50ffdb67619aa0ab6c13fcd800a78ef7c7a

# Pinned Chrome for Testing version.
# Check latest stable: https://googlechromelabs.github.io/chrome-for-testing/
ARG CHROME_VERSION=147.0.7727.56

# Expected SHA256 of chrome-linux64.zip for the pinned version.
# Run `make chrome-checksum` to obtain the value for a new version.
# If empty the download is accepted without verification (NOT recommended for production builds).
ARG CHROME_SHA256=""

# Pin APT to a reproducible snapshot so package versions are fixed.
# Update DEBIAN_SNAPSHOT intentionally when you want security patches.
# Enterprise alternative: replace the URL with your internal APT mirror.
ARG DEBIAN_SNAPSHOT=20260414T000000Z

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
    && ln -s /opt/chrome/chrome /usr/bin/chrome \
    && rm /tmp/chrome.zip \
    && test -x /opt/chrome/chrome
