package obsidian

import (
	"encoding/json"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	localmcp "personal-mcp-gateway/internal/mcp"
)

func summarizeReadArgs(builder *localmcp.SafeSummaryBuilder, raw json.RawMessage) error {
	if err := summarizeArgs(builder, raw, []string{"path", "base", "selector", "max_bytes", "cursor"}, false); err != nil {
		return err
	}
	object, ok := summaryObject(raw)
	if !ok {
		return nil
	}
	if err := summarizeUint(builder, localmcp.CounterMaxBytes, object["max_bytes"]); err != nil {
		return err
	}
	if err := summarizeSelectorKind(builder, object["selector"]); err != nil {
		return err
	}
	return summarizeCursorArg(builder, object)
}

func summarizeReadManyArgs(builder *localmcp.SafeSummaryBuilder, raw json.RawMessage) error {
	object, ok := summaryObject(raw)
	if !ok {
		if len(raw) == 0 {
			return nil
		}
		return builder.Enum(localmcp.SectionArguments, localmcp.EnumShape, localmcp.ValueInvalidJSON)
	}
	if err := builder.UnknownArgumentKeys(object, "requests", "max_bytes", "cursor"); err != nil {
		return err
	}
	if requests, ok := object["requests"].([]any); ok {
		if err := builder.Counter(localmcp.SectionArguments, localmcp.CounterRequestCount, uint64(len(requests))); err != nil {
			return err
		}
	}
	if err := summarizeUint(builder, localmcp.CounterMaxBytes, object["max_bytes"]); err != nil {
		return err
	}
	return summarizeCursorArg(builder, object)
}

func summarizeGrepArgs(builder *localmcp.SafeSummaryBuilder, raw json.RawMessage) error {
	known := []string{"pattern", "path", "base", "regex", "case_sensitive", "context_lines", "limit", "max_files", "max_bytes", "cursor"}
	if err := summarizeArgs(builder, raw, known, false); err != nil {
		return err
	}
	object, ok := summaryObject(raw)
	if !ok {
		return nil
	}
	if pattern, ok := object["pattern"].(string); ok {
		if err := builder.Counter(localmcp.SectionArguments, localmcp.CounterPatternBytes, uint64(len([]byte(pattern)))); err != nil {
			return err
		}
	}
	for metric, key := range map[localmcp.BoolMetric]string{
		localmcp.BoolRegex:         "regex",
		localmcp.BoolCaseSensitive: "case_sensitive",
	} {
		if value, ok := object[key].(bool); ok {
			if err := builder.Bool(localmcp.SectionArguments, metric, value); err != nil {
				return err
			}
		}
	}
	for metric, key := range map[localmcp.CounterMetric]string{
		localmcp.CounterContextLines: "context_lines",
		localmcp.CounterLimit:        "limit",
		localmcp.CounterMaxFiles:     "max_files",
		localmcp.CounterMaxBytes:     "max_bytes",
	} {
		if err := summarizeUint(builder, metric, object[key]); err != nil {
			return err
		}
	}
	return summarizeCursorArg(builder, object)
}

func summarizeReadResult(builder *localmcp.SafeSummaryBuilder, result *sdk.CallToolResult) error {
	if err := summarizeResultBase(builder, result); err != nil {
		return err
	}
	var out ReadOutput
	if !decodeStructured(result, &out) {
		return nil
	}
	if err := builder.Bool(localmcp.SectionResult, localmcp.BoolOK, out.OK); err != nil {
		return err
	}
	if err := summarizeRetrievalCoverage(builder, out.Truncated, out.Coverage); err != nil {
		return err
	}
	return summarizeErrorCode(builder, out.Error)
}

func summarizeReadManyResult(builder *localmcp.SafeSummaryBuilder, result *sdk.CallToolResult) error {
	if err := summarizeResultBase(builder, result); err != nil {
		return err
	}
	var out ReadManyOutput
	if !decodeStructured(result, &out) {
		return nil
	}
	if err := builder.Bool(localmcp.SectionResult, localmcp.BoolOK, out.OK); err != nil {
		return err
	}
	if err := builder.Counter(localmcp.SectionResult, localmcp.CounterItemCount, uint64(len(out.Items))); err != nil {
		return err
	}
	itemErrors := 0
	for _, item := range out.Items {
		if !item.OK && item.Error != nil {
			itemErrors++
		}
	}
	if err := builder.Counter(localmcp.SectionResult, localmcp.CounterItemErrorCount, uint64(itemErrors)); err != nil {
		return err
	}
	if err := builder.Counter(localmcp.SectionResult, localmcp.CounterRemainingCount, uint64(max(out.RemainingRequestCount, 0))); err != nil {
		return err
	}
	if err := summarizeRetrievalCoverage(builder, out.Truncated, out.Coverage); err != nil {
		return err
	}
	return summarizeErrorCode(builder, out.Error)
}

func summarizeGrepResult(builder *localmcp.SafeSummaryBuilder, result *sdk.CallToolResult) error {
	if err := summarizeResultBase(builder, result); err != nil {
		return err
	}
	var out GrepOutput
	if !decodeStructured(result, &out) {
		return nil
	}
	if err := builder.Bool(localmcp.SectionResult, localmcp.BoolOK, out.OK); err != nil {
		return err
	}
	if err := builder.Counter(localmcp.SectionResult, localmcp.CounterMatchCount, uint64(len(out.Matches))); err != nil {
		return err
	}
	if err := summarizeRetrievalCoverage(builder, out.Truncated, out.Coverage); err != nil {
		return err
	}
	return summarizeErrorCode(builder, out.Error)
}

func summarizeRetrievalCoverage(builder *localmcp.SafeSummaryBuilder, truncated bool, coverage Coverage) error {
	for metric, value := range map[localmcp.BoolMetric]bool{
		localmcp.BoolTruncated:      truncated,
		localmcp.BoolResultComplete: coverage.ResultComplete,
		localmcp.BoolScopeComplete:  coverage.ScopeComplete,
	} {
		if err := builder.Bool(localmcp.SectionResult, metric, value); err != nil {
			return err
		}
	}
	for metric, value := range map[localmcp.CounterMetric]uint64{
		localmcp.CounterFilesScanned:           coverage.FilesScanned,
		localmcp.CounterBytesScanned:           coverage.BytesScanned,
		localmcp.CounterSourceEntriesValidated: coverage.SourceEntriesValidated,
	} {
		if err := builder.Counter(localmcp.SectionResult, metric, value); err != nil {
			return err
		}
	}
	for metric, value := range map[localmcp.EnumMetric]string{
		localmcp.EnumStoppedBy:    coverage.StoppedBy,
		localmcp.EnumContinuation: coverage.Continuation,
		localmcp.EnumConsistency:  coverage.Consistency,
	} {
		if encoded, ok := summaryCoverageValue(value); ok {
			if err := builder.Enum(localmcp.SectionResult, metric, encoded); err != nil {
				return err
			}
		}
	}
	return nil
}

func summaryObject(raw json.RawMessage) (map[string]any, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var object map[string]any
	return object, json.Unmarshal(raw, &object) == nil
}

func summarizeCursorArg(builder *localmcp.SafeSummaryBuilder, object map[string]any) error {
	value, present := object["cursor"]
	cursor, _ := value.(string)
	return builder.Cursor(present, uint64(len([]byte(cursor))))
}

func summarizeUint(builder *localmcp.SafeSummaryBuilder, metric localmcp.CounterMetric, value any) error {
	number, ok := value.(float64)
	if !ok || number < 0 || number > float64(MaxGrepMaxBytes) || number != float64(uint64(number)) {
		return nil
	}
	return builder.Counter(localmcp.SectionArguments, metric, uint64(number))
}

func summarizeSelectorKind(builder *localmcp.SafeSummaryBuilder, value any) error {
	selector, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	kind, _ := selector["kind"].(string)
	values := map[string]localmcp.EnumValue{
		SelectorContent:     localmcp.ValueContent,
		SelectorHeading:     localmcp.ValueHeading,
		SelectorBlock:       localmcp.ValueBlock,
		SelectorFrontmatter: localmcp.ValueFrontmatter,
		SelectorOutline:     localmcp.ValueOutline,
	}
	encoded, ok := values[kind]
	if !ok {
		return nil
	}
	return builder.Enum(localmcp.SectionArguments, localmcp.EnumSelectorKind, encoded)
}
