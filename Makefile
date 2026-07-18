SHELL := /bin/bash

GO ?= go
GOCACHE ?= $(CURDIR)/.gocache
BUILD_DIR ?= $(CURDIR)/.build
GATEWAY_CANDIDATE ?= $(BUILD_DIR)/personal-mcp-gateway
RELEASE_ACTIVATION_CANDIDATE ?= $(BUILD_DIR)/release-activation
LAUNCHD_LABEL ?= com.ericfeunekes.personal-mcp-gateway.obsidian-tunnel

export GO GOCACHE BUILD_DIR GATEWAY_CANDIDATE RELEASE_ACTIVATION_CANDIDATE LAUNCHD_LABEL

.PHONY: help test build build-release-controller release release-status release-accept release-rollback update restart verify-live install-launchagent uninstall-launchagent

help:
	@echo "Personal MCP Gateway targets:"
	@echo "  make test                 Run the canonical Go test suite"
	@echo "  make build                Build the local release candidate"
	@echo "  make release              Test, deploy, and leave the candidate pending live proof"
	@echo "  make release-status       Show the current release transaction"
	@echo "  make release-accept RELEASE_ID=<id>   Accept a model-proven candidate"
	@echo "  make release-rollback RELEASE_ID=<id> Roll back an exact pending candidate"
	@echo "  make update               Fast-forward local main from origin, then release"
	@echo "  make restart              Restart the installed tunnel LaunchAgent"
	@echo "  make verify-live          Verify LaunchAgent, tunnel liveness, and readiness"
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
	    "$$module_path/cmd/gateway-smoke"|"$$module_path/scripts") ;; \
	    *) ordinary_packages+=("$$package") ;; \
	  esac; \
	done <<< "$$package_list"; \
	if (( $${#ordinary_packages[@]} > 0 )); then \
	  env GOCACHE="$(GOCACHE)" "$(GO)" test -count=1 "$${ordinary_packages[@]}"; \
	fi; \
	env GOCACHE="$(GOCACHE)" "$(GO)" test -count=1 ./cmd/gateway-smoke; \
	env GOCACHE="$(GOCACHE)" "$(GO)" test -count=1 ./scripts

build:
	@mkdir -p "$(BUILD_DIR)" "$(GOCACHE)"
	@env CGO_ENABLED=1 GOCACHE="$(GOCACHE)" "$(GO)" build \
		-buildvcs=false -trimpath -o "$(GATEWAY_CANDIDATE)" ./cmd/gateway

build-release-controller:
	@mkdir -p "$(BUILD_DIR)" "$(GOCACHE)"
	@env CGO_ENABLED=1 GOCACHE="$(GOCACHE)" "$(GO)" build \
		-buildvcs=false -trimpath -o "$(RELEASE_ACTIVATION_CANDIDATE)" ./cmd/release-activation

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
	@if ! source "$(CURDIR)/scripts/internal/release-config.sh" >/dev/null 2>&1; then echo 'error=release_config message=release configuration is invalid' >&2; exit 1; fi; \
	health_url_file="/tmp/personal-mcp-gateway/tunnel-health.url"; \
	if [[ -f "$(CURDIR)/.env.local" ]]; then \
	  if ! load_release_config "$(CURDIR)/.env.local"; then echo 'error=release_config message=release configuration is invalid' >&2; exit 1; fi; \
	  health_url_file="$${TUNNEL_HEALTH_URL_FILE:-/tmp/personal-mcp-gateway/tunnel-health.url}"; \
	fi; \
	./scripts/release-activation.sh restart --repo-root "$(CURDIR)" --label "$(LAUNCHD_LABEL)" --health-url-file "$$health_url_file"

verify-live:
	@./scripts/verify-live.sh

install-launchagent:
	@if ! source "$(CURDIR)/scripts/internal/release-config.sh" >/dev/null 2>&1; then echo 'error=release_config message=release configuration is invalid' >&2; exit 1; fi; \
	if [[ -f "$(CURDIR)/.env.local" ]] && ! load_release_config "$(CURDIR)/.env.local"; then echo 'error=release_config message=release configuration is invalid' >&2; exit 1; fi
	@if ! $(MAKE) --no-print-directory build-release-controller >/dev/null 2>&1; then echo 'error=release_build_failed message=release build failed' >&2; exit 1; fi
	@./scripts/release-activation.sh install-launchagent --repo-root "$(CURDIR)" --label "$(LAUNCHD_LABEL)"

uninstall-launchagent:
	@if ! $(MAKE) --no-print-directory build-release-controller >/dev/null 2>&1; then echo 'error=release_build_failed message=release build failed' >&2; exit 1; fi
	@./scripts/release-activation.sh uninstall-launchagent --repo-root "$(CURDIR)" --label "$(LAUNCHD_LABEL)"
