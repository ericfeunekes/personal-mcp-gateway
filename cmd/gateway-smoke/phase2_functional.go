package main

import (
	"personal-mcp-gateway/internal/limits"
	"personal-mcp-gateway/internal/tools/obsidian"
)

const functionalReportSchema = "personal-mcp-gateway.functional.v4"

type functionalToolCallCounts struct {
	Resolve  int `json:"resolve"`
	LS       int `json:"ls"`
	Read     int `json:"read"`
	ReadMany int `json:"read_many"`
	Grep     int `json:"grep"`
	Stat     int `json:"stat"`
	Write    int `json:"write"`
	Edit     int `json:"edit"`
	Move     int `json:"move"`
	Delete   int `json:"delete"`
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
	case obsidian.ToolStat:
		c.Stat++
	case obsidian.ToolWrite:
		c.Write++
	case obsidian.ToolEdit:
		c.Edit++
	case obsidian.ToolMove:
		c.Move++
	case obsidian.ToolDelete:
		c.Delete++
	}
}

func (c functionalToolCallCounts) total() int {
	return c.Resolve + c.LS + c.Read + c.ReadMany + c.Grep + c.Stat + c.Write + c.Edit + c.Move + c.Delete
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
		report.ToolCalls.Resolve == 4 && report.ToolCalls.LS == 3 && report.ToolCalls.Read == 3 &&
		report.ToolCalls.Grep == 1 && report.ToolCalls.ReadMany == report.SyntheticReadManyPages && report.ToolCalls.ReadMany >= 2 &&
		report.ToolCalls.Stat == 8 && report.ToolCalls.Write == 10 && report.ToolCalls.Edit == 4 && report.ToolCalls.Move == 2 && report.ToolCalls.Delete == 2 &&
		report.SyntheticEmptyDirectoryStat &&
		mutationEvidencePasses(report.SyntheticMutation) && report.SyntheticHTTPMutation &&
		report.SDKResultCount == report.ToolCalls.total() && report.MaxStructuredResultBytes > 0 &&
		report.MaxStructuredResultBytes <= obsidian.MaxStructuredResultBytes &&
		report.MaxClientLatencyMicroseconds >= 0 && report.MaxClientLatencyMicroseconds < limits.ToolOperationTimeout.Microseconds() &&
		report.TotalFilesScanned > 0 && report.TotalBytesScanned > 0 && report.TotalSourceEntriesValidated > 0
}

func functionalBehaviorPasses(report smokeReport) bool {
	return report.ToolCount == candidateDescriptorCount && report.CurrentResolveExistingDir &&
		report.SyntheticCanonicalResolve && report.SyntheticPageCount >= 2 &&
		report.SyntheticEntryCount == 3 && report.SyntheticSecondProgress &&
		report.SyntheticNoDuplicates && report.SyntheticFullEquivalence &&
		report.SyntheticReadSelected && report.SyntheticGrepMatchCount == 3 &&
		report.SyntheticReadManyPages >= 2 && report.SyntheticReadManyContinued &&
		report.SyntheticRetrievalEquivalent && report.SyntheticTelemetrySanitized && report.SyntheticEmptyDirectoryStat &&
		mutationEvidencePasses(report.SyntheticMutation) && report.SyntheticHTTPMutation &&
		report.SDKResultCount >= 39 && report.MaxSDKResultBytes > 0 &&
		report.MaxSDKResultBytes <= obsidian.MaxSDKResultBytes
}

func mutationEvidencePasses(evidence syntheticMutationEvidence) bool {
	return evidence.CreateAbsent && evidence.ReplaceFingerprint && evidence.MultiReplacement && evidence.MoveAbsent &&
		evidence.DeletePermanent && evidence.FollowOnObserved && evidence.CollisionRefused && evidence.StaleRefused &&
		evidence.PatchRefused && evidence.DeniedRefused && evidence.ResidueFree
}
