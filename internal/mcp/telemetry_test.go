package mcp

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"personal-mcp-gateway/internal/audit"
	"personal-mcp-gateway/internal/limits"
)

func TestSummarizeArgsSanitizesUnknownKeysAndPathValues(t *testing.T) {
	raw := json.RawMessage(`{
		"path":"home/projects/alpha.md",
		"base":"notes",
		"limit":999,
		"/synthetic/private-vault/private.md":"x",
		"secret-token-value":"y"
	}`)

	got := summarizeArgs(raw, "test-run")
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, leaked := range []string{
		"home/projects/alpha.md",
		"/synthetic/private-vault",
		"private.md",
		"secret-token-value",
	} {
		if strings.Contains(text, leaked) {
			t.Fatalf("argument summary leaked %q: %s", leaked, text)
		}
	}
	if strings.Contains(text, `"keys"`) {
		t.Fatalf("argument summary persisted raw keys list: %s", text)
	}
	if got["unknown_key_count"] != 2 {
		t.Fatalf("unknown_key_count = %#v, want 2 in %s", got["unknown_key_count"], text)
	}
	if got["known_fields"] == nil {
		t.Fatalf("missing known_fields in %s", text)
	}
	limit := got["limit"].(map[string]any)
	if limit["category"] != "over_max" {
		t.Fatalf("limit summary = %#v, want over_max", limit)
	}
}

func TestSummarizeArgsTooLargeDoesNotUnmarshal(t *testing.T) {
	raw := bytes.Repeat([]byte("a"), limits.TelemetryArgsBytes+1)
	got := summarizeArgs(raw, "test-run")
	if got["too_large"] != true {
		t.Fatalf("too_large = %#v, want true", got["too_large"])
	}
	if _, ok := got["known_fields"]; ok {
		t.Fatalf("known_fields present for too-large args: %#v", got)
	}
}

func TestLogToolCallSanitizesUnknownToolName(t *testing.T) {
	var events bytes.Buffer
	log := audit.NewJSONL(&events, "test-run")
	req := &sdk.ServerRequest[*sdk.CallToolParamsRaw]{
		Params: &sdk.CallToolParamsRaw{
			Name:      "/synthetic/private-vault/private-tool",
			Arguments: json.RawMessage(`{"path":"README.md"}`),
		},
	}

	logToolCall(log, "stdio", []string{"ls"}, req, nil, errUnknownToolForTest{}, time.Millisecond)
	text := events.String()
	if strings.Contains(text, "/synthetic/private-vault") || strings.Contains(text, "private-tool") {
		t.Fatalf("tool telemetry leaked raw unknown tool name: %s", text)
	}
	records := decodeAuditRecords(t, text)
	record := records[0]
	if record["tool"] != "unknown" || record["error_code"] != "unknown_tool" {
		t.Fatalf("record = %#v, want sanitized unknown_tool", record)
	}
	meta, ok := record["tool_meta"].(map[string]any)
	if !ok || meta["known"] != false || meta["hash"] == "" {
		t.Fatalf("tool_meta = %#v, want unknown hash", record["tool_meta"])
	}
}

type errUnknownToolForTest struct{}

func (errUnknownToolForTest) Error() string {
	return "unknown tool"
}

func decodeAuditRecords(t *testing.T, text string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("invalid JSON %q: %v", line, err)
		}
		out = append(out, record)
	}
	return out
}
