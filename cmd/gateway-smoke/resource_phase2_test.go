package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"personal-mcp-gateway/internal/tools/obsidian"
)

func TestPhase2ResourceFixtureLocksExactComplexityBoundaries(t *testing.T) {
	fixture, err := newResourceFixture()
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(fixture.root)

	assertSize := func(path string, want int64) {
		t.Helper()
		info, err := os.Stat(filepath.Join(fixture.root, path))
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() != want {
			t.Fatalf("fixture size = %d, want %d", info.Size(), want)
		}
	}
	assertSize(fixture.boundary.near8MiB, obsidian.MaxMarkdownSourceBytes)
	assertSize(fixture.boundary.over8MiB, obsidian.MaxMarkdownSourceBytes+1)
	for path, wantLines := range map[string]int{
		fixture.boundary.dense50000:     obsidian.MaxMarkdownSourceLines,
		fixture.boundary.over50000Lines: obsidian.MaxMarkdownSourceLines + 1,
	} {
		content, err := os.ReadFile(filepath.Join(fixture.root, path))
		if err != nil {
			t.Fatal(err)
		}
		if got := bytes.Count(content, []byte{'\n'}); got != wantLines {
			t.Fatalf("fixture physical lines = %d, want %d", got, wantLines)
		}
	}
	for _, path := range []string{
		fixture.boundary.grepMatching,
		fixture.boundary.grepNonmatch,
		fixture.boundary.grepUnicode,
		fixture.boundary.grepZeroWidth,
		fixture.boundary.grepInvalid,
	} {
		assertSize(path, obsidian.MaxGrepPhysicalLineBytes)
	}
	assertSize(fixture.boundary.grepOver, obsidian.MaxGrepPhysicalLineBytes+1)
	if !fixture.generated.InventoryComplete || !fixture.generated.InventoryReconciled ||
		fixture.generated.GeneratedMarkdownFiles != fixture.generated.InventoryMarkdownFiles ||
		fixture.generated.GeneratedBytes != fixture.generated.InventoryBytes {
		t.Fatalf("fixture inventory = %#v", fixture.generated)
	}
}

func TestPhase2ResourceProbeExercisesBuiltFiveToolCandidate(t *testing.T) {
	candidate := buildGatewayCandidate(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	sampler := &phase2ResourceSampler{}
	report, err := probeCandidateResources(ctx, candidate, t.TempDir(), resourceProbeOptions{
		ColdProcesses: 2,
		Stabilize5:    time.Millisecond,
		Stabilize30:   2 * time.Millisecond,
		IdleDuration:  5 * time.Millisecond,
		ControlTime:   2 * time.Second,
	}, sampler)
	if err != nil {
		t.Fatalf("probeCandidateResources: %v; report=%#v", err, report)
	}
	if !report.Passed || report.DescriptorCount != 5 || !validResourceWorkload(report.Workload) ||
		!validResourceBoundaries(report.Boundaries) || report.Idle.ToolCallRowsBefore != resourceMeasuredCalls ||
		report.Idle.ToolCallRowsAfter != resourceMeasuredCalls {
		t.Fatalf("resource report = %#v", report)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"small-a.md", "near.md", "grep-match.md", "resource-workload-needle", "resource-boundary-match", "next_cursor",
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("resource report retained private fixture evidence %q", forbidden)
		}
	}
}

func TestPhase2ResourceProbeSystemProcessMetrics(t *testing.T) {
	if os.Getenv("RUN_LIVE_PHASE2_RESOURCE_PROBE") != "1" {
		t.Skip("set RUN_LIVE_PHASE2_RESOURCE_PROBE=1 to exercise exact candidate process sampling")
	}
	candidate := buildGatewayCandidate(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	report, err := probeCandidateResources(ctx, candidate, t.TempDir(), defaultResourceProbeOptions(), systemResourceSampler{})
	if err != nil {
		t.Fatalf("system resource probe: %v; report=%#v", err, report)
	}
	if !report.Passed || !report.HighWaterWithinBound || !report.HeapAllocGrowthWithinBound ||
		!report.RSSAfter30SecondsGrowthWithinBound || !report.AllFDsRecovered {
		t.Fatalf("system resource report = %#v", report)
	}
}

func TestPhase2BoundaryRSSRecoversAfterThirtySeconds(t *testing.T) {
	if os.Getenv("RUN_LIVE_PHASE2_RESOURCE_RECOVERY") != "1" {
		t.Skip("set RUN_LIVE_PHASE2_RESOURCE_RECOVERY=1 to exercise the exact 30-second RSS recovery boundary")
	}
	candidateBin := buildGatewayCandidate(t)
	fixture, err := newResourceFixture()
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(fixture.root)
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	candidate, err := connectResourceCandidate(ctx, candidateBin, fixture.root)
	if err != nil {
		t.Fatal(err)
	}
	defer candidate.closeDiscard()
	sampler := systemResourceSampler{}
	options := resourceProbeOptions{Stabilize5: 5 * time.Second, Stabilize30: 30 * time.Second, ControlTime: 2 * time.Second}
	baseline, err := observeResourceBaseline(ctx, candidate.process.command.Process.Pid, options, sampler, candidate.control)
	if err != nil {
		t.Fatal(err)
	}
	accumulator := newResourceWorkloadAccumulator()
	if _, err := probeResourceBoundaries(ctx, candidate.process.session, fixture.boundary, accumulator); err != nil {
		t.Fatal(err)
	}
	observation, err := observePostGCResources(ctx, candidate.process.command.Process.Pid, options, sampler, candidate.control)
	if err != nil {
		t.Fatal(err)
	}
	delta := nonnegativeDelta(observation.after30.rssBytes, baseline.RSSAfter30SecondsBytes)
	if delta > resourceRSSGrowthLimitBytes {
		t.Fatalf("30-second RSS growth = %d, want <= %d; baseline=%d after=%d", delta, resourceRSSGrowthLimitBytes, baseline.RSSAfter30SecondsBytes, observation.after30.rssBytes)
	}
}

type phase2ResourceSampler struct {
	cpu int64
}

func (s *phase2ResourceSampler) Sample(_ context.Context, _ int, _ bool) (processResourceSample, error) {
	s.cpu++
	// The fake sample must remain coherent with the independently observed
	// child high-water; real RSS thresholds are exercised by the live sampler.
	return processResourceSample{rssBytes: 8 * 1024 * 1024, cpuMicros: s.cpu, fdCount: 7}, nil
}

func TestPhase2ResourceGateRejectsBoundaryAndToolMixDrift(t *testing.T) {
	report := passingPhase2ResourceGateReport()
	if !resourceReportPasses(report, resourceColdProcesses) {
		t.Fatalf("passing Phase 2 report was rejected: %#v", report)
	}

	mutations := []struct {
		name string
		edit func(*resourceReport)
	}{
		{name: "boundary count", edit: func(r *resourceReport) { r.Boundaries.CallCount-- }},
		{name: "dense decoy", edit: func(r *resourceReport) { r.Boundaries.Dense50000DecoyRejected = false }},
		{name: "dense accepted", edit: func(r *resourceReport) { r.Boundaries.Dense50000BlockAccepted = false }},
		{name: "matching code", edit: func(r *resourceReport) { r.Boundaries.GrepExactMatchingErrorCode = "input_too_large" }},
		{name: "invalid utf8 code", edit: func(r *resourceReport) { r.Boundaries.GrepExactInvalidUTF8ErrorCode = "input_too_large" }},
		{name: "tool mix", edit: func(r *resourceReport) { r.Workload.ToolCalls.Read--; r.Workload.ToolCalls.Resolve++ }},
		{name: "batch mix", edit: func(r *resourceReport) { r.Batches[0].ToolCalls.Grep-- }},
		{name: "idle rows", edit: func(r *resourceReport) { r.Idle.ToolCallRowsAfter++ }},
		{name: "runtime", edit: func(r *resourceReport) { r.CandidateRuntime.GOOS = "" }},
		{name: "inventory", edit: func(r *resourceReport) { r.Fixture.InventoryReconciled = false }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			changed := report
			changed.Batches = append([]resourceBatchReport(nil), report.Batches...)
			mutation.edit(&changed)
			if resourceReportPasses(changed, resourceColdProcesses) {
				t.Fatal("drifted report was accepted")
			}
		})
	}
}

func passingPhase2ResourceGateReport() resourceReport {
	report := passingResourceGateReport()
	report.DescriptorCount = 5
	report.Cold.MaxSDKResultBytes = 1
	report.Cold.MaxStructuredBytes = 1
	report.CandidateRuntime = candidateRuntimeProfile{GoVersion: "go1.26.1", GOOS: "darwin", GOARCH: "amd64"}
	report.Machine = machineProfile{LogicalCPUCount: 8, GOMAXPROCS: 8}
	report.Vault = vaultAggregateProfile{
		InventoryPolicy: markdownInventoryPolicy, InventoryComplete: true, StoppedBy: "scope",
	}
	report.Fixture = resourceVaultReport{
		GeneratedMarkdownFiles: 13, GeneratedBytes: 1, InventoryMarkdownFiles: 13, InventoryBytes: 1,
		InventoryComplete: true, InventoryReconciled: true,
	}
	report.Workload = resourceWorkloadReport{
		BatchCount: resourceBatchCount, CallsPerBatch: resourceBatchCalls, CallsPerToolPerBatch: resourceCallsPerToolPerBatch,
		MixedCallCount: resourceBatchCount * resourceBatchCalls, BoundaryCallCount: resourceBoundaryCalls, MeasuredCallCount: resourceMeasuredCalls,
		ToolCalls:                    resourceToolCallCounts{Resolve: 60, LS: 60, Read: 65, ReadMany: 60, Grep: 67},
		MaxClientLatencyMicroseconds: 1, MaxSDKResultBytes: 1, MaxStructuredBytes: 1, MaxBytesScanned: 1,
		EveryCallWithinTwoSeconds: true, EverySDKResultWithin64KiB: true,
	}
	report.Boundaries = resourceBoundaryReport{
		CallCount: resourceBoundaryCalls, BatchNumber: 1, RanAfterBaseline: true, RanBeforeBlockingGC: true,
		Near8MiBStructuralAccepted: true, Dense50000DecoyRejected: true, Dense50000BlockAccepted: true,
		Over8MiBErrorCode: "input_too_large", Over50000LinesErrorCode: "input_too_large",
		GrepExactMatchingErrorCode: obsidian.ResponseTooLargeCode, GrepExactNonmatchingAccepted: true,
		GrepExactContextErrorCode: obsidian.ResponseTooLargeCode, GrepExactUnicodeErrorCode: obsidian.ResponseTooLargeCode,
		GrepExactZeroWidthErrorCode: obsidian.ResponseTooLargeCode, GrepExactInvalidUTF8ErrorCode: obsidian.InvalidUTF8Code,
		GrepOver1MiBErrorCode: "input_too_large", EveryCallWithinTwoSeconds: true, EverySDKResultWithin64KiB: true,
	}
	for index := range report.Batches {
		if index == 0 {
			report.Batches[index].BoundaryCallCount = resourceBoundaryCalls
		}
		report.Batches[index].ToolCalls = resourceToolCallCounts{Resolve: 20, LS: 20, Read: 20, ReadMany: 20, Grep: 20}
		report.Batches[index].MaxClientLatencyMicroseconds = 1
		report.Batches[index].MaxSDKResultBytes = 1
		report.Batches[index].MaxStructuredBytes = 1
		report.Batches[index].EveryCallWithinTwoSeconds = true
		report.Batches[index].EverySDKResultWithin64KiB = true
	}
	report.Idle.DescriptorCountAfter = 5
	report.Idle.FDBeforeCount = report.Baseline.FDImmediateCount
	report.Idle.FDAfterCount = report.Baseline.FDImmediateCount
	report.Idle.ExpectedToolCallRows = resourceMeasuredCalls
	report.Idle.ToolCallRowsBefore = resourceMeasuredCalls
	report.Idle.ToolCallRowsAfter = resourceMeasuredCalls
	report.Process = candidateProcessProfile{
		BaselineCPUMicroseconds: report.Baseline.CPUTimeMicroseconds,
		FinalCPUMicroseconds:    report.Idle.CPUTimeAfterMicroseconds,
		CPUDeltaMicroseconds:    report.Idle.CPUTimeAfterMicroseconds - report.Baseline.CPUTimeMicroseconds,
		LifetimeCPUMicroseconds: report.Idle.CPUTimeAfterMicroseconds,
		BaselineRSSBytes:        report.Baseline.RSSAfter30SecondsBytes,
		FinalRSSBytes:           report.Idle.RSSAfterBytes,
		MaxObservedRSSBytes:     report.Baseline.RSSAfter30SecondsBytes,
		HighWaterRSSBytes:       report.HighWaterRSSBytes,
		BaselineFDCount:         report.Baseline.FDImmediateCount,
		FinalFDCount:            report.Idle.FDAfterCount,
		MaxObservedFDCount:      report.Baseline.FDImmediateCount,
		FDsRecovered:            true,
	}
	return deriveResourceReport(report)
}
