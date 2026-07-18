package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"personal-mcp-gateway/internal/audit"
	"personal-mcp-gateway/internal/limits"
)

const (
	methodToolsCall = "tools/call"

	summaryErrorCallback = "callback_error"
	summaryErrorPanic    = "callback_panic"
	summaryErrorTooLarge = "too_large"
)

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

func telemetryMiddleware(log *audit.Logger, transport string, descriptors []ToolDescriptor) sdk.Middleware {
	lookup := descriptorLookup(descriptors)
	knownTools := make([]string, 0, len(descriptors))
	for _, descriptor := range descriptors {
		knownTools = append(knownTools, descriptor.Name())
	}
	sort.Strings(knownTools)

	return func(next sdk.MethodHandler) sdk.MethodHandler {
		return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
			start := time.Now()
			res, err := next(ctx, method, req)
			duration := time.Since(start)

			if method == methodToolsCall {
				logToolCall(log, transport, lookup, knownTools, req, res, err, duration)
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

func logToolCall(log *audit.Logger, transport string, descriptors map[string]ToolDescriptor, knownTools []string, req sdk.Request, res sdk.Result, callErr error, duration time.Duration) {
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
	attrs := map[string]any{
		"transport":   transport,
		"method":      methodToolsCall,
		"tool":        toolValue,
		"tool_meta":   toolDetails,
		"outcome":     "ok",
		"duration_ms": duration.Milliseconds(),
	}

	var result *sdk.CallToolResult
	if typed, ok := res.(*sdk.CallToolResult); ok {
		result = typed
		if result != nil {
			attrs["is_error"] = result.IsError
		}
	}

	descriptor, known := descriptors[toolName]
	var (
		envelope    safeSummaryEnvelope
		summaryCode string
		resultCode  string
		haveSummary bool
	)
	if known {
		envelope, resultCode, summaryCode, haveSummary = buildDescriptorSummary(log.RunID(), descriptor, rawArgs, result)
	} else {
		envelope, summaryCode, haveSummary = buildUnknownSummary(log.RunID(), rawArgs)
	}
	if haveSummary {
		attrs["summary"] = envelope
	}
	if summaryCode != "" {
		attrs["summary_error"] = summaryCode
	}

	if callErr != nil {
		attrs["outcome"] = "protocol_error"
		attrs["error_code"] = protocolErrorCode(callErr)
		log.Event("tool.call", attrs)
		return
	}
	if result != nil && result.IsError {
		attrs["outcome"] = "tool_error"
		if resultCode != "" {
			attrs["error_code"] = resultCode
		} else {
			attrs["error_code"] = toolErrorCode(result)
		}
	}

	log.Event("tool.call", attrs)
}

func buildDescriptorSummary(runID string, descriptor ToolDescriptor, rawArgs json.RawMessage, result *sdk.CallToolResult) (envelope safeSummaryEnvelope, resultCode, summaryCode string, ok bool) {
	return buildDescriptorSummaryWithLimit(runID, descriptor, rawArgs, result, limits.TelemetryEventBytes)
}

func buildDescriptorSummaryWithLimit(runID string, descriptor ToolDescriptor, rawArgs json.RawMessage, result *sdk.CallToolResult, maxBytes int) (envelope safeSummaryEnvelope, resultCode, summaryCode string, ok bool) {
	builder := newSafeSummaryBuilder(runID)
	if err := builder.Bool(SectionArguments, BoolPresent, len(rawArgs) > 0); err != nil {
		return safeSummaryEnvelope{}, "", summaryErrorCallback, false
	}
	if err := builder.Counter(SectionArguments, CounterRawBytes, uint64(len(rawArgs))); err != nil {
		return safeSummaryEnvelope{}, "", summaryErrorCallback, false
	}

	if len(rawArgs) > limits.TelemetryArgsBytes {
		if err := builder.Bool(SectionArguments, BoolTooLarge, true); err != nil {
			return safeSummaryEnvelope{}, "", summaryErrorCallback, false
		}
	} else if err, panicked := invokeSummary(func() error {
		return descriptor.summarizeArgs(builder, rawArgs)
	}); err != nil {
		if panicked {
			return safeSummaryEnvelope{}, "", summaryErrorPanic, false
		}
		return safeSummaryEnvelope{}, "", summaryErrorCallback, false
	}

	if err := builder.Bool(SectionResult, BoolPresent, result != nil); err != nil {
		return safeSummaryEnvelope{}, "", summaryErrorCallback, false
	}
	if result != nil {
		if err := builder.Bool(SectionResult, BoolIsError, result.IsError); err != nil {
			return safeSummaryEnvelope{}, "", summaryErrorCallback, false
		}
		if err, panicked := invokeSummary(func() error {
			return descriptor.summarizeResult(builder, result)
		}); err != nil {
			if panicked {
				return safeSummaryEnvelope{}, "", summaryErrorPanic, false
			}
			return safeSummaryEnvelope{}, "", summaryErrorCallback, false
		}
	}

	resultCode, _ = builder.resultErrorCode()
	envelope, err := builder.sealWithLimit(maxBytes)
	if err != nil {
		if errors.Is(err, errSafeSummaryTooLarge) {
			return safeSummaryEnvelope{}, "", summaryErrorTooLarge, false
		}
		return safeSummaryEnvelope{}, "", summaryErrorCallback, false
	}
	return envelope, resultCode, "", true
}

func invokeSummary(callback func() error) (err error, panicked bool) {
	defer func() {
		if recover() != nil {
			err = errors.New("summary callback panicked")
			panicked = true
		}
	}()
	return callback(), false
}

func buildUnknownSummary(runID string, rawArgs json.RawMessage) (safeSummaryEnvelope, string, bool) {
	builder := newSafeSummaryBuilder(runID)
	if err := builder.Bool(SectionArguments, BoolPresent, len(rawArgs) > 0); err != nil {
		return safeSummaryEnvelope{}, summaryErrorCallback, false
	}
	if err := builder.Counter(SectionArguments, CounterRawBytes, uint64(len(rawArgs))); err != nil {
		return safeSummaryEnvelope{}, summaryErrorCallback, false
	}
	if len(rawArgs) > limits.TelemetryArgsBytes {
		if err := builder.Bool(SectionArguments, BoolTooLarge, true); err != nil {
			return safeSummaryEnvelope{}, summaryErrorCallback, false
		}
		return sealUnknownSummary(builder)
	}
	if len(rawArgs) > 0 {
		var value any
		if err := json.Unmarshal(rawArgs, &value); err != nil {
			if err := builder.Enum(SectionArguments, EnumShape, ValueInvalidJSON); err != nil {
				return safeSummaryEnvelope{}, summaryErrorCallback, false
			}
		} else {
			shape, object := summaryJSONShape(value)
			if err := builder.Enum(SectionArguments, EnumShape, shape); err != nil {
				return safeSummaryEnvelope{}, summaryErrorCallback, false
			}
			if object != nil {
				keys := make([]string, 0, len(object))
				for key := range object {
					keys = append(keys, key)
				}
				sort.Strings(keys)
				tooLarge := 0
				hashes := make([]string, 0, min(len(keys), limits.TelemetryMaxKeys))
				for _, key := range keys {
					if len([]byte(key)) > limits.TelemetryMaxKeyBytes {
						tooLarge++
					}
					if len(hashes) < limits.TelemetryMaxKeys {
						hashes = append(hashes, audit.HashString(runID, key))
					}
				}
				if err := builder.Counter(SectionArguments, CounterUnknownKeyCount, uint64(len(keys))); err != nil {
					return safeSummaryEnvelope{}, summaryErrorCallback, false
				}
				if tooLarge > 0 {
					if err := builder.Counter(SectionArguments, CounterUnknownKeyTooLarge, uint64(tooLarge)); err != nil {
						return safeSummaryEnvelope{}, summaryErrorCallback, false
					}
				}
				if len(keys) > len(hashes) {
					if err := builder.Bool(SectionArguments, BoolUnknownKeyTruncated, true); err != nil {
						return safeSummaryEnvelope{}, summaryErrorCallback, false
					}
				}
				if err := builder.unknownKeyHashes(hashes); err != nil {
					return safeSummaryEnvelope{}, summaryErrorCallback, false
				}
			}
		}
	}
	return sealUnknownSummary(builder)
}

func sealUnknownSummary(builder *SafeSummaryBuilder) (safeSummaryEnvelope, string, bool) {
	envelope, err := builder.seal()
	if err != nil {
		if errors.Is(err, errSafeSummaryTooLarge) {
			return safeSummaryEnvelope{}, summaryErrorTooLarge, false
		}
		return safeSummaryEnvelope{}, summaryErrorCallback, false
	}
	return envelope, "", true
}

func summaryJSONShape(value any) (EnumValue, map[string]any) {
	switch typed := value.(type) {
	case nil:
		return ValueNull, nil
	case string:
		return ValueString, nil
	case bool:
		return ValueBoolean, nil
	case float64:
		return ValueNumber, nil
	case []any:
		return ValueArray, nil
	case map[string]any:
		return ValueObject, typed
	default:
		return ValueOther, nil
	}
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
