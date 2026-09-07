package ynab

import (
	"encoding/json"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	localmcp "personal-mcp-gateway/internal/mcp"
)

// Summaries deliberately retain only batch shape and outcomes: financial IDs,
// amounts, names, dates, export locations, and provider payloads stay private.
func summarizeArgs(b *localmcp.SafeSummaryBuilder, raw json.RawMessage) error {
	var in struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return b.Enum(localmcp.SectionArguments, localmcp.EnumShape, localmcp.ValueInvalidJSON)
	}
	return b.Counter(localmcp.SectionArguments, localmcp.CounterRequestCount, uint64(len(in.Items)))
}
func summarizeResult(b *localmcp.SafeSummaryBuilder, result *sdk.CallToolResult) error {
	if result == nil {
		return nil
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return nil
	}
	var out Output
	if err := json.Unmarshal(encoded, &out); err != nil {
		return nil
	}
	var failures uint64
	for _, item := range out.Results {
		if item.Status != "success" {
			failures++
		}
	}
	if err := b.Counter(localmcp.SectionResult, localmcp.CounterItemCount, uint64(len(out.Results))); err != nil {
		return err
	}
	return b.Counter(localmcp.SectionResult, localmcp.CounterItemErrorCount, failures)
}
