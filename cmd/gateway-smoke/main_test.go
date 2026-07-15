package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
	if err == nil || err.Error() != "--report-json and --performance-json are mutually exclusive" {
		t.Fatalf("error = %v", err)
	}
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
