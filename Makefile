SHELL := /bin/bash

GO ?= go
GOCACHE ?= $(CURDIR)/.gocache
BUILD_DIR ?= $(CURDIR)/.build
GATEWAY_CANDIDATE ?= $(BUILD_DIR)/personal-mcp-gateway
RELEASE_ACTIVATION_CANDIDATE ?= $(BUILD_DIR)/release-activation
SERVER ?= obsidian

export GO GOCACHE BUILD_DIR GATEWAY_CANDIDATE RELEASE_ACTIVATION_CANDIDATE SERVER

.PHONY: help test test-ynab build build-release-controller release release-status release-accept release-rollback update restart verify-live install-launchagent uninstall-launchagent

help:
	@echo "Personal MCP Gateway targets:"
	@echo "  make test                 Run the canonical Go test suite"
	@echo "  make test-ynab            Run quota-free YNAB, MCP, and process boundary tests"
	@echo "  make build                Build the local release candidate"
	@echo "  make release              Test, deploy, and leave the candidate pending live proof"
	@echo "  make release-status       Show the current release transaction"
	@echo "  make release-accept RELEASE_ID=<id>   Accept a model-proven candidate"
	@echo "  make release-rollback RELEASE_ID=<id> Roll back an exact pending candidate"
	@echo "  make update               Fast-forward local main from origin, then release"
	@echo "  make restart              Restart the installed service LaunchAgent (SERVER=obsidian|ynab|obsidian-http|ynab-http)"
	@echo "  make verify-live          Verify LaunchAgent, service liveness, and readiness"
	@echo "  make install-launchagent  Install or refresh the user LaunchAgent"
	@echo "  make uninstall-launchagent Remove the user LaunchAgent"

test:
	@mkdir -p "$(GOCACHE)"
	@set -euo pipefail; \
	module_path="$$(env GOCACHE="$(GOCACHE)" "$(GO)" list -m)"; \
	package_list="$$(env GOCACHE="$(GOCACHE)" "$(GO)" list ./...)"; \
	ordinary_packages=(); \
	while IFS= read -r package; do \
	  case "$$package" in \
	    "$$module_path/internal/tools/ynab"|"$$module_path/cmd/gateway-smoke"|"$$module_path/scripts") ;; \
	    *) ordinary_packages+=("$$package") ;; \
	  esac; \
	done <<< "$$package_list"; \
	if (( $${#ordinary_packages[@]} > 0 )); then \
	  env GOCACHE="$(GOCACHE)" "$(GO)" test -count=1 "$${ordinary_packages[@]}"; \
	fi; \
	env GOCACHE="$(GOCACHE)" "$(GO)" test -count=1 ./internal/tools/ynab; \
	env GOCACHE="$(GOCACHE)" "$(GO)" test -count=1 ./cmd/gateway-smoke; \
	env GOCACHE="$(GOCACHE)" "$(GO)" test -count=1 ./scripts

test-ynab:
	@mkdir -p "$(GOCACHE)"
	@env -u YNAB_TOKEN -u CONTROL_PLANE_API_KEY -u OPENAI_API_KEY GOCACHE="$(GOCACHE)" "$(GO)" test -count=1 ./internal/tools/ynab ./internal/mcp ./internal/app ./internal/config ./cmd/gateway ./cmd/ynab-schema-gen

build:
	@mkdir -p "$(BUILD_DIR)" "$(GOCACHE)"
	@env CGO_ENABLED=1 GOCACHE="$(GOCACHE)" "$(GO)" build \
		-buildvcs=false -trimpath \
		-ldflags "-X personal-mcp-gateway/internal/tools/obsidian.documentReadingBuild=pdf_candidate" \
		-o "$(GATEWAY_CANDIDATE)" ./cmd/gateway

build-release-controller:
	@mkdir -p "$(BUILD_DIR)" "$(GOCACHE)"
	@env CGO_ENABLED=1 GOCACHE="$(GOCACHE)" "$(GO)" build \
		-buildvcs=false -trimpath \
		-o "$(RELEASE_ACTIVATION_CANDIDATE)" ./cmd/release-activation

release:
	@./scripts/release-local.sh

release-status:
	@./scripts/release-activation.sh status

release-accept:
	@./scripts/release-activation.sh accept --release-id "$(RELEASE_ID)"

release-rollback:
	@./scripts/release-activation.sh rollback --release-id "$(RELEASE_ID)"

update:
	@./scripts/update-local.sh

restart:
	@if ! $(MAKE) --no-print-directory build-release-controller >/dev/null 2>&1; then echo 'error=release_build_failed message=release build failed' >&2; exit 1; fi
	@( \
	  case "$(SERVER)" in \
	    ynab|ynab-http) env_file="$(CURDIR)/.env.ynab.local" ;; \
	    *) env_file="$(CURDIR)/.env.local" ;; \
	  esac; \
	  source "$(CURDIR)/scripts/internal/release-config.sh" >/dev/null 2>&1 || exit 1; \
	  if [[ -f "$$env_file" ]] && ! load_release_config "$$env_file"; then exit 1; fi \
	) || { echo 'error=release_config message=release configuration is invalid' >&2; exit 1; }
	./scripts/release-activation.sh restart --repo-root "$(CURDIR)" --server "$(SERVER)"

verify-live:
	@if ! $(MAKE) --no-print-directory build-release-controller >/dev/null 2>&1; then echo 'error=release_build_failed message=release build failed' >&2; exit 1; fi
	./scripts/release-activation.sh verify-live --repo-root "$(CURDIR)" --server "$(SERVER)"

install-launchagent:
	@( \
	  case "$(SERVER)" in \
	    ynab|ynab-http) env_file="$(CURDIR)/.env.ynab.local" ;; \
	    *) env_file="$(CURDIR)/.env.local" ;; \
	  esac; \
	  source "$(CURDIR)/scripts/internal/release-config.sh" >/dev/null 2>&1 || exit 1; \
	  if [[ -f "$$env_file" ]] && ! load_release_config "$$env_file"; then exit 1; fi \
	) || { echo 'error=release_config message=release configuration is invalid' >&2; exit 1; }
	@if ! $(MAKE) --no-print-directory build-release-controller >/dev/null 2>&1; then echo 'error=release_build_failed message=release build failed' >&2; exit 1; fi
	./scripts/release-activation.sh install-launchagent --repo-root "$(CURDIR)" --server "$(SERVER)"

uninstall-launchagent:
	@if ! $(MAKE) --no-print-directory build-release-controller >/dev/null 2>&1; then echo 'error=release_build_failed message=release build failed' >&2; exit 1; fi
	./scripts/release-activation.sh uninstall-launchagent --repo-root "$(CURDIR)" --server "$(SERVER)"
