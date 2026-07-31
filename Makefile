# CEE — Cognitive Execution Engine
# Build, test, and install the `cee` CLI. Run `make` (or `make help`) to list
# targets. Uses only the Go toolchain; the core module has zero dependencies.

BINARY := cee
BIN_DIR := bin
CMD := ./cmd/cee

# Overridable on the command line, e.g. `make serve MANIFEST=path ADDR=127.0.0.1:9000`.
MANIFEST ?= catalog/plugins/sla-guard/manifest.json
ADDR ?=
DESC ?=

# Where `go install` puts the binary: GOBIN if set, else GOPATH/bin.
INSTALL_DIR := $(shell go env GOBIN)
ifeq ($(INSTALL_DIR),)
INSTALL_DIR := $(shell go env GOPATH)/bin
endif

.DEFAULT_GOAL := help

.PHONY: help build install uninstall test lint bench serve draft stats clean

help: ## List the available targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'

build: ## Build the cee binary into ./bin
	@mkdir -p $(BIN_DIR)
	go build -trimpath -o $(BIN_DIR)/$(BINARY) $(CMD)
	@echo "built $(BIN_DIR)/$(BINARY)"

install: ## Install cee into your Go bin
	go install -trimpath $(CMD)
	@echo "installed $(INSTALL_DIR)/$(BINARY)"
	@case ":$$PATH:" in \
		*":$(INSTALL_DIR):"*) echo "$(INSTALL_DIR) is on your PATH — run: cee --help" ;; \
		*) echo "note: $(INSTALL_DIR) is not on your PATH. Add it (zsh):"; \
		   echo "  echo 'export PATH=\"$(INSTALL_DIR):\$$PATH\"' >> ~/.zshrc && source ~/.zshrc" ;; \
	esac

uninstall: ## Remove the installed cee binary
	rm -f $(INSTALL_DIR)/$(BINARY)
	@echo "removed $(INSTALL_DIR)/$(BINARY)"

test: ## Run all tests (core + satellites)
	go test ./...
	@for mod in satellites/*/; do echo "== $$mod =="; ( cd "$$mod" && go test ./... ); done

lint: ## gofmt check + go vet + catalog lint
	@unformatted="$$(gofmt -l .)"; \
		if [ -n "$$unformatted" ]; then echo "unformatted:"; echo "$$unformatted"; exit 1; fi
	go vet ./...
	go run $(CMD) lint

bench: ## Run the plugin leaderboard
	go run $(CMD) bench

serve: ## Serve a manifest over HTTP, loopback only (MANIFEST=path [ADDR=host:port])
	go run $(CMD) serve $(MANIFEST) $(ADDR)

draft: ## Draft a workflow from a description (DESC="..."); needs CEE_LLM_* env
	@test -n '$(DESC)' || { echo 'usage: make draft DESC="<description of the process>"'; exit 2; }
	go run $(CMD) draft '$(DESC)'

stats: ## Print the repo figures the docs quote, so they can never drift unchecked
	@echo "Go lines (core, excl. satellites): $$(find . -name '*.go' -not -path './.git/*' -not -path './satellites/*' -exec cat {} + | wc -l | tr -d ' ')"
	@echo "Go lines (all):                    $$(find . -name '*.go' -not -path './.git/*' -exec cat {} + | wc -l | tr -d ' ')"
	@echo "Packages:                          $$(go list ./... | wc -l | tr -d ' ')"
	@echo "Tests (core):                      $$(grep -rn '^func Test' --include='*_test.go' . | grep -v satellites | wc -l | tr -d ' ')"
	@echo "Tests (satellites):                $$(grep -rn '^func Test' --include='*_test.go' satellites/ | wc -l | tr -d ' ')"
	@echo "Third-party requires in go.mod:    $$(grep -c '^	' go.mod || true)"
	@echo "Catalog plugins:                   $$(ls -1 catalog/plugins | wc -l | tr -d ' ')"

clean: ## Remove build artifacts
	rm -rf $(BIN_DIR)
	@echo "removed $(BIN_DIR)"
