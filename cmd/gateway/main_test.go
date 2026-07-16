package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"personal-mcp-gateway/internal/audit"
	"personal-mcp-gateway/internal/config"
	"personal-mcp-gateway/internal/limits"
	"personal-mcp-gateway/internal/testutil"
	"personal-mcp-gateway/internal/tools/obsidian"
)

const gatewaySubprocessRootEnv = "PERSONAL_MCP_GATEWAY_TEST_ROOT"
const gatewaySubprocessTelemetryDBEnv = "PERSONAL_MCP_GATEWAY_TEST_TELEMETRY_DB"

type synchronizedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *synchronizedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(data)
}

func (b *synchronizedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func TestGatewayStdioSubprocess(t *testing.T) {
	root := os.Getenv(gatewaySubprocessRootEnv)
	if root == "" {
		return
	}
	args := []string{"stdio", "--obsidian-root", root}
	if dbPath := os.Getenv(gatewaySubprocessTelemetryDBEnv); dbPath != "" {
		args = append(args, "--telemetry", "sqlite", "--telemetry-db", dbPath)
	}
	os.Exit(run(args, os.Stderr))
}

func TestStdioSubprocessServesMCP(t *testing.T) {
	root := testutil.FixtureVault(t)
	dbPath := filepath.Join(t.TempDir(), "telemetry.sqlite")
	cmd := exec.Command(os.Args[0], "-test.run=TestGatewayStdioSubprocess")
	cmd.Env = append(os.Environ(),
		gatewaySubprocessRootEnv+"="+root,
		gatewaySubprocessTelemetryDBEnv+"="+dbPath,
	)
	var stderr synchronizedBuffer
	cmd.Stderr = &stderr

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "v0.0.1"}, nil)
	session, err := client.Connect(ctx, &sdk.CommandTransport{
		Command:           cmd,
		TerminateDuration: 2 * time.Second,
	}, nil)
	if err != nil {
		t.Fatalf("Connect() failed: %v\nstderr:\n%s", err, stderr.String())
	}
	serverInfo := session.InitializeResult().ServerInfo
	if serverInfo == nil || len(serverInfo.Icons) != 1 {
		t.Fatalf("initialize server icons = %#v, want one icon", serverInfo)
	}
	icon := serverInfo.Icons[0]
	if !strings.HasPrefix(icon.Source, "data:image/svg+xml;base64,") || icon.MIMEType != "image/svg+xml" || len(icon.Sizes) != 1 || icon.Sizes[0] != "any" || icon.Theme != sdk.IconThemeDark {
		t.Fatalf("initialize server icon = %#v, want embedded scalable SVG", icon)
	}
	closed := false
	defer func() {
		if !closed {
			_ = session.Close()
		}
	}()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools() failed: %v\nstderr:\n%s", err, stderr.String())
	}
	if len(tools.Tools) != 5 {
		t.Fatalf("tool count = %d, want 5", len(tools.Tools))
	}
	wantTools := map[string]bool{
		obsidian.ToolResolve:  true,
		obsidian.ToolLS:       true,
		obsidian.ToolRead:     true,
		obsidian.ToolReadMany: true,
		obsidian.ToolGrep:     true,
	}
	for _, tool := range tools.Tools {
		if !wantTools[tool.Name] {
			t.Fatalf("unexpected tool %q in exact candidate surface", tool.Name)
		}
		delete(wantTools, tool.Name)
	}
	if len(wantTools) != 0 {
		t.Fatalf("missing tools from exact candidate surface: %#v", wantTools)
	}

	result, err := session.CallTool(ctx, &sdk.CallToolParams{
		Name: obsidian.ToolResolve,
		Arguments: map[string]any{
			"path": "README.md",
		},
	})
	if err != nil {
		t.Fatalf("CallTool() failed: %v\nstderr:\n%s", err, stderr.String())
	}
	if result.IsError {
		t.Fatalf("CallTool() returned tool error: %#v", result.Content)
	}
	var out obsidian.ResolveOutput
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || out.Path != "README.md" || !out.Exists {
		t.Fatalf("resolve output = %#v", out)
	}

	if leakedHostPath(stderr.String(), root) {
		t.Fatalf("stderr leaked host path: %q", stderr.String())
	}
	if err := session.Close(); err != nil {
		t.Fatalf("session.Close() failed: %v", err)
	}
	closed = true
	waitForTelemetryRow(t, dbPath, obsidian.ToolResolve, "ok")
	waitForEventRow(t, dbPath, "gateway.start")
	waitForEventRow(t, dbPath, "gateway.backend_ready")
	waitForEventRow(t, dbPath, "gateway.stop")
}

func TestStdioSubprocessRejectsOversizedRawMessage(t *testing.T) {
	root := testutil.FixtureVault(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestGatewayStdioSubprocess")
	cmd.Env = append(os.Environ(), gatewaySubprocessRootEnv+"="+root)
	cmd.Stdin = strings.NewReader(strings.Repeat("x", int(limits.StdioMessageBytes)+1))
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err == nil {
		t.Fatal("oversized stdio message exited successfully, want failure")
	}
	if ctx.Err() != nil {
		t.Fatalf("oversized stdio subprocess timed out; stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "stdio server stopped") {
		t.Fatalf("stderr = %q, want stdio stopped message", stderr.String())
	}
	if leakedHostPath(stderr.String(), root) {
		t.Fatalf("stderr leaked local path: %q", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want no MCP output for invalid oversized frame", stdout.String())
	}
}

func TestRunConfigErrorsAreSanitized(t *testing.T) {
	missingRoot := filepath.Join(t.TempDir(), "missing")
	var stderr bytes.Buffer
	code := run([]string{"stdio", "--obsidian-root", missingRoot}, &stderr)
	if code == 0 {
		t.Fatalf("run() code = 0, want failure")
	}
	if leakedHostPath(stderr.String(), missingRoot) {
		t.Fatalf("stderr leaked host path: %q", stderr.String())
	}
}

func TestProductionWrappersSanitizeMissingRoot(t *testing.T) {
	missingRoot := filepath.Join(t.TempDir(), "private-vault")
	envFile := filepath.Join(t.TempDir(), "local.env")
	repoRoot := filepath.Clean(filepath.Join("..", ".."))

	tests := []struct {
		name   string
		script string
		env    []string
	}{
		{
			name:   "MCP stdio wrapper",
			script: filepath.Join(repoRoot, "scripts", "run-obsidian-mcp-stdio.sh"),
		},
		{
			name:   "tunnel wrapper",
			script: filepath.Join(repoRoot, "scripts", "run-obsidian-tunnel.sh"),
			env: []string{
				"CONTROL_PLANE_TUNNEL_ID=tunnel_test",
				"CONTROL_PLANE_API_KEY=test-runtime-key",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command(tt.script)
			cmd.Env = append(os.Environ(),
				"MCP_GATEWAY_ENV_FILE="+envFile,
				"OBSIDIAN_ROOT="+missingRoot,
			)
			cmd.Env = append(cmd.Env, tt.env...)
			output, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatal("wrapper succeeded with a missing root")
			}
			exitErr, ok := err.(*exec.ExitError)
			if !ok || exitErr.ExitCode() != 1 {
				t.Fatalf("wrapper error = %v, want exit code 1", err)
			}
			text := string(output)
			if !strings.Contains(text, "OBSIDIAN_ROOT does not exist or is not a directory.") {
				t.Fatalf("wrapper output = %q, want sanitized missing-root diagnostic", text)
			}
			for _, leaked := range []string{missingRoot, envFile} {
				if strings.Contains(text, leaked) {
					t.Fatalf("wrapper output leaked host path %q: %q", leaked, text)
				}
			}
		})
	}
}

func TestRunTelemetryStoreErrorsAreSanitized(t *testing.T) {
	root := testutil.FixtureVault(t)
	parentFile := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(parentFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(parentFile, "telemetry.sqlite")
	var stderr bytes.Buffer
	code := run([]string{"stdio", "--obsidian-root", root, "--telemetry", "sqlite", "--telemetry-db", dbPath}, &stderr)
	if code == 0 {
		t.Fatalf("run() code = 0, want failure")
	}
	if !strings.Contains(stderr.String(), "telemetry store is not writable") {
		t.Fatalf("stderr = %q, want telemetry store message", stderr.String())
	}
	if leakedHostPath(stderr.String(), dbPath) || leakedHostPath(stderr.String(), root) {
		t.Fatalf("stderr leaked local path: %q", stderr.String())
	}
}

func TestRunReportsTelemetryCloseFailure(t *testing.T) {
	root := testutil.FixtureVault(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var stderr bytes.Buffer
	code := runWithContext(ctx, []string{
		"http",
		"--obsidian-root", root,
		"--addr", "127.0.0.1:0",
		"--telemetry", "off",
	}, &stderr, func(config.Config, io.Writer) (*audit.Logger, func() error, error) {
		return audit.Disabled(), func() error { return errTelemetryCloseFailed{} }, nil
	})
	if code != 1 {
		t.Fatalf("runWithContext() code = %d, want close-failure code 1; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "telemetry close failed") {
		t.Fatalf("stderr = %q, want telemetry close message", stderr.String())
	}
	if leakedHostPath(stderr.String(), root) {
		t.Fatalf("stderr leaked local path: %q", stderr.String())
	}
}

func TestRunHTTPShutsDownOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runHTTP(ctx, "127.0.0.1:0", http.NewServeMux(), nil)
	}()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runHTTP() = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runHTTP did not stop after context cancellation")
	}
}

type errTelemetryCloseFailed struct{}

func (errTelemetryCloseFailed) Error() string { return "close failed" }

func leakedHostPath(text, root string) bool {
	return strings.Contains(text, root) ||
		strings.Contains(text, filepath.Dir(root)) ||
		strings.Contains(text, os.Getenv("HOME"))
}

func waitForTelemetryRow(t *testing.T, dbPath, tool, outcome string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		count, err := telemetryRowCount(dbPath, tool, outcome)
		if err == nil && count > 0 {
			return
		}
		lastErr = err
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("missing telemetry row for tool=%s outcome=%s: %v", tool, outcome, lastErr)
}

func telemetryRowCount(dbPath, tool, outcome string) (int, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return 0, err
	}
	defer db.Close()

	var count int
	err = db.QueryRow(`
SELECT COUNT(*)
FROM audit_events
WHERE event = 'tool.call' AND tool = ? AND outcome = ?
`, tool, outcome).Scan(&count)
	return count, err
}

func waitForEventRow(t *testing.T, dbPath, event string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		count, err := eventRowCount(dbPath, event)
		if err == nil && count > 0 {
			return
		}
		lastErr = err
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("missing telemetry row for event=%s: %v", event, lastErr)
}

func eventRowCount(dbPath, event string) (int, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return 0, err
	}
	defer db.Close()

	var count int
	err = db.QueryRow(`
SELECT COUNT(*)
FROM audit_events
WHERE event = ?
`, event).Scan(&count)
	return count, err
}
