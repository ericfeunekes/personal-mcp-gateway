package obsidian

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"personal-mcp-gateway/internal/audit"
	"personal-mcp-gateway/internal/fsx"
	localmcp "personal-mcp-gateway/internal/mcp"
)

// TestGrepSpeculativeSuffixTelemetryUsesReducerCoverage lives in this package
// rather than internal/app because it drives grep's concurrency test hooks
// (grepConcurrentHooks), which stay unexported so they are unreachable from
// production code outside this package.
func TestGrepSpeculativeSuffixTelemetryUsesReducerCoverage(t *testing.T) {
	var jsonRecord map[string]any
	t.Run("jsonl", func(t *testing.T) {
		var buffer bytes.Buffer
		jsonRecord = driveSpeculativeGrepTelemetry(t, audit.NewJSONL(&buffer, "speculative-grep-jsonl"), func() []map[string]any {
			return grepTelemetryToolCallRecords(grepTelemetryAuditRecords(t, buffer.String()))
		})
		assertSpeculativeGrepTelemetry(t, jsonRecord)
	})
	t.Run("sqlite", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "speculative-grep.sqlite")
		log, err := audit.NewSQLite(path, "speculative-grep-sqlite")
		if err != nil {
			t.Fatal(err)
		}
		record := driveSpeculativeGrepTelemetry(t, log, func() []map[string]any {
			return grepTelemetryToolCallRecords(grepTelemetrySQLiteRows(t, path))
		})
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
	vault, err := fsx.NewVault(root)
	if err != nil {
		t.Fatal(err)
	}
	release, completedSuffix := make(chan struct{}), make(chan struct{})
	var once sync.Once
	descriptors, err := descriptorsWithGrepHooks(vault, nil, &grepConcurrentHooks{
		gate: func(ctx context.Context, sequence int) error {
			if sequence > 0 {
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-release:
				return nil
			}
		},
		terminalEvent: func(sequence int) {
			if sequence > 0 {
				once.Do(func() { close(completedSuffix) })
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server, _, err := localmcp.NewServer(log, "stdio", descriptors)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, stop := connectGrepTelemetrySession(t, ctx, server)
	defer stop()
	done := make(chan *sdk.CallToolResult, 1)
	go func() {
		result, callErr := session.CallTool(ctx, &sdk.CallToolParams{Name: ToolGrep, Arguments: map[string]any{"pattern": "hit", "regex": false, "case_sensitive": true, "limit": 1}})
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
	var out GrepOutput
	grepTelemetryUnmarshalStructured(t, result, &out)
	if result.IsError || !out.OK || out.Coverage.FilesScanned != 1 || out.Coverage.BytesScanned != 4 {
		t.Fatalf("grep=%#v", out)
	}
	return grepTelemetryFindRow(records(), "tool.call", ToolGrep, "ok", "")
}

func assertSpeculativeGrepTelemetry(t *testing.T, record map[string]any) {
	t.Helper()
	if record == nil {
		t.Fatal("missing grep telemetry")
	}
	summary, ok := record["summary"].(map[string]any)
	if !ok {
		t.Fatalf("record summary = %#v", record["summary"])
	}
	result, ok := summary["result"].(map[string]any)
	if !ok {
		t.Fatalf("summary result = %#v", summary["result"])
	}
	if grepTelemetryIntFromRecord(result, "files_scanned") != 1 || grepTelemetryIntFromRecord(result, "bytes_scanned") != 4 || result["stopped_by"] != "result_limit" {
		t.Fatalf("summary=%#v", result)
	}
}

// connectGrepTelemetrySession mirrors internal/app's connectPipeTransport
// test helper but connects directly to an *sdk.Server, since this package has
// no App type.
func connectGrepTelemetrySession(t *testing.T, ctx context.Context, server *sdk.Server) (*sdk.ClientSession, func()) {
	t.Helper()

	serverReader, clientWriter := io.Pipe()
	clientReader, serverWriter := io.Pipe()

	serverTransport := &sdk.IOTransport{Reader: serverReader, Writer: serverWriter}
	clientTransport := &sdk.IOTransport{Reader: clientReader, Writer: clientWriter}

	serverCtx, cancelServer := context.WithCancel(ctx)
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- server.Run(serverCtx, serverTransport)
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

func grepTelemetryUnmarshalStructured(t *testing.T, result *sdk.CallToolResult, out any) {
	t.Helper()
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		t.Fatal(err)
	}
}

func grepTelemetryToolCallRecords(records []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		if record["event"] == "tool.call" {
			out = append(out, record)
		}
	}
	return out
}

func grepTelemetryAuditRecords(t *testing.T, text string) []map[string]any {
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

func grepTelemetrySQLiteRows(t *testing.T, dbPath string) []map[string]any {
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

func grepTelemetryFindRow(rows []map[string]any, event, tool, outcome, code string) map[string]any {
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

func grepTelemetryIntFromRecord(record map[string]any, key string) int {
	if n, ok := record[key].(float64); ok {
		return int(n)
	}
	return 0
}
