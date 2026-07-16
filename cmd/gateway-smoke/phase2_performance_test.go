package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestPhase2PerformanceExactCandidate(t *testing.T) {
	candidate := buildGatewayCandidate(t)
	runtimeProfile, err := inspectCandidateRuntime(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if runtimeProfile.GOOS != runtime.GOOS || runtimeProfile.GOARCH != runtime.GOARCH ||
		!strings.HasPrefix(runtimeProfile.GoVersion, "go") || !candidateRuntimeProfilePasses(runtimeProfile) {
		t.Fatalf("candidate runtime = %#v", runtimeProfile)
	}

	vault := t.TempDir()
	privatePath := filepath.Join(vault, "private-current-vault-note.md")
	privateContent := "private-current-vault-content\n"
	if err := os.WriteFile(privatePath, []byte(privateContent), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	evidence, err := probePhase2Performance(ctx, candidate, vault)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.syntheticCorpus.MarkdownFileCount != phase2SyntheticFileCount ||
		evidence.syntheticCorpus.MarkdownByteCount != phase2SyntheticCorpusBytes ||
		!phase2SyntheticReadMetricsPass(evidence.syntheticRead) || !phase2SyntheticGrepMetricsPass(evidence.syntheticGrep) ||
		!broadGrepObservationPasses(evidence.broadCurrentGrep, evidence.currentVault) ||
		!candidateProcessProfilePasses(evidence.syntheticProcess) || !candidateProcessProfilePasses(evidence.currentVaultProcess) {
		t.Fatalf("phase 2 evidence = %#v", evidence)
	}
	report := performanceReport{
		ReportSchema: performanceReportSchema, DescriptorCount: 5,
		CandidateRuntime: evidence.runtime, Machine: evidence.machine,
		CurrentVault: evidence.currentVault, SyntheticCorpus: evidence.syntheticCorpus,
		SyntheticRead: evidence.syntheticRead, SyntheticGrep: evidence.syntheticGrep,
		BroadCurrentGrep: evidence.broadCurrentGrep,
		SyntheticProcess: evidence.syntheticProcess, CurrentVaultProcess: evidence.currentVaultProcess,
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{vault, privatePath, "private-current-vault-note.md", privateContent, phase2PerformanceNeedle} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("performance evidence retained private value %q: %s", forbidden, encoded)
		}
	}
}

func TestVaultAggregateInventoryMatchesObsidianPolicyAndIsBounded(t *testing.T) {
	root := t.TempDir()
	visible := map[string]string{
		"one.md":          "one\n",
		"nested/two.MD":   "two-two\n",
		"nested/skip.txt": "not markdown\n",
		".hidden.md":      "hidden\n",
		".private/x.md":   "private\n",
	}
	for relative, content := range visible {
		path := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "one.md"), filepath.Join(root, "linked.md")); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "vault-alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	profile, err := inspectVaultAggregate(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	wantBytes := uint64(len(visible["one.md"]) + len(visible["nested/two.MD"]))
	if !profile.InventoryComplete || profile.MarkdownFileCount != 2 || profile.MarkdownByteCount != wantBytes ||
		profile.StoppedBy != "scope" || !vaultAggregateProfilePasses(profile) {
		t.Fatalf("inventory = %#v, want two/%d", profile, wantBytes)
	}
	aliasProfile, err := inspectVaultAggregate(context.Background(), alias)
	if err != nil || aliasProfile != profile {
		t.Fatalf("symlink-root inventory = %#v, %v; want %#v", aliasProfile, err, profile)
	}
	for name, candidateRoot := range map[string]string{"real": root, "symlink": alias} {
		cardinality, err := inspectRootCardinality(context.Background(), candidateRoot)
		if err != nil || cardinality != 3 {
			t.Fatalf("%s root cardinality = %d, %v; want 3 visible ls entries", name, cardinality, err)
		}
	}
}

func TestVaultAggregateInventoryReportsLimitsCancellationAndSourceChange(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "one.md"), []byte("1234"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "two.md"), []byte("5678"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, test := range map[string]struct {
		options inventoryOptions
		stop    string
	}{
		"file": {options: inventoryOptions{maxFiles: 1, maxBytes: 100, timeout: time.Second}, stop: "file_limit"},
		"byte": {options: inventoryOptions{maxFiles: 10, maxBytes: 4, timeout: time.Second}, stop: "byte_limit"},
	} {
		t.Run(name, func(t *testing.T) {
			profile, err := inspectVaultAggregateWithOptions(context.Background(), root, test.options)
			if err != nil || profile.InventoryComplete || profile.StoppedBy != test.stop || !vaultAggregateProfilePasses(profile) {
				t.Fatalf("profile=%#v err=%v", profile, err)
			}
		})
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	profile, err := inspectVaultAggregate(canceled, root)
	if err != nil || profile.InventoryComplete || profile.StoppedBy != "timeout" {
		t.Fatalf("canceled profile=%#v err=%v", profile, err)
	}

	raceRoot := t.TempDir()
	nested := filepath.Join(raceRoot, "nested")
	outside := t.TempDir()
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "inside.md"), []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "outside-sentinel.md"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	mutated := false
	options := defaultInventoryOptions()
	options.hooks.beforeOpenDirectory = func(name string) {
		if name != "nested" || mutated {
			return
		}
		mutated = true
		_ = os.Rename(nested, filepath.Join(raceRoot, "moved"))
		_ = os.Symlink(outside, nested)
	}
	profile, err = inspectVaultAggregateWithOptions(context.Background(), raceRoot, options)
	if err != nil || profile.InventoryComplete || profile.StoppedBy != "source_change" || profile.MarkdownByteCount != 0 {
		t.Fatalf("race profile=%#v err=%v", profile, err)
	}
}

func TestPerformanceReportEvidenceRequiresEveryPhase2Gate(t *testing.T) {
	report := passingPhase2PerformanceReportShape()
	if performanceReportEvidencePasses(report) {
		t.Fatal("incomplete Phase 1 evidence unexpectedly passed")
	}
	if !phase2PerformanceEvidencePasses(report) {
		t.Fatal("complete Phase 2 evidence did not pass")
	}

	for name, mutate := range map[string]func(*performanceReport){
		"descriptor_count": func(report *performanceReport) { report.DescriptorCount = 4 },
		"synthetic_size":   func(report *performanceReport) { report.SyntheticCorpus.MarkdownByteCount-- },
		"read_latency":     func(report *performanceReport) { report.SyntheticRead.P95Microseconds = performanceP95LimitUS + 1 },
		"grep_latency":     func(report *performanceReport) { report.SyntheticGrep.P95Microseconds = phase2ScopedGrepP95LimitUS + 1 },
		"validation_work":  func(report *performanceReport) { report.SyntheticGrep.MaxSourceEntriesValidated = 1 },
		"false_completeness": func(report *performanceReport) {
			report.BroadCurrentGrep.CompletenessClaimed = true
			report.BroadCurrentGrep.CompletenessReconciled = false
		},
		"fd_recovery": func(report *performanceReport) { report.SyntheticProcess.FDsRecovered = false },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := report
			mutate(&candidate)
			if phase2PerformanceEvidencePasses(candidate) {
				t.Fatalf("mutated Phase 2 evidence passed: %#v", candidate)
			}
		})
	}
}

func passingPhase2PerformanceReportShape() performanceReport {
	metric := func(files, bytes uint64, p95 int64) performanceMetrics {
		return performanceMetrics{
			N: performanceSamples, P50Microseconds: 1, P95Microseconds: p95, MaxMicroseconds: p95,
			MaxSDKResultBytes: 1, MaxStructuredBytes: 1, MaxFilesScanned: files, MaxBytesScanned: bytes,
		}
	}
	process := candidateProcessProfile{
		BaselineCPUMicroseconds: 1, FinalCPUMicroseconds: 2, CPUDeltaMicroseconds: 1, LifetimeCPUMicroseconds: 2,
		BaselineRSSBytes: 1, FinalRSSBytes: 1, MaxObservedRSSBytes: 1, HighWaterRSSBytes: 1,
		BaselineFDCount: 1, FinalFDCount: 1, MaxObservedFDCount: 1, FDsRecovered: true,
	}
	return performanceReport{
		ReportSchema: performanceReportSchema, DescriptorCount: 5,
		CandidateRuntime: candidateRuntimeProfile{GoVersion: "go1.0", GOOS: "darwin", GOARCH: "arm64"},
		Machine:          machineProfile{LogicalCPUCount: 1, GOMAXPROCS: 1},
		CurrentVault:     vaultAggregateProfile{InventoryPolicy: markdownInventoryPolicy, InventoryComplete: true, StoppedBy: "scope", MarkdownFileCount: 1, MarkdownByteCount: 1},
		SyntheticCorpus:  vaultAggregateProfile{InventoryPolicy: markdownInventoryPolicy, InventoryComplete: true, StoppedBy: "scope", MarkdownFileCount: phase2SyntheticFileCount, MarkdownByteCount: phase2SyntheticCorpusBytes},
		SyntheticRead:    metric(1, phase2SyntheticFileBytes, 1), SyntheticGrep: metric(phase2SyntheticFileCount, phase2SyntheticCorpusBytes, 1),
		BroadCurrentGrep: broadGrepObservation{LatencyMicroseconds: 1, SDKResultBytes: 1, StructuredBytes: 1, MatchCount: 1,
			FilesScanned: 1, BytesScanned: 1, Continuation: "complete", StoppedBy: "scope", UsefulMatch: true,
			CompletenessClaimed: true, CompletenessReconciled: true, UnderTwoSecondBound: true},
		SyntheticProcess: process, CurrentVaultProcess: process,
	}
}
