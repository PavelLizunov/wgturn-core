# wgturn-core: Makefile
#
# Conventions:
#   - All Go invocations go through $(GO); override for non-PATH installs:
#       make GO=/tmp/go-toolchain/go/bin/go test
#   - Targets stay POSIX sh, no bashisms.

GO        ?= go
GOFLAGS   ?=
PKG       := ./...
COVERFILE := coverage.out

.PHONY: all
all: vet test

.PHONY: build
build:
	$(GO) build $(GOFLAGS) $(PKG)

.PHONY: vet
vet:
	$(GO) vet $(GOFLAGS) $(PKG)

.PHONY: test
test:
	$(GO) test $(GOFLAGS) -timeout 60s $(PKG)

.PHONY: race
race:
	$(GO) test $(GOFLAGS) -race -timeout 120s $(PKG)

.PHONY: cover
cover:
	$(GO) test $(GOFLAGS) -coverprofile=$(COVERFILE) -covermode=atomic $(PKG)
	$(GO) tool cover -func=$(COVERFILE) | tail -1

.PHONY: cover-html
cover-html: cover
	$(GO) tool cover -html=$(COVERFILE) -o coverage.html

.PHONY: lint
lint:
	@command -v golangci-lint >/dev/null 2>&1 || { \
		echo "golangci-lint not installed; run: make lint-install"; exit 1; }
	golangci-lint run $(PKG)

.PHONY: lint-install
lint-install:
	$(GO) install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

.PHONY: tidy
tidy:
	$(GO) mod tidy

.PHONY: cli
cli:
	$(GO) build $(GOFLAGS) -o bin/wgturn-cli ./cmd/wgturn-cli

.PHONY: clean
clean:
	rm -rf bin/ dist/ $(COVERFILE) coverage.html
