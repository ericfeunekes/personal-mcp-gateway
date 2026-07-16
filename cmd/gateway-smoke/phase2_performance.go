package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"personal-mcp-gateway/internal/tools/obsidian"
)

const (
	performanceReportSchema    = "personal-mcp-gateway.performance.v3"
	phase2SyntheticFileCount   = 50
	phase2SyntheticFileBytes   = 5_120
	phase2SyntheticCorpusBytes = phase2SyntheticFileCount * phase2SyntheticFileBytes
	phase2ScopedGrepP95LimitUS = 250_000
	phase2BroadGrepBound       = 2 * time.Second
	phase2PerformanceNeedle    = "phase2-candidate-performance-needle"
)

type broadGrepObservation struct {
	LatencyMicroseconds    int64  `json:"latency_microseconds"`
	SDKResultBytes         int    `json:"sdk_result_bytes"`
	StructuredBytes        int    `json:"structured_bytes"`
	MatchCount             int    `json:"match_count"`
	FilesScanned           uint64 `json:"files_scanned"`
	BytesScanned           uint64 `json:"bytes_scanned"`
	SourceEntriesValidated uint64 `json:"source_entries_validated"`
	Continuation           string `json:"continuation"`
	StoppedBy              string `json:"stopped_by"`
	UsefulMatch            bool   `json:"useful_match"`
	AdvancingCursor        bool   `json:"advancing_cursor"`
	CompletenessClaimed    bool   `json:"completeness_claimed"`
	CompletenessReconciled bool   `json:"completeness_reconciled"`
	UnderTwoSecondBound    bool   `json:"under_two_second_bound"`
}

type phase2PerformanceEvidence struct {
	runtime             candidateRuntimeProfile
	machine             machineProfile
	currentVault        vaultAggregateProfile
	syntheticCorpus     vaultAggregateProfile
	syntheticRead       performanceMetrics
	syntheticGrep       performanceMetrics
	broadCurrentGrep    broadGrepObservation
	syntheticProcess    candidateProcessProfile
	currentVaultProcess candidateProcessProfile
}

type phase2SyntheticFixture struct {
	root        string
	readPath    string
	readContent string
}

func probePhase2Performance(ctx context.Context, gatewayBin, currentVaultRoot string) (phase2PerformanceEvidence, error) {
	runtimeProfile, err := inspectCandidateRuntime(gatewayBin)
	if err != nil {
		return phase2PerformanceEvidence{}, err
	}
	machine, err := inspectMachineProfile()
	if err != nil {
		return phase2PerformanceEvidence{}, err
	}
	currentVault, err := inspectVaultAggregate(ctx, currentVaultRoot)
	if err != nil {
		return phase2PerformanceEvidence{}, err
	}
	syntheticCorpus, syntheticRead, syntheticGrep, syntheticProcess, err := probeSyntheticRetrievalPerformance(ctx, gatewayBin)
	if err != nil {
		return phase2PerformanceEvidence{}, err
	}
	broad, currentProcess, err := probeBroadCurrentVaultGrep(ctx, gatewayBin, currentVaultRoot, currentVault)
	if err != nil {
		return phase2PerformanceEvidence{}, err
	}
	return phase2PerformanceEvidence{
		runtime: runtimeProfile, machine: machine, currentVault: currentVault, syntheticCorpus: syntheticCorpus,
		syntheticRead: syntheticRead, syntheticGrep: syntheticGrep, broadCurrentGrep: broad,
		syntheticProcess: syntheticProcess, currentVaultProcess: currentProcess,
	}, nil
}

func probeSyntheticRetrievalPerformance(ctx context.Context, gatewayBin string) (vaultAggregateProfile, performanceMetrics, performanceMetrics, candidateProcessProfile, error) {
	fixture, err := newPhase2SyntheticFixture()
	if err != nil {
		return vaultAggregateProfile{}, performanceMetrics{}, performanceMetrics{}, candidateProcessProfile{}, err
	}
	defer os.RemoveAll(fixture.root)
	profile, err := inspectVaultAggregate(ctx, fixture.root)
	if err != nil || !profile.InventoryComplete || profile.MarkdownFileCount != phase2SyntheticFileCount || profile.MarkdownByteCount != phase2SyntheticCorpusBytes {
		return vaultAggregateProfile{}, performanceMetrics{}, performanceMetrics{}, candidateProcessProfile{}, errors.New("phase 2 synthetic aggregate inventory failed")
	}

	process, err := connectCandidateProcessHandle(ctx, exec.Command(gatewayBin,
		"stdio", "--obsidian-root", fixture.root, "--telemetry", "off"), io.Discard)
	if err != nil {
		return vaultAggregateProfile{}, performanceMetrics{}, performanceMetrics{}, candidateProcessProfile{}, err
	}
	closed := false
	defer func() {
		if !closed {
			_ = process.session.Close()
		}
	}()
	if count, err := requireExactToolList(ctx, process.session); err != nil || count != 5 {
		return vaultAggregateProfile{}, performanceMetrics{}, performanceMetrics{}, candidateProcessProfile{}, errors.New("phase 2 synthetic descriptor gate failed")
	}
	tracker, err := startCandidateProcessTracker(ctx, process, systemResourceSampler{})
	if err != nil {
		return vaultAggregateProfile{}, performanceMetrics{}, performanceMetrics{}, candidateProcessProfile{}, err
	}

	readOperation := func() (operationSample, error) {
		out, measured, callErr := callMeasured[obsidian.ReadOutput](ctx, process.session, obsidian.ToolRead, map[string]any{
			"path": fixture.readPath, "max_bytes": phase2SyntheticFileBytes,
		})
		if callErr != nil || !out.OK || out.Content == nil || *out.Content != fixture.readContent ||
			out.Coverage.Continuation != obsidian.CoverageContinuationComplete || !out.Coverage.ResultComplete || !out.Coverage.ScopeComplete ||
			out.Coverage.FilesScanned != 1 || out.Coverage.BytesScanned != phase2SyntheticFileBytes || out.Coverage.SourceEntriesValidated != 0 {
			return operationSample{}, errors.New("phase 2 synthetic read call failed")
		}
		return sampleFromCoverage(measured, out.Coverage), nil
	}
	grepOperation := func() (operationSample, error) {
		out, measured, callErr := callMeasured[obsidian.GrepOutput](ctx, process.session, obsidian.ToolGrep, map[string]any{
			"pattern": phase2PerformanceNeedle, "path": ".", "regex": false, "case_sensitive": true,
			"context_lines": 0, "limit": phase2SyntheticFileCount + 1,
			"max_files": phase2SyntheticFileCount, "max_bytes": phase2SyntheticCorpusBytes,
		})
		if callErr != nil || !out.OK || len(out.Matches) != phase2SyntheticFileCount || out.Truncated ||
			out.Coverage.Continuation != obsidian.CoverageContinuationComplete || !out.Coverage.ResultComplete || !out.Coverage.ScopeComplete ||
			out.Coverage.FilesScanned != phase2SyntheticFileCount || out.Coverage.BytesScanned != phase2SyntheticCorpusBytes ||
			out.Coverage.SourceEntriesValidated != 0 || measured.sdkResultBytes > obsidian.MaxSDKResultBytes {
			return operationSample{}, errors.New("phase 2 synthetic grep call failed")
		}
		return sampleFromCoverage(measured, out.Coverage), nil
	}

	for warmup := 0; warmup < performanceWarmups; warmup++ {
		if _, err := readOperation(); err != nil {
			return vaultAggregateProfile{}, performanceMetrics{}, performanceMetrics{}, candidateProcessProfile{}, err
		}
		if _, err := grepOperation(); err != nil {
			return vaultAggregateProfile{}, performanceMetrics{}, performanceMetrics{}, candidateProcessProfile{}, err
		}
	}
	if err := tracker.sample(ctx); err != nil {
		return vaultAggregateProfile{}, performanceMetrics{}, performanceMetrics{}, candidateProcessProfile{}, err
	}
	readMetrics, err := collectPerformanceMetrics(readOperation, performanceSamples)
	if err != nil || !phase2SyntheticReadMetricsPass(readMetrics) {
		return vaultAggregateProfile{}, performanceMetrics{}, performanceMetrics{}, candidateProcessProfile{}, errors.New("phase 2 synthetic read performance gate failed")
	}
	if err := tracker.sample(ctx); err != nil {
		return vaultAggregateProfile{}, performanceMetrics{}, performanceMetrics{}, candidateProcessProfile{}, err
	}
	grepMetrics, err := collectPerformanceMetrics(grepOperation, performanceSamples)
	if err != nil || !phase2SyntheticGrepMetricsPass(grepMetrics) {
		return vaultAggregateProfile{}, performanceMetrics{}, performanceMetrics{}, candidateProcessProfile{}, errors.New("phase 2 synthetic grep performance gate failed")
	}
	if err := tracker.sample(ctx); err != nil {
		return vaultAggregateProfile{}, performanceMetrics{}, performanceMetrics{}, candidateProcessProfile{}, err
	}
	processProfile, err := tracker.finish(ctx, process)
	if err != nil {
		return vaultAggregateProfile{}, performanceMetrics{}, performanceMetrics{}, candidateProcessProfile{}, err
	}
	closed = true
	return profile, readMetrics, grepMetrics, processProfile, nil
}

func probeBroadCurrentVaultGrep(ctx context.Context, gatewayBin, root string, inventory vaultAggregateProfile) (broadGrepObservation, candidateProcessProfile, error) {
	process, err := connectCandidateProcessHandle(ctx, exec.Command(gatewayBin,
		"stdio", "--obsidian-root", root, "--telemetry", "off"), io.Discard)
	if err != nil {
		return broadGrepObservation{}, candidateProcessProfile{}, err
	}
	closed := false
	defer func() {
		if !closed {
			_ = process.session.Close()
		}
	}()
	if count, err := requireExactToolList(ctx, process.session); err != nil || count != 5 {
		return broadGrepObservation{}, candidateProcessProfile{}, errors.New("broad current-vault descriptor gate failed")
	}
	tracker, err := startCandidateProcessTracker(ctx, process, systemResourceSampler{})
	if err != nil {
		return broadGrepObservation{}, candidateProcessProfile{}, err
	}
	callCtx, cancel := context.WithTimeout(ctx, phase2BroadGrepBound)
	out, measured, callErr := callMeasured[obsidian.GrepOutput](callCtx, process.session, obsidian.ToolGrep, map[string]any{
		"pattern": "^", "path": ".", "regex": true, "case_sensitive": true, "context_lines": 0, "limit": 1,
	})
	cancel()
	if callErr != nil || !out.OK {
		return broadGrepObservation{}, candidateProcessProfile{}, errors.New("broad current-vault grep failed")
	}
	if err := tracker.sample(ctx); err != nil {
		return broadGrepObservation{}, candidateProcessProfile{}, err
	}
	observation := broadGrepObservation{
		LatencyMicroseconds: measured.latency.Microseconds(), SDKResultBytes: measured.sdkResultBytes,
		StructuredBytes: measured.structuredBytes, MatchCount: len(out.Matches),
		FilesScanned: out.Coverage.FilesScanned, BytesScanned: out.Coverage.BytesScanned,
		SourceEntriesValidated: out.Coverage.SourceEntriesValidated,
		Continuation:           out.Coverage.Continuation, StoppedBy: out.Coverage.StoppedBy,
		UsefulMatch:         len(out.Matches) > 0,
		AdvancingCursor:     out.Coverage.Continuation == obsidian.CoverageContinuationCursor && out.Coverage.NextCursor != "" && !out.Coverage.ResultComplete,
		CompletenessClaimed: out.Coverage.Continuation == obsidian.CoverageContinuationComplete && out.Coverage.ResultComplete && out.Coverage.ScopeComplete,
		UnderTwoSecondBound: measured.latency < phase2BroadGrepBound,
	}
	if observation.CompletenessClaimed {
		observation.CompletenessReconciled = inventory.InventoryComplete &&
			observation.FilesScanned == inventory.MarkdownFileCount && observation.BytesScanned == inventory.MarkdownByteCount
	}
	if !broadGrepObservationPasses(observation, inventory) {
		return broadGrepObservation{}, candidateProcessProfile{}, errors.New("broad current-vault grep gate failed")
	}
	processProfile, err := tracker.finish(ctx, process)
	if err != nil {
		return broadGrepObservation{}, candidateProcessProfile{}, err
	}
	closed = true
	return observation, processProfile, nil
}

func newPhase2SyntheticFixture() (phase2SyntheticFixture, error) {
	root, err := os.MkdirTemp("", "personal-mcp-gateway-phase2-performance-")
	if err != nil {
		return phase2SyntheticFixture{}, errors.New("phase 2 synthetic fixture setup failed")
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(root)
		}
	}()
	fixture := phase2SyntheticFixture{root: root}
	for index := 0; index < phase2SyntheticFileCount; index++ {
		name := fmt.Sprintf("note-%02d.md", index)
		prefix := fmt.Sprintf("# Synthetic %02d\n%s %02d\n", index, phase2PerformanceNeedle, index)
		if len(prefix) >= phase2SyntheticFileBytes {
			return phase2SyntheticFixture{}, errors.New("phase 2 synthetic fixture setup failed")
		}
		content := prefix + string(makeFilledBytes(phase2SyntheticFileBytes-len(prefix), byte('a'+index%26)))
		if len(content) != phase2SyntheticFileBytes || os.WriteFile(filepath.Join(root, name), []byte(content), 0o600) != nil {
			return phase2SyntheticFixture{}, errors.New("phase 2 synthetic fixture setup failed")
		}
		if index == 0 {
			fixture.readPath = name
			fixture.readContent = content
		}
	}
	cleanup = false
	return fixture, nil
}

func makeFilledBytes(count int, value byte) []byte {
	out := make([]byte, count)
	for index := range out {
		out[index] = value
	}
	return out
}

func sampleFromCoverage(measured callMeasurement, coverage obsidian.Coverage) operationSample {
	return operationSample{
		measurement: measured, filesScanned: coverage.FilesScanned, bytesScanned: coverage.BytesScanned,
		sourceEntriesValidated: coverage.SourceEntriesValidated,
	}
}

func phase2SyntheticReadMetricsPass(metrics performanceMetrics) bool {
	return performanceMetricsWithin(metrics, performanceSamples, performanceP95LimitUS) &&
		metrics.MaxFilesScanned == 1 && metrics.MaxBytesScanned == phase2SyntheticFileBytes && metrics.MaxSourceEntriesValidated == 0
}

func phase2SyntheticGrepMetricsPass(metrics performanceMetrics) bool {
	return performanceMetricsWithin(metrics, performanceSamples, phase2ScopedGrepP95LimitUS) &&
		metrics.MaxFilesScanned == phase2SyntheticFileCount && metrics.MaxBytesScanned == phase2SyntheticCorpusBytes &&
		metrics.MaxSourceEntriesValidated == 0
}

func performanceMetricsWithin(metrics performanceMetrics, samples int, p95LimitUS int64) bool {
	return metrics.N == samples && metrics.P50Microseconds >= 0 && metrics.P95Microseconds >= metrics.P50Microseconds &&
		metrics.MaxMicroseconds >= metrics.P95Microseconds && metrics.P95Microseconds <= p95LimitUS &&
		metrics.MaxSDKResultBytes > 0 && metrics.MaxSDKResultBytes <= obsidian.MaxSDKResultBytes &&
		metrics.MaxStructuredBytes > 0 && metrics.MaxStructuredBytes <= metrics.MaxSDKResultBytes
}

func broadGrepObservationPasses(observation broadGrepObservation, inventory vaultAggregateProfile) bool {
	if observation.LatencyMicroseconds < 0 || observation.SDKResultBytes <= 0 || observation.SDKResultBytes > obsidian.MaxSDKResultBytes ||
		observation.StructuredBytes <= 0 || observation.MatchCount < 0 || !observation.UnderTwoSecondBound ||
		observation.LatencyMicroseconds >= phase2BroadGrepBound.Microseconds() || (!observation.UsefulMatch && !observation.AdvancingCursor) {
		return false
	}
	if observation.CompletenessClaimed {
		return observation.Continuation == obsidian.CoverageContinuationComplete && observation.StoppedBy == obsidian.CoverageStopScope &&
			observation.CompletenessReconciled && inventory.InventoryComplete &&
			observation.FilesScanned == inventory.MarkdownFileCount && observation.BytesScanned == inventory.MarkdownByteCount
	}
	return !observation.CompletenessReconciled && observation.Continuation == obsidian.CoverageContinuationCursor && observation.AdvancingCursor
}

// performanceReportEvidencePasses is the single production predicate used to
// derive performanceReport.Passed after every Phase 1 and Phase 2 field has
// been populated. Report-set validation calls the same predicate.
func performanceReportEvidencePasses(report performanceReport) bool {
	return reportSchemaTuplePasses(report.ReportKind, report.ReportSchema, report.SchemaVersion) &&
		validGitOID(report.CandidateCommit) && validDigest(report.CandidateSHA256) && validDigest(report.DependencySHA256) &&
		phase2PerformanceEvidencePasses(report) && phase1PerformanceEvidencePasses(report)
}

func phase2PerformanceEvidencePasses(report performanceReport) bool {
	if report.ReportSchema != performanceReportSchema || report.DescriptorCount != 5 ||
		!candidateRuntimeProfilePasses(report.CandidateRuntime) || !machineProfilePasses(report.Machine) ||
		!vaultAggregateProfilePasses(report.CurrentVault) || !vaultAggregateProfilePasses(report.SyntheticCorpus) ||
		!candidateProcessProfilePasses(report.SyntheticProcess) || !candidateProcessProfilePasses(report.CurrentVaultProcess) ||
		report.SyntheticCorpus.InventoryComplete != true || report.SyntheticCorpus.MarkdownFileCount != phase2SyntheticFileCount ||
		report.SyntheticCorpus.MarkdownByteCount != phase2SyntheticCorpusBytes ||
		!phase2SyntheticReadMetricsPass(report.SyntheticRead) || !phase2SyntheticGrepMetricsPass(report.SyntheticGrep) ||
		!broadGrepObservationPasses(report.BroadCurrentGrep, report.CurrentVault) {
		return false
	}
	return true
}

func phase1PerformanceEvidencePasses(report performanceReport) bool {
	if !containsString([]string{"2_10", "11_100", "101_1000", "1001_plus"}, report.CardinalityBucket) {
		return false
	}
	current := []performanceMetrics{report.ResolveCached, report.LSFirstLimit1, report.LSContinuedLimit1, report.LSFirstLimit100}
	for _, metrics := range current {
		if !performanceMetricsPass(metrics, performanceSamples) {
			return false
		}
	}
	if report.ResolveCached.MaxFilesScanned != 0 || report.LSFirstLimit1.MaxFilesScanned == 0 ||
		report.LSContinuedLimit1.MaxFilesScanned == 0 || report.LSFirstLimit100.MaxFilesScanned == 0 ||
		!sqliteProofPasses(report.CurrentSQLite, (performanceWarmups+performanceSamples)*4) ||
		len(report.Stratified) != len(stratifiedEntryCounts) {
		return false
	}
	expectedStratifiedRows := 0
	for index, entryCount := range stratifiedEntryCounts {
		stratum := report.Stratified[index]
		if stratum.EntryCount != entryCount ||
			!candidateReportStratifiedMetricsPass(stratum.FirstLimit1, entryCount) ||
			!candidateReportStratifiedMetricsPass(stratum.FirstLimit100, entryCount) ||
			!candidateReportStratifiedMetricsPass(stratum.FirstLimit500, entryCount) ||
			!continuedMetricsPass(stratum.ContinuedLimit1, entryCount, entryCount > 1) ||
			!continuedMetricsPass(stratum.ContinuedLimit100, entryCount, entryCount > 100) ||
			!continuedMetricsPass(stratum.ContinuedLimit500, entryCount, entryCount > 500) {
			return false
		}
		for _, limit := range []int{1, 100, 500} {
			operations := 1
			if entryCount > limit {
				operations++
			}
			expectedStratifiedRows += (stratifiedWarmups + stratifiedSamples) * operations
		}
	}
	degradation := report.SQLiteDegradation
	cancellation := report.Cancellation
	return sqliteProofPasses(report.StratifiedSQLite, expectedStratifiedRows) &&
		degradation.FailureInjected && degradation.DegradationObserved && degradation.ToolCallSucceeded &&
		degradation.WithinResponseBudget && degradation.WithinLatencyBudget && degradation.SDKResultBytes > 0 &&
		degradation.SDKResultBytes <= obsidian.MaxSDKResultBytes && degradation.LatencyMicroseconds <= performanceP95LimitUS &&
		degradation.LatencyMicroseconds >= 0 && degradation.BoundMicroseconds == performanceP95LimitUS &&
		cancellation.ServerCompleted && cancellation.PartialWork && cancellation.WithinBound && cancellation.FollowupSucceeded &&
		cancellation.EntryCount == 10_000 && cancellation.DeadlineMicroseconds == cancellationDelay.Microseconds() &&
		cancellation.BoundMicroseconds == cancellationBound.Microseconds() &&
		cancellation.ClientReturnMicroseconds >= 0 && cancellation.ServerCompletionMicroseconds >= 0 &&
		cancellation.ClientReturnMicroseconds <= cancellationBound.Microseconds() &&
		cancellation.ServerCompletionMicroseconds <= cancellationBound.Microseconds() &&
		cancellation.FilesScanned > 0 && cancellation.FilesScanned < uint64(cancellation.EntryCount)
}
