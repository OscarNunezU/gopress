BINARY        = gopress
VERSION      ?= dev
CHROME_VERSION ?= 147.0.7727.56
DOCKER_REPO   = ghcr.io/oscarnunezu/gopress
DOCKERFILE    = build/Dockerfile
BASE_IMAGE    = $(DOCKER_REPO)-base:$(CHROME_VERSION)

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

.PHONY: test
test: ## Run unit tests with race detector
	go test -race ./...

.PHONY: coverage
coverage: ## Run tests and open HTML coverage report
	go test -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

.PHONY: docker-base
docker-base: ## Build the Chrome for Testing base image (CHROME_VERSION=x.y.z)
	docker build \
		--build-arg CHROME_VERSION=$(CHROME_VERSION) \
		-f build/base.Dockerfile \
		-t $(BASE_IMAGE) .

.PHONY: docker-push-base
docker-push-base: ## Push the Chrome base image to GHCR
	docker push $(BASE_IMAGE)

.PHONY: docker-build
docker-build: ## Build the gopress Docker image
	docker build \
		--build-arg VERSION=$(VERSION) \
		--build-arg CHROME_BASE_IMAGE=$(BASE_IMAGE) \
		-t $(DOCKER_REPO):$(VERSION) \
		-f $(DOCKERFILE) .

.PHONY: docker-push
docker-push: ## Push the gopress image to GHCR
	docker push $(DOCKER_REPO):$(VERSION)

.PHONY: docker-run
docker-run: ## Run gopress in Docker
	docker run --rm -p 3000:3000 $(DOCKER_REPO):$(VERSION)

.PHONY: lint
lint: ## Lint the codebase
	golangci-lint run

.PHONY: fmt
fmt: ## Format code and tidy dependencies
	go fmt ./...
	go mod tidy

.PHONY: clean
clean: ## Remove build artifacts
	rm -f $(BINARY) coverage.out coverage.html
