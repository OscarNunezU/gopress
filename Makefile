BINARY       = gopress
VERSION     ?= dev
DOCKER_REPO  = ghcr.io/oscarnunezu/gopress
DOCKERFILE   = build/Dockerfile

.PHONY: help
help: ## Show available targets
	@grep -hE '^[A-Za-z0-9_-]+:.*##' $(MAKEFILE_LIST) | awk 'BEGIN {FS=":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the gopress binary
	CGO_ENABLED=0 go build -ldflags="-s -w -X github.com/OscarNunezU/gopress/internal/api.version=$(VERSION)" \
		-o $(BINARY) ./cmd/server

.PHONY: run
run: build ## Build and run locally
	./$(BINARY)

.PHONY: test
test: ## Run unit tests
	go test -race ./...

.PHONY: docker-base
docker-base: ## Build the Chrome for Testing base image
	docker build -f build/base.Dockerfile -t $(DOCKER_REPO)-base:latest .

.PHONY: docker-build
docker-build: ## Build the gopress Docker image
	docker build \
		--build-arg VERSION=$(VERSION) \
		-t $(DOCKER_REPO):$(VERSION) \
		-f $(DOCKERFILE) .

.PHONY: docker-run
docker-run: ## Run gopress in Docker
	docker run --rm -p 3000:3000 $(DOCKER_REPO):$(VERSION)

.PHONY: lint
lint: ## Lint the codebase
	golangci-lint run

.PHONY: fmt
fmt: ## Format code and tidy dependencies
	go fix ./...
	go mod tidy

.PHONY: clean
clean: ## Remove build artifacts
	rm -f $(BINARY)
