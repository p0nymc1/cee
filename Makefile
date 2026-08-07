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

.PHONY: help build install uninstall test lint bench serve draft stats site playground clean

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

site: ## Build the published site into ./site (runs the examples first)
	@mkdir -p demo-output $(BIN_DIR)
	@for ex in rule_change code_audit crypto_surveillance network_detection security_monitoring human_approval meta_scenarios; do \
		printf 'running %s\n' "$$ex"; \
		go run "./examples/$$ex" > "demo-output/$$ex.txt" 2>&1 || true; \
	done
	@: > demo-output/validate.txt
	@for m in examples/manifests/*.json catalog/plugins/*/manifest.json; do \
		printf '%-46s ' "$$m" >> demo-output/validate.txt; \
		go run $(CMD) validate "$$m" >> demo-output/validate.txt; \
	done
	@go run $(CMD) list  > demo-output/list.txt
	@go run $(CMD) bench > demo-output/bench.txt
	@go test ./... 2>&1 | grep -c '^ok' > demo-output/packages.txt
	@grep -rh '^func Test' --include='*_test.go' . | wc -l | tr -d ' ' > demo-output/tests.txt
	@cd .github/site && go build -o "$(CURDIR)/$(BIN_DIR)/ceesite" .
	@$(BIN_DIR)/ceesite
	@$(MAKE) --no-print-directory playground

playground: ## Compile the engine to WebAssembly for the browser playground
	@mkdir -p site/playground
	@GOOS=js GOARCH=wasm go build -ldflags="-s -w" -o site/playground/cee.wasm ./cmd/ceewasm
	@cp "$$(go env GOROOT)/lib/wasm/wasm_exec.js" site/playground/wasm_exec.js 2>/dev/null || \
	 cp "$$(go env GOROOT)/misc/wasm/wasm_exec.js" site/playground/wasm_exec.js
	@printf 'playground: %s wasm\n' "$$(du -h site/playground/cee.wasm | cut -f1)"

clean: ## Remove build artifacts
	rm -rf $(BIN_DIR) demo-output site
	@echo "removed $(BIN_DIR), demo-output, site"
