package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"personal-mcp-gateway/internal/audit"
	"personal-mcp-gateway/internal/limits"
)

func TestUnknownSummarySanitizesKeysAndDoesNotInspectValues(t *testing.T) {
	raw := json.RawMessage(`{
		"/synthetic/private-vault/private.md":"home/projects/alpha.md",
		"secret-token-value":"never-retain"
	}`)

	envelope, summaryError, ok := buildUnknownSummary("test-run", raw)
	if !ok || summaryError != "" {
		t.Fatalf("buildUnknownSummary() = ok %v, error %q", ok, summaryError)
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, leaked := range []string{
		"/synthetic/private-vault",
		"private.md",
		"home/projects/alpha.md",
		"secret-token-value",
		"never-retain",
	} {
		if strings.Contains(text, leaked) {
			t.Fatalf("unknown summary leaked %q: %s", leaked, text)
		}
	}

	decoded := decodeSummary(t, envelope)
	arguments := decoded["arguments"].(map[string]any)
	if arguments["shape"] != "object" || int(arguments["unknown_key_count"].(float64)) != 2 {
		t.Fatalf("arguments = %#v", arguments)
	}
	if hashes, ok := arguments["unknown_key_hashes"].([]any); !ok || len(hashes) != 2 {
		t.Fatalf("unknown_key_hashes = %#v, want two hashes", arguments["unknown_key_hashes"])
	}
}

func TestUnknownSummaryTooLargeDoesNotParseArguments(t *testing.T) {
	raw := bytes.Repeat([]byte("private-value"), limits.TelemetryArgsBytes)
	envelope, summaryError, ok := buildUnknownSummary("test-run", raw)
	if !ok || summaryError != "" {
		t.Fatalf("buildUnknownSummary() = ok %v, error %q", ok, summaryError)
	}
	arguments := decodeSummary(t, envelope)["arguments"].(map[string]any)
	if arguments["too_large"] != true {
		t.Fatalf("arguments = %#v, want too_large", arguments)
	}
	if _, found := arguments["unknown_key_hashes"]; found {
		t.Fatalf("oversized arguments were parsed: %#v", arguments)
	}
}

func TestSafeSummaryBuilderUnknownArgumentKeysAreBoundedAndDeterministic(t *testing.T) {
	const runID = "bounded-key-run"
	object := map[string]any{
		"path": "known-path-value-never-retain",
		"base": "known-base-value-never-retain",
	}
	unknown := make([]string, 0, limits.TelemetryMaxKeys+3)
	for i := 0; i < limits.TelemetryMaxKeys+2; i++ {
		key := fmt.Sprintf("unknown-%03d", i)
		unknown = append(unknown, key)
		object[key] = fmt.Sprintf("unknown-value-never-retain-%03d", i)
	}
	tooLargeKey := "oversized-" + strings.Repeat("k", limits.TelemetryMaxKeyBytes)
	unknown = append(unknown, tooLargeKey)
	object[tooLargeKey] = "oversized-key-value-never-retain"
	sort.Strings(unknown)

	build := func() safeSummaryEnvelope {
		builder := newSafeSummaryBuilder(runID)
		if err := builder.UnknownArgumentKeys(object, "path", "base"); err != nil {
			t.Fatal(err)
		}
		envelope, err := builder.seal()
		if err != nil {
			t.Fatal(err)
		}
		return envelope
	}
	first := build()
	second := build()
	if !reflect.DeepEqual(first.raw, second.raw) {
		t.Fatalf("unknown-key summaries are nondeterministic:\n%s\n%s", first.raw, second.raw)
	}

	arguments := decodeSummary(t, first)["arguments"].(map[string]any)
	if got := int(arguments["unknown_key_count"].(float64)); got != len(unknown) {
		t.Fatalf("unknown_key_count = %d, want %d", got, len(unknown))
	}
	if arguments["unknown_key_truncated"] != true {
		t.Fatalf("unknown_key_truncated = %#v, want true", arguments["unknown_key_truncated"])
	}
	if got := int(arguments["unknown_key_too_large_count"].(float64)); got != 1 {
		t.Fatalf("unknown_key_too_large_count = %d, want 1", got)
	}
	hashes, ok := arguments["unknown_key_hashes"].([]any)
	if !ok || len(hashes) != limits.TelemetryMaxKeys {
		t.Fatalf("unknown_key_hashes = %#v, want %d entries", arguments["unknown_key_hashes"], limits.TelemetryMaxKeys)
	}
	for i, got := range hashes {
		if want := audit.HashString(runID, unknown[i]); got != want {
			t.Fatalf("unknown_key_hashes[%d] = %#v, want %q", i, got, want)
		}
	}

	text := string(first.raw)
	for _, leaked := range []string{
		unknown[0],
		tooLargeKey,
		"known-path-value-never-retain",
		"unknown-value-never-retain-000",
		"oversized-key-value-never-retain",
	} {
		if strings.Contains(text, leaked) {
			t.Fatalf("bounded unknown-key summary leaked %q: %s", leaked, text)
		}
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

	logToolCall(log, "stdio", nil, []string{"ls"}, req, nil, errUnknownToolForTest{}, time.Millisecond)
	text := events.String()
	if strings.Contains(text, "/synthetic/private-vault") || strings.Contains(text, "private-tool") {
		t.Fatalf("tool telemetry leaked raw unknown tool name: %s", text)
	}
	record := decodeAuditRecords(t, text)[0]
	if record["tool"] != "unknown" || record["error_code"] != "unknown_tool" || record["outcome"] != "protocol_error" {
		t.Fatalf("record = %#v, want sanitized unknown_tool", record)
	}
	if _, ok := record["summary"].(map[string]any); !ok {
		t.Fatalf("record summary = %#v, want safe unknown fallback", record["summary"])
	}
}

func TestDescriptorSummaryIsSelectedAndPromotesClosedErrorCode(t *testing.T) {
	descriptor := testDescriptor(t,
		func(builder *SafeSummaryBuilder, raw json.RawMessage) error {
			var input summaryTestInput
			if err := json.Unmarshal(raw, &input); err != nil {
				return err
			}
			return builder.Path(PathInput, true, JSONString, input.Path)
		},
		func(builder *SafeSummaryBuilder, _ *sdk.CallToolResult) error {
			if err := builder.Bool(SectionResult, BoolOK, false); err != nil {
				return err
			}
			return builder.Enum(SectionResult, EnumErrorCode, ValuePathDenied)
		},
	)

	var events bytes.Buffer
	log := audit.NewJSONL(&events, "test-run")
	req := &sdk.ServerRequest[*sdk.CallToolParamsRaw]{Params: &sdk.CallToolParamsRaw{
		Name:      descriptor.Name(),
		Arguments: json.RawMessage(`{"path":"home/projects/private.md"}`),
	}}
	result := &sdk.CallToolResult{IsError: true, StructuredContent: json.RawMessage(`{"ok":false}`)}
	logToolCall(log, "stdio", descriptorLookup([]ToolDescriptor{descriptor}), []string{descriptor.Name()}, req, result, nil, time.Millisecond)

	text := events.String()
	if strings.Contains(text, "home/projects") || strings.Contains(text, "private.md") {
		t.Fatalf("descriptor summary leaked path: %s", text)
	}
	record := decodeAuditRecords(t, text)[0]
	if record["outcome"] != "tool_error" || record["error_code"] != "path_denied" || record["is_error"] != true {
		t.Fatalf("record = %#v", record)
	}
	summary := record["summary"].(map[string]any)
	resultSummary := summary["result"].(map[string]any)
	if resultSummary["error_code"] != "path_denied" || resultSummary["ok"] != false {
		t.Fatalf("result summary = %#v", resultSummary)
	}
}

func TestDescriptorSummaryFailureOmitsEnvelopeButPreservesBaseCall(t *testing.T) {
	tests := []struct {
		name string
		args ArgumentSummarizer
		want string
	}{
		{
			name: "returned error",
			args: func(*SafeSummaryBuilder, json.RawMessage) error { return errors.New("private callback detail") },
			want: summaryErrorCallback,
		},
		{
			name: "panic",
			args: func(*SafeSummaryBuilder, json.RawMessage) error { panic("private panic detail") },
			want: summaryErrorPanic,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			descriptor := testDescriptor(t, tt.args, noResultSummary)
			var events bytes.Buffer
			log := audit.NewJSONL(&events, "test-run")
			req := &sdk.ServerRequest[*sdk.CallToolParamsRaw]{Params: &sdk.CallToolParamsRaw{
				Name: descriptor.Name(), Arguments: json.RawMessage(`{"path":"private.md"}`),
			}}
			logToolCall(log, "stdio", descriptorLookup([]ToolDescriptor{descriptor}), []string{descriptor.Name()}, req, &sdk.CallToolResult{}, nil, time.Millisecond)

			text := events.String()
			if strings.Contains(text, "private callback detail") || strings.Contains(text, "private panic detail") || strings.Contains(text, "private.md") {
				t.Fatalf("failed summary leaked callback data: %s", text)
			}
			record := decodeAuditRecords(t, text)[0]
			if record["event"] != "tool.call" || record["tool"] != descriptor.Name() || record["outcome"] != "ok" {
				t.Fatalf("base tool.call = %#v", record)
			}
			if record["summary_error"] != tt.want {
				t.Fatalf("summary_error = %#v, want %q", record["summary_error"], tt.want)
			}
			if _, found := record["summary"]; found {
				t.Fatalf("failed summary was retained: %#v", record["summary"])
			}
		})
	}
}

func TestDescriptorSummaryValidationFailureOmitsOnlySummary(t *testing.T) {
	tests := []struct {
		name       string
		args       ArgumentSummarizer
		result     ResultSummarizer
		callResult *sdk.CallToolResult
		want       string
	}{
		{
			name: "incompatible enum",
			args: func(builder *SafeSummaryBuilder, _ json.RawMessage) error {
				return builder.Enum(SectionArguments, EnumShape, ValuePathDenied)
			},
			result:     noResultSummary,
			callResult: &sdk.CallToolResult{},
			want:       "ok",
		},
		{
			name: "argument base overwrite",
			args: func(builder *SafeSummaryBuilder, _ json.RawMessage) error {
				return builder.Counter(SectionArguments, CounterRawBytes, 0)
			},
			result:     noResultSummary,
			callResult: &sdk.CallToolResult{},
			want:       "ok",
		},
		{
			name: "result base overwrite",
			args: noArgumentSummary,
			result: func(builder *SafeSummaryBuilder, _ *sdk.CallToolResult) error {
				return builder.Bool(SectionResult, BoolIsError, false)
			},
			callResult: &sdk.CallToolResult{IsError: true},
			want:       "tool_error",
		},
		{
			name: "duplicate domain field",
			args: func(builder *SafeSummaryBuilder, _ json.RawMessage) error {
				if err := builder.Path(PathInput, true, JSONString, "first.md"); err != nil {
					return err
				}
				return builder.Path(PathInput, true, JSONString, "second.md")
			},
			result:     noResultSummary,
			callResult: &sdk.CallToolResult{},
			want:       "ok",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			descriptor := testDescriptor(t, tt.args, tt.result)
			var events bytes.Buffer
			log := audit.NewJSONL(&events, "test-run")
			req := &sdk.ServerRequest[*sdk.CallToolParamsRaw]{Params: &sdk.CallToolParamsRaw{
				Name: descriptor.Name(), Arguments: json.RawMessage(`{"path":"private.md"}`),
			}}

			logToolCall(log, "stdio", descriptorLookup([]ToolDescriptor{descriptor}), []string{descriptor.Name()}, req, tt.callResult, nil, time.Millisecond)

			record := decodeAuditRecords(t, events.String())[0]
			if record["outcome"] != tt.want {
				t.Fatalf("outcome = %#v, want %q in %#v", record["outcome"], tt.want, record)
			}
			if record["summary_error"] != summaryErrorCallback {
				t.Fatalf("summary_error = %#v, want %q", record["summary_error"], summaryErrorCallback)
			}
			if _, found := record["summary"]; found {
				t.Fatalf("invalid summary was retained: %#v", record["summary"])
			}
			if tt.callResult.IsError && record["is_error"] != true {
				t.Fatalf("base result outcome was altered: %#v", record)
			}
		})
	}
}

func TestSafeSummaryBuilderRejectsDuplicateWrites(t *testing.T) {
	tests := []struct {
		name  string
		write func(*SafeSummaryBuilder) error
	}{
		{
			name: "bool",
			write: func(builder *SafeSummaryBuilder) error {
				if err := builder.Bool(SectionResult, BoolOK, true); err != nil {
					return err
				}
				return builder.Bool(SectionResult, BoolOK, false)
			},
		},
		{
			name: "counter",
			write: func(builder *SafeSummaryBuilder) error {
				if err := builder.Counter(SectionResult, CounterEntryCount, 1); err != nil {
					return err
				}
				return builder.Counter(SectionResult, CounterEntryCount, 2)
			},
		},
		{
			name: "enum",
			write: func(builder *SafeSummaryBuilder) error {
				if err := builder.Enum(SectionResult, EnumResultType, ValueFile); err != nil {
					return err
				}
				return builder.Enum(SectionResult, EnumResultType, ValueDirectory)
			},
		},
		{
			name: "path",
			write: func(builder *SafeSummaryBuilder) error {
				if err := builder.Path(PathInput, true, JSONString, "first.md"); err != nil {
					return err
				}
				return builder.Path(PathInput, true, JSONString, "second.md")
			},
		},
		{
			name: "cursor",
			write: func(builder *SafeSummaryBuilder) error {
				if err := builder.Cursor(true, 10); err != nil {
					return err
				}
				return builder.Cursor(false, 0)
			},
		},
		{
			name: "unknown key hashes",
			write: func(builder *SafeSummaryBuilder) error {
				if err := builder.unknownKeyHashes([]string{"first"}); err != nil {
					return err
				}
				return builder.unknownKeyHashes([]string{"second"})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.write(newSafeSummaryBuilder("test-run")); err == nil || err.Error() != "duplicate summary field" {
				t.Fatalf("duplicate write error = %v, want fixed duplicate error", err)
			}
		})
	}
}

func TestSafeSummaryBuilderRejectsInvalidMetricPlacementAndEnumPairing(t *testing.T) {
	tests := []struct {
		name  string
		write func(*SafeSummaryBuilder) error
	}{
		{
			name:  "boolean section",
			write: func(builder *SafeSummaryBuilder) error { return builder.Bool(SectionArguments, BoolOK, true) },
		},
		{
			name:  "counter section",
			write: func(builder *SafeSummaryBuilder) error { return builder.Counter(SectionResult, CounterRawBytes, 1) },
		},
		{
			name: "enum section",
			write: func(builder *SafeSummaryBuilder) error {
				return builder.Enum(SectionArguments, EnumErrorCode, ValuePathDenied)
			},
		},
		{
			name: "enum pairing",
			write: func(builder *SafeSummaryBuilder) error {
				return builder.Enum(SectionResult, EnumResultType, ValueComplete)
			},
		},
		{
			name:  "invalid section",
			write: func(builder *SafeSummaryBuilder) error { return builder.Bool(0, BoolPresent, true) },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.write(newSafeSummaryBuilder("test-run")); err == nil {
				t.Fatal("invalid summary write succeeded")
			}
		})
	}
}

func TestSafeSummaryBuilderUsesMetricSpecificSourceChangeValue(t *testing.T) {
	builder := newSafeSummaryBuilder("test-run")
	if err := builder.Enum(SectionResult, EnumStoppedBy, ValueSourceChange); err != nil {
		t.Fatal(err)
	}
	if err := builder.Enum(SectionResult, EnumErrorCode, ValueSourceChange); err != nil {
		t.Fatal(err)
	}
	envelope, err := builder.seal()
	if err != nil {
		t.Fatal(err)
	}
	result := decodeSummary(t, envelope)["result"].(map[string]any)
	if result["stopped_by"] != "source_change" || result["error_code"] != "source_changed" {
		t.Fatalf("source change values = %#v", result)
	}
}

func TestDescriptorSummaryOversizeOmitsEnvelope(t *testing.T) {
	descriptor := testDescriptor(t, noArgumentSummary, noResultSummary)
	_, _, summaryError, ok := buildDescriptorSummaryWithLimit("test-run", descriptor, json.RawMessage(`{}`), &sdk.CallToolResult{}, 1)
	if ok || summaryError != summaryErrorTooLarge {
		t.Fatalf("oversized summary = ok %v, error %q", ok, summaryError)
	}
}

type summaryTestInput struct {
	Path string `json:"path"`
}

type summaryTestOutput struct {
	OK bool `json:"ok"`
}

func testDescriptor(t *testing.T, args ArgumentSummarizer, result ResultSummarizer) ToolDescriptor {
	t.Helper()
	descriptor, err := NewToolDescriptor(
		sdk.Tool{Name: "test_tool"},
		func(context.Context, *sdk.CallToolRequest, summaryTestInput) (*sdk.CallToolResult, summaryTestOutput, error) {
			return nil, summaryTestOutput{OK: true}, nil
		},
		args,
		result,
	)
	if err != nil {
		t.Fatal(err)
	}
	return descriptor
}

func noArgumentSummary(*SafeSummaryBuilder, json.RawMessage) error   { return nil }
func noResultSummary(*SafeSummaryBuilder, *sdk.CallToolResult) error { return nil }

type errUnknownToolForTest struct{}

func (errUnknownToolForTest) Error() string { return "unknown tool" }

func decodeSummary(t *testing.T, envelope safeSummaryEnvelope) map[string]any {
	t.Helper()
	data, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
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
