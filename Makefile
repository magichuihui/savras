BINARY   ?= savras
MODULE   := savras
COMMAND  := ./cmd/savras
GO       ?= go
DOCKER   ?= docker
GIT_TAG  ?= $(shell git describe --tags --match 'v*' 2>/dev/null || echo dev)
VERSION  ?= $(GIT_TAG)

BUILD_FLAGS := -ldflags="-s -w"
TEST_FLAGS  ?= -v -race -count=1
COVER_FLAGS := -coverprofile=coverage.out -covermode=atomic

.PHONY: all build test test-cover clean run fmt tidy vet lint docker-build docker-buildx help

all: fmt vet test build       

build:           
	$(GO) build $(BUILD_FLAGS) -o $(BINARY) $(COMMAND)

build-linux-amd64:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build $(BUILD_FLAGS) -o $(BINARY)-linux-amd64 $(COMMAND)

build-linux-arm64:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build $(BUILD_FLAGS) -o $(BINARY)-linux-arm64 $(COMMAND)

build-all: build-linux-amd64 build-linux-arm64
	@echo "--> built linux/amd64 and linux/arm64"

test:
	$(GO) test $(TEST_FLAGS) ./...

test-cover:
	$(GO) test $(TEST_FLAGS) $(COVER_FLAGS) ./...
	$(GO) tool cover -func=coverage.out | tail -1

test-cover-html: test-cover
	$(GO) tool cover -html=coverage.out -o coverage.html

clean:
	rm -f $(BINARY) $(BINARY)-linux-* coverage.out coverage.html
	rm -rf dist/

run:
	$(GO) run $(COMMAND)

fmt:
	$(GO) fmt ./...

tidy:
	$(GO) mod tidy

vet:
	$(GO) vet ./...

lint:
	$(GO) run honnef.co/go/tools/cmd/staticcheck@latest ./...

docker-build:
	$(DOCKER) build -t $(BINARY):$(VERSION) .

docker-buildx:
	$(DOCKER) buildx build \
		--platform linux/amd64,linux/arm64 \
		-t $(BINARY):$(VERSION) \
		.

help:
	@echo "Usage:"
	@echo "  make build             - build binary for current platform"
	@echo "  make build-all         - cross-compile for linux/amd64 + linux/arm64"
	@echo "  make test              - run all tests"
	@echo "  make test-cover        - run tests with coverage report"
	@echo "  make test-cover-html   - run tests and open HTML coverage"
	@echo "  make clean             - remove build artifacts"
	@echo "  make run               - run server directly"
	@echo "  make fmt               - format Go source code"
	@echo "  make vet               - run go vet"
	@echo "  make lint              - run staticcheck"
	@echo "  make tidy              - tidy go modules"
	@echo "  make docker-build      - build Docker image"
	@echo "  make docker-buildx     - build multi-arch Docker image with buildx"
