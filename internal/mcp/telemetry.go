package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"personal-mcp-gateway/internal/audit"
	"personal-mcp-gateway/internal/limits"
)

const methodToolsCall = "tools/call"

var knownMCPMethods = []string{
	"initialize",
	"notifications/initialized",
	"ping",
	"tools/list",
	"tools/call",
	"resources/list",
	"resources/templates/list",
	"prompts/list",
	"completion/complete",
}

func telemetryMiddleware(log *audit.Logger, transport string, knownTools []string) sdk.Middleware {
	return func(next sdk.MethodHandler) sdk.MethodHandler {
		return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
			start := time.Now()
			res, err := next(ctx, method, req)
			duration := time.Since(start)

			if method == methodToolsCall {
				logToolCall(log, transport, knownTools, req, res, err, duration)
			} else {
				logMCPRequest(log, transport, method, err, duration)
			}
			return res, err
		}
	}
}

func logMCPRequest(log *audit.Logger, transport, method string, err error, duration time.Duration) {
	if log == nil || !log.Enabled() {
		return
	}
	outcome := "ok"
	if err != nil {
		outcome = "protocol_error"
	}
	methodValue, methodDetails := audit.SafeIdentifier(method, log.RunID(), "unknown", knownMCPMethods...)
	attrs := map[string]any{
		"transport":   transport,
		"method":      methodValue,
		"method_meta": methodDetails,
		"outcome":     outcome,
		"duration_ms": duration.Milliseconds(),
	}
	if err != nil {
		attrs["error_code"] = protocolErrorCode(err)
	}
	log.Event("mcp.request", attrs)
}

func logToolCall(log *audit.Logger, transport string, knownTools []string, req sdk.Request, res sdk.Result, err error, duration time.Duration) {
	if log == nil || !log.Enabled() {
		return
	}

	toolName := ""
	var rawArgs json.RawMessage
	if params, ok := req.GetParams().(*sdk.CallToolParamsRaw); ok && params != nil {
		toolName = params.Name
		rawArgs = params.Arguments
	}

	toolValue, toolDetails := audit.SafeIdentifier(toolName, log.RunID(), "unknown", knownTools...)
	outcome := "ok"
	attrs := map[string]any{
		"transport":   transport,
		"method":      methodToolsCall,
		"tool":        toolValue,
		"tool_meta":   toolDetails,
		"outcome":     outcome,
		"duration_ms": duration.Milliseconds(),
		"args":        summarizeArgs(rawArgs, log.RunID()),
	}

	if err != nil {
		outcome = "protocol_error"
		attrs["outcome"] = outcome
		attrs["error_code"] = protocolErrorCode(err)
		log.Event("tool.call", attrs)
		return
	}

	if result, ok := res.(*sdk.CallToolResult); ok && result != nil {
		attrs["is_error"] = result.IsError
		for k, v := range summarizeToolResult(result) {
			attrs[k] = v
		}
		if result.IsError {
			attrs["outcome"] = "tool_error"
			if _, ok := attrs["error_code"]; !ok {
				attrs["error_code"] = toolErrorCode(result)
			}
		}
	}

	log.Event("tool.call", attrs)
}

func summarizeArgs(raw json.RawMessage, runID string) map[string]any {
	out := map[string]any{
		"present": false,
		"bytes":   len(raw),
	}
	if len(raw) == 0 {
		return out
	}
	out["present"] = true
	if len(raw) > limits.TelemetryArgsBytes {
		out["too_large"] = true
		return out
	}

	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		out["shape"] = "invalid_json"
		return out
	}

	knownFields := make([]string, 0, 3)
	unknownHashes := make([]string, 0)
	unknownTooLarge := 0
	for key := range obj {
		switch key {
		case "path", "base", "limit":
			knownFields = append(knownFields, key)
		default:
			if len([]byte(key)) > limits.TelemetryMaxKeyBytes {
				unknownTooLarge++
			}
			if len(unknownHashes) < limits.TelemetryMaxKeys {
				unknownHashes = append(unknownHashes, audit.HashString(runID, key))
			}
		}
	}
	sort.Strings(knownFields)
	out["known_fields"] = knownFields
	unknownCount := len(obj) - len(knownFields)
	if unknownCount > 0 {
		out["unknown_key_count"] = unknownCount
		out["unknown_key_hashes"] = unknownHashes
		out["unknown_key_truncated"] = unknownCount > len(unknownHashes)
		if unknownTooLarge > 0 {
			out["unknown_key_too_large_count"] = unknownTooLarge
		}
	}

	for _, field := range []string{"path", "base"} {
		if value, ok := obj[field]; ok {
			out[field] = summarizePathValue(value, runID)
		}
	}
	if value, ok := obj["limit"]; ok {
		out["limit"] = summarizeLimitValue(value)
	}
	return out
}

func summarizePathValue(value any, runID string) map[string]any {
	out := map[string]any{
		"present": true,
		"type":    jsonType(value),
	}
	s, ok := value.(string)
	if !ok {
		return out
	}

	out["bytes"] = len([]byte(s))
	if len([]byte(s)) > limits.PathMaxBytes {
		out["too_large"] = true
		out["hash"] = audit.HashString(runID, s)
		return out
	}

	normalized := strings.TrimSpace(strings.ReplaceAll(s, "\\", "/"))
	out["empty"] = normalized == ""
	out["hash"] = audit.HashString(runID, normalized)
	out["absolute"] = strings.HasPrefix(normalized, "/")

	clean := path.Clean(normalized)
	if clean == "." && normalized != "." {
		clean = normalized
	}

	segments := pathSegments(clean)
	out["segments"] = len(segments)
	if len(segments) > limits.PathMaxSegments {
		out["too_many_segments"] = true
	}
	out["traversal"] = hasSegment(segments, "..")
	out["hidden_segment"] = hasHiddenSegment(segments)
	if ext := strings.TrimPrefix(strings.ToLower(path.Ext(clean)), "."); ext != "" {
		out["ext"] = ext
	}
	return out
}

func summarizeLimitValue(value any) map[string]any {
	out := map[string]any{
		"present": true,
		"type":    jsonType(value),
	}
	switch n := value.(type) {
	case float64:
		if n == float64(int(n)) {
			addLimitCategory(out, int(n))
		}
	case int:
		addLimitCategory(out, n)
	}
	return out
}

func addLimitCategory(out map[string]any, n int) {
	switch {
	case n < 0:
		out["category"] = "negative"
	case n <= 500:
		out["value"] = n
	default:
		out["category"] = "over_max"
	}
}

func summarizeToolResult(result *sdk.CallToolResult) map[string]any {
	out := map[string]any{}
	structured, tooLarge := structuredMap(result.StructuredContent)
	if tooLarge {
		out["result_too_large"] = true
		return out
	}
	if structured == nil {
		if result.IsError && result.GetError() != nil {
			out["error_code"] = toolErrorCode(result)
		}
		return out
	}

	if ok, found := structured["ok"].(bool); found {
		out["ok"] = ok
	}
	if exists, found := structured["exists"].(bool); found {
		out["exists"] = exists
	}
	if typ, found := structured["type"].(string); found {
		out["result_type"] = typ
	}
	if truncated, found := structured["truncated"].(bool); found {
		out["truncated"] = truncated
	}
	if entries, found := structured["entries"].([]any); found {
		out["entry_count"] = len(entries)
	}
	if errObj, found := structured["error"].(map[string]any); found {
		if code, ok := errObj["code"].(string); ok {
			out["error_code"] = code
		}
	}
	return out
}

func structuredMap(value any) (map[string]any, bool) {
	if value == nil {
		return nil, false
	}

	var data []byte
	switch v := value.(type) {
	case json.RawMessage:
		data = v
	case []byte:
		data = v
	case map[string]any:
		return v, false
	default:
		var err error
		data, err = json.Marshal(v)
		if err != nil {
			return nil, false
		}
	}
	if len(data) > limits.TelemetryArgsBytes {
		return nil, true
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, false
	}
	return out, false
}

func toolErrorCode(result *sdk.CallToolResult) string {
	if result == nil {
		return "tool_error"
	}
	err := result.GetError()
	if err == nil {
		return "tool_error"
	}
	msg := err.Error()
	if strings.Contains(msg, "validating \"arguments\"") {
		return "schema_validation"
	}
	if strings.Contains(msg, "unmarshal") || strings.Contains(msg, "cannot unmarshal") {
		return "argument_unmarshal"
	}
	return "tool_error"
}

func protocolErrorCode(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "unknown tool"):
		return "unknown_tool"
	case strings.Contains(msg, "unsupported"):
		return "unsupported_method"
	case strings.Contains(msg, "invalid"):
		return "invalid_request"
	default:
		return "protocol_error"
	}
}

func jsonType(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case float64, int:
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return fmt.Sprintf("%T", value)
	}
}

func pathSegments(s string) []string {
	if s == "" || s == "." {
		return nil
	}
	parts := strings.Split(s, "/")
	segments := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		segments = append(segments, part)
	}
	return segments
}

func hasSegment(segments []string, want string) bool {
	for _, segment := range segments {
		if segment == want {
			return true
		}
	}
	return false
}

func hasHiddenSegment(segments []string) bool {
	for _, segment := range segments {
		if strings.HasPrefix(segment, ".") {
			return true
		}
	}
	return false
}
