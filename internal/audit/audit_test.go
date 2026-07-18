package audit

import (
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"personal-mcp-gateway/internal/limits"
)

func TestSQLiteLoggerPersistsStructuredEvents(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "telemetry.sqlite")
	log, err := NewSQLite(dbPath, "test-run")
	if err != nil {
		t.Fatal(err)
	}

	log.Event("tool.call", map[string]any{
		"transport":   "stdio",
		"method":      "tools/call",
		"tool":        "ls",
		"outcome":     "tool_error",
		"error_code":  "path_denied",
		"duration_ms": int64(7),
		"args": map[string]any{
			"path": map[string]any{
				"present": true,
				"hash":    "abc123",
			},
		},
	})
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var tool, outcome, code, body string
	var duration int64
	err = db.QueryRow(`
SELECT tool, outcome, error_code, duration_ms, body_json
FROM audit_events
WHERE event = 'tool.call'
`).Scan(&tool, &outcome, &code, &duration, &body)
	if err != nil {
		t.Fatal(err)
	}
	if tool != "ls" || outcome != "tool_error" || code != "path_denied" || duration != 7 {
		t.Fatalf("unexpected row: tool=%q outcome=%q code=%q duration=%d", tool, outcome, code, duration)
	}
	if !json.Valid([]byte(body)) {
		t.Fatalf("body_json is not valid JSON: %q", body)
	}
}

func TestLoggerRecordsSinkWriteDegradation(t *testing.T) {
	sink := &failingSink{writeErr: errors.New("boom")}
	log := New("test-run", sink)

	var got Degradation
	log.SetDegradationHandler(func(d Degradation) {
		got = d
	})
	log.Event("tool.call", map[string]any{"tool": "ls"})

	if !log.Degraded() {
		t.Fatal("logger is not degraded after write failure")
	}
	if got.Sink != "fake" || got.Operation != "write" || got.ErrorCode != "write_failed" {
		t.Fatalf("degradation = %#v", got)
	}
	if d := log.Degradation(); d.Count != 1 {
		t.Fatalf("degradation count = %d, want 1", d.Count)
	}
}

func TestLoggerRecordsSQLiteWriteDegradationAfterStartup(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "telemetry.sqlite")
	sink, err := OpenSQLiteSink(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	log := New("test-run", sink)
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}

	log.Event("tool.call", map[string]any{"tool": "ls"})

	d := log.Degradation()
	if !d.Degraded || d.Sink != "sqlite" || d.Operation != "write" {
		t.Fatalf("degradation = %#v, want sqlite write degradation", d)
	}
}

func TestLoggerRecordsCloseDegradation(t *testing.T) {
	sink := &failingSink{closeErr: errors.New("boom")}
	log := New("test-run", sink)

	err := log.Close()
	if err == nil {
		t.Fatal("Close() error = nil, want error")
	}
	d := log.Degradation()
	if !d.Degraded || d.Sink != "fake" || d.Operation != "close" || d.ErrorCode != "close_failed" {
		t.Fatalf("degradation = %#v", d)
	}
}

func TestLoggerCapsOversizedEventBody(t *testing.T) {
	sink := &captureSink{}
	log := New("test-run", sink)
	log.Event("tool.call", map[string]any{
		"transport":     "stdio",
		"method":        "tools/call",
		"tool":          "ls",
		"outcome":       "tool_error",
		"error_code":    "path_denied",
		"duration_ms":   int64(7),
		"is_error":      true,
		"summary_error": "too_large",
		"entry_count":   999,
		"summary": map[string]any{
			"arguments": map[string]any{
				"raw": strings.Repeat("x", limits.TelemetryEventBytes*2),
			},
		},
	})
	if len(sink.records) != 1 {
		t.Fatalf("records = %d, want 1", len(sink.records))
	}
	record := sink.records[0]
	if record["body_truncated"] != true {
		t.Fatalf("body_truncated = %#v, want true in %#v", record["body_truncated"], record)
	}
	body, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) > limits.TelemetryEventBytes {
		t.Fatalf("bounded event size = %d, limit %d", len(body), limits.TelemetryEventBytes)
	}
	if strings.Contains(string(body), strings.Repeat("x", 128)) {
		t.Fatalf("bounded event retained large raw payload")
	}
	for _, key := range []string{"summary", "args", "entry_count"} {
		if _, found := record[key]; found {
			t.Fatalf("overflow record retained optional/domain key %q: %#v", key, record)
		}
	}
	if record["tool"] != "ls" || record["outcome"] != "tool_error" || record["error_code"] != "path_denied" || record["is_error"] != true || record["summary_error"] != "too_large" {
		t.Fatalf("overflow record lost validated base scalars: %#v", record)
	}
}

func TestLoggerBoundsUnmarshalableEventToValidatedBase(t *testing.T) {
	sink := &captureSink{}
	log := New("test-run", sink)
	log.Event("tool.call", map[string]any{
		"tool":        "ls",
		"outcome":     "ok",
		"duration_ms": int64(2),
		"summary":     make(chan struct{}),
		"route":       strings.Repeat("private", limits.TelemetryMaxKeyBytes),
	})

	if len(sink.records) != 1 {
		t.Fatalf("records = %d, want 1", len(sink.records))
	}
	record := sink.records[0]
	if record["body_truncated"] != true || record["body_bytes"] != 0 {
		t.Fatalf("invalid body fallback = %#v", record)
	}
	if record["tool"] != "ls" || record["outcome"] != "ok" || record["duration_ms"] != int64(2) {
		t.Fatalf("invalid body lost validated base: %#v", record)
	}
	if _, found := record["summary"]; found {
		t.Fatalf("invalid summary retained: %#v", record)
	}
	if _, found := record["route"]; found {
		t.Fatalf("oversized base string retained: %#v", record)
	}
}

type failingSink struct {
	writeErr error
	closeErr error
}

func (s *failingSink) Name() string { return "fake" }

func (s *failingSink) WriteEvent(map[string]any) error { return s.writeErr }

func (s *failingSink) Close() error { return s.closeErr }

type captureSink struct {
	records []map[string]any
}

func (s *captureSink) WriteEvent(record map[string]any) error {
	s.records = append(s.records, record)
	return nil
}

func (s *captureSink) Close() error { return nil }
