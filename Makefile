BINARY          = gopress

# The race detector requires CGO, which is unavailable on Windows without gcc.
# Set RACE_FLAG automatically so `make test` works on all platforms.
ifeq ($(OS),Windows_NT)
    RACE_FLAG :=
else
    RACE_FLAG := -race
endif
VERSION        ?= dev
GO_VERSION     ?= 1.26.2
CHROME_VERSION ?= 147.0.7727.56
CHROME_SHA256  ?= 6019890a909bb6359ea40c2f0f82d40c25dae52aa6fcb9c7706739b8dcd6b28e
# Multi-arch target platforms. Override with PLATFORMS=linux/amd64 for a faster local build.
PLATFORMS      ?= linux/amd64,linux/arm64
DEBIAN_SNAPSHOT ?= 20260414T000000Z
DOCKER_REPO    = ghcr.io/oscarnunezu/gopress
DOCKERFILE     = build/Dockerfile
BASE_IMAGE     = $(DOCKER_REPO)-base:$(CHROME_VERSION)

.PHONY: help
help: ## Show available targets
	@grep -hE '^[A-Za-z0-9_-]+:.*##' $(MAKEFILE_LIST) | awk 'BEGIN {FS=":.*?## "}; {printf "\033[36m%-22s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the gopress binary
	CGO_ENABLED=0 go build -ldflags="-s -w -X github.com/OscarNunezU/gopress/internal/api.version=$(VERSION)" \
		-o $(BINARY) ./cmd/server

.PHONY: run
run: build ## Build and run locally (set CHROME_BIN_PATH to your local Chrome)
	CHROME_BIN_PATH=$${CHROME_BIN_PATH:-/usr/bin/google-chrome} ./$(BINARY)

# ── CI local ──────────────────────────────────────────────────────────────────
#
# Corre lo mismo que corría el workflow de GitHub, en el mismo orden. Existe
# porque el 2026-08-26 se agotó el crédito de Actions y la red pasó a ser esto:
# si un objetivo local es más flojo que el que reemplaza, no es una red, es la
# sensación de tener una. Pasó en sgdoc-web, cuyo `npm run ci` permitía 98
# warnings mientras GitHub exigía 60.
#
# La versión del linter es la misma que fijaba el CI (GOLANGCI_VERSION), porque
# versiones distintas reportan conjuntos distintos de problemas.
.PHONY: ci
ci: ## Corre localmente lo mismo que corría el CI de GitHub
	@echo "── gofmt ──"
	@test -z "$$(gofmt -l . 2>/dev/null | grep -v vendor)" || { gofmt -l . | grep -v vendor; echo "ERROR: hay archivos sin formatear"; exit 1; }
	@echo "── go mod verify ──"
	@go mod verify
	@echo "── go vet ──"
	@go vet ./...
	@echo "── build ──"
	@CGO_ENABLED=0 go build ./...
	@echo "── tests (race) ──"
	@go test -race ./internal/... ./cmd/...
	@echo "── linter ──"
	@$(MAKE) --no-print-directory lint
	@echo ""
	@echo "OK: lo mismo que corría el CI de GitHub, en verde."

.PHONY: test
test: ## Run unit tests (race detector on Linux/macOS; disabled on Windows — no CGO)
	go test $(RACE_FLAG) ./...

.PHONY: coverage
coverage: ## Run tests and open HTML coverage report
	go test $(RACE_FLAG) -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

.PHONY: chrome-checksum
chrome-checksum: ## Compute SHA256 for Chrome for Testing linux64 (amd64 only — no arm64 build exists)
	@echo "Downloading Chrome $(CHROME_VERSION) linux64 to compute SHA256..."
	@curl -fsSL \
		"https://storage.googleapis.com/chrome-for-testing-public/$(CHROME_VERSION)/linux64/chrome-linux64.zip" \
		-o /tmp/chrome-$(CHROME_VERSION).zip \
	&& sha256sum /tmp/chrome-$(CHROME_VERSION).zip | awk '{print $$1}' \
	&& rm /tmp/chrome-$(CHROME_VERSION).zip

.PHONY: docker-base
docker-base: ## Build the Chrome for Testing base image (CHROME_VERSION=x.y.z CHROME_SHA256=<hash>)
	docker build \
		--build-arg CHROME_VERSION=$(CHROME_VERSION) \
		--build-arg CHROME_SHA256=$(CHROME_SHA256) \
		--build-arg DEBIAN_SNAPSHOT=$(DEBIAN_SNAPSHOT) \
		-f build/base.Dockerfile \
		-t $(BASE_IMAGE) .

.PHONY: docker-push-base
docker-push-base: ## Push the Chrome base image to GHCR
	docker push $(BASE_IMAGE)

.PHONY: docker-build
docker-build: ## Build the gopress Docker image (multi-arch; set PLATFORMS=linux/amd64 to build amd64 only)
	docker buildx build \
		--platform $(PLATFORMS) \
		--build-arg VERSION=$(VERSION) \
		--build-arg CHROME_VERSION=$(CHROME_VERSION) \
		--build-arg CHROME_SHA256=$(CHROME_SHA256) \
		--build-arg DEBIAN_SNAPSHOT=$(DEBIAN_SNAPSHOT) \
		-t $(DOCKER_REPO):$(VERSION) \
		-f $(DOCKERFILE) .

.PHONY: docker-push
docker-push: ## Push the gopress image to GHCR
	docker push $(DOCKER_REPO):$(VERSION)

.PHONY: docker-run
docker-run: ## Run gopress in Docker with hardened seccomp profile
	docker run --rm -p 3000:3000 \
		--security-opt seccomp=build/chrome.seccomp.json \
		$(DOCKER_REPO):$(VERSION)

.PHONY: lint
lint: ## Lint the codebase
	golangci-lint run

.PHONY: fmt
fmt: ## Format code and tidy dependencies
	go fmt ./...
	go mod tidy

## Benchmark targets — require a running gopress instance (make run / make docker-run)
VUS      ?= 4
DURATION ?= 30s
DOC      ?= simple

.PHONY: bench
bench: ## HTML→PDF load benchmark: simple doc, 4 VUs, 30s (set VUS= DURATION= DOC=)
	go run ./bench -vus=$(VUS) -duration=$(DURATION) -doc=$(DOC)

.PHONY: bench-all
bench-all: ## Run benchmark for both simple and complex documents
	go run ./bench -vus=$(VUS) -duration=$(DURATION) -all

.PHONY: bench-docker
bench-docker: ## Run benchmark on Linux inside Docker — production-representative numbers (set VUS= DURATION=)
	VUS=$(VUS) DURATION=$(DURATION) docker compose -f docker-compose.bench.yml up \
		--build --abort-on-container-exit --exit-code-from bench
	docker compose -f docker-compose.bench.yml down --remove-orphans

.PHONY: bench-compare
bench-compare: ## Side-by-side: gopress vs Gotenberg (requires Gotenberg on localhost:3010)
	@echo ""
	@echo "═══ gopress ═══════════════════════════════════════════"
	go run ./bench -vus=$(VUS) -duration=$(DURATION) -all
	@echo "═══ Gotenberg ══════════════════════════════════════════"
	go run ./bench \
		-target=http://localhost:3010 \
		-endpoint=/forms/chromium/convert/html \
		-field=files \
		-vus=$(VUS) -duration=$(DURATION) -all

.PHONY: clean
clean: ## Remove build artifacts
	rm -f $(BINARY) coverage.out coverage.html
