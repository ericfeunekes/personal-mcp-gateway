package main

import (
	"personal-mcp-gateway/internal/limits"
	"personal-mcp-gateway/internal/tools/obsidian"
)

const functionalReportSchema = "personal-mcp-gateway.functional.v3"

type functionalToolCallCounts struct {
	Resolve  int `json:"resolve"`
	LS       int `json:"ls"`
	Read     int `json:"read"`
	ReadMany int `json:"read_many"`
	Grep     int `json:"grep"`
}

func (c *functionalToolCallCounts) add(tool string) {
	if c == nil {
		return
	}
	switch tool {
	case obsidian.ToolResolve:
		c.Resolve++
	case obsidian.ToolLS:
		c.LS++
	case obsidian.ToolRead:
		c.Read++
	case obsidian.ToolReadMany:
		c.ReadMany++
	case obsidian.ToolGrep:
		c.Grep++
	}
}

func (c functionalToolCallCounts) total() int {
	return c.Resolve + c.LS + c.Read + c.ReadMany + c.Grep
}

func functionalCoverage(value any) obsidian.Coverage {
	switch out := value.(type) {
	case obsidian.LSOutput:
		return out.Coverage
	case obsidian.ReadOutput:
		return out.Coverage
	case obsidian.ReadManyOutput:
		return out.Coverage
	case obsidian.GrepOutput:
		return out.Coverage
	default:
		return obsidian.Coverage{}
	}
}

func functionalReportEvidencePasses(report smokeReport) bool {
	return reportSchemaTuplePasses(report.ReportKind, report.ReportSchema, report.SchemaVersion) &&
		candidateRuntimeProfilePasses(report.CandidateRuntime) && machineProfilePasses(report.Machine) &&
		vaultAggregateProfilePasses(report.CurrentVault) && vaultAggregateProfilePasses(report.SyntheticVault) &&
		report.SyntheticVault.InventoryComplete && report.SyntheticVault.MarkdownFileCount == 3 && report.SyntheticVault.MarkdownByteCount > 0 &&
		candidateProcessProfilePasses(report.CurrentProcess) && candidateProcessProfilePasses(report.SyntheticProcess) &&
		report.ToolCalls.Resolve == 2 && report.ToolCalls.LS == 3 && report.ToolCalls.Read == 1 &&
		report.ToolCalls.Grep == 1 && report.ToolCalls.ReadMany == report.SyntheticReadManyPages && report.ToolCalls.ReadMany >= 2 &&
		report.SDKResultCount == report.ToolCalls.total() && report.MaxStructuredResultBytes > 0 &&
		report.MaxStructuredResultBytes <= obsidian.MaxStructuredResultBytes &&
		report.MaxClientLatencyMicroseconds >= 0 && report.MaxClientLatencyMicroseconds < limits.ToolOperationTimeout.Microseconds() &&
		report.TotalFilesScanned > 0 && report.TotalBytesScanned > 0 && report.TotalSourceEntriesValidated > 0
}

func functionalBehaviorPasses(report smokeReport) bool {
	return report.ToolCount == 5 && report.CurrentResolveExistingDir &&
		report.SyntheticCanonicalResolve && report.SyntheticPageCount >= 2 &&
		report.SyntheticEntryCount == 3 && report.SyntheticSecondProgress &&
		report.SyntheticNoDuplicates && report.SyntheticFullEquivalence &&
		report.SyntheticReadSelected && report.SyntheticGrepMatchCount == 3 &&
		report.SyntheticReadManyPages >= 2 && report.SyntheticReadManyContinued &&
		report.SyntheticRetrievalEquivalent && report.SyntheticTelemetrySanitized &&
		report.SDKResultCount >= 8 && report.MaxSDKResultBytes > 0 &&
		report.MaxSDKResultBytes <= obsidian.MaxSDKResultBytes
}
