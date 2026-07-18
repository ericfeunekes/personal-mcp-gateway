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
	"reflect"
	"sort"
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
		assertStdioPhase2DescriptorGrammar(t, tool)
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

func assertStdioPhase2DescriptorGrammar(t *testing.T, tool *sdk.Tool) {
	t.Helper()
	description, ok := map[string]string{
		obsidian.ToolResolve:  obsidian.ResolveDescription,
		obsidian.ToolLS:       obsidian.LSDescription,
		obsidian.ToolRead:     obsidian.ReadDescription,
		obsidian.ToolReadMany: obsidian.ReadManyDescription,
		obsidian.ToolGrep:     obsidian.GrepDescription,
	}[tool.Name]
	if !ok || tool.Description != description {
		t.Fatalf("%s description = %q, want %q", tool.Name, tool.Description, description)
	}
	annotations := tool.Annotations
	if annotations == nil || !annotations.ReadOnlyHint || annotations.DestructiveHint == nil || *annotations.DestructiveHint ||
		annotations.OpenWorldHint == nil || *annotations.OpenWorldHint || annotations.IdempotentHint || annotations.Title != "" {
		t.Fatalf("%s annotations = %#v, want exact read-only/non-destructive/closed-world hints", tool.Name, annotations)
	}

	schema := stdioSchemaObject(t, tool.InputSchema)
	switch tool.Name {
	case obsidian.ToolResolve:
		assertStdioObjectShape(t, schema, []string{"base", "path"}, []string{"path"})
	case obsidian.ToolLS:
		assertStdioObjectShape(t, schema, []string{"base", "cursor", "limit", "path"}, []string{"path"})
		assertStdioIntegerContract(t, stdioSchemaProperty(t, schema, "limit"), 1, 500, 100)
	case obsidian.ToolRead:
		assertStdioObjectShape(t, schema, []string{"base", "cursor", "max_bytes", "path", "selector"}, []string{"path"})
		assertStdioIntegerContract(t, stdioSchemaProperty(t, schema, "max_bytes"), 1, obsidian.MaxReadBytes, obsidian.DefaultReadBytes)
		assertStdioSelectorGrammar(t, stdioSchemaProperty(t, schema, "selector"))
	case obsidian.ToolReadMany:
		assertStdioObjectShape(t, schema, []string{"cursor", "max_bytes", "requests"}, []string{"requests"})
		assertStdioIntegerContract(t, stdioSchemaProperty(t, schema, "max_bytes"), 1, obsidian.MaxReadManyBytes, obsidian.DefaultReadManyBytes)
		requests := stdioSchemaProperty(t, schema, "requests")
		assertStdioSchemaValue(t, requests, "type", "array")
		assertStdioSchemaValue(t, requests, "minItems", 1)
		assertStdioSchemaValue(t, requests, "maxItems", obsidian.MaxReadManyRequests)
		request, ok := requests["items"].(map[string]any)
		if !ok {
			t.Fatalf("read_many items = %#v", requests["items"])
		}
		assertStdioObjectShape(t, request, []string{"base", "max_bytes", "path", "selector"}, []string{"path"})
		assertStdioIntegerContract(t, stdioSchemaProperty(t, request, "max_bytes"), 1, obsidian.MaxReadBytes, obsidian.DefaultReadBytes)
		assertStdioSelectorGrammar(t, stdioSchemaProperty(t, request, "selector"))
	case obsidian.ToolGrep:
		assertStdioObjectShape(t, schema, []string{
			"base", "case_sensitive", "context_lines", "cursor", "limit", "max_bytes", "max_files", "path", "pattern", "regex",
		}, []string{"pattern"})
		pattern := stdioSchemaProperty(t, schema, "pattern")
		assertStdioSchemaValue(t, pattern, "minLength", 1)
		assertStdioSchemaValue(t, pattern, "maxLength", obsidian.MaxGrepPatternBytes)
		assertStdioSchemaValue(t, stdioSchemaProperty(t, schema, "path"), "default", ".")
		assertStdioSchemaValue(t, stdioSchemaProperty(t, schema, "regex"), "default", true)
		assertStdioSchemaValue(t, stdioSchemaProperty(t, schema, "case_sensitive"), "default", false)
		assertStdioIntegerContract(t, stdioSchemaProperty(t, schema, "context_lines"), 0, obsidian.MaxGrepContextLines, obsidian.DefaultGrepContextLines)
		assertStdioIntegerContract(t, stdioSchemaProperty(t, schema, "limit"), 1, obsidian.MaxGrepLimit, obsidian.DefaultGrepLimit)
		assertStdioIntegerContract(t, stdioSchemaProperty(t, schema, "max_files"), 1, obsidian.MaxGrepMaxFiles, obsidian.DefaultGrepMaxFiles)
		assertStdioIntegerContract(t, stdioSchemaProperty(t, schema, "max_bytes"), 1, obsidian.MaxGrepMaxBytes, obsidian.DefaultGrepMaxBytes)
	}
}

func stdioSchemaObject(t *testing.T, value any) map[string]any {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	return schema
}

func stdioSchemaProperty(t *testing.T, schema map[string]any, name string) map[string]any {
	t.Helper()
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties = %#v", schema["properties"])
	}
	property, ok := properties[name].(map[string]any)
	if !ok {
		t.Fatalf("schema property %q = %#v", name, properties[name])
	}
	return property
}

func assertStdioObjectShape(t *testing.T, schema map[string]any, wantProperties, wantRequired []string) {
	t.Helper()
	assertStdioSchemaValue(t, schema, "type", "object")
	assertStdioSchemaValue(t, schema, "additionalProperties", false)
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties = %#v", schema["properties"])
	}
	gotProperties := make([]string, 0, len(properties))
	for name := range properties {
		gotProperties = append(gotProperties, name)
	}
	sort.Strings(gotProperties)
	wantProperties = append([]string(nil), wantProperties...)
	sort.Strings(wantProperties)
	if !reflect.DeepEqual(gotProperties, wantProperties) {
		t.Fatalf("schema properties = %#v, want %#v", gotProperties, wantProperties)
	}

	rawRequired, ok := schema["required"].([]any)
	if !ok {
		t.Fatalf("schema required = %#v", schema["required"])
	}
	gotRequired := make([]string, len(rawRequired))
	for index, value := range rawRequired {
		text, ok := value.(string)
		if !ok {
			t.Fatalf("schema required[%d] = %#v", index, value)
		}
		gotRequired[index] = text
	}
	sort.Strings(gotRequired)
	wantRequired = append([]string(nil), wantRequired...)
	sort.Strings(wantRequired)
	if !reflect.DeepEqual(gotRequired, wantRequired) {
		t.Fatalf("schema required = %#v, want %#v", gotRequired, wantRequired)
	}
}

func assertStdioIntegerContract(t *testing.T, schema map[string]any, minimum, maximum, defaultValue any) {
	t.Helper()
	assertStdioSchemaValue(t, schema, "type", "integer")
	assertStdioSchemaValue(t, schema, "minimum", minimum)
	assertStdioSchemaValue(t, schema, "maximum", maximum)
	assertStdioSchemaValue(t, schema, "default", defaultValue)
}

func assertStdioSchemaValue(t *testing.T, schema map[string]any, name string, want any) {
	t.Helper()
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var normalized any
	if err := json.Unmarshal(data, &normalized); err != nil {
		t.Fatal(err)
	}
	if got := schema[name]; !reflect.DeepEqual(got, normalized) {
		t.Fatalf("schema %s = %#v, want %#v in %#v", name, got, normalized, schema)
	}
}

func assertStdioSelectorGrammar(t *testing.T, schema map[string]any) {
	t.Helper()
	assertStdioSchemaValue(t, schema, "default", map[string]any{"kind": obsidian.SelectorContent, "start_line": 1})
	variants, ok := schema["oneOf"].([]any)
	if !ok || len(variants) != 5 {
		t.Fatalf("selector oneOf = %#v", schema["oneOf"])
	}
	byKind := make(map[string]map[string]any, len(variants))
	for index, raw := range variants {
		variant, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("selector variant %d = %#v", index, raw)
		}
		kindSchema := stdioSchemaProperty(t, variant, "kind")
		kind, ok := kindSchema["const"].(string)
		if !ok || kind == "" {
			t.Fatalf("selector variant %d kind.const = %#v", index, kindSchema["const"])
		}
		if _, duplicate := byKind[kind]; duplicate {
			t.Fatalf("duplicate selector kind.const %q", kind)
		}
		byKind[kind] = variant
	}
	expected := []struct {
		kind       string
		properties []string
		required   []string
	}{
		{obsidian.SelectorContent, []string{"kind", "start_line"}, []string{"kind"}},
		{obsidian.SelectorHeading, []string{"heading", "kind", "occurrence"}, []string{"heading", "kind"}},
		{obsidian.SelectorBlock, []string{"block_id", "kind"}, []string{"block_id", "kind"}},
		{obsidian.SelectorFrontmatter, []string{"kind"}, []string{"kind"}},
		{obsidian.SelectorOutline, []string{"kind"}, []string{"kind"}},
	}
	for _, want := range expected {
		variant, ok := byKind[want.kind]
		if !ok {
			t.Fatalf("selector variant %q missing; kinds = %#v", want.kind, byKind)
		}
		assertStdioObjectShape(t, variant, want.properties, want.required)
		assertStdioSchemaValue(t, stdioSchemaProperty(t, variant, "kind"), "const", want.kind)
	}
	assertStdioIntegerContract(t, stdioSchemaProperty(t, byKind[obsidian.SelectorContent], "start_line"), 1, obsidian.MaxMarkdownSourceLines, 1)
	heading := byKind[obsidian.SelectorHeading]
	assertStdioSchemaValue(t, stdioSchemaProperty(t, heading, "heading"), "minLength", 1)
	assertStdioIntegerContract(t, stdioSchemaProperty(t, heading, "occurrence"), 1, obsidian.MaxMarkdownSourceLines, 1)
	assertStdioSchemaValue(t, stdioSchemaProperty(t, byKind[obsidian.SelectorBlock], "block_id"), "minLength", 1)
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
