package obsidian

import (
	"encoding/json"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	localmcp "personal-mcp-gateway/internal/mcp"
)

// Mutation telemetry intentionally records only request shape and bounded
// counts. In particular it never records, hashes, or inspects paths, content,
// fingerprints, destination names, or patch values.
func summarizeStatArgs(builder *localmcp.SafeSummaryBuilder, raw json.RawMessage) error {
	return summarizeMutationArgs(builder, raw, []string{"path", "base"}, false)
}

func summarizeWriteArgs(builder *localmcp.SafeSummaryBuilder, raw json.RawMessage) error {
	return summarizeMutationArgs(builder, raw, []string{"path", "base", "encoding", "value", "precondition"}, false)
}

func summarizeEditArgs(builder *localmcp.SafeSummaryBuilder, raw json.RawMessage) error {
	return summarizeMutationArgs(builder, raw, []string{"path", "base", "fingerprint", "encoding", "replacements"}, true)
}

func summarizeMoveArgs(builder *localmcp.SafeSummaryBuilder, raw json.RawMessage) error {
	return summarizeMutationArgs(builder, raw, []string{"source", "destination", "base", "fingerprint", "destination_precondition"}, false)
}

func summarizeDeleteArgs(builder *localmcp.SafeSummaryBuilder, raw json.RawMessage) error {
	return summarizeMutationArgs(builder, raw, []string{"path", "base", "fingerprint"}, false)
}

func summarizeMutationArgs(builder *localmcp.SafeSummaryBuilder, raw json.RawMessage, known []string, countValues bool) error {
	if len(raw) == 0 {
		return nil
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return builder.Enum(localmcp.SectionArguments, localmcp.EnumShape, localmcp.ValueInvalidJSON)
	}
	if err := builder.UnknownArgumentKeys(object, known...); err != nil {
		return err
	}
	if !countValues {
		return nil
	}
	if replacements, ok := object["replacements"].([]any); ok {
		if err := builder.Counter(localmcp.SectionArguments, localmcp.CounterRequestCount, uint64(len(replacements))); err != nil {
			return err
		}
	}
	return nil
}

func summarizeStatResult(builder *localmcp.SafeSummaryBuilder, result *sdk.CallToolResult) error {
	if err := summarizeResultBase(builder, result); err != nil {
		return err
	}
	var out StatOutput
	if !decodeStructured(result, &out) {
		return nil
	}
	if err := builder.Bool(localmcp.SectionResult, localmcp.BoolOK, out.OK); err != nil {
		return err
	}
	if value, ok := summaryKindValue(out.Type); ok {
		if err := builder.Enum(localmcp.SectionResult, localmcp.EnumResultType, value); err != nil {
			return err
		}
	}
	return summarizeErrorCode(builder, out.Error)
}

func summarizeMutationResult(builder *localmcp.SafeSummaryBuilder, result *sdk.CallToolResult) error {
	if err := summarizeResultBase(builder, result); err != nil {
		return err
	}
	var out MutationOutput
	if !decodeStructured(result, &out) {
		return nil
	}
	if err := builder.Bool(localmcp.SectionResult, localmcp.BoolOK, out.OK); err != nil {
		return err
	}
	if value, ok := summaryKindValue(out.Type); ok {
		if err := builder.Enum(localmcp.SectionResult, localmcp.EnumResultType, value); err != nil {
			return err
		}
	}
	return summarizeErrorCode(builder, out.Error)
}

func summarizeDeleteResult(builder *localmcp.SafeSummaryBuilder, result *sdk.CallToolResult) error {
	if err := summarizeResultBase(builder, result); err != nil {
		return err
	}
	var out DeleteOutput
	if !decodeStructured(result, &out) {
		return nil
	}
	if err := builder.Bool(localmcp.SectionResult, localmcp.BoolOK, out.OK); err != nil {
		return err
	}
	return summarizeErrorCode(builder, out.Error)
}
