package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"personal-mcp-gateway/internal/resourceprobe"
	"personal-mcp-gateway/internal/tools/obsidian"
)

func TestRunProbesBuiltGatewayCandidate(t *testing.T) {
	candidate := buildGatewayCandidate(t)
	vault := t.TempDir()
	var stdout, stderr bytes.Buffer
	if err := run([]string{
		"--gateway-bin", candidate,
		"--obsidian-root", vault,
	}, &stdout, &stderr); err != nil {
		t.Fatalf("run() failed: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	if stdout.String() != defaultSuccessMessage+"\n" || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunReportJSONIsOneSanitizedAggregate(t *testing.T) {
	candidate := buildGatewayCandidate(t)
	vault := filepath.Join(t.TempDir(), "private-vault-sentinel")
	if err := os.Mkdir(vault, 0o700); err != nil {
		t.Fatal(err)
	}
	privateEntry := "private-entry-sentinel.md"
	if err := os.WriteFile(filepath.Join(vault, privateEntry), []byte("private-content-sentinel\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if err := run([]string{
		"--gateway-bin", candidate,
		"--obsidian-root", vault,
		"--report-json",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("run() failed: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr=%q", stderr.String())
	}
	for _, forbidden := range []string{candidate, vault, filepath.Dir(vault), privateEntry, "private-content-sentinel", "Caf\u00e9", "Alpha.md", "next_cursor"} {
		if strings.Contains(stdout.String(), forbidden) {
			t.Fatalf("report leaked %q: %s", forbidden, stdout.String())
		}
	}

	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	var report smokeReport
	if err := decoder.Decode(&report); err != nil {
		t.Fatal(err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		t.Fatalf("report contained trailing JSON: %v", err)
	}
	if !report.Passed || report.SchemaVersion != smokeReportVersion || report.ToolCount != 2 ||
		!report.CurrentResolveExistingDir || !report.SyntheticCanonicalResolve ||
		report.SyntheticPageCount < 2 || report.SyntheticEntryCount != 3 ||
		!report.SyntheticSecondProgress || !report.SyntheticNoDuplicates ||
		!report.SyntheticFullEquivalence || report.SDKResultCount < 5 ||
		report.MaxSDKResultBytes <= 0 {
		t.Fatalf("report = %#v", report)
	}
}

func TestRunPerformanceJSONIsBoundedAndSanitized(t *testing.T) {
	candidate := buildGatewayCandidate(t)
	vault := filepath.Join(t.TempDir(), "performance-private-vault")
	representative := filepath.Join(vault, "representative-private-directory")
	if err := os.MkdirAll(representative, 0o700); err != nil {
		t.Fatal(err)
	}
	privateNames := []string{"private-alpha.md", "private-beta.md", "private-gamma.md"}
	for _, name := range privateNames {
		if err := os.WriteFile(filepath.Join(representative, name), []byte("private-performance-content\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var stdout, stderr bytes.Buffer
	if err := run([]string{
		"--gateway-bin", candidate,
		"--obsidian-root", vault,
		"--performance-json",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("run() failed: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr=%q", stderr.String())
	}
	for _, forbidden := range append([]string{
		candidate,
		vault,
		representative,
		"representative-private-directory",
		"private-performance-content",
		"fixture-00",
		"entry-00000.md",
		"next_cursor",
	}, privateNames...) {
		if strings.Contains(stdout.String(), forbidden) {
			t.Fatalf("performance report leaked %q: %s", forbidden, stdout.String())
		}
	}

	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	var report performanceReport
	if err := decoder.Decode(&report); err != nil {
		t.Fatal(err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		t.Fatalf("performance report contained trailing JSON: %v", err)
	}
	if !report.Passed || report.SchemaVersion != smokeReportVersion || report.DescriptorCount != 2 || report.CardinalityBucket != "2_10" {
		t.Fatalf("report header = %#v", report)
	}
	for name, metrics := range map[string]performanceMetrics{
		"resolve":     report.ResolveCached,
		"first_1":     report.LSFirstLimit1,
		"continued_1": report.LSContinuedLimit1,
		"first_100":   report.LSFirstLimit100,
	} {
		if metrics.N != performanceSamples || metrics.P95Microseconds > performanceP95LimitUS ||
			metrics.MaxSDKResultBytes <= 0 || metrics.MaxStructuredBytes <= 0 {
			t.Fatalf("%s metrics = %#v", name, metrics)
		}
	}
	if report.LSFirstLimit1.MaxFilesScanned < 3 || report.LSContinuedLimit1.MaxFilesScanned < 3 ||
		report.LSFirstLimit100.MaxFilesScanned < 3 || report.ResolveCached.MaxFilesScanned != 0 {
		t.Fatalf("unexpected work metrics: %#v", report)
	}
	expectedCurrentRows := (performanceWarmups + performanceSamples) * 4
	assertSQLiteTelemetryProof(t, "current", report.CurrentSQLite, expectedCurrentRows)
	if report.CurrentSQLite.SetupToolCallRows <= 0 {
		t.Fatalf("current SQLite setup rows = %#v", report.CurrentSQLite)
	}
	if len(report.Stratified) != len(stratifiedEntryCounts) {
		t.Fatalf("stratified metrics = %#v", report.Stratified)
	}
	for i, stratum := range report.Stratified {
		wantCount := stratifiedEntryCounts[i]
		if stratum.EntryCount != wantCount {
			t.Fatalf("stratum[%d] entry count = %d, want %d", i, stratum.EntryCount, wantCount)
		}
		assertStratifiedMetrics(t, "first_1", stratum.FirstLimit1, wantCount)
		assertStratifiedMetrics(t, "first_100", stratum.FirstLimit100, wantCount)
		assertStratifiedMetrics(t, "first_500", stratum.FirstLimit500, wantCount)
		assertContinuedMetrics(t, "continued_1", stratum.ContinuedLimit1, wantCount, wantCount > 1)
		assertContinuedMetrics(t, "continued_100", stratum.ContinuedLimit100, wantCount, wantCount > 100)
		assertContinuedMetrics(t, "continued_500", stratum.ContinuedLimit500, wantCount, wantCount > 500)
	}
	expectedStratifiedRows := 0
	for _, entryCount := range stratifiedEntryCounts {
		for _, limit := range []int{1, 100, 500} {
			operations := 1
			if entryCount > limit {
				operations++
			}
			expectedStratifiedRows += (stratifiedWarmups + stratifiedSamples) * operations
		}
	}
	assertSQLiteTelemetryProof(t, "stratified", report.StratifiedSQLite, expectedStratifiedRows)
	if report.StratifiedSQLite.SetupToolCallRows != len(stratifiedEntryCounts)*3 {
		t.Fatalf("stratified SQLite setup rows = %#v", report.StratifiedSQLite)
	}
	if !report.SQLiteDegradation.FailureInjected || !report.SQLiteDegradation.DegradationObserved ||
		!report.SQLiteDegradation.ToolCallSucceeded || !report.SQLiteDegradation.WithinResponseBudget ||
		!report.SQLiteDegradation.WithinLatencyBudget || report.SQLiteDegradation.SDKResultBytes <= 0 ||
		report.SQLiteDegradation.SDKResultBytes > 64*1024 ||
		report.SQLiteDegradation.LatencyMicroseconds > performanceP95LimitUS ||
		report.SQLiteDegradation.BoundMicroseconds != performanceP95LimitUS {
		t.Fatalf("SQLite degradation proof = %#v", report.SQLiteDegradation)
	}
	if !report.Cancellation.ServerCompleted || !report.Cancellation.PartialWork || !report.Cancellation.WithinBound || !report.Cancellation.FollowupSucceeded ||
		report.Cancellation.EntryCount != 10_000 || report.Cancellation.DeadlineMicroseconds != cancellationDelay.Microseconds() ||
		report.Cancellation.BoundMicroseconds != cancellationBound.Microseconds() ||
		report.Cancellation.ClientReturnMicroseconds > cancellationBound.Microseconds() ||
		report.Cancellation.ServerCompletionMicroseconds > cancellationBound.Microseconds() ||
		report.Cancellation.FilesScanned == 0 || report.Cancellation.FilesScanned >= uint64(report.Cancellation.EntryCount) {
		t.Fatalf("cancellation observation = %#v", report.Cancellation)
	}

	var generic map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &generic); err != nil {
		t.Fatal(err)
	}
	assertOnlySanitizedReportValues(t, generic)
}

func assertSQLiteTelemetryProof(t *testing.T, name string, proof sqliteTelemetryProof, expectedMeasured int) {
	t.Helper()
	if !proof.Validated || proof.ExpectedMeasuredToolCallRows != expectedMeasured ||
		proof.MeasuredToolCallRows != expectedMeasured ||
		proof.TotalToolCallRows != proof.SetupToolCallRows+expectedMeasured ||
		proof.PersistedRows < proof.TotalToolCallRows || proof.ParsedBodyRows != proof.PersistedRows {
		t.Fatalf("%s SQLite proof = %#v", name, proof)
	}
}

func assertStratifiedMetrics(t *testing.T, name string, metrics performanceMetrics, entryCount int) {
	t.Helper()
	if metrics.N != stratifiedSamples || metrics.P95Microseconds > performanceP95LimitUS ||
		metrics.MaxSDKResultBytes <= 0 || metrics.MaxSDKResultBytes > 64*1024 || metrics.MaxStructuredBytes <= 0 ||
		metrics.MaxFilesScanned != uint64(entryCount) || metrics.MaxBytesScanned != 0 {
		t.Fatalf("%s/%d metrics = %#v", name, entryCount, metrics)
	}
}

func assertContinuedMetrics(t *testing.T, name string, metrics *performanceMetrics, entryCount int, want bool) {
	t.Helper()
	if !want {
		if metrics != nil {
			t.Fatalf("%s/%d unexpectedly measured: %#v", name, entryCount, metrics)
		}
		return
	}
	if metrics == nil {
		t.Fatalf("%s/%d missing", name, entryCount)
	}
	assertStratifiedMetrics(t, name, *metrics, entryCount)
}

func TestRunRejectsBothJSONModes(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run([]string{
		"--gateway-bin", "candidate",
		"--obsidian-root", "vault",
		"--report-json",
		"--performance-json",
	}, &stdout, &stderr)
	if err == nil || err.Error() != "--report-json, --performance-json, and --resource-json are mutually exclusive" {
		t.Fatalf("error = %v", err)
	}
}

func TestRunRejectsResourceJSONCombinedWithAnotherJSONMode(t *testing.T) {
	for _, other := range []string{"--report-json", "--performance-json"} {
		t.Run(other, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := run([]string{
				"--gateway-bin", "candidate",
				"--obsidian-root", "vault",
				"--resource-json",
				other,
			}, &stdout, &stderr)
			if err == nil || err.Error() != "--report-json, --performance-json, and --resource-json are mutually exclusive" {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestProbeCandidateResourcesUsesFreshProcessesAndEmitsOnlySanitizedAggregates(t *testing.T) {
	candidate := buildGatewayCandidate(t)
	vault := filepath.Join(t.TempDir(), "resource-private-vault")
	representative := filepath.Join(vault, "resource-private-directory")
	if err := os.MkdirAll(representative, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"private-one.md", "private-two.md", "private-three.md"} {
		if err := os.WriteFile(filepath.Join(representative, name), []byte("private-resource-content\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sampler := &fixedResourceSampler{}
	report, err := probeCandidateResources(ctx, candidate, vault, resourceProbeOptions{
		ColdProcesses: 2,
		Stabilize5:    time.Millisecond,
		Stabilize30:   2 * time.Millisecond,
		IdleDuration:  5 * time.Millisecond,
		ControlTime:   2 * time.Second,
	}, sampler)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Passed || report.DescriptorCount != 2 || report.Cold.FreshProcessCount != 2 ||
		len(report.Batches) != resourceBatchCount || !report.HighWaterWithinBound ||
		!report.AllBatchFDsRecovered || !report.BatchEndsDoNotGrow ||
		!report.Idle.CPUWithinBound || !report.Idle.FDsRecovered || !report.Idle.NoExtraToolCalls ||
		!report.Idle.NoVaultActivity || !report.Idle.DescriptorsUnchanged || report.Idle.DescriptorCountAfter != 2 {
		t.Fatalf("resource report = %#v", report)
	}
	for _, batch := range report.Batches {
		if batch.CallCount != resourceBatchCalls || !batch.GCAcknowledged {
			t.Fatalf("batch did not satisfy the fixed call and GC contract: %#v", batch)
		}
	}
	if sampler.distinctPIDs() < 3 {
		t.Fatalf("sampled candidate processes = %d, want at least three", sampler.distinctPIDs())
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{candidate, vault, representative, "resource-private-directory", "private-one.md", "private-resource-content", "next_cursor", "pid"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("resource report leaked %q: %s", forbidden, encoded)
		}
	}
	var generic map[string]any
	if err := json.Unmarshal(encoded, &generic); err != nil {
		t.Fatal(err)
	}
	assertOnlySanitizedReportValues(t, generic)
}

func TestObserveResourceIdleRejectsRealCandidateLSActivity(t *testing.T) {
	candidateBin := buildGatewayCandidate(t)
	vault := t.TempDir()
	representative := filepath.Join(vault, "representative")
	if err := os.Mkdir(representative, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one.md", "two.md", "three.md"} {
		if err := os.WriteFile(filepath.Join(representative, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	candidate, err := connectResourceCandidate(ctx, candidateBin, vault)
	if err != nil {
		t.Fatal(err)
	}
	defer candidate.closeDiscard()
	descriptorCount, err := requireExactToolList(ctx, candidate.process.session)
	if err != nil {
		t.Fatal(err)
	}
	relativeRepresentative, _, err := selectRepresentativeDirectory(ctx, candidate.process.session)
	if err != nil {
		t.Fatal(err)
	}
	sampler := &idleInterferenceSampler{started: make(chan struct{})}
	options := resourceProbeOptions{IdleDuration: 150 * time.Millisecond, ControlTime: 2 * time.Second}
	type result struct {
		report idleResourceReport
		err    error
	}
	resultChannel := make(chan result, 1)
	go func() {
		report, observeErr := observeResourceIdle(
			ctx,
			candidate.process.session,
			candidate.process.command.Process.Pid,
			descriptorCount,
			7,
			candidate.dbPath,
			options,
			sampler,
			candidate.control,
		)
		resultChannel <- result{report: report, err: observeErr}
	}()
	select {
	case <-sampler.started:
	case <-ctx.Done():
		t.Fatal("idle observation did not start")
	}
	page, _, err := callMeasured[obsidian.LSOutput](ctx, candidate.process.session, obsidian.ToolLS, map[string]any{
		"path":  relativeRepresentative,
		"limit": 1,
	})
	if err != nil || !validFirstPerformancePage(page) {
		t.Fatalf("real idle-window ls failed: %#v, %v", page, err)
	}
	observed := <-resultChannel
	if observed.err != nil {
		t.Fatal(observed.err)
	}
	idle := observed.report
	if idle.NoVaultActivity || idle.VaultActivityTotalAfter <= idle.VaultActivityTotalBefore ||
		idle.VaultActivityActiveBefore != 0 || idle.VaultActivityActiveAfter != 0 {
		t.Fatalf("idle activity report = %#v", idle)
	}

	// Isolate the activity decision from the expected telemetry-row change and
	// prove that this real ls alone is sufficient to reject the resource gate.
	idle.CPUWithinBound = true
	idle.FDsRecovered = true
	idle.NoExtraToolCalls = true
	idle.DescriptorsUnchanged = true
	gate := passingResourceGateReport()
	gate.Idle = idle
	if resourceReportPasses(gate, 10) {
		t.Fatal("resource gate accepted real vault activity during idle")
	}
}

func TestSystemResourceSamplerAgainstBuiltCandidate(t *testing.T) {
	if os.Getenv("RUN_LIVE_RESOURCE_PROBE") != "1" {
		t.Skip("set RUN_LIVE_RESOURCE_PROBE=1 to exercise macOS process sampling")
	}
	candidate := buildGatewayCandidate(t)
	vault := t.TempDir()
	representative := filepath.Join(vault, "representative")
	if err := os.Mkdir(representative, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one.md", "two.md", "three.md"} {
		if err := os.WriteFile(filepath.Join(representative, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	report, err := probeCandidateResources(ctx, candidate, vault, resourceProbeOptions{
		ColdProcesses: 2,
		Stabilize5:    25 * time.Millisecond,
		Stabilize30:   50 * time.Millisecond,
		IdleDuration:  200 * time.Millisecond,
		ControlTime:   2 * time.Second,
	}, systemResourceSampler{})
	if err != nil && err.Error() != "candidate resource gate failed" {
		t.Fatalf("%v: %#v", err, report)
	}
	if report.Cold.FreshProcessCount != 2 || report.DescriptorCount != 2 || !report.HighWaterWithinBound ||
		!report.AllBatchFDsRecovered || !report.Idle.CPUWithinBound || !report.Idle.FDsRecovered ||
		!report.Idle.NoExtraToolCalls || !report.Idle.NoVaultActivity || !report.Idle.DescriptorsUnchanged {
		t.Fatalf("resource report = %#v", report)
	}
	for _, batch := range report.Batches {
		if !batch.GCAcknowledged || batch.CallCount != resourceBatchCalls {
			t.Fatalf("batch did not observe a strictly post-batch GC: %#v", batch)
		}
	}
}

func TestParseProcessCPU(t *testing.T) {
	for input, want := range map[string]int64{
		"0:00.01":      10_000,
		"12:34.56":     754_560_000,
		"1:02:03.4":    3_723_400_000,
		"2-01:02:03.4": 176_523_400_000,
	} {
		got, err := parseProcessCPU(input)
		if err != nil || got != want {
			t.Fatalf("parseProcessCPU(%q) = %d, %v; want %d", input, got, err, want)
		}
	}
	for _, input := range []string{"", "1", "1:60", "-1:00", "1:02:03:04"} {
		if _, err := parseProcessCPU(input); err == nil {
			t.Fatalf("parseProcessCPU(%q) succeeded", input)
		}
	}
}

func TestEnvironmentWithOverridePreservesGODEBUGAndReplacesOnlyPrivateMarker(t *testing.T) {
	got := environmentWithOverride([]string{
		"A=1",
		"GODEBUG=existing",
		resourceprobe.Environment + "=old",
		"B=2",
	}, resourceprobe.Environment, "3,4")
	want := []string{"A=1", "GODEBUG=existing", "B=2", resourceprobe.Environment + "=3,4"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("environment = %#v, want %#v", got, want)
	}
}

func TestDefaultResourceProbeContractIsFrozen(t *testing.T) {
	got := defaultResourceProbeOptions()
	want := resourceProbeOptions{
		ColdProcesses: 10,
		Stabilize5:    5 * time.Second,
		Stabilize30:   30 * time.Second,
		IdleDuration:  60 * time.Second,
		ControlTime:   5 * time.Second,
	}
	if !reflect.DeepEqual(got, want) || resourceBatchCount != 3 || resourceBatchCalls != 100 {
		t.Fatalf("resource probe defaults = %#v, batches=%d, calls=%d", got, resourceBatchCount, resourceBatchCalls)
	}
}

func TestBatchEndsDoNotGrowTruthTable(t *testing.T) {
	for _, test := range []struct {
		name string
		rss  [3]int64
		want bool
	}{
		{name: "flat", rss: [3]int64{1, 1, 1}, want: true},
		{name: "plateau then rise", rss: [3]int64{1, 1, 2}, want: false},
		{name: "rise then plateau", rss: [3]int64{1, 2, 2}, want: false},
		{name: "strict rise", rss: [3]int64{1, 2, 3}, want: false},
		{name: "decline", rss: [3]int64{3, 2, 1}, want: true},
		{name: "rise then decline", rss: [3]int64{1, 3, 2}, want: true},
		{name: "decline then recover", rss: [3]int64{2, 1, 2}, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			batches := make([]resourceBatchReport, 3)
			for index := range batches {
				batches[index].RSSAfter30SecondsBytes = test.rss[index]
			}
			if got := batchEndsDoNotGrow(batches); got != test.want {
				t.Fatalf("batchEndsDoNotGrow(%v) = %v, want %v", test.rss, got, test.want)
			}
		})
	}
}

func TestWaitedUsageFromRusageUsesPlatformHighWaterUnits(t *testing.T) {
	rusage := &syscall.Rusage{
		Maxrss: 1_234,
		Utime:  syscall.Timeval{Sec: 1, Usec: 2},
		Stime:  syscall.Timeval{Sec: 3, Usec: 4},
	}
	darwin, err := waitedUsageFromRusage(rusage, "darwin")
	if err != nil || darwin.highWaterRSSBytes != 1_234 || darwin.cpuMicros != 4_000_006 {
		t.Fatalf("darwin usage = %#v, %v", darwin, err)
	}
	linux, err := waitedUsageFromRusage(rusage, "linux")
	if err != nil || linux.highWaterRSSBytes != 1_234*1024 || linux.cpuMicros != 4_000_006 {
		t.Fatalf("linux usage = %#v, %v", linux, err)
	}
	if _, err := waitedUsageFromRusage(nil, "darwin"); err == nil {
		t.Fatal("nil rusage was accepted")
	}
}

func TestWaitedHighWaterPreservesTransientRSSBreachAfterGC(t *testing.T) {
	const helperEnvironment = "PERSONAL_MCP_GATEWAY_TRANSIENT_RSS_HELPER"
	if os.Getenv(helperEnvironment) == "1" {
		runTransientRSSHelper(t)
		return
	}
	if runtime.GOOS != "darwin" {
		t.Skip("waited ru_maxrss byte semantics are verified on the supported macOS release host")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWaitedHighWaterPreservesTransientRSSBreachAfterGC$")
	cmd.Env = append(os.Environ(), helperEnvironment+"=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(stdout)
	waitFor := func(want string) {
		t.Helper()
		line, err := reader.ReadString('\n')
		if err != nil || line != want+"\n" {
			t.Fatalf("helper acknowledgement = %q, %v; stderr=%q", line, err, stderr.String())
		}
	}
	advance := func() {
		t.Helper()
		if _, err := io.WriteString(stdin, "x"); err != nil {
			t.Fatal(err)
		}
	}

	waitFor("ready")
	sampler := systemResourceSampler{}
	baseline, err := sampler.Sample(ctx, cmd.Process.Pid, false)
	if err != nil {
		t.Fatal(err)
	}
	advance()
	waitFor("allocated")
	peak, err := sampler.Sample(ctx, cmd.Process.Pid, false)
	if err != nil {
		t.Fatal(err)
	}
	if nonnegativeDelta(peak.rssBytes, baseline.rssBytes) <= resourceRSSLimitBytes {
		t.Fatalf("transient RSS delta = %d, want > %d", nonnegativeDelta(peak.rssBytes, baseline.rssBytes), resourceRSSLimitBytes)
	}
	advance()
	waitFor("released")

	var released processResourceSample
	for {
		released, err = sampler.Sample(ctx, cmd.Process.Pid, false)
		if err != nil {
			t.Fatal(err)
		}
		if nonnegativeDelta(released.rssBytes, baseline.rssBytes) <= resourceRSSLimitBytes {
			break
		}
		if err := waitResource(ctx, 50*time.Millisecond); err != nil {
			t.Fatalf("current RSS did not fall after release and GC: %v", err)
		}
	}
	advance()
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("helper failed: %v; stderr=%q", err, stderr.String())
	}
	usage, err := waitedUsageFromProcessState(cmd.ProcessState)
	if err != nil {
		t.Fatal(err)
	}
	if highWaterWithinBound(usage.highWaterRSSBytes, baseline.rssBytes) {
		t.Fatalf("waited high-water delta = %d, want > %d; peak=%d released=%d", nonnegativeDelta(usage.highWaterRSSBytes, baseline.rssBytes), resourceRSSLimitBytes, peak.rssBytes, released.rssBytes)
	}
	if usage.highWaterRSSBytes < peak.rssBytes {
		t.Fatalf("waited high water = %d, below observed peak = %d", usage.highWaterRSSBytes, peak.rssBytes)
	}
}

func runTransientRSSHelper(t *testing.T) {
	announce := func(value string) {
		t.Helper()
		if _, err := io.WriteString(os.Stdout, value+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	wait := func() {
		t.Helper()
		var signal [1]byte
		if _, err := io.ReadFull(os.Stdin, signal[:]); err != nil {
			t.Fatal(err)
		}
	}
	announce("ready")
	wait()
	allocation, err := unix.Mmap(-1, 0, 96*1024*1024, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_ANON|unix.MAP_PRIVATE)
	if err != nil {
		t.Fatal(err)
	}
	for offset := 0; offset < len(allocation); offset += 4096 {
		allocation[offset] = 1
	}
	allocation[len(allocation)-1] = 1
	announce("allocated")
	wait()
	if err := unix.Munmap(allocation); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	debug.FreeOSMemory()
	announce("released")
	wait()
}

func TestHighWaterWithinBoundIncludesExactLimit(t *testing.T) {
	baseline := int64(10 * 1024 * 1024)
	if resourceRSSLimitBytes != int64(64*1024*1024) {
		t.Fatalf("RSS limit = %d, want literal 64 MiB", resourceRSSLimitBytes)
	}
	if !highWaterWithinBound(baseline+resourceRSSLimitBytes, baseline) {
		t.Fatal("exact 64 MiB delta was rejected")
	}
	if highWaterWithinBound(baseline+resourceRSSLimitBytes+1, baseline) {
		t.Fatal("delta above 64 MiB was accepted")
	}
	if highWaterWithinBound(baseline-1, baseline) {
		t.Fatal("lifetime high water below the current baseline was accepted")
	}
}

func TestHighWaterGateConservativelyIncludesPreBaselineLifetimeSpike(t *testing.T) {
	baseline := int64(32 * 1024 * 1024)
	preBaselineLifetimeHighWater := baseline + resourceRSSLimitBytes + 1
	if highWaterWithinBound(preBaselineLifetimeHighWater, baseline) {
		t.Fatal("pre-baseline lifetime spike false-passed the conservative gate")
	}
}

func TestIdleCPUWithinBoundLocksOnePercentBoundary(t *testing.T) {
	if !idleCPUWithinBound(600_000, 60*time.Second) {
		t.Fatal("exactly one percent of a 60-second CPU core was rejected")
	}
	if idleCPUWithinBound(600_001, 60*time.Second) {
		t.Fatal("CPU above one percent of a 60-second core was accepted")
	}
}

func TestNoVaultActivityRejectsInFlightAndNewWork(t *testing.T) {
	if !noVaultActivity(resourceActivitySnapshot{total: 4}, resourceActivitySnapshot{total: 4}) {
		t.Fatal("unchanged idle snapshots were rejected")
	}
	for _, snapshots := range [][2]resourceActivitySnapshot{
		{{total: 4, active: 1}, {total: 4}},
		{{total: 4}, {total: 5}},
		{{total: 4}, {total: 5, active: 1}},
	} {
		if noVaultActivity(snapshots[0], snapshots[1]) {
			t.Fatalf("vault work was accepted: %#v", snapshots)
		}
	}
}

func TestResourceReportPassesRejectsEachGateFailure(t *testing.T) {
	passing := passingResourceGateReport()
	if !resourceReportPasses(passing, 10) {
		t.Fatal("passing report was rejected")
	}
	for _, test := range []struct {
		name   string
		mutate func(*resourceReport)
	}{
		{name: "cold count", mutate: func(r *resourceReport) { r.Cold.FreshProcessCount = 9 }},
		{name: "descriptor count", mutate: func(r *resourceReport) { r.DescriptorCount = 3 }},
		{name: "high water", mutate: func(r *resourceReport) { r.HighWaterWithinBound = false }},
		{name: "batch fd aggregate", mutate: func(r *resourceReport) { r.AllBatchFDsRecovered = false }},
		{name: "monotonic growth", mutate: func(r *resourceReport) { r.BatchEndsDoNotGrow = false }},
		{name: "batch call count", mutate: func(r *resourceReport) { r.Batches[1].CallCount = 99 }},
		{name: "gc acknowledgement", mutate: func(r *resourceReport) { r.Batches[1].GCAcknowledged = false }},
		{name: "batch fd sample", mutate: func(r *resourceReport) { r.Batches[1].FDRecoveredAtEverySample = false }},
		{name: "idle cpu", mutate: func(r *resourceReport) { r.Idle.CPUWithinBound = false }},
		{name: "idle fds", mutate: func(r *resourceReport) { r.Idle.FDsRecovered = false }},
		{name: "idle telemetry", mutate: func(r *resourceReport) { r.Idle.NoExtraToolCalls = false }},
		{name: "idle vault activity", mutate: func(r *resourceReport) { r.Idle.NoVaultActivity = false }},
		{name: "descriptor drift", mutate: func(r *resourceReport) { r.Idle.DescriptorsUnchanged = false }},
	} {
		t.Run(test.name, func(t *testing.T) {
			report := passing
			report.Batches = append([]resourceBatchReport(nil), passing.Batches...)
			test.mutate(&report)
			if resourceReportPasses(report, 10) {
				t.Fatal("failed gate was accepted")
			}
		})
	}
}

func passingResourceGateReport() resourceReport {
	return resourceReport{
		DescriptorCount:      2,
		Cold:                 coldResourceReport{FreshProcessCount: 10},
		HighWaterWithinBound: true,
		AllBatchFDsRecovered: true,
		BatchEndsDoNotGrow:   true,
		Batches: []resourceBatchReport{
			{CallCount: 100, FDRecoveredAtEverySample: true, GCAcknowledged: true},
			{CallCount: 100, FDRecoveredAtEverySample: true, GCAcknowledged: true},
			{CallCount: 100, FDRecoveredAtEverySample: true, GCAcknowledged: true},
		},
		Idle: idleResourceReport{
			CPUWithinBound:       true,
			FDsRecovered:         true,
			NoExtraToolCalls:     true,
			NoVaultActivity:      true,
			DescriptorsUnchanged: true,
		},
	}
}

type fixedResourceSampler struct {
	pids map[int]struct{}
	cpu  int64
}

type idleInterferenceSampler struct {
	started chan struct{}
	calls   int64
}

func (s *idleInterferenceSampler) Sample(_ context.Context, pid int, _ bool) (processResourceSample, error) {
	if pid <= 0 {
		return processResourceSample{}, errors.New("invalid pid")
	}
	s.calls++
	if s.calls == 1 {
		close(s.started)
	}
	return processResourceSample{rssBytes: 1024 * 1024, cpuMicros: s.calls, fdCount: 7}, nil
}

func (s *fixedResourceSampler) Sample(_ context.Context, pid int, _ bool) (processResourceSample, error) {
	if pid <= 0 {
		return processResourceSample{}, errors.New("invalid pid")
	}
	if s.pids == nil {
		s.pids = make(map[int]struct{})
	}
	s.pids[pid] = struct{}{}
	s.cpu++
	return processResourceSample{rssBytes: 1024 * 1024, cpuMicros: s.cpu, fdCount: 7}, nil
}

func (s *fixedResourceSampler) distinctPIDs() int {
	return len(s.pids)
}

func TestInspectSQLiteRejectsCorruptBody(t *testing.T) {
	dbPath := newSmokeSQLiteFixture(t)
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO audit_events(event, body_json) VALUES ('tool.call', '{')`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectSQLite(context.Background(), dbPath); err == nil {
		t.Fatal("inspectSQLite accepted corrupt body_json")
	}
}

func TestSQLiteBurstTrackerRejectsUndercount(t *testing.T) {
	dbPath := newSmokeSQLiteFixture(t)
	tracker, err := newSQLiteBurstTracker(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO audit_events(event, body_json) VALUES ('tool.call', '{"event":"tool.call"}')`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tracker.recordMeasuredBurst(2); err == nil {
		t.Fatal("SQLite burst tracker accepted an undercount")
	}
}

func newSmokeSQLiteFixture(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "telemetry.sqlite")
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE audit_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		event TEXT NOT NULL,
		body_json TEXT NOT NULL
	)`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return dbPath
}

func TestRunSanitizesCandidateStartFailure(t *testing.T) {
	privateCandidate := filepath.Join(t.TempDir(), "missing-gateway")
	privateVault := t.TempDir()
	var stdout, stderr bytes.Buffer
	err := run([]string{
		"--gateway-bin", privateCandidate,
		"--obsidian-root", privateVault,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("run() succeeded with missing candidate")
	}
	for _, privatePath := range []string{privateCandidate, privateVault, filepath.Dir(privateVault)} {
		if strings.Contains(err.Error(), privatePath) || strings.Contains(stdout.String(), privatePath) || strings.Contains(stderr.String(), privatePath) {
			t.Fatalf("smoke failure leaked private path %q: err=%q stdout=%q stderr=%q", privatePath, err, stdout.String(), stderr.String())
		}
	}
}

func buildGatewayCandidate(t *testing.T) string {
	t.Helper()
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	candidate := filepath.Join(t.TempDir(), "personal-mcp-gateway")
	build := exec.Command("go", "build", "-buildvcs=false", "-o", candidate, "./cmd/gateway")
	build.Dir = repoRoot
	build.Env = os.Environ()
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build candidate: %v\n%s", err, output)
	}
	return candidate
}

func assertOnlySanitizedReportValues(t *testing.T, value any) {
	t.Helper()
	switch typed := value.(type) {
	case map[string]any:
		for _, child := range typed {
			assertOnlySanitizedReportValues(t, child)
		}
	case []any:
		for _, child := range typed {
			assertOnlySanitizedReportValues(t, child)
		}
	case string:
		switch typed {
		case "2_10", "11_100", "101_1000", "1001_plus":
		default:
			t.Fatalf("unexpected string value in sanitized report: %q", typed)
		}
	case float64, bool, nil:
	default:
		t.Fatalf("unexpected JSON value type %T", value)
	}
}
