package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"personal-mcp-gateway/internal/audit"
	"personal-mcp-gateway/internal/config"
	"personal-mcp-gateway/internal/fsx"
	"personal-mcp-gateway/internal/limits"
	"personal-mcp-gateway/internal/testutil"
	"personal-mcp-gateway/internal/tools/obsidian"
)

func TestSDKPipeTransportListsAndCallsObsidianTools(t *testing.T) {
	application := newTestApp(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	session, stop := connectPipeTransport(t, ctx, application)
	defer stop()

	assertTools(t, ctx, session)

	resolve := callTool[obsidian.ResolveOutput](t, ctx, session, obsidian.ToolResolve, map[string]any{
		"path": "home/projects/alpha.md",
	})
	if !resolve.OK || !resolve.Exists || resolve.Path != "home/projects/alpha.md" || resolve.Type != "file" {
		t.Fatalf("resolve output = %#v", resolve)
	}

	list := callTool[obsidian.LSOutput](t, ctx, session, obsidian.ToolLS, map[string]any{
		"path":  "home/projects",
		"limit": 10,
	})
	if !list.OK || list.Path != "home/projects" || list.Truncated {
		t.Fatalf("ls output = %#v", list)
	}
	if got, want := entryNames(list.Entries), []string{"alpha.md", "beta.md"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ls names = %#v, want %#v", got, want)
	}
}

func TestSDKPipeTransportReturnsStructuredToolErrors(t *testing.T) {
	application := newTestApp(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	session, stop := connectPipeTransport(t, ctx, application)
	defer stop()

	result, err := session.CallTool(ctx, &sdk.CallToolParams{
		Name: obsidian.ToolLS,
		Arguments: map[string]any{
			"path":  ".obsidian",
			"limit": 10,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("IsError = false, want true")
	}

	var out obsidian.LSOutput
	unmarshalStructured(t, result, &out)
	if out.OK || out.Error == nil || out.Error.Code != "path_denied" {
		t.Fatalf("structured error = %#v", out)
	}
	assertResultDoesNotLeak(t, result, application.cfg.ObsidianRoot)
}

func TestSDKValidationRejectsMissingRequiredPath(t *testing.T) {
	application := newTestApp(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	session, stop := connectPipeTransport(t, ctx, application)
	defer stop()

	result, err := session.CallTool(ctx, &sdk.CallToolParams{
		Name:      obsidian.ToolResolve,
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("IsError = false, want true")
	}
	assertResultDoesNotLeak(t, result, application.cfg.ObsidianRoot)

	result, err = session.CallTool(ctx, &sdk.CallToolParams{
		Name:      obsidian.ToolLS,
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("IsError = false, want true")
	}
	assertResultDoesNotLeak(t, result, application.cfg.ObsidianRoot)
}

func TestTelemetryRecordsToolUsageAndFailures(t *testing.T) {
	const (
		runID        = "test-run"
		hostileKey   = "/synthetic/private-vault/private.md"
		hostileValue = "hostile-value-never-persist"
	)
	root := testutil.FixtureVault(t)
	cfg, err := config.Validate(config.Config{
		Mode:         config.ModeStdio,
		ObsidianRoot: root,
		Telemetry:    config.TelemetryStderr,
	})
	if err != nil {
		t.Fatal(err)
	}

	var events bytes.Buffer
	log := audit.NewJSONL(&events, runID)
	application, err := New(cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, stop := connectPipeTransport(t, ctx, application)
	defer stop()

	_ = callTool[obsidian.LSOutput](t, ctx, session, obsidian.ToolLS, map[string]any{
		"path":  "home/projects",
		"limit": 10,
	})

	result, err := session.CallTool(ctx, &sdk.CallToolParams{
		Name: obsidian.ToolLS,
		Arguments: map[string]any{
			"path": ".obsidian",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("IsError = false, want true")
	}

	result, err = session.CallTool(ctx, &sdk.CallToolParams{
		Name: obsidian.ToolResolve,
		Arguments: map[string]any{
			"path":     "README.md",
			hostileKey: hostileValue,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("IsError = false, want true")
	}

	result, err = session.CallTool(ctx, &sdk.CallToolParams{
		Name: obsidian.ToolLS,
		Arguments: map[string]any{
			"path":  ".",
			"limit": fsx.MaxLimit + 1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("IsError = false, want true")
	}

	_, _ = session.CallTool(ctx, &sdk.CallToolParams{
		Name: "/synthetic/private-vault/private-tool",
		Arguments: map[string]any{
			"path":                                "README.md",
			"/synthetic/private-vault/private.md": "secret",
		},
	})

	text := events.String()
	if strings.Contains(text, root) || strings.Contains(text, "home/projects") || strings.Contains(text, ".obsidian") ||
		strings.Contains(text, "private-tool") || strings.Contains(text, "private.md") || strings.Contains(text, hostileValue) {
		t.Fatalf("telemetry leaked raw path: %s", text)
	}
	records := auditRecords(t, text)

	lsOK := findToolRecord(records, obsidian.ToolLS, "ok", "")
	if lsOK == nil {
		t.Fatalf("missing successful ls telemetry: %s", text)
	}
	if got := intFromRecord(summarySection(t, lsOK, "result"), "entry_count"); got != 2 {
		t.Fatalf("entry_count = %d, want 2 in %#v", got, lsOK)
	}

	denied := findToolRecord(records, obsidian.ToolLS, "tool_error", "path_denied")
	if denied == nil {
		t.Fatalf("missing path_denied telemetry: %s", text)
	}

	validation := findToolRecord(records, obsidian.ToolResolve, "tool_error", "schema_validation")
	if validation == nil {
		t.Fatalf("missing schema_validation telemetry: %s", text)
	}
	assertHostileKeySummary(t, validation, runID, hostileKey)

	limitExceeded := findToolRecord(records, obsidian.ToolLS, "tool_error", "limit_exceeded")
	if limitExceeded == nil {
		t.Fatalf("missing limit_exceeded telemetry: %s", text)
	}

	unknown := findToolRecord(records, "unknown", "protocol_error", "unknown_tool")
	if unknown == nil {
		t.Fatalf("missing unknown_tool telemetry: %s", text)
	}
}

func TestSQLiteTelemetryFromRealToolCallsIsSanitized(t *testing.T) {
	const (
		runID        = "test-run"
		hostileKey   = "/synthetic/private-vault/private.md"
		hostileValue = "hostile-value-never-persist"
	)
	root := testutil.FixtureVault(t)
	dbPath := filepath.Join(t.TempDir(), "telemetry.sqlite")
	log, err := audit.NewSQLite(dbPath, runID)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Validate(config.Config{
		Mode:         config.ModeStdio,
		ObsidianRoot: root,
		Telemetry:    config.TelemetrySQLite,
		TelemetryDB:  dbPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	application, err := New(cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, stop := connectPipeTransport(t, ctx, application)
	defer stop()

	listedTools := listedToolNames(t, ctx, session)
	wantTools := []string{obsidian.ToolLS, obsidian.ToolResolve}
	if !reflect.DeepEqual(listedTools, wantTools) {
		t.Fatalf("tools = %#v, want %#v", listedTools, wantTools)
	}
	_ = callTool[obsidian.LSOutput](t, ctx, session, obsidian.ToolLS, map[string]any{
		"path":  "home/projects",
		"limit": 10,
	})
	result, err := session.CallTool(ctx, &sdk.CallToolParams{
		Name: obsidian.ToolLS,
		Arguments: map[string]any{
			"path": ".obsidian",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("path-denied call IsError = false, want true")
	}
	result, err = session.CallTool(ctx, &sdk.CallToolParams{
		Name: obsidian.ToolResolve,
		Arguments: map[string]any{
			"path":     "README.md",
			hostileKey: hostileValue,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("hostile-key call IsError = false, want true")
	}
	result, err = session.CallTool(ctx, &sdk.CallToolParams{
		Name: obsidian.ToolLS,
		Arguments: map[string]any{
			"path":  ".",
			"limit": fsx.MaxLimit + 1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("limit-exceeded call IsError = false, want true")
	}
	_, _ = session.CallTool(ctx, &sdk.CallToolParams{
		Name: "/synthetic/private-vault/private-tool",
		Arguments: map[string]any{
			"path": "README.md",
		},
	})
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	rows := sqliteAuditRows(t, dbPath)
	text := mustJSON(t, rows)
	for _, leaked := range []string{
		root,
		"home/projects",
		".obsidian",
		"/synthetic/private-vault",
		"private-tool",
		"private.md",
		hostileValue,
	} {
		if strings.Contains(text, leaked) {
			t.Fatalf("SQLite telemetry leaked %q: %s", leaked, text)
		}
	}
	if findMCPMethodRow(rows, "tools/list", "ok") == nil {
		t.Fatalf("missing mcp.request tools/list row: %s", text)
	}
	ready := findRow(rows, "gateway.backend_ready", "", "", "")
	if ready == nil {
		t.Fatalf("missing gateway.backend_ready row: %s", text)
	}
	readyTools := stringListFromRecord(ready, "tools")
	if !reflect.DeepEqual(readyTools, listedTools) {
		t.Fatalf("backend_ready tools = %#v, SDK tools = %#v", readyTools, listedTools)
	}
	if findRow(rows, "tool.call", obsidian.ToolLS, "ok", "") == nil {
		t.Fatalf("missing successful ls row: %s", text)
	}
	if findRow(rows, "tool.call", obsidian.ToolLS, "tool_error", "path_denied") == nil {
		t.Fatalf("missing path_denied row: %s", text)
	}
	validation := findRow(rows, "tool.call", obsidian.ToolResolve, "tool_error", "schema_validation")
	if validation == nil {
		t.Fatalf("missing schema_validation row: %s", text)
	}
	assertHostileKeySummary(t, validation, runID, hostileKey)
	if findRow(rows, "tool.call", obsidian.ToolLS, "tool_error", "limit_exceeded") == nil {
		t.Fatalf("missing limit_exceeded row: %s", text)
	}
	if findRow(rows, "tool.call", "unknown", "protocol_error", "unknown_tool") == nil {
		t.Fatalf("missing sanitized unknown_tool row: %s", text)
	}
}

func TestJSONLAndSQLiteSafeSummariesMatchForRealCursorCalls(t *testing.T) {
	const runID = "summary-parity-run"

	var jsonl bytes.Buffer
	jsonLog := audit.NewJSONL(&jsonl, runID)
	jsonCursors := driveSafeSummaryScenario(t, testutil.FixtureVault(t), jsonLog)
	jsonRecords := toolCallRecords(auditRecords(t, jsonl.String()))

	dbPath := filepath.Join(t.TempDir(), "summary-parity.sqlite")
	sqliteLog, err := audit.NewSQLite(dbPath, runID)
	if err != nil {
		t.Fatal(err)
	}
	sqliteCursors := driveSafeSummaryScenario(t, testutil.FixtureVault(t), sqliteLog)
	if err := sqliteLog.Close(); err != nil {
		t.Fatal(err)
	}
	sqliteRecords := toolCallRecords(sqliteAuditRows(t, dbPath))

	if len(jsonRecords) != 3 || len(sqliteRecords) != 3 {
		t.Fatalf("tool-call record counts jsonl=%d sqlite=%d", len(jsonRecords), len(sqliteRecords))
	}
	for i := range jsonRecords {
		jsonSummary := jsonRecords[i]["summary"]
		sqliteSummary := sqliteRecords[i]["summary"]
		if !reflect.DeepEqual(jsonSummary, sqliteSummary) {
			t.Fatalf("summary[%d] differs:\njsonl=%#v\nsqlite=%#v", i, jsonSummary, sqliteSummary)
		}
	}

	resolveResult := summarySection(t, jsonRecords[0], "result")
	if resolveResult["ok"] != true || resolveResult["exists"] != true || resolveResult["result_type"] != "file" || resolveResult["is_error"] != false {
		t.Fatalf("resolve result summary = %#v", resolveResult)
	}
	firstResult := summarySection(t, jsonRecords[1], "result")
	if intFromRecord(firstResult, "entry_count") != 1 || intFromRecord(firstResult, "files_scanned") != 2 ||
		firstResult["truncated"] != true || firstResult["scope_complete"] != true || firstResult["continuation"] != "cursor" || firstResult["consistency"] != "stable" {
		t.Fatalf("first ls result summary = %#v", firstResult)
	}
	continuedArgs := summarySection(t, jsonRecords[2], "arguments")
	cursorShape, _ := continuedArgs["cursor"].(map[string]any)
	if cursorShape["present"] != true || intFromRecord(cursorShape, "bytes") != len(jsonCursors[0]) {
		t.Fatalf("continued cursor summary = %#v", cursorShape)
	}
	continuedResult := summarySection(t, jsonRecords[2], "result")
	if continuedResult["continuation"] != "complete" || continuedResult["result_complete"] != true || continuedResult["scope_complete"] != true {
		t.Fatalf("continued ls result summary = %#v", continuedResult)
	}

	jsonText := jsonl.String()
	sqliteText := mustJSON(t, sqliteRecords)
	for _, cursor := range append(jsonCursors, sqliteCursors...) {
		digest := fmt.Sprintf("%x", sha256.Sum256([]byte(cursor)))
		for _, encoded := range []string{jsonText, sqliteText} {
			if strings.Contains(encoded, cursor) || strings.Contains(encoded, digest) {
				t.Fatalf("telemetry retained cursor or cursor digest")
			}
		}
	}
	for _, leaked := range []string{"alpha.md", "beta.md", "synthetic alpha note", "synthetic beta note"} {
		if strings.Contains(jsonText, leaked) || strings.Contains(sqliteText, leaked) {
			t.Fatalf("telemetry retained cursor-carried or content sentinel %q", leaked)
		}
	}
}

func TestSQLiteHTTPTelemetryRecordsRoutes(t *testing.T) {
	root := testutil.FixtureVault(t)
	dbPath := filepath.Join(t.TempDir(), "telemetry.sqlite")
	log, err := audit.NewSQLite(dbPath, "test-run")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Validate(config.Config{
		Mode:         config.ModeHTTP,
		ObsidianRoot: root,
		Addr:         config.DefaultHTTPAddr,
		Telemetry:    config.TelemetrySQLite,
		TelemetryDB:  dbPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	application, err := New(cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(application.HTTPHandler())
	defer server.Close()

	for _, path := range []string{"/healthz", "/readyz"} {
		resp, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", path, resp.StatusCode)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "v0.0.1"}, nil)
	session, err := client.Connect(ctx, &sdk.StreamableClientTransport{Endpoint: server.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertTools(t, ctx, session)
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequest(http.MethodPost, server.URL+"/mcp", strings.NewReader(strings.Repeat("x", int(limits.HTTPRequestBodyBytes)+1)))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("/mcp status = %d, want 413", resp.StatusCode)
	}

	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	rows := sqliteAuditRows(t, dbPath)
	text := mustJSON(t, rows)
	if strings.Contains(text, root) || strings.Contains(text, strings.Repeat("x", 128)) {
		t.Fatalf("SQLite HTTP telemetry leaked local/raw body data: %s", text)
	}
	if findHTTPRecord(rows, "healthz", http.MethodGet, http.StatusOK) == nil {
		t.Fatalf("missing SQLite healthz http.request row: %s", text)
	}
	if findHTTPRecord(rows, "readyz", http.MethodGet, http.StatusOK) == nil {
		t.Fatalf("missing SQLite readyz http.request row: %s", text)
	}
	if findHTTPRecord(rows, "mcp", http.MethodPost, http.StatusOK) == nil {
		t.Fatalf("missing SQLite mcp success http.request row: %s", text)
	}
	if findHTTPRecord(rows, "mcp", http.MethodPost, http.StatusRequestEntityTooLarge) == nil {
		t.Fatalf("missing SQLite mcp 413 http.request row: %s", text)
	}
}

func TestStreamableHTTPTransportAndHealthReadiness(t *testing.T) {
	root := testutil.FixtureVault(t)
	cfg, err := config.Validate(config.Config{
		Mode:         config.ModeHTTP,
		ObsidianRoot: root,
		Addr:         config.DefaultHTTPAddr,
	})
	if err != nil {
		t.Fatal(err)
	}
	application, err := New(cfg, audit.Disabled())
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(application.HTTPHandler())
	defer server.Close()

	resp, err := http.Get(server.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "ok\n" {
		t.Fatalf("/healthz status=%d body=%q", resp.StatusCode, body)
	}

	resp, err = http.Get(server.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || strings.Contains(string(body), root) {
		t.Fatalf("/readyz status=%d body=%q", resp.StatusCode, body)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "v0.0.1"}, nil)
	session, err := client.Connect(ctx, &sdk.StreamableClientTransport{Endpoint: server.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	assertTools(t, ctx, session)
	list := callTool[obsidian.LSOutput](t, ctx, session, obsidian.ToolLS, map[string]any{
		"path":  ".",
		"limit": 2,
	})
	if !list.OK || !list.Truncated {
		t.Fatalf("ls over HTTP = %#v", list)
	}
}

func TestHTTPTelemetryRecordsRoutesAndBodyLimit(t *testing.T) {
	root := testutil.FixtureVault(t)
	cfg, err := config.Validate(config.Config{
		Mode:         config.ModeHTTP,
		ObsidianRoot: root,
		Addr:         config.DefaultHTTPAddr,
	})
	if err != nil {
		t.Fatal(err)
	}
	var events bytes.Buffer
	log := audit.NewJSONL(&events, "test-run")
	application, err := New(cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(application.HTTPHandler())
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "v0.0.1"}, nil)
	session, err := client.Connect(ctx, &sdk.StreamableClientTransport{Endpoint: server.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	assertTools(t, ctx, session)
	result, err := session.CallTool(ctx, &sdk.CallToolParams{
		Name: obsidian.ToolResolve,
		Arguments: map[string]any{
			"path": strings.Repeat("z", limits.TelemetryArgsBytes+1),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("oversized argument IsError = false, want true")
	}

	for _, path := range []string{"/healthz", "/readyz"} {
		resp, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", path, resp.StatusCode)
		}
	}

	privateMethod := "PRIVATEPATH"
	req, err := http.NewRequest(privateMethod, server.URL+"/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("nonstandard method status = %d, want 200", resp.StatusCode)
	}

	req, err = http.NewRequest(http.MethodPost, server.URL+"/mcp", strings.NewReader(strings.Repeat("x", int(limits.HTTPRequestBodyBytes)+1)))
	if err != nil {
		t.Fatal(err)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("/mcp status=%d body=%q, want 413", resp.StatusCode, body)
	}

	req, err = http.NewRequest(http.MethodPost, server.URL+"/mcp", strings.NewReader(strings.Repeat("y", int(limits.HTTPRequestBodyBytes)+1)))
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = -1
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("/mcp chunked status=%d body=%q, want 413", resp.StatusCode, body)
	}

	text := events.String()
	if strings.Contains(text, root) || strings.Contains(text, privateMethod) ||
		strings.Contains(text, strings.Repeat("x", 128)) ||
		strings.Contains(text, strings.Repeat("y", 128)) ||
		strings.Contains(text, strings.Repeat("z", 128)) {
		t.Fatalf("HTTP telemetry leaked local/raw body data: %s", text)
	}
	records := auditRecords(t, text)
	if findHTTPRecord(records, "healthz", http.MethodGet, http.StatusOK) == nil {
		t.Fatalf("missing healthz http.request row: %s", text)
	}
	if findHTTPRecord(records, "readyz", http.MethodGet, http.StatusOK) == nil {
		t.Fatalf("missing readyz http.request row: %s", text)
	}
	if findHTTPRecord(records, "mcp", http.MethodPost, http.StatusOK) == nil {
		t.Fatalf("missing successful mcp http.request row: %s", text)
	}
	if findHTTPRecord(records, "healthz", "other", http.StatusOK) == nil {
		t.Fatalf("missing sanitized nonstandard-method http.request row: %s", text)
	}
	if findHTTPRecord(records, "mcp", http.MethodPost, http.StatusRequestEntityTooLarge) == nil {
		t.Fatalf("missing mcp 413 http.request row: %s", text)
	}
	tooLargeArgs := findToolRecord(records, obsidian.ToolResolve, "tool_error", "input_too_large")
	if tooLargeArgs == nil {
		t.Fatalf("missing HTTP oversized-argument tool.call row: %s", text)
	}
	args := summarySection(t, tooLargeArgs, "arguments")
	if args["too_large"] != true {
		t.Fatalf("oversized-argument args summary = %#v, want too_large", args)
	}
}

func summarySection(t *testing.T, record map[string]any, section string) map[string]any {
	t.Helper()
	summary, ok := record["summary"].(map[string]any)
	if !ok {
		t.Fatalf("record summary = %#v", record["summary"])
	}
	value, ok := summary[section].(map[string]any)
	if !ok {
		t.Fatalf("summary section %q = %#v", section, summary[section])
	}
	return value
}

func assertHostileKeySummary(t *testing.T, record map[string]any, runID, hostileKey string) {
	t.Helper()
	arguments := summarySection(t, record, "arguments")
	if got := intFromRecord(arguments, "unknown_key_count"); got != 1 {
		t.Fatalf("unknown_key_count = %d, want 1 in %#v", got, arguments)
	}
	hashes, ok := arguments["unknown_key_hashes"].([]any)
	if !ok || len(hashes) != 1 || hashes[0] != audit.HashString(runID, hostileKey) {
		t.Fatalf("unknown_key_hashes = %#v, want the run hash of the hostile key", arguments["unknown_key_hashes"])
	}
	if _, found := arguments["unknown_key_truncated"]; found {
		t.Fatalf("single hostile key was unexpectedly truncated: %#v", arguments)
	}
	if _, found := arguments["unknown_key_too_large_count"]; found {
		t.Fatalf("short hostile key was unexpectedly classified too large: %#v", arguments)
	}
}

func TestReadyzFailsClosedWhenRootDisappears(t *testing.T) {
	root := testutil.FixtureVault(t)
	cfg, err := config.Validate(config.Config{
		Mode:         config.ModeHTTP,
		ObsidianRoot: root,
		Addr:         config.DefaultHTTPAddr,
	})
	if err != nil {
		t.Fatal(err)
	}
	application, err := New(cfg, audit.Disabled())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(application.HTTPHandler())
	defer server.Close()

	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(server.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("/readyz status=%d body=%q", resp.StatusCode, body)
	}
	if strings.Contains(string(body), root) || strings.Contains(string(body), os.Getenv("HOME")) {
		t.Fatalf("/readyz leaked host path: %q", body)
	}
}

func TestReadyzFailsWhenTelemetryDegraded(t *testing.T) {
	root := testutil.FixtureVault(t)
	cfg, err := config.Validate(config.Config{
		Mode:         config.ModeHTTP,
		ObsidianRoot: root,
		Addr:         config.DefaultHTTPAddr,
	})
	if err != nil {
		t.Fatal(err)
	}
	log := audit.New("test-run", &appFailingSink{})
	application, err := New(cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	application.ready(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz status=%d body=%q, want 503", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "telemetry_degraded") {
		t.Fatalf("/readyz body=%q missing telemetry_degraded", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), root) {
		t.Fatalf("/readyz leaked root: %q", rec.Body.String())
	}
}

func TestToolCallsDoNotMutateVault(t *testing.T) {
	root := testutil.FixtureVault(t)
	before := testutil.Snapshot(t, root)
	cfg, err := config.Validate(config.Config{Mode: config.ModeStdio, ObsidianRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	application, err := New(cfg, audit.Disabled())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, stop := connectPipeTransport(t, ctx, application)
	defer stop()

	_ = callTool[obsidian.ResolveOutput](t, ctx, session, obsidian.ToolResolve, map[string]any{"path": "README.md"})
	_ = callTool[obsidian.LSOutput](t, ctx, session, obsidian.ToolLS, map[string]any{"path": ".", "limit": 10})

	after := testutil.Snapshot(t, root)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("fixture mutated:\nbefore=%#v\nafter=%#v", before, after)
	}
}

func TestStartupValidationDoesNotScanNestedDirectories(t *testing.T) {
	root := t.TempDir()
	sentinel := root + "/nested/sentinel"
	if err := os.MkdirAll(sentinel, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sentinel, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sentinel, 0o755) })

	cfg, err := config.Validate(config.Config{Mode: config.ModeStdio, ObsidianRoot: root})
	if err != nil {
		t.Fatalf("Validate() descended into unreadable nested dir: %v", err)
	}
	if _, err := New(cfg, audit.Disabled()); err != nil {
		t.Fatalf("New() descended into unreadable nested dir: %v", err)
	}
}

func newTestApp(t *testing.T) *App {
	t.Helper()
	cfg, err := config.Validate(config.Config{
		Mode:         config.ModeStdio,
		ObsidianRoot: testutil.FixtureVault(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	application, err := New(cfg, audit.Disabled())
	if err != nil {
		t.Fatal(err)
	}
	return application
}

func connectPipeTransport(t *testing.T, ctx context.Context, application *App) (*sdk.ClientSession, func()) {
	t.Helper()

	serverReader, clientWriter := io.Pipe()
	clientReader, serverWriter := io.Pipe()

	serverTransport := &sdk.IOTransport{Reader: serverReader, Writer: serverWriter}
	clientTransport := &sdk.IOTransport{Reader: clientReader, Writer: clientWriter}

	serverCtx, cancelServer := context.WithCancel(ctx)
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- application.Server().Run(serverCtx, serverTransport)
	}()

	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "v0.0.1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		cancelServer()
		t.Fatal(err)
	}

	stop := func() {
		session.Close()
		cancelServer()
		_ = clientWriter.Close()
		_ = clientReader.Close()
		_ = serverWriter.Close()
		_ = serverReader.Close()
		select {
		case <-serverDone:
		case <-time.After(2 * time.Second):
			t.Fatal("server did not stop")
		}
	}
	return session, stop
}

func assertTools(t *testing.T, ctx context.Context, session *sdk.ClientSession) {
	t.Helper()
	got := listedToolNames(t, ctx, session)
	want := []string{obsidian.ToolLS, obsidian.ToolResolve}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tools = %#v, want %#v", got, want)
	}
}

func listedToolNames(t *testing.T, ctx context.Context, session *sdk.ClientSession) []string {
	t.Helper()
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(tools.Tools))
	for _, tool := range tools.Tools {
		got = append(got, tool.Name)
	}
	sort.Strings(got)
	for _, tool := range tools.Tools {
		assertSchemaRequiresPath(t, tool)
		assertReadOnlyToolAnnotations(t, tool)
	}
	return got
}

func assertReadOnlyToolAnnotations(t *testing.T, tool *sdk.Tool) {
	t.Helper()
	annotations := tool.Annotations
	if annotations == nil {
		t.Fatalf("%s annotations are missing", tool.Name)
	}
	if !annotations.ReadOnlyHint {
		t.Fatalf("%s ReadOnlyHint = false, want true", tool.Name)
	}
	if annotations.DestructiveHint == nil || *annotations.DestructiveHint {
		t.Fatalf("%s DestructiveHint = %v, want explicit false", tool.Name, annotations.DestructiveHint)
	}
	if annotations.OpenWorldHint == nil || *annotations.OpenWorldHint {
		t.Fatalf("%s OpenWorldHint = %v, want explicit false", tool.Name, annotations.OpenWorldHint)
	}
}

func assertSchemaRequiresPath(t *testing.T, tool *sdk.Tool) {
	t.Helper()
	data, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties map[string]any `json:"properties"`
		Required   []string       `json:"required"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	if _, ok := schema.Properties["path"]; !ok {
		t.Fatalf("%s schema missing path property: %s", tool.Name, data)
	}
	for _, field := range schema.Required {
		if field == "path" {
			return
		}
	}
	t.Fatalf("%s schema does not require path: %s", tool.Name, data)
}

func callTool[Out any](t *testing.T, ctx context.Context, session *sdk.ClientSession, name string, args map[string]any) Out {
	t.Helper()
	result, err := session.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("%s returned tool error: %#v", name, result.Content)
	}
	var out Out
	unmarshalStructured(t, result, &out)
	return out
}

func unmarshalStructured(t *testing.T, result *sdk.CallToolResult, out any) {
	t.Helper()
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		t.Fatal(err)
	}
}

func assertResultDoesNotLeak(t *testing.T, result *sdk.CallToolResult, root string) {
	t.Helper()
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, root) || strings.Contains(text, os.Getenv("HOME")) {
		t.Fatalf("result leaked host path: %s", text)
	}
}

func entryNames(entries []obsidian.LSEntry) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Name)
	}
	return out
}

func driveSafeSummaryScenario(t *testing.T, root string, log *audit.Logger) []string {
	t.Helper()
	cfg, err := config.Validate(config.Config{
		Mode:         config.ModeStdio,
		ObsidianRoot: root,
		Telemetry:    config.TelemetryStderr,
	})
	if err != nil {
		t.Fatal(err)
	}
	application, err := New(cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, stop := connectPipeTransport(t, ctx, application)
	defer stop()

	resolved := callTool[obsidian.ResolveOutput](t, ctx, session, obsidian.ToolResolve, map[string]any{"path": "README.md"})
	if !resolved.OK || !resolved.Exists {
		t.Fatalf("summary scenario resolve = %#v", resolved)
	}
	first := callTool[obsidian.LSOutput](t, ctx, session, obsidian.ToolLS, map[string]any{"path": "home/projects", "limit": 1})
	if !first.OK || first.Coverage.NextCursor == "" {
		t.Fatalf("summary scenario first ls = %#v", first)
	}
	continued := callTool[obsidian.LSOutput](t, ctx, session, obsidian.ToolLS, map[string]any{
		"path": "home/projects", "limit": 1, "cursor": first.Coverage.NextCursor,
	})
	if !continued.OK || continued.Coverage.Continuation != "complete" {
		t.Fatalf("summary scenario continued ls = %#v", continued)
	}
	return []string{first.Coverage.NextCursor}
}

func toolCallRecords(records []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		if record["event"] == "tool.call" {
			out = append(out, record)
		}
	}
	return out
}

func auditRecords(t *testing.T, text string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("invalid audit JSON %q: %v", line, err)
		}
		out = append(out, record)
	}
	return out
}

func findToolRecord(records []map[string]any, tool, outcome, errorCode string) map[string]any {
	for _, record := range records {
		if record["event"] != "tool.call" || record["tool"] != tool || record["outcome"] != outcome {
			continue
		}
		if errorCode != "" && record["error_code"] != errorCode {
			continue
		}
		return record
	}
	return nil
}

func findHTTPRecord(records []map[string]any, route, method string, status int) map[string]any {
	for _, record := range records {
		if record["event"] != "http.request" || record["route"] != route || record["method"] != method {
			continue
		}
		if intFromRecord(record, "status") != status {
			continue
		}
		return record
	}
	return nil
}

func intFromRecord(record map[string]any, key string) int {
	if n, ok := record[key].(float64); ok {
		return int(n)
	}
	return 0
}

type appFailingSink struct{}

func (s *appFailingSink) WriteEvent(map[string]any) error { return errAppSinkFailed{} }

func (s *appFailingSink) Close() error { return nil }

type errAppSinkFailed struct{}

func (errAppSinkFailed) Error() string { return "failed" }

func sqliteAuditRows(t *testing.T, dbPath string) []map[string]any {
	t.Helper()
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	rows, err := db.Query(`SELECT event, COALESCE(method, ''), COALESCE(tool, ''), COALESCE(outcome, ''), COALESCE(error_code, ''), body_json FROM audit_events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var out []map[string]any
	for rows.Next() {
		var event, method, tool, outcome, code, body string
		if err := rows.Scan(&event, &method, &tool, &outcome, &code, &body); err != nil {
			t.Fatal(err)
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(body), &decoded); err != nil {
			t.Fatalf("body_json invalid: %v: %s", err, body)
		}
		decoded["event"] = event
		decoded["method"] = method
		decoded["tool"] = tool
		decoded["outcome"] = outcome
		decoded["error_code"] = code
		out = append(out, decoded)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func findRow(rows []map[string]any, event, tool, outcome, code string) map[string]any {
	for _, row := range rows {
		if row["event"] != event {
			continue
		}
		if tool != "" && row["tool"] != tool {
			continue
		}
		if outcome != "" && row["outcome"] != outcome {
			continue
		}
		if code != "" && row["error_code"] != code {
			continue
		}
		return row
	}
	return nil
}

func findMCPMethodRow(rows []map[string]any, method, outcome string) map[string]any {
	for _, row := range rows {
		if row["event"] != "mcp.request" || row["method"] != method || row["outcome"] != outcome {
			continue
		}
		return row
	}
	return nil
}

func stringListFromRecord(record map[string]any, key string) []string {
	values, ok := record[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if s, ok := value.(string); ok {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
