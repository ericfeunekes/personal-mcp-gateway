package main

import "path/filepath"

// serviceSpec is the single fixed-authority row for one supervised server.
// Everything the controller needs to administer a server (label, wrapper,
// optional MCP stdio wrapper, environment file name, health-URL file,
// stdout/stderr log names, and readiness bounds) lives here so callers only
// ever pass --server; nothing else can disagree with it.
type serviceSpec struct {
	label                 string
	wrapperName           string
	mcpWrapperName        string // empty when the server execs the gateway directly (no MCP stdio wrapper)
	envFileName           string // relative to repo root; empty when the server shares obsidian's caller-supplied --environment path
	healthURLFile         string
	stdoutName            string
	stderrName            string
	readyTimeoutSeconds   int
	readyPollMilliseconds int
}

// additionalServiceOrder is the fixed, canonical order in which non-obsidian
// services are captured, restarted, and health-checked.
var additionalServiceOrder = []string{"ynab", "obsidian-http", "ynab-http"}

// serviceTable is the one authority for every server the release controller
// administers. --server selects a row; nothing else may disagree with it.
var serviceTable = map[string]serviceSpec{
	"obsidian": {
		label:                 "com.ericfeunekes.personal-mcp-gateway.obsidian-tunnel",
		wrapperName:           "run-obsidian-tunnel.sh",
		mcpWrapperName:        "run-obsidian-mcp-stdio.sh",
		healthURLFile:         "/tmp/personal-mcp-gateway/tunnel-health.url",
		stdoutName:            "obsidian-tunnel.out.log",
		stderrName:            "obsidian-tunnel.err.log",
		readyTimeoutSeconds:   45,
		readyPollMilliseconds: 1000,
	},
	"ynab": {
		label:                 "com.ericfeunekes.personal-mcp-gateway.ynab-tunnel",
		wrapperName:           "run-ynab-tunnel.sh",
		mcpWrapperName:        "run-ynab-mcp-stdio.sh",
		envFileName:           ".env.ynab.local",
		healthURLFile:         "/tmp/personal-mcp-gateway/ynab-tunnel-health.url",
		stdoutName:            "ynab-tunnel.out.log",
		stderrName:            "ynab-tunnel.err.log",
		readyTimeoutSeconds:   45,
		readyPollMilliseconds: 1000,
	},
	"obsidian-http": {
		// obsidian-http execs the gateway binary directly over loopback HTTP: no
		// second-level MCP stdio wrapper, so mcpWrapperName stays unset. It shares
		// the obsidian tunnel's environment file rather than duplicating
		// credentials into a second copy (envFileName stays unset; resolved via
		// serviceEnvironmentPath).
		label:                 "com.ericfeunekes.personal-mcp-gateway.obsidian-http",
		wrapperName:           "run-obsidian-http.sh",
		healthURLFile:         "/tmp/personal-mcp-gateway/obsidian-http-health.url",
		stdoutName:            "obsidian-http.out.log",
		stderrName:            "obsidian-http.err.log",
		readyTimeoutSeconds:   45,
		readyPollMilliseconds: 1000,
	},
	"ynab-http": {
		// ynab-http shares the ynab tunnel's environment file for the same reason.
		label:                 "com.ericfeunekes.personal-mcp-gateway.ynab-http",
		wrapperName:           "run-ynab-http.sh",
		envFileName:           ".env.ynab.local",
		healthURLFile:         "/tmp/personal-mcp-gateway/ynab-http-health.url",
		stdoutName:            "ynab-http.out.log",
		stderrName:            "ynab-http.err.log",
		readyTimeoutSeconds:   45,
		readyPollMilliseconds: 1000,
	},
}

func (s serviceSpec) wrapperPath(repoRoot string) string {
	return filepath.Join(repoRoot, "scripts", s.wrapperName)
}

func (s serviceSpec) mcpWrapperPath(repoRoot string) string {
	if s.mcpWrapperName == "" {
		return ""
	}
	return filepath.Join(repoRoot, "scripts", s.mcpWrapperName)
}

func (s serviceSpec) plistPath(home string) string {
	return filepath.Join(home, "Library", "LaunchAgents", s.label+".plist")
}

func (s serviceSpec) stdoutPath(home string) string {
	return filepath.Join(home, "Library", "Logs", "personal-mcp-gateway", s.stdoutName)
}

func (s serviceSpec) stderrPath(home string) string {
	return filepath.Join(home, "Library", "Logs", "personal-mcp-gateway", s.stderrName)
}

// serviceEnvironmentPath resolves a server's environment file. Servers with a
// fixed envFileName resolve it against repoRoot; obsidian and obsidian-http
// have none and instead share the caller-supplied obsidian --environment
// path (obsidian's own environment file is not a fixed repo-relative name).
func serviceEnvironmentPath(server, repoRoot, obsidianEnvironmentPath string) string {
	if name := serviceTable[server].envFileName; name != "" {
		return filepath.Join(repoRoot, name)
	}
	return obsidianEnvironmentPath
}
