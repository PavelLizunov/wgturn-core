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

# Build wgturn-cli with the embedded Chromium fallback. Requires
# `make fetch-chromium` to have populated
# pkg/wgturn/provider/vk/captchasolve/embedded/chromium/. Adds ~95-115 MB
# to the binary depending on platform.
.PHONY: cli-embedded
cli-embedded:
	$(GO) build $(GOFLAGS) -tags embedded -trimpath -ldflags "-s -w" \
		-o bin/wgturn-cli-embedded ./cmd/wgturn-cli

# Fetch chrome-headless-shell archives for the 4 supported platforms
# (linux/amd64, darwin/amd64, darwin/arm64, windows/amd64) into the
# embedded package's chromium/ subdirectory. Idempotent. Total
# ~400 MB on disk; archives are gitignored. Bump CHROMIUM_VERSION when
# you've updated chromium_*.go pin lines too.
CHROMIUM_VERSION ?= 148.0.7778.97
CHROMIUM_DIR     := pkg/wgturn/provider/vk/captchasolve/embedded/chromium
.PHONY: fetch-chromium
fetch-chromium:
	@mkdir -p $(CHROMIUM_DIR)
	@for plat in linux64 mac-x64 mac-arm64 win64; do \
	  out="$(CHROMIUM_DIR)/chrome-headless-shell-$$plat.zip"; \
	  if [ -f "$$out" ]; then \
	    echo "$$out — already present"; continue; \
	  fi; \
	  url="https://storage.googleapis.com/chrome-for-testing-public/$(CHROMIUM_VERSION)/$$plat/chrome-headless-shell-$$plat.zip"; \
	  echo "fetch $$plat from $$url"; \
	  curl -sSL --max-time 300 -o "$$out" "$$url"; \
	  ls -lh "$$out" | awk '{print $$5, $$NF}'; \
	done

.PHONY: clean
clean:
	rm -rf bin/ dist/ $(COVERFILE) coverage.html
