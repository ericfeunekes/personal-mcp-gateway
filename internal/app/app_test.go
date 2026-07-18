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
	"sync"
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

	overLimitValidation := findToolRecord(records, obsidian.ToolLS, "tool_error", "schema_validation")
	if overLimitValidation == nil {
		t.Fatalf("missing over-limit schema_validation telemetry: %s", text)
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
	wantTools := []string{
		obsidian.ToolGrep,
		obsidian.ToolLS,
		obsidian.ToolRead,
		obsidian.ToolReadMany,
		obsidian.ToolResolve,
	}
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
	if findRow(rows, "tool.call", obsidian.ToolLS, "tool_error", "schema_validation") == nil {
		t.Fatalf("missing over-limit schema_validation row: %s", text)
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

func TestGrepSpeculativeSuffixTelemetryUsesReducerCoverage(t *testing.T) {
	var jsonRecord map[string]any
	t.Run("jsonl", func(t *testing.T) {
		var buffer bytes.Buffer
		jsonRecord = driveSpeculativeGrepTelemetry(t, audit.NewJSONL(&buffer, "speculative-grep-jsonl"), func() []map[string]any { return toolCallRecords(auditRecords(t, buffer.String())) })
		assertSpeculativeGrepTelemetry(t, jsonRecord)
	})
	t.Run("sqlite", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "speculative-grep.sqlite")
		log, err := audit.NewSQLite(path, "speculative-grep-sqlite")
		if err != nil {
			t.Fatal(err)
		}
		record := driveSpeculativeGrepTelemetry(t, log, func() []map[string]any { return toolCallRecords(sqliteAuditRows(t, path)) })
		if err := log.Close(); err != nil {
			t.Fatal(err)
		}
		assertSpeculativeGrepTelemetry(t, record)
		if !reflect.DeepEqual(jsonRecord["summary"], record["summary"]) {
			t.Fatalf("sink summaries differ: json=%#v sqlite=%#v", jsonRecord["summary"], record["summary"])
		}
	})
}

func driveSpeculativeGrepTelemetry(t *testing.T, log *audit.Logger, records func() []map[string]any) map[string]any {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.md"), []byte("hit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.md"), append([]byte("broken "), 0xff), 0o600); err != nil {
		t.Fatal(err)
	}
	release, completedSuffix := make(chan struct{}), make(chan struct{})
	var once sync.Once
	cfg, err := config.Validate(config.Config{Mode: config.ModeStdio, ObsidianRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	application, err := NewWithGrepTestHooks(cfg, log, &obsidian.GrepTestHooks{Gate: func(ctx context.Context, sequence int) error {
		if sequence > 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-release:
			return nil
		}
	}, TerminalEvent: func(sequence int) {
		if sequence > 0 {
			once.Do(func() { close(completedSuffix) })
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, stop := connectPipeTransport(t, ctx, application)
	defer stop()
	done := make(chan *sdk.CallToolResult, 1)
	go func() {
		result, callErr := session.CallTool(ctx, &sdk.CallToolParams{Name: obsidian.ToolGrep, Arguments: map[string]any{"pattern": "hit", "regex": false, "case_sensitive": true, "limit": 1}})
		if callErr != nil {
			t.Errorf("grep call: %v", callErr)
			return
		}
		done <- result
	}()
	select {
	case <-completedSuffix:
	case <-ctx.Done():
		t.Fatal("suffix scan did not complete before the canonical boundary")
	}
	close(release)
	result := <-done
	var out obsidian.GrepOutput
	unmarshalStructured(t, result, &out)
	if result.IsError || !out.OK || out.Coverage.FilesScanned != 1 || out.Coverage.BytesScanned != 4 {
		t.Fatalf("grep=%#v", out)
	}
	return findRow(records(), "tool.call", obsidian.ToolGrep, "ok", "")
}

func assertSpeculativeGrepTelemetry(t *testing.T, record map[string]any) {
	t.Helper()
	if record == nil {
		t.Fatal("missing grep telemetry")
	}
	result := summarySection(t, record, "result")
	if intFromRecord(result, "files_scanned") != 1 || intFromRecord(result, "bytes_scanned") != 4 || result["stopped_by"] != "result_limit" {
		t.Fatalf("summary=%#v", result)
	}
}

func TestPhase2JSONLAndSQLiteSafeSummariesMatchAcrossOutcomeClasses(t *testing.T) {
	const runID = "phase2-summary-parity-run"

	var jsonl synchronizedBuffer
	jsonLog := audit.NewJSONL(&jsonl, runID)
	jsonEvidence := drivePhase2SafeSummaryScenario(t, testutil.FixtureVault(t), jsonLog)
	jsonTerminalEvidence := drivePhase2SDKCancellationScenarios(t, testutil.FixtureVault(t), jsonLog, func() []map[string]any {
		return toolCallRecords(completeAuditRecords(t, jsonl.String()))
	})
	jsonEvidence.forbidden = append(jsonEvidence.forbidden, jsonTerminalEvidence.forbidden...)
	jsonRecords := toolCallRecords(auditRecords(t, jsonl.String()))

	dbPath := filepath.Join(t.TempDir(), "phase2-summary-parity.sqlite")
	sqliteLog, err := audit.NewSQLite(dbPath, runID)
	if err != nil {
		t.Fatal(err)
	}
	sqliteEvidence := drivePhase2SafeSummaryScenario(t, testutil.FixtureVault(t), sqliteLog)
	sqliteTerminalEvidence := drivePhase2SDKCancellationScenarios(t, testutil.FixtureVault(t), sqliteLog, func() []map[string]any {
		return toolCallRecords(sqliteAuditRows(t, dbPath))
	})
	sqliteEvidence.forbidden = append(sqliteEvidence.forbidden, sqliteTerminalEvidence.forbidden...)
	if err := sqliteLog.Close(); err != nil {
		t.Fatal(err)
	}
	sqliteRecords := toolCallRecords(sqliteAuditRows(t, dbPath))

	const deterministicRecords = 12
	const recordsPerCanceledTool = 2
	terminalTools := []string{obsidian.ToolRead, obsidian.ToolReadMany, obsidian.ToolGrep}
	wantRecords := deterministicRecords + recordsPerCanceledTool*len(terminalTools)
	if len(jsonRecords) != len(sqliteRecords) || len(jsonRecords) != wantRecords {
		t.Fatalf("Phase 2 tool-call record counts jsonl=%d sqlite=%d, want %d", len(jsonRecords), len(sqliteRecords), wantRecords)
	}
	for i := 0; i < deterministicRecords; i++ {
		for _, key := range []string{"tool", "outcome", "error_code"} {
			jsonValue, _ := jsonRecords[i][key].(string)
			sqliteValue, _ := sqliteRecords[i][key].(string)
			if jsonValue != sqliteValue {
				t.Fatalf("record[%d] %s differs: jsonl=%#v sqlite=%#v", i, key, jsonRecords[i][key], sqliteRecords[i][key])
			}
		}
		if !reflect.DeepEqual(jsonRecords[i]["summary"], sqliteRecords[i]["summary"]) {
			t.Fatalf("Phase 2 summary[%d] differs:\njsonl=%#v\nsqlite=%#v", i, jsonRecords[i]["summary"], sqliteRecords[i]["summary"])
		}
	}

	batchWithItemError := jsonRecords[3]
	if batchWithItemError["tool"] != obsidian.ToolReadMany || batchWithItemError["outcome"] != "ok" {
		t.Fatalf("item-error batch record = %#v", batchWithItemError)
	}
	batchResult := summarySection(t, batchWithItemError, "result")
	if intFromRecord(batchResult, "item_count") != 2 || intFromRecord(batchResult, "item_error_count") != 1 {
		t.Fatalf("item-error batch summary = %#v", batchResult)
	}
	for _, expected := range []struct {
		index int
		tool  string
		code  string
	}{
		{2, obsidian.ToolRead, "not_found"},
		{6, obsidian.ToolReadMany, "cursor_invalid"},
		{9, obsidian.ToolGrep, "invalid_regex"},
		{11, obsidian.ToolRead, "cursor_stale"},
	} {
		record := jsonRecords[expected.index]
		if record["tool"] != expected.tool || record["outcome"] != "tool_error" || record["error_code"] != expected.code {
			t.Fatalf("record[%d] = %#v, want %s tool_error/%s", expected.index, record, expected.tool, expected.code)
		}
	}
	for _, index := range []int{0, 4, 7, 10} {
		result := summarySection(t, jsonRecords[index], "result")
		if result["truncated"] != true || result["continuation"] != "cursor" || result["result_complete"] != false {
			t.Fatalf("partial summary[%d] = %#v", index, result)
		}
	}
	restart := summarySection(t, jsonRecords[11], "result")
	if restart["continuation"] != "restart" || restart["stopped_by"] != "source_change" || restart["scope_complete"] != false {
		t.Fatalf("restart summary = %#v", restart)
	}

	jsonText := jsonl.String()
	sqliteText := mustJSON(t, sqliteRecords)
	for _, forbidden := range append(jsonEvidence.forbidden, sqliteEvidence.forbidden...) {
		if strings.Contains(jsonText, forbidden) || strings.Contains(sqliteText, forbidden) {
			t.Fatalf("Phase 2 telemetry retained prohibited evidence %q", forbidden)
		}
	}
	for _, cursor := range append(jsonEvidence.cursors, sqliteEvidence.cursors...) {
		digest := fmt.Sprintf("%x", sha256.Sum256([]byte(cursor)))
		if strings.Contains(jsonText, cursor) || strings.Contains(sqliteText, cursor) ||
			strings.Contains(jsonText, digest) || strings.Contains(sqliteText, digest) {
			t.Fatal("Phase 2 telemetry retained a cursor or cursor digest")
		}
	}
	for index, tool := range terminalTools {
		terminalIndex := deterministicRecords + index*recordsPerCanceledTool
		assertPhase2TerminalRecordParity(t, tool, jsonRecords[terminalIndex], sqliteRecords[terminalIndex])
		followupIndex := terminalIndex + 1
		for _, key := range []string{"tool", "outcome", "error_code"} {
			jsonValue, _ := jsonRecords[followupIndex][key].(string)
			sqliteValue, _ := sqliteRecords[followupIndex][key].(string)
			if jsonValue != sqliteValue {
				t.Fatalf("follow-up record[%d] %s differs: jsonl=%#v sqlite=%#v", followupIndex, key, jsonRecords[followupIndex][key], sqliteRecords[followupIndex][key])
			}
		}
		if jsonRecords[followupIndex]["tool"] != tool || jsonRecords[followupIndex]["outcome"] != "ok" ||
			!reflect.DeepEqual(jsonRecords[followupIndex]["summary"], sqliteRecords[followupIndex]["summary"]) {
			t.Fatalf("%s follow-up parity differs:\njsonl=%#v\nsqlite=%#v", tool, jsonRecords[followupIndex], sqliteRecords[followupIndex])
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
	read := callTool[obsidian.ReadOutput](t, ctx, session, obsidian.ToolRead, map[string]any{"path": "README.md"})
	if !read.OK || read.Content == nil || *read.Content != "synthetic root note\n" {
		t.Fatalf("read over HTTP = %#v", read)
	}
	batch := callTool[obsidian.ReadManyOutput](t, ctx, session, obsidian.ToolReadMany, map[string]any{
		"requests": []any{
			map[string]any{"path": "home/projects/alpha.md"},
			map[string]any{"path": "home/projects/beta.md"},
		},
	})
	if !batch.OK || len(batch.Items) != 2 || batch.Coverage.Continuation != "complete" {
		t.Fatalf("read_many over HTTP = %#v", batch)
	}
	grep := callTool[obsidian.GrepOutput](t, ctx, session, obsidian.ToolGrep, map[string]any{
		"pattern": "synthetic", "path": ".", "regex": false, "context_lines": 0,
	})
	if !grep.OK || len(grep.Matches) != 3 || grep.Coverage.Continuation != "complete" {
		t.Fatalf("grep over HTTP = %#v", grep)
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
	before := testutil.Snapshot(t, root)
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, stop := connectPipeTransport(t, ctx, application)
	defer stop()
	result, err := session.CallTool(ctx, &sdk.CallToolParams{Name: obsidian.ToolRead, Arguments: map[string]any{
		"path": "README.md", "max_bytes": 4,
	}})
	if err != nil || result == nil || result.IsError {
		t.Fatalf("retrieval result did not survive telemetry degradation: result=%#v err=%v", result, err)
	}
	assertSDKResultBudget(t, result)
	var out obsidian.ReadOutput
	unmarshalStructured(t, result, &out)
	if !out.OK || out.Coverage.Continuation != "cursor" || out.Coverage.NextCursor == "" {
		t.Fatalf("degraded retrieval output = %#v", out)
	}
	if after := testutil.Snapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatalf("telemetry-degraded retrieval mutated vault:\nbefore=%#v\nafter=%#v", before, after)
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
	_ = callTool[obsidian.ReadOutput](t, ctx, session, obsidian.ToolRead, map[string]any{"path": "README.md"})
	_ = callTool[obsidian.ReadManyOutput](t, ctx, session, obsidian.ToolReadMany, map[string]any{
		"requests": []any{
			map[string]any{"path": "README.md"},
			map[string]any{"path": "home/projects/alpha.md"},
		},
	})
	_ = callTool[obsidian.GrepOutput](t, ctx, session, obsidian.ToolGrep, map[string]any{
		"pattern": "synthetic", "path": ".", "regex": false, "context_lines": 0,
	})

	for _, call := range []struct {
		name string
		args map[string]any
	}{
		{name: obsidian.ToolRead, args: map[string]any{"path": "README.md", "max_bytes": 4}},
		{name: obsidian.ToolRead, args: map[string]any{"path": "missing.md"}},
		{name: obsidian.ToolReadMany, args: map[string]any{"requests": []any{
			map[string]any{"path": "README.md"}, map[string]any{"path": "missing.md"},
		}}},
		{name: obsidian.ToolReadMany, args: map[string]any{"requests": []any{
			map[string]any{"path": "README.md"}, map[string]any{"path": "home/projects/alpha.md"},
		}, "max_bytes": 4}},
		{name: obsidian.ToolGrep, args: map[string]any{"pattern": "synthetic", "path": ".", "regex": false, "context_lines": 0, "limit": 1}},
		{name: obsidian.ToolGrep, args: map[string]any{"pattern": "(", "path": ".", "regex": true}},
	} {
		result, callErr := session.CallTool(ctx, &sdk.CallToolParams{Name: call.name, Arguments: call.args})
		if callErr != nil || result == nil {
			t.Fatalf("%s outcome call failed: result=%#v err=%v", call.name, result, callErr)
		}
		assertSDKResultBudget(t, result)
	}

	tools := obsidian.New(application.vault)
	canceled, cancelCall := context.WithCancel(context.Background())
	cancelCall()
	expired, cancelExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelExpired()
	literal := false
	for _, outcomeCtx := range []context.Context{canceled, expired} {
		_, readOut, readErr := tools.Read(outcomeCtx, nil, obsidian.ReadInput{Path: "README.md"})
		if readErr != nil || readOut.OK || readOut.Error == nil {
			t.Fatalf("terminal read outcome = %#v err=%v", readOut, readErr)
		}
		_, batchOut, batchErr := tools.ReadMany(outcomeCtx, nil, obsidian.ReadManyInput{Requests: []obsidian.ReadRequest{{Path: "README.md"}}})
		if batchErr != nil || batchOut.OK || batchOut.Error == nil {
			t.Fatalf("terminal read_many outcome = %#v err=%v", batchOut, batchErr)
		}
		_, grepOut, grepErr := tools.Grep(outcomeCtx, nil, obsidian.GrepInput{Pattern: "synthetic", Path: ".", Regex: &literal})
		if grepErr != nil || grepOut.OK || grepOut.Error == nil {
			t.Fatalf("terminal grep outcome = %#v err=%v", grepOut, grepErr)
		}
	}

	after := testutil.Snapshot(t, root)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("fixture mutated:\nbefore=%#v\nafter=%#v", before, after)
	}
}

func TestPhase2SDKRestartOutcomesDoNotMutateVault(t *testing.T) {
	t.Run("read", func(t *testing.T) {
		root := t.TempDir()
		path := "read-restart.md"
		if err := os.WriteFile(filepath.Join(root, path), []byte("alpha\nbeta\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		ctx, session, stop := connectPhase2TestSession(t, root, audit.Disabled())
		defer stop()

		arguments := map[string]any{"path": path, "max_bytes": 1}
		first := callTool[obsidian.ReadOutput](t, ctx, session, obsidian.ToolRead, arguments)
		if !first.OK || first.Coverage.NextCursor == "" {
			t.Fatalf("read restart seed = %#v", first)
		}
		if err := os.WriteFile(filepath.Join(root, path), []byte("changed alpha\nbeta\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		before := testutil.Snapshot(t, root)
		arguments["cursor"] = first.Coverage.NextCursor
		result, err := session.CallTool(ctx, &sdk.CallToolParams{Name: obsidian.ToolRead, Arguments: arguments})
		if err != nil || result == nil || !result.IsError {
			t.Fatalf("stale read result=%#v err=%v", result, err)
		}
		assertSDKResultBudget(t, result)
		var out obsidian.ReadOutput
		unmarshalStructured(t, result, &out)
		if out.OK || out.Error == nil || out.Error.Code != obsidian.CursorStaleCode || out.Coverage.Continuation != "restart" ||
			out.Coverage.StoppedBy != "source_change" || out.Coverage.ScopeComplete || out.Coverage.NextCursor != "" || out.Content != nil || out.Outline != nil {
			t.Fatalf("stale read output = %#v", out)
		}
		assertVaultSnapshotUnchanged(t, root, before, "stale read")
	})

	t.Run("read_many", func(t *testing.T) {
		root := t.TempDir()
		priorPath := "prior.md"
		currentPath := "current.md"
		if err := os.WriteFile(filepath.Join(root, priorPath), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, currentPath), []byte("abcdefgh"), 0o600); err != nil {
			t.Fatal(err)
		}
		ctx, session, stop := connectPhase2TestSession(t, root, audit.Disabled())
		defer stop()

		arguments := map[string]any{"requests": []any{
			map[string]any{"path": priorPath},
			map[string]any{"path": currentPath, "max_bytes": 1},
		}}
		first := callTool[obsidian.ReadManyOutput](t, ctx, session, obsidian.ToolReadMany, arguments)
		if !first.OK || first.Coverage.NextCursor == "" || first.NextRequestIndex != 1 {
			t.Fatalf("read_many restart seed = %#v", first)
		}
		if err := os.WriteFile(filepath.Join(root, priorPath), []byte("changed"), 0o600); err != nil {
			t.Fatal(err)
		}
		before := testutil.Snapshot(t, root)
		arguments["cursor"] = first.Coverage.NextCursor
		result, err := session.CallTool(ctx, &sdk.CallToolParams{Name: obsidian.ToolReadMany, Arguments: arguments})
		if err != nil || result == nil || !result.IsError {
			t.Fatalf("stale read_many result=%#v err=%v", result, err)
		}
		assertSDKResultBudget(t, result)
		var out obsidian.ReadManyOutput
		unmarshalStructured(t, result, &out)
		if out.OK || out.Error == nil || out.Error.Code != obsidian.CursorStaleCode || out.Coverage.Continuation != "restart" ||
			out.Coverage.StoppedBy != "source_change" || out.Coverage.ScopeComplete || out.Coverage.NextCursor != "" || len(out.Items) != 0 {
			t.Fatalf("stale read_many output = %#v", out)
		}
		assertVaultSnapshotUnchanged(t, root, before, "stale read_many")
	})

	t.Run("grep", func(t *testing.T) {
		root := t.TempDir()
		firstPath := "a.md"
		if err := os.WriteFile(filepath.Join(root, firstPath), []byte("none\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "b.md"), []byte("none\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		ctx, session, stop := connectPhase2TestSession(t, root, audit.Disabled())
		defer stop()

		arguments := map[string]any{
			"pattern": "absent", "path": ".", "regex": false, "context_lines": 0, "max_files": 1,
		}
		first := callTool[obsidian.GrepOutput](t, ctx, session, obsidian.ToolGrep, arguments)
		if !first.OK || first.Coverage.NextCursor == "" {
			t.Fatalf("grep restart seed = %#v", first)
		}
		if err := os.WriteFile(filepath.Join(root, firstPath), []byte("changed\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		before := testutil.Snapshot(t, root)
		arguments["cursor"] = first.Coverage.NextCursor
		result, err := session.CallTool(ctx, &sdk.CallToolParams{Name: obsidian.ToolGrep, Arguments: arguments})
		if err != nil || result == nil || !result.IsError {
			t.Fatalf("stale grep result=%#v err=%v", result, err)
		}
		assertSDKResultBudget(t, result)
		var out obsidian.GrepOutput
		unmarshalStructured(t, result, &out)
		if out.OK || out.Error == nil || out.Error.Code != obsidian.CursorStaleCode || out.Coverage.Continuation != "restart" ||
			out.Coverage.StoppedBy != "source_change" || out.Coverage.ScopeComplete || out.Coverage.NextCursor != "" || len(out.Matches) != 0 {
			t.Fatalf("stale grep output = %#v", out)
		}
		assertVaultSnapshotUnchanged(t, root, before, "stale grep")
	})
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
	want := []string{
		obsidian.ToolGrep,
		obsidian.ToolLS,
		obsidian.ToolRead,
		obsidian.ToolReadMany,
		obsidian.ToolResolve,
	}
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
		assertRequiredToolInputs(t, tool)
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

func assertRequiredToolInputs(t *testing.T, tool *sdk.Tool) {
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
	want := []string{"path"}
	switch tool.Name {
	case obsidian.ToolReadMany:
		want = []string{"requests"}
	case obsidian.ToolGrep:
		want = []string{"pattern"}
	}
	for _, required := range want {
		if _, ok := schema.Properties[required]; !ok {
			t.Fatalf("%s schema missing %s property: %s", tool.Name, required, data)
		}
		found := false
		for _, field := range schema.Required {
			if field == required {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("%s schema does not require %s: %s", tool.Name, required, data)
		}
	}
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

type phase2SafeSummaryEvidence struct {
	cursors   []string
	forbidden []string
}

func drivePhase2SafeSummaryScenario(t *testing.T, root string, log *audit.Logger) phase2SafeSummaryEvidence {
	t.Helper()
	const (
		privatePath    = "private-health-workout.md"
		secondPath     = "private-health-recovery.md"
		privatePattern = "private-health-needle"
		privateBody    = "# Private Health\nprivate-health-needle workout evidence\nsecond line\n"
		changedBody    = "# Private Health\nchanged private medical evidence\n"
	)
	if err := os.WriteFile(filepath.Join(root, privatePath), []byte(privateBody), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, secondPath), []byte(privatePattern+" recovery evidence\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stableSourceTime := time.Unix(1_700_000_000, 123_456_789)
	for _, relative := range []string{privatePath, secondPath} {
		if err := os.Chtimes(filepath.Join(root, relative), stableSourceTime, stableSourceTime); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Validate(config.Config{Mode: config.ModeStdio, ObsidianRoot: root, Telemetry: config.TelemetryStderr})
	if err != nil {
		t.Fatal(err)
	}
	application, err := New(cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, stop := connectPipeTransport(t, ctx, application)
	defer stop()

	call := func(name string, args map[string]any, wantError bool, out any) *sdk.CallToolResult {
		t.Helper()
		result, callErr := session.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
		if callErr != nil {
			t.Fatal(callErr)
		}
		if result.IsError != wantError {
			t.Fatalf("%s IsError=%v, want %v: %#v", name, result.IsError, wantError, result.Content)
		}
		if out != nil {
			unmarshalStructured(t, result, out)
		}
		return result
	}

	readArgs := map[string]any{"path": privatePath, "max_bytes": 8}
	var firstRead obsidian.ReadOutput
	call(obsidian.ToolRead, readArgs, false, &firstRead)
	if !firstRead.OK || firstRead.Coverage.NextCursor == "" {
		t.Fatalf("partial read setup = %#v", firstRead)
	}
	readContinue := map[string]any{"path": privatePath, "max_bytes": 8, "cursor": firstRead.Coverage.NextCursor}
	var continuedRead obsidian.ReadOutput
	call(obsidian.ToolRead, readContinue, false, &continuedRead)
	call(obsidian.ToolRead, map[string]any{"path": "missing-private-health.md"}, true, &obsidian.ReadOutput{})

	itemErrorRequests := []any{map[string]any{"path": privatePath}, map[string]any{"path": "missing-private-health.md"}}
	var itemErrorBatch obsidian.ReadManyOutput
	call(obsidian.ToolReadMany, map[string]any{"requests": itemErrorRequests}, false, &itemErrorBatch)
	if !itemErrorBatch.OK || len(itemErrorBatch.Items) != 2 || itemErrorBatch.Items[1].OK {
		t.Fatalf("item-error batch = %#v", itemErrorBatch)
	}
	batchRequests := []any{map[string]any{"path": privatePath}, map[string]any{"path": secondPath}}
	batchArgs := map[string]any{"requests": batchRequests, "max_bytes": 8}
	var firstBatch obsidian.ReadManyOutput
	call(obsidian.ToolReadMany, batchArgs, false, &firstBatch)
	if !firstBatch.OK || firstBatch.Coverage.NextCursor == "" {
		t.Fatalf("partial read_many setup = %#v", firstBatch)
	}
	var continuedBatch obsidian.ReadManyOutput
	call(obsidian.ToolReadMany, map[string]any{"requests": batchRequests, "max_bytes": 8, "cursor": firstBatch.Coverage.NextCursor}, false, &continuedBatch)
	call(obsidian.ToolReadMany, map[string]any{"requests": batchRequests, "max_bytes": 8, "cursor": "invalid"}, true, &obsidian.ReadManyOutput{})

	grepArgs := map[string]any{
		"pattern": privatePattern, "path": ".", "regex": false, "context_lines": 0, "limit": 1,
	}
	var firstGrep obsidian.GrepOutput
	call(obsidian.ToolGrep, grepArgs, false, &firstGrep)
	if !firstGrep.OK || firstGrep.Coverage.NextCursor == "" {
		t.Fatalf("partial grep setup = %#v", firstGrep)
	}
	var continuedGrep obsidian.GrepOutput
	call(obsidian.ToolGrep, map[string]any{
		"pattern": privatePattern, "path": ".", "regex": false, "context_lines": 0, "limit": 1,
		"cursor": firstGrep.Coverage.NextCursor,
	}, false, &continuedGrep)
	call(obsidian.ToolGrep, map[string]any{"pattern": "(", "path": ".", "regex": true}, true, &obsidian.GrepOutput{})

	var staleSeed obsidian.ReadOutput
	call(obsidian.ToolRead, readArgs, false, &staleSeed)
	if staleSeed.Coverage.NextCursor == "" {
		t.Fatalf("stale read seed = %#v", staleSeed)
	}
	if err := os.WriteFile(filepath.Join(root, privatePath), []byte(changedBody), 0o600); err != nil {
		t.Fatal(err)
	}
	changedSourceTime := stableSourceTime.Add(time.Second)
	if err := os.Chtimes(filepath.Join(root, privatePath), changedSourceTime, changedSourceTime); err != nil {
		t.Fatal(err)
	}
	call(obsidian.ToolRead, map[string]any{"path": privatePath, "max_bytes": 8, "cursor": staleSeed.Coverage.NextCursor}, true, &obsidian.ReadOutput{})

	return phase2SafeSummaryEvidence{
		cursors: []string{
			firstRead.Coverage.NextCursor,
			firstBatch.Coverage.NextCursor,
			firstGrep.Coverage.NextCursor,
			staleSeed.Coverage.NextCursor,
		},
		forbidden: []string{
			privatePath, secondPath, "missing-private-health.md", privatePattern, privateBody, changedBody,
			audit.HashString(log.RunID(), privatePattern),
		},
	}
}

func drivePhase2SDKCancellationScenarios(
	t *testing.T,
	root string,
	log *audit.Logger,
	records func() []map[string]any,
) phase2SafeSummaryEvidence {
	t.Helper()
	const (
		terminalPath        = "terminal-private-source.md"
		followupPath        = "terminal-followup.md"
		terminalNeedle      = "terminal-private-content-never-persist"
		terminalPattern     = "terminal-private-pattern-never-persist"
		terminalWaitTimeout = 2 * time.Second
	)
	line := "# " + terminalNeedle + " " + strings.Repeat("x", 80) + "\n"
	content := strings.Repeat(line, obsidian.MaxMarkdownSourceLines-1)
	if len(content) >= obsidian.MaxMarkdownSourceBytes {
		t.Fatalf("terminal-context fixture bytes = %d, max %d", len(content), obsidian.MaxMarkdownSourceBytes)
	}
	if err := os.WriteFile(filepath.Join(root, terminalPath), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, followupPath), []byte("terminal follow-up evidence\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stableSourceTime := time.Unix(1_700_000_000, 123_456_789)
	for _, relative := range []string{terminalPath, followupPath} {
		if err := os.Chtimes(filepath.Join(root, relative), stableSourceTime, stableSourceTime); err != nil {
			t.Fatal(err)
		}
	}

	cfg, err := config.Validate(config.Config{Mode: config.ModeStdio, ObsidianRoot: root, Telemetry: config.TelemetryStderr})
	if err != nil {
		t.Fatal(err)
	}
	activity := &fsx.ActivityCounter{}
	application, err := NewWithVaultActivity(cfg, log, activity)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancelSession := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelSession()
	session, stop := connectPipeTransport(t, ctx, application)
	defer stop()

	tests := []struct {
		tool         string
		arguments    map[string]any
		followupArgs map[string]any
	}{
		{
			tool: obsidian.ToolRead,
			arguments: map[string]any{
				"path": terminalPath, "selector": map[string]any{"kind": obsidian.SelectorOutline},
			},
			followupArgs: map[string]any{"path": followupPath, "max_bytes": 8},
		},
		{
			tool: obsidian.ToolReadMany,
			arguments: map[string]any{"requests": []any{map[string]any{
				"path": terminalPath, "selector": map[string]any{"kind": obsidian.SelectorOutline},
			}}},
			followupArgs: map[string]any{"requests": []any{map[string]any{"path": followupPath, "max_bytes": 8}}},
		},
		{
			tool: obsidian.ToolGrep,
			arguments: map[string]any{
				"pattern": terminalPattern, "path": terminalPath, "regex": false, "context_lines": 0,
			},
			followupArgs: map[string]any{
				"pattern": "follow-up", "path": followupPath, "regex": false, "context_lines": 0,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.tool, func(t *testing.T) {
			before := testutil.Snapshot(t, root)
			baselineRecords := len(records())
			baselineActivity := activity.Snapshot().Total
			callCtx, cancelCall := context.WithCancel(ctx)
			type callOutcome struct {
				result *sdk.CallToolResult
				err    error
			}
			completed := make(chan callOutcome, 1)
			go func() {
				result, callErr := session.CallTool(callCtx, &sdk.CallToolParams{Name: test.tool, Arguments: test.arguments})
				completed <- callOutcome{result: result, err: callErr}
			}()

			waitForPhase2VaultActivity(t, activity, baselineActivity, terminalWaitTimeout)
			cancelCall()
			var outcome callOutcome
			select {
			case outcome = <-completed:
			case <-time.After(terminalWaitTimeout):
				t.Fatalf("%s SDK call did not return after cancellation", test.tool)
			}
			if outcome.err == nil && (outcome.result == nil || !outcome.result.IsError) {
				t.Fatalf("%s canceled SDK call returned success: %#v", test.tool, outcome.result)
			}

			record := waitForPhase2ToolRecord(t, records, baselineRecords, test.tool, "canceled", terminalWaitTimeout)
			assertPhase2CanceledRecord(t, test.tool, record)

			followup, followupErr := session.CallTool(ctx, &sdk.CallToolParams{Name: test.tool, Arguments: test.followupArgs})
			if followupErr != nil || followup == nil || followup.IsError {
				t.Fatalf("%s follow-up did not recover: result=%#v err=%v", test.tool, followup, followupErr)
			}
			assertSDKResultBudget(t, followup)
			assertVaultSnapshotUnchanged(t, root, before, test.tool+" cancellation and follow-up")
		})
	}

	return phase2SafeSummaryEvidence{forbidden: []string{terminalPath, followupPath, terminalNeedle, terminalPattern}}
}

func waitForPhase2VaultActivity(t *testing.T, activity *fsx.ActivityCounter, baseline uint64, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		snapshot := activity.Snapshot()
		if snapshot.Total > baseline && snapshot.Active > 0 {
			return
		}
		time.Sleep(100 * time.Microsecond)
	}
	t.Fatalf("Phase 2 SDK call did not enter vault work before cancellation")
}

func waitForPhase2ToolRecord(
	t *testing.T,
	records func() []map[string]any,
	baseline int,
	tool string,
	errorCode string,
	timeout time.Duration,
) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		current := records()
		if baseline < len(current) {
			for _, record := range current[baseline:] {
				if record["tool"] == tool && record["outcome"] == "tool_error" && record["error_code"] == errorCode {
					return record
				}
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("missing terminal %s/%s telemetry after SDK cancellation", tool, errorCode)
	return nil
}

func assertPhase2CanceledRecord(t *testing.T, tool string, record map[string]any) {
	t.Helper()
	if record["tool"] != tool || record["outcome"] != "tool_error" || record["error_code"] != "canceled" {
		t.Fatalf("%s cancellation record = %#v", tool, record)
	}
	result := summarySection(t, record, "result")
	if result["ok"] != false || result["is_error"] != true || result["error_code"] != "canceled" ||
		result["stopped_by"] != "canceled" || result["continuation"] != "restart" ||
		result["scope_complete"] != false || result["result_complete"] != false {
		t.Fatalf("%s cancellation summary = %#v", tool, result)
	}
}

func assertPhase2TerminalRecordParity(t *testing.T, tool string, jsonRecord, sqliteRecord map[string]any) {
	t.Helper()
	assertPhase2CanceledRecord(t, tool, jsonRecord)
	assertPhase2CanceledRecord(t, tool, sqliteRecord)
	jsonSummary := normalizedPhase2TerminalSummary(t, jsonRecord)
	sqliteSummary := normalizedPhase2TerminalSummary(t, sqliteRecord)
	if !reflect.DeepEqual(jsonSummary, sqliteSummary) {
		t.Fatalf("%s terminal summary parity differs after normalizing work counters:\njsonl=%#v\nsqlite=%#v", tool, jsonSummary, sqliteSummary)
	}
}

func normalizedPhase2TerminalSummary(t *testing.T, record map[string]any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(record["summary"])
	if err != nil {
		t.Fatal(err)
	}
	var summary map[string]any
	if err := json.Unmarshal(encoded, &summary); err != nil {
		t.Fatal(err)
	}
	result, ok := summary["result"].(map[string]any)
	if !ok {
		t.Fatalf("terminal summary result = %#v", summary["result"])
	}
	for _, key := range []string{
		"files_scanned", "bytes_scanned", "source_entries_validated", "item_count", "item_error_count", "remaining_count", "match_count", "response_bytes",
	} {
		if value, found := result[key]; found {
			number, numeric := value.(float64)
			if !numeric || number < 0 {
				t.Fatalf("terminal summary counter %s = %#v", key, value)
			}
			result[key] = float64(0)
		}
	}
	return summary
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

func completeAuditRecords(t *testing.T, text string) []map[string]any {
	t.Helper()
	lastNewline := strings.LastIndexByte(text, '\n')
	if lastNewline < 0 {
		return nil
	}
	return auditRecords(t, text[:lastNewline])
}

type synchronizedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *synchronizedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}

func (b *synchronizedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func connectPhase2TestSession(t *testing.T, root string, log *audit.Logger) (context.Context, *sdk.ClientSession, func()) {
	t.Helper()
	cfg, err := config.Validate(config.Config{Mode: config.ModeStdio, ObsidianRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	application, err := New(cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	session, stop := connectPipeTransport(t, ctx, application)
	return ctx, session, func() {
		stop()
		cancel()
	}
}

func assertVaultSnapshotUnchanged(t *testing.T, root string, before []string, operation string) {
	t.Helper()
	if after := testutil.Snapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatalf("%s mutated vault:\nbefore=%#v\nafter=%#v", operation, before, after)
	}
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
