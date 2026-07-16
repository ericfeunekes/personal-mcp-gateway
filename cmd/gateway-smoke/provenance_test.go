package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"personal-mcp-gateway/internal/tools/obsidian"
)

func TestCanonicalDependencySHA256UsesFixedLengthFramedRecord(t *testing.T) {
	repo := t.TempDir()
	goMod := []byte("module example.test\n")
	goSum := []byte("example.test v1.0.0 h1:synthetic\n")
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), goMod, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.sum"), goSum, 0o600); err != nil {
		t.Fatal(err)
	}
	modHash := sha256.Sum256(goMod)
	sumHash := sha256.Sum256(goSum)
	record := "go.mod=" + hex.EncodeToString(modHash[:]) + "\ngo.sum=" + hex.EncodeToString(sumHash[:]) + "\n"
	want := sha256.Sum256([]byte(record))
	got, err := canonicalDependencySHA256(repo)
	if err != nil {
		t.Fatal(err)
	}
	if got != hex.EncodeToString(want[:]) {
		t.Fatalf("dependency digest = %s, want %x", got, want)
	}
}

func TestVerifyCandidateProvenanceRejectsDriftBeforeWork(t *testing.T) {
	candidate := filepath.Join(t.TempDir(), "candidate")
	if err := os.WriteFile(candidate, []byte("candidate"), 0o700); err != nil {
		t.Fatal(err)
	}
	args := provenanceArgs(t, candidate)
	repo, expected := provenanceFromArgs(t, args)

	if _, err := verifyCandidateProvenance(repo, candidate, expected); err != nil {
		t.Fatalf("valid provenance rejected: %v", err)
	}

	t.Run("commit", func(t *testing.T) {
		changed := expected
		changed.Commit = strings.Repeat("a", 40)
		if _, err := verifyCandidateProvenance(repo, candidate, changed); err == nil {
			t.Fatal("stale commit was accepted")
		}
	})
	t.Run("candidate", func(t *testing.T) {
		changed := expected
		changed.CandidateSHA256 = strings.Repeat("a", 64)
		if _, err := verifyCandidateProvenance(repo, candidate, changed); err == nil {
			t.Fatal("candidate digest drift was accepted")
		}
	})
	t.Run("dependency", func(t *testing.T) {
		changed := expected
		changed.DependencySHA256 = strings.Repeat("a", 64)
		if _, err := verifyCandidateProvenance(repo, candidate, changed); err == nil {
			t.Fatal("dependency digest drift was accepted")
		}
	})
	t.Run("dirty", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(repo, "untracked"), []byte("dirty"), 0o600); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(filepath.Join(repo, "untracked"))
		if _, err := verifyCandidateProvenance(repo, candidate, expected); err == nil {
			t.Fatal("dirty repository was accepted")
		}
	})
}

func TestValidateReportSetRequiresOneMatchingReportPerMode(t *testing.T) {
	expected := candidateProvenance{
		Commit: strings.Repeat("1", 40), CandidateSHA256: strings.Repeat("2", 64), DependencySHA256: strings.Repeat("3", 64),
	}
	dir := t.TempDir()
	write := func(name string, report any) string {
		t.Helper()
		data, err := json.Marshal(report)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	reports := completeCandidateReports(expected)
	functional := write("functional.json", reports[0])
	performance := write("performance.json", reports[1])
	resource := write("resource.json", reports[2])
	if err := validateReportSet([]string{functional, performance, resource}, expected); err != nil {
		t.Fatalf("valid report set rejected: %v", err)
	}

	t.Run("cross-report drift", func(t *testing.T) {
		report := reports[2].(resourceReport)
		report.DependencySHA256 = strings.Repeat("4", 64)
		drifted := write("drifted.json", report)
		if err := validateReportSet([]string{functional, performance, drifted}, expected); err == nil {
			t.Fatal("cross-report drift was accepted")
		}
	})
	t.Run("duplicate mode", func(t *testing.T) {
		if err := validateReportSet([]string{functional, performance, performance}, expected); err == nil {
			t.Fatal("duplicate report mode was accepted")
		}
	})
	t.Run("failed report", func(t *testing.T) {
		report := reports[2].(resourceReport)
		report.Passed = false
		failed := write("failed.json", report)
		if err := validateReportSet([]string{functional, performance, failed}, expected); err == nil {
			t.Fatal("failed report was accepted")
		}
	})
	t.Run("stale schema", func(t *testing.T) {
		report := reports[2].(resourceReport)
		report.SchemaVersion--
		stale := write("stale.json", report)
		if err := validateReportSet([]string{functional, performance, stale}, expected); err == nil {
			t.Fatal("stale report schema was accepted")
		}
	})
	for _, test := range []struct {
		name   string
		mutate func(*resourceReport)
	}{
		{name: "missing named schema", mutate: func(report *resourceReport) { report.ReportSchema = "" }},
		{name: "cross-kind named schema", mutate: func(report *resourceReport) { report.ReportSchema = functionalReportSchema }},
		{name: "unknown named schema", mutate: func(report *resourceReport) { report.ReportSchema = "personal-mcp-gateway.resource.v99" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			report := reports[2].(resourceReport)
			test.mutate(&report)
			invalid := write(test.name+".json", report)
			if err := validateReportSet([]string{functional, performance, invalid}, expected); err == nil {
				t.Fatal("invalid report tuple was accepted")
			}
		})
	}
	t.Run("version two performance schema", func(t *testing.T) {
		report := reports[1].(performanceReport)
		report.SchemaVersion = 2
		stale := write("performance-v2.json", report)
		if err := validateReportSet([]string{functional, stale, resource}, expected); err == nil {
			t.Fatal("version-two performance report was accepted")
		}
	})
	t.Run("version two functional schema", func(t *testing.T) {
		report := reports[0].(smokeReport)
		report.SchemaVersion = 2
		stale := write("functional-v2.json", report)
		if err := validateReportSet([]string{stale, performance, resource}, expected); err == nil {
			t.Fatal("version-two functional report was accepted")
		}
	})
	t.Run("malformed", func(t *testing.T) {
		malformed := filepath.Join(dir, "malformed.json")
		if err := os.WriteFile(malformed, []byte("{}{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := validateReportSet([]string{functional, performance, malformed}, expected); err == nil {
			t.Fatal("malformed report was accepted")
		}
	})
	t.Run("header only", func(t *testing.T) {
		headerOnly := write("header-only.json", reportEnvelope{
			ReportKind: reportKindResource, ReportSchema: resourceReportSchema, SchemaVersion: resourceReportVersion, Passed: true,
			CandidateCommit: expected.Commit, CandidateSHA256: expected.CandidateSHA256, DependencySHA256: expected.DependencySHA256,
		})
		if err := validateReportSet([]string{functional, performance, headerOnly}, expected); err == nil {
			t.Fatal("header-only proof was accepted")
		}
	})
	t.Run("missing proof field", func(t *testing.T) {
		data, err := json.Marshal(reports[0])
		if err != nil {
			t.Fatal(err)
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(data, &object); err != nil {
			t.Fatal(err)
		}
		delete(object, "tool_count")
		missing := write("missing.json", object)
		if err := validateReportSet([]string{missing, performance, resource}, expected); err == nil {
			t.Fatal("missing proof field was accepted")
		}
	})
	t.Run("duplicate proof field", func(t *testing.T) {
		data, err := json.Marshal(reports[0])
		if err != nil {
			t.Fatal(err)
		}
		data = append([]byte(`{"passed":true,`), data[1:]...)
		duplicate := filepath.Join(dir, "duplicate.json")
		if err := os.WriteFile(duplicate, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := validateReportSet([]string{duplicate, performance, resource}, expected); err == nil {
			t.Fatal("duplicate proof field was accepted")
		}
	})
	t.Run("functional result exceeds response bound", func(t *testing.T) {
		report := reports[0].(smokeReport)
		report.MaxSDKResultBytes = obsidian.MaxSDKResultBytes + 1
		oversized := write("oversized-functional.json", report)
		if err := validateReportSet([]string{oversized, performance, resource}, expected); err == nil {
			t.Fatal("oversized functional result was accepted")
		}
	})
	t.Run("functional evidence drift", func(t *testing.T) {
		report := reports[0].(smokeReport)
		report.ToolCalls.Read = 0
		drifted := write("functional-evidence-drift.json", report)
		if err := validateReportSet([]string{drifted, performance, resource}, expected); err == nil {
			t.Fatal("functional evidence drift was accepted")
		}
	})
	t.Run("performance evidence drift", func(t *testing.T) {
		report := reports[1].(performanceReport)
		report.SyntheticCorpus.MarkdownByteCount--
		drifted := write("performance-evidence-drift.json", report)
		if err := validateReportSet([]string{functional, drifted, resource}, expected); err == nil {
			t.Fatal("performance evidence drift was accepted")
		}
	})
	for _, test := range []struct {
		name   string
		mutate func(*performanceReport)
	}{
		{name: "negative current p50", mutate: func(report *performanceReport) { report.ResolveCached.P50Microseconds = -1 }},
		{name: "unordered current max", mutate: func(report *performanceReport) { report.ResolveCached.MaxMicroseconds = 0 }},
		{name: "unordered stratified p95", mutate: func(report *performanceReport) {
			report.Stratified = append([]stratifiedMetrics(nil), report.Stratified...)
			report.Stratified[0].FirstLimit1.P50Microseconds = 2
			report.Stratified[0].FirstLimit1.P95Microseconds = 1
		}},
		{name: "negative degradation duration", mutate: func(report *performanceReport) { report.SQLiteDegradation.LatencyMicroseconds = -1 }},
		{name: "negative cancellation duration", mutate: func(report *performanceReport) { report.Cancellation.ClientReturnMicroseconds = -1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			report := reports[1].(performanceReport)
			test.mutate(&report)
			invalid := write(test.name+".json", report)
			if err := validateReportSet([]string{functional, invalid, resource}, expected); err == nil {
				t.Fatal("incoherent performance metric was accepted")
			}
		})
	}
	t.Run("resource evidence drift", func(t *testing.T) {
		report := reports[2].(resourceReport)
		report.Boundaries.CallCount--
		drifted := write("resource-evidence-drift.json", report)
		if err := validateReportSet([]string{functional, performance, drifted}, expected); err == nil {
			t.Fatal("resource evidence drift was accepted")
		}
	})
	t.Run("unknown field", func(t *testing.T) {
		data, err := json.Marshal(reports[0])
		if err != nil {
			t.Fatal(err)
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(data, &object); err != nil {
			t.Fatal(err)
		}
		object["unexpected"] = json.RawMessage("true")
		unknown := write("unknown.json", object)
		if err := validateReportSet([]string{unknown, performance, resource}, expected); err == nil {
			t.Fatal("unknown report field was accepted")
		}
	})
}

func TestPrivateCandidateSnapshotPinsBytesPermissionsAndCleanup(t *testing.T) {
	directory := t.TempDir()
	candidate := filepath.Join(directory, "candidate")
	if err := os.WriteFile(candidate, []byte("#!/bin/sh\nprintf 'candidate-a\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	digest, err := hashRegularBounded(candidate, maxCandidateBytes)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, cleanup, err := createPrivateCandidateSnapshot(candidate, digest)
	if err != nil {
		t.Fatal(err)
	}
	snapshotDirectory := filepath.Dir(snapshot)
	for path, want := range map[string]os.FileMode{snapshotDirectory: 0o700, snapshot: 0o700} {
		info, statErr := os.Stat(path)
		if statErr != nil || info.Mode().Perm() != want {
			t.Fatalf("snapshot mode %s = %v, want %o; err=%v", path, info, want, statErr)
		}
	}
	if err := os.WriteFile(candidate, []byte("#!/bin/sh\nprintf 'candidate-b\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(snapshot).CombinedOutput()
	if err != nil || string(output) != "candidate-a\n" {
		t.Fatalf("snapshot execution err=%v output=%q", err, output)
	}
	cleanup()
	if _, err := os.Stat(snapshotDirectory); !os.IsNotExist(err) {
		t.Fatalf("snapshot directory survived cleanup: %v", err)
	}
}

func TestPrivateCandidateSnapshotRejectsMutationDuringCopy(t *testing.T) {
	candidate := filepath.Join(t.TempDir(), "candidate")
	if err := os.WriteFile(candidate, []byte("#!/bin/sh\nprintf a\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	digest, err := hashRegularBounded(candidate, maxCandidateBytes)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = createPrivateCandidateSnapshotWithHooks(candidate, digest, candidateSnapshotHooks{afterSourceOpen: func() {
		_ = os.WriteFile(candidate, []byte("#!/bin/sh\nprintf b\n"), 0o700)
	}})
	if err == nil {
		t.Fatal("candidate mutation during snapshot copy was accepted")
	}
}

func TestEveryReportSchemaCarriesTheSameCandidateProvenance(t *testing.T) {
	expected := candidateProvenance{
		Commit: strings.Repeat("1", 40), CandidateSHA256: strings.Repeat("2", 64), DependencySHA256: strings.Repeat("3", 64),
	}
	reports := completeCandidateReports(expected)
	dir := t.TempDir()
	paths := make([]string, 0, len(reports))
	for index, report := range reports {
		data, err := json.Marshal(report)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, string(rune('a'+index))+".json")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	if err := validateReportSet(paths, expected); err != nil {
		t.Fatalf("concrete report schemas do not share provenance: %v", err)
	}
}

func TestRunReportSetValidationEmitsNoPrivateData(t *testing.T) {
	candidate := filepath.Join(t.TempDir(), "candidate-private-sentinel")
	if err := os.WriteFile(candidate, []byte("candidate"), 0o700); err != nil {
		t.Fatal(err)
	}
	provenance := provenanceArgs(t, candidate)
	_, expected := provenanceFromArgs(t, provenance)
	dir := t.TempDir()
	var paths []string
	for index, report := range completeCandidateReports(expected) {
		data, err := json.Marshal(report)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, string(rune('a'+index))+".json")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	args := append([]string{"--gateway-bin", candidate, "--validate-report-set"}, provenance...)
	args = append(args, paths...)
	var stdout, stderr bytes.Buffer
	if err := run(args, &stdout, &stderr); err != nil {
		t.Fatalf("report-set run failed: %v", err)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("report-set validation emitted output: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func completeCandidateReports(expected candidateProvenance) []any {
	functional := smokeReport{
		ReportKind: reportKindFunctional, ReportSchema: functionalReportSchema, SchemaVersion: smokeReportVersion, Passed: true,
		CandidateCommit: expected.Commit, CandidateSHA256: expected.CandidateSHA256, DependencySHA256: expected.DependencySHA256,
		ToolCalls: functionalToolCallCounts{Resolve: 2, LS: 3, Read: 1, ReadMany: 2, Grep: 1},
		ToolCount: 5, SDKResultCount: 9, MaxSDKResultBytes: 1, MaxStructuredResultBytes: 1,
		MaxClientLatencyMicroseconds: 1, TotalFilesScanned: 1, TotalBytesScanned: 1, TotalSourceEntriesValidated: 1,
		CurrentResolveExistingDir: true,
		SyntheticCanonicalResolve: true, SyntheticPageCount: 2, SyntheticEntryCount: 3,
		SyntheticSecondProgress: true, SyntheticNoDuplicates: true, SyntheticFullEquivalence: true,
		SyntheticReadSelected: true, SyntheticGrepMatchCount: 3, SyntheticReadManyPages: 2,
		SyntheticReadManyContinued: true, SyntheticRetrievalEquivalent: true, SyntheticTelemetrySanitized: true,
	}
	profileShape := passingPhase2PerformanceReportShape()
	functional.CandidateRuntime = profileShape.CandidateRuntime
	functional.Machine = profileShape.Machine
	functional.CurrentVault = profileShape.CurrentVault
	functional.SyntheticVault = vaultAggregateProfile{InventoryPolicy: markdownInventoryPolicy, InventoryComplete: true,
		MarkdownFileCount: 3, MarkdownByteCount: 3, StoppedBy: "scope"}
	functional.CurrentProcess = profileShape.CurrentVaultProcess
	functional.SyntheticProcess = profileShape.SyntheticProcess
	metric := func(samples, files int) performanceMetrics {
		return performanceMetrics{N: samples, P50Microseconds: 1, P95Microseconds: 1, MaxMicroseconds: 1,
			MaxSDKResultBytes: 1, MaxStructuredBytes: 1, MaxFilesScanned: uint64(files)}
	}
	stratified := make([]stratifiedMetrics, 0, len(stratifiedEntryCounts))
	expectedStratifiedRows := 0
	for _, entryCount := range stratifiedEntryCounts {
		stratum := stratifiedMetrics{
			EntryCount: entryCount, FirstLimit1: metric(stratifiedSamples, entryCount),
			FirstLimit100: metric(stratifiedSamples, entryCount), FirstLimit500: metric(stratifiedSamples, entryCount),
		}
		if entryCount > 1 {
			continued := metric(stratifiedSamples, entryCount)
			stratum.ContinuedLimit1 = &continued
		}
		if entryCount > 100 {
			continued := metric(stratifiedSamples, entryCount)
			stratum.ContinuedLimit100 = &continued
		}
		if entryCount > 500 {
			continued := metric(stratifiedSamples, entryCount)
			stratum.ContinuedLimit500 = &continued
		}
		stratified = append(stratified, stratum)
		for _, limit := range []int{1, 100, 500} {
			operations := 1
			if entryCount > limit {
				operations++
			}
			expectedStratifiedRows += (stratifiedWarmups + stratifiedSamples) * operations
		}
	}
	sqliteProof := func(setup, measured int) sqliteTelemetryProof {
		total := setup + measured
		return sqliteTelemetryProof{SetupToolCallRows: setup, ExpectedMeasuredToolCallRows: measured,
			MeasuredToolCallRows: measured, TotalToolCallRows: total, PersistedRows: total, ParsedBodyRows: total, Validated: true}
	}
	performance := passingPhase2PerformanceReportShape()
	performance.ReportKind = reportKindPerformance
	performance.SchemaVersion = smokeReportVersion
	performance.Passed = true
	performance.CandidateCommit = expected.Commit
	performance.CandidateSHA256 = expected.CandidateSHA256
	performance.DependencySHA256 = expected.DependencySHA256
	performance.CardinalityBucket = "2_10"
	performance.ResolveCached = metric(performanceSamples, 0)
	performance.LSFirstLimit1 = metric(performanceSamples, 3)
	performance.LSContinuedLimit1 = metric(performanceSamples, 3)
	performance.LSFirstLimit100 = metric(performanceSamples, 3)
	performance.Stratified = stratified
	performance.CurrentSQLite = sqliteProof(1, (performanceWarmups+performanceSamples)*4)
	performance.StratifiedSQLite = sqliteProof(len(stratifiedEntryCounts)*3, expectedStratifiedRows)
	performance.SQLiteDegradation = sqliteDegradationProof{FailureInjected: true, DegradationObserved: true, ToolCallSucceeded: true,
		SDKResultBytes: 1, LatencyMicroseconds: 1, BoundMicroseconds: performanceP95LimitUS,
		WithinResponseBudget: true, WithinLatencyBudget: true}
	performance.Cancellation = cancellationObservation{EntryCount: 10_000, DeadlineMicroseconds: cancellationDelay.Microseconds(),
		ClientReturnMicroseconds: 1, ServerCompletionMicroseconds: 1, BoundMicroseconds: cancellationBound.Microseconds(),
		FilesScanned: 1, ServerCompleted: true, PartialWork: true, WithinBound: true, FollowupSucceeded: true}
	resource := passingPhase2ResourceGateReport()
	resource.ReportKind = reportKindResource
	resource.Passed = true
	resource.CandidateCommit = expected.Commit
	resource.CandidateSHA256 = expected.CandidateSHA256
	resource.DependencySHA256 = expected.DependencySHA256
	return []any{functional, performance, resource}
}

func provenanceFromArgs(t *testing.T, args []string) (string, candidateProvenance) {
	t.Helper()
	value := func(name string) string {
		for i := 0; i+1 < len(args); i++ {
			if args[i] == name {
				return args[i+1]
			}
		}
		t.Fatalf("missing provenance argument %s", name)
		return ""
	}
	return value("--repo-root"), candidateProvenance{
		Commit: value("--candidate-commit"), CandidateSHA256: value("--candidate-sha256"), DependencySHA256: value("--dependency-sha256"),
	}
}
