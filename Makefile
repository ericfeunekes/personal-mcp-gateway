SHELL := /bin/bash

GO ?= go
GOCACHE ?= $(CURDIR)/.gocache
BUILD_DIR ?= $(CURDIR)/.build
GATEWAY_CANDIDATE ?= $(BUILD_DIR)/personal-mcp-gateway
LAUNCHD_LABEL ?= com.ericfeunekes.personal-mcp-gateway.obsidian-tunnel

export GO GOCACHE BUILD_DIR GATEWAY_CANDIDATE LAUNCHD_LABEL

.PHONY: help test build release update restart verify-live install-launchagent uninstall-launchagent

help:
	@echo "Personal MCP Gateway targets:"
	@echo "  make test                 Run the canonical Go test suite"
	@echo "  make build                Build the local release candidate"
	@echo "  make release              Test, build, smoke, install, restart, and verify"
	@echo "  make update               Fast-forward local main from origin, then release"
	@echo "  make restart              Restart the installed tunnel LaunchAgent"
	@echo "  make verify-live          Verify LaunchAgent, tunnel liveness, and readiness"
	@echo "  make install-launchagent  Install or refresh the user LaunchAgent"
	@echo "  make uninstall-launchagent Remove the user LaunchAgent"

test:
	@mkdir -p "$(GOCACHE)"
	@env GOCACHE="$(GOCACHE)" "$(GO)" test -count=1 ./...

build:
	@mkdir -p "$(BUILD_DIR)" "$(GOCACHE)"
	@env CGO_ENABLED=1 GOCACHE="$(GOCACHE)" "$(GO)" build \
		-buildvcs=false -trimpath -o "$(GATEWAY_CANDIDATE)" ./cmd/gateway

release:
	@./scripts/release-local.sh

update:
	@./scripts/update-local.sh

restart:
	@launchctl kickstart -k "gui/$$(id -u)/$(LAUNCHD_LABEL)"

verify-live:
	@./scripts/verify-live.sh

install-launchagent:
	@./scripts/install-obsidian-tunnel-launchagent.sh

uninstall-launchagent:
	@./scripts/uninstall-obsidian-tunnel-launchagent.sh
