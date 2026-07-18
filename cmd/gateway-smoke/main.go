package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"personal-mcp-gateway/internal/tools/obsidian"
)

const (
	smokeTimeout             = 10 * time.Second
	performanceTimeout       = 90 * time.Second
	resourceTimeout          = 4 * time.Minute
	performanceWarmups       = 10
	performanceSamples       = 100
	stratifiedWarmups        = 2
	stratifiedSamples        = 20
	performanceP95LimitUS    = 100_000
	cancellationDelay        = 2 * time.Millisecond
	cancellationBound        = 100 * time.Millisecond
	defaultSuccessMessage    = "gateway smoke passed: resolve(.) returned an existing directory"
	smokeReportVersion       = 3
	performanceReportVersion = 4
)

var stratifiedEntryCounts = [...]int{1, 100, 1_000, 10_000}

type smokeReport struct {
	ReportKind                   string                   `json:"report_kind"`
	ReportSchema                 string                   `json:"report_schema"`
	SchemaVersion                int                      `json:"schema_version"`
	Passed                       bool                     `json:"passed"`
	CandidateCommit              string                   `json:"candidate_commit"`
	CandidateSHA256              string                   `json:"candidate_sha256"`
	DependencySHA256             string                   `json:"dependency_sha256"`
	CandidateRuntime             candidateRuntimeProfile  `json:"candidate_runtime"`
	Machine                      machineProfile           `json:"machine"`
	CurrentVault                 vaultAggregateProfile    `json:"current_vault"`
	SyntheticVault               vaultAggregateProfile    `json:"synthetic_vault"`
	CurrentProcess               candidateProcessProfile  `json:"current_process"`
	SyntheticProcess             candidateProcessProfile  `json:"synthetic_process"`
	ToolCalls                    functionalToolCallCounts `json:"tool_calls"`
	ToolCount                    int                      `json:"tool_count"`
	SDKResultCount               int                      `json:"sdk_result_count"`
	MaxSDKResultBytes            int                      `json:"max_sdk_result_bytes"`
	MaxStructuredResultBytes     int                      `json:"max_structured_result_bytes"`
	MaxClientLatencyMicroseconds int64                    `json:"max_client_latency_microseconds"`
	TotalFilesScanned            uint64                   `json:"total_files_scanned"`
	TotalBytesScanned            uint64                   `json:"total_bytes_scanned"`
	TotalSourceEntriesValidated  uint64                   `json:"total_source_entries_validated"`
	CurrentResolveExistingDir    bool                     `json:"current_resolve_existing_directory"`
	SyntheticCanonicalResolve    bool                     `json:"synthetic_canonical_resolve"`
	SyntheticPageCount           int                      `json:"synthetic_page_count"`
	SyntheticEntryCount          int                      `json:"synthetic_entry_count"`
	SyntheticSecondProgress      bool                     `json:"synthetic_second_page_progress"`
	SyntheticNoDuplicates        bool                     `json:"synthetic_no_duplicates"`
	SyntheticFullEquivalence     bool                     `json:"synthetic_full_equivalence"`
	SyntheticReadSelected        bool                     `json:"synthetic_read_selected"`
	SyntheticGrepMatchCount      int                      `json:"synthetic_grep_match_count"`
	SyntheticReadManyPages       int                      `json:"synthetic_read_many_pages"`
	SyntheticReadManyContinued   bool                     `json:"synthetic_read_many_continued"`
	SyntheticRetrievalEquivalent bool                     `json:"synthetic_retrieval_equivalent"`
	SyntheticTelemetrySanitized  bool                     `json:"synthetic_telemetry_sanitized"`
}

type performanceReport struct {
	ReportKind          string                   `json:"report_kind"`
	ReportSchema        string                   `json:"report_schema"`
	SchemaVersion       int                      `json:"schema_version"`
	Passed              bool                     `json:"passed"`
	CandidateCommit     string                   `json:"candidate_commit"`
	CandidateSHA256     string                   `json:"candidate_sha256"`
	DependencySHA256    string                   `json:"dependency_sha256"`
	CandidateRuntime    candidateRuntimeProfile  `json:"candidate_runtime"`
	Machine             machineProfile           `json:"machine"`
	CurrentVault        vaultAggregateProfile    `json:"current_vault"`
	SyntheticCorpus     vaultAggregateProfile    `json:"synthetic_corpus"`
	DescriptorCount     int                      `json:"descriptor_count"`
	CardinalityBucket   string                   `json:"cardinality_bucket"`
	ResolveCached       performanceMetrics       `json:"resolve_cached"`
	LSFirstLimit1       performanceMetrics       `json:"ls_first_limit_1"`
	LSContinuedLimit1   performanceMetrics       `json:"ls_continued_limit_1"`
	LSFirstLimit100     performanceMetrics       `json:"ls_first_limit_100"`
	SyntheticRead       performanceMetrics       `json:"synthetic_read"`
	SyntheticGrep       performanceMetrics       `json:"synthetic_grep"`
	BroadCurrentGrep    broadGrepObservation     `json:"broad_current_grep"`
	BroadNegativeGrep   broadNegativeObservation `json:"broad_negative_grep"`
	SyntheticProcess    candidateProcessProfile  `json:"synthetic_process"`
	CurrentVaultProcess candidateProcessProfile  `json:"current_vault_process"`
	Stratified          []stratifiedMetrics      `json:"stratified"`
	CurrentSQLite       sqliteTelemetryProof     `json:"current_sqlite"`
	StratifiedSQLite    sqliteTelemetryProof     `json:"stratified_sqlite"`
	SQLiteDegradation   sqliteDegradationProof   `json:"sqlite_degradation"`
	Cancellation        cancellationObservation  `json:"cancellation"`
}

type sqliteTelemetryProof struct {
	SetupToolCallRows            int  `json:"setup_tool_call_rows"`
	ExpectedMeasuredToolCallRows int  `json:"expected_measured_tool_call_rows"`
	MeasuredToolCallRows         int  `json:"measured_tool_call_rows"`
	TotalToolCallRows            int  `json:"total_tool_call_rows"`
	PersistedRows                int  `json:"persisted_rows"`
	ParsedBodyRows               int  `json:"parsed_body_rows"`
	Validated                    bool `json:"validated"`
}

type sqliteDegradationProof struct {
	FailureInjected      bool  `json:"failure_injected"`
	DegradationObserved  bool  `json:"degradation_observed"`
	ToolCallSucceeded    bool  `json:"tool_call_succeeded"`
	SDKResultBytes       int   `json:"sdk_result_bytes"`
	LatencyMicroseconds  int64 `json:"latency_microseconds"`
	BoundMicroseconds    int64 `json:"bound_microseconds"`
	WithinResponseBudget bool  `json:"within_response_budget"`
	WithinLatencyBudget  bool  `json:"within_latency_budget"`
}

type stratifiedMetrics struct {
	EntryCount        int                 `json:"entry_count"`
	FirstLimit1       performanceMetrics  `json:"first_limit_1"`
	ContinuedLimit1   *performanceMetrics `json:"continued_limit_1"`
	FirstLimit100     performanceMetrics  `json:"first_limit_100"`
	ContinuedLimit100 *performanceMetrics `json:"continued_limit_100"`
	FirstLimit500     performanceMetrics  `json:"first_limit_500"`
	ContinuedLimit500 *performanceMetrics `json:"continued_limit_500"`
}

type cancellationObservation struct {
	EntryCount                   int    `json:"entry_count"`
	DeadlineMicroseconds         int64  `json:"deadline_microseconds"`
	ClientReturnMicroseconds     int64  `json:"client_return_microseconds"`
	ServerCompletionMicroseconds int64  `json:"server_completion_microseconds"`
	BoundMicroseconds            int64  `json:"bound_microseconds"`
	FilesScanned                 uint64 `json:"files_scanned"`
	ServerCompleted              bool   `json:"server_completed"`
	PartialWork                  bool   `json:"partial_work"`
	WithinBound                  bool   `json:"within_bound"`
	FollowupSucceeded            bool   `json:"followup_succeeded"`
}

type cancellationTelemetry struct {
	Event     string `json:"event"`
	Tool      string `json:"tool"`
	Outcome   string `json:"outcome"`
	ErrorCode string `json:"error_code"`
	Summary   struct {
		Result struct {
			FilesScanned uint64 `json:"files_scanned"`
			StoppedBy    string `json:"stopped_by"`
		} `json:"result"`
	} `json:"summary"`
}

type performanceMetrics struct {
	N                         int    `json:"n"`
	P50Microseconds           int64  `json:"p50_microseconds"`
	P95Microseconds           int64  `json:"p95_microseconds"`
	MaxMicroseconds           int64  `json:"max_microseconds"`
	MaxSDKResultBytes         int    `json:"max_sdk_result_bytes"`
	MaxStructuredBytes        int    `json:"max_structured_bytes"`
	MaxFilesScanned           uint64 `json:"max_files_scanned"`
	MaxBytesScanned           uint64 `json:"max_bytes_scanned"`
	MaxSourceEntriesValidated uint64 `json:"max_source_entries_validated"`
}

type callMeasurement struct {
	latency         time.Duration
	sdkResultBytes  int
	structuredBytes int
}

type operationSample struct {
	measurement            callMeasurement
	filesScanned           uint64
	bytesScanned           uint64
	sourceEntriesValidated uint64
}

type performanceOperation func() (operationSample, error)

type syntheticFixture struct {
	root             string
	canonicalInput   string
	canonicalPath    string
	directoryPath    string
	expectedListings []string
	expectedContents map[string]string
}

type stratifiedFixture struct {
	root        string
	directories []stratifiedDirectory
}

type stratifiedDirectory struct {
	path       string
	entryCount int
}

type sqliteSnapshot struct {
	persistedRows  int
	parsedBodyRows int
	toolCallRows   int
}

type sqliteBurstTracker struct {
	ctx              context.Context
	dbPath           string
	last             sqliteSnapshot
	setupToolCalls   int
	expectedMeasured int
	measured         int
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "gateway smoke failed:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	return runWithCandidateSnapshotter(args, stdout, stderr, createPrivateCandidateSnapshot)
}

func runWithCandidateSnapshotter(args []string, stdout, stderr io.Writer, snapshotter candidateSnapshotter) error {
	flags := flag.NewFlagSet("gateway-smoke", flag.ContinueOnError)
	flags.SetOutput(stderr)
	gatewayBin := flags.String("gateway-bin", "", "gateway executable to probe")
	obsidianRoot := flags.String("obsidian-root", "", "vault root used for the probe")
	repoRoot := flags.String("repo-root", "", "clean source repository for the candidate")
	candidateCommit := flags.String("candidate-commit", "", "full source commit for the candidate")
	candidateSHA256 := flags.String("candidate-sha256", "", "SHA-256 of the candidate executable")
	dependencySHA256 := flags.String("dependency-sha256", "", "canonical go.mod/go.sum dependency SHA-256")
	reportJSON := flags.Bool("report-json", false, "emit one sanitized aggregate JSON report")
	performanceJSON := flags.Bool("performance-json", false, "emit one sanitized current-vault and stratified candidate performance report")
	resourceJSON := flags.Bool("resource-json", false, "emit one sanitized fresh-process, repeated-batch, and idle resource report")
	validateReports := flags.Bool("validate-report-set", false, "validate exactly one functional, performance, and resource report")
	if err := flags.Parse(args); err != nil {
		return err
	}
	selectedJSONModes := 0
	for _, selected := range []bool{*reportJSON, *performanceJSON, *resourceJSON} {
		if selected {
			selectedJSONModes++
		}
	}
	if selectedJSONModes > 1 {
		return errors.New("--report-json, --performance-json, and --resource-json are mutually exclusive")
	}
	if *gatewayBin == "" || *repoRoot == "" || *candidateCommit == "" || *candidateSHA256 == "" || *dependencySHA256 == "" {
		return errors.New("candidate provenance arguments are required")
	}
	if !*validateReports && *obsidianRoot == "" {
		return errors.New("--obsidian-root is required")
	}
	if *validateReports && (selectedJSONModes != 0 || *obsidianRoot != "" || len(flags.Args()) != 3) {
		return errors.New("report-set validation requires exactly three report files")
	}
	if !*validateReports && len(flags.Args()) != 0 {
		return errors.New("unexpected positional arguments")
	}
	provenance, err := verifyCandidateProvenance(*repoRoot, *gatewayBin, candidateProvenance{
		Commit: *candidateCommit, CandidateSHA256: *candidateSHA256, DependencySHA256: *dependencySHA256,
	})
	if err != nil {
		return err
	}
	if snapshotter == nil {
		return errors.New("candidate snapshot failed")
	}
	candidatePath, cleanupCandidate, err := snapshotter(*gatewayBin, provenance.CandidateSHA256)
	if err != nil {
		return err
	}
	defer cleanupCandidate()
	if *validateReports {
		return validateReportSet(flags.Args(), provenance)
	}

	timeout := smokeTimeout
	if *performanceJSON {
		timeout = performanceTimeout
	} else if *resourceJSON {
		timeout = resourceTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if *resourceJSON {
		report, err := probeCandidateResources(ctx, candidatePath, *obsidianRoot, defaultResourceProbeOptions(), systemResourceSampler{})
		report.ReportKind = reportKindResource
		report.CandidateCommit = provenance.Commit
		report.CandidateSHA256 = provenance.CandidateSHA256
		report.DependencySHA256 = provenance.DependencySHA256
		if report.SchemaVersion != 0 {
			if encodeErr := json.NewEncoder(stdout).Encode(report); encodeErr != nil {
				return errors.New("encode resource report failed")
			}
		}
		if err != nil {
			return err
		}
		if report.SchemaVersion == 0 {
			return errors.New("encode resource report failed")
		}
		return nil
	}
	if *performanceJSON {
		report, err := probeCurrentVaultPerformance(ctx, candidatePath, *obsidianRoot)
		if err != nil {
			return err
		}
		report.ReportKind = reportKindPerformance
		report.ReportSchema = performanceReportSchema
		report.SchemaVersion = performanceReportVersion
		report.CandidateCommit = provenance.Commit
		report.CandidateSHA256 = provenance.CandidateSHA256
		report.DependencySHA256 = provenance.DependencySHA256
		phase2, err := probePhase2Performance(ctx, candidatePath, *obsidianRoot)
		if err != nil {
			return err
		}
		report.CandidateRuntime = phase2.runtime
		report.Machine = phase2.machine
		report.CurrentVault = phase2.currentVault
		report.SyntheticCorpus = phase2.syntheticCorpus
		report.SyntheticRead = phase2.syntheticRead
		report.SyntheticGrep = phase2.syntheticGrep
		report.BroadCurrentGrep = phase2.broadCurrentGrep
		report.BroadNegativeGrep = phase2.broadNegativeGrep
		report.SyntheticProcess = phase2.syntheticProcess
		report.CurrentVaultProcess = phase2.currentVaultProcess
		report.Stratified, report.StratifiedSQLite, report.Cancellation, err = probeStratifiedPerformance(ctx, candidatePath)
		if err != nil {
			return err
		}
		report.SQLiteDegradation, err = probeSQLiteDegradation(ctx, candidatePath)
		if err != nil {
			return err
		}
		report.Passed = performanceReportEvidencePasses(report)
		if !report.Passed {
			return errors.New("candidate performance report gate failed")
		}
		if err := json.NewEncoder(stdout).Encode(report); err != nil {
			return errors.New("encode performance report failed")
		}
		return nil
	}

	runtimeProfile, err := inspectCandidateRuntime(candidatePath)
	if err != nil {
		return err
	}
	machine, err := inspectMachineProfile()
	if err != nil {
		return err
	}
	report := smokeReport{
		ReportKind: reportKindFunctional, ReportSchema: functionalReportSchema,
		SchemaVersion: smokeReportVersion, SyntheticNoDuplicates: true,
		CandidateCommit: provenance.Commit, CandidateSHA256: provenance.CandidateSHA256, DependencySHA256: provenance.DependencySHA256,
		CandidateRuntime: runtimeProfile, Machine: machine,
	}
	if err := probeCurrentVault(ctx, candidatePath, *obsidianRoot, &report); err != nil {
		return err
	}
	if err := probeSyntheticVault(ctx, candidatePath, &report); err != nil {
		return err
	}
	report.Passed = functionalBehaviorPasses(report) && functionalReportEvidencePasses(report)
	if !report.Passed {
		return errors.New("candidate functional report gate failed")
	}

	if *reportJSON {
		if err := json.NewEncoder(stdout).Encode(report); err != nil {
			return errors.New("encode smoke report failed")
		}
		return nil
	}
	_, err = fmt.Fprintln(stdout, defaultSuccessMessage)
	return err
}

func probeCurrentVault(ctx context.Context, gatewayBin, root string, report *smokeReport) error {
	inventory, err := inspectVaultAggregate(ctx, root)
	if err != nil {
		return err
	}
	report.CurrentVault = inventory
	process, err := connectCandidateProcessHandle(ctx, exec.Command(gatewayBin,
		"stdio", "--obsidian-root", root, "--telemetry", "off"), io.Discard)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			_ = process.session.Close()
		}
	}()

	toolCount, err := requireExactToolList(ctx, process.session)
	if err != nil {
		return err
	}
	report.ToolCount = toolCount
	tracker, err := startCandidateProcessTracker(ctx, process, systemResourceSampler{})
	if err != nil {
		return err
	}

	out, err := callStructured[obsidian.ResolveOutput](ctx, process.session, obsidian.ToolResolve, map[string]any{"path": "."}, report)
	if err != nil {
		return errors.New("candidate resolve call failed")
	}
	if !out.OK || !out.Exists || out.Type != "directory" {
		return errors.New("resolve did not return an existing directory")
	}
	report.CurrentResolveExistingDir = true
	report.CurrentProcess, err = tracker.finish(ctx, process)
	if err != nil {
		return err
	}
	closed = true
	return nil
}

func probeCurrentVaultPerformance(ctx context.Context, gatewayBin, root string) (performanceReport, error) {
	dbPath, cleanup, err := newPrivateSQLiteStore()
	if err != nil {
		return performanceReport{}, err
	}
	defer cleanup()

	session, err := connectCandidateDefaultSQLite(ctx, gatewayBin, root, dbPath, io.Discard)
	if err != nil {
		return performanceReport{}, err
	}
	sessionOpen := true
	defer func() {
		if sessionOpen {
			_ = session.Close()
		}
	}()

	toolCount, err := requireExactToolList(ctx, session)
	if err != nil {
		return performanceReport{}, err
	}
	tracker, err := newSQLiteBurstTracker(ctx, dbPath)
	if err != nil {
		return performanceReport{}, err
	}
	const representative = "."
	cardinality, err := inspectRootCardinality(ctx, root)
	if err != nil {
		return performanceReport{}, err
	}
	if cardinality < 2 {
		return performanceReport{}, errors.New("current vault root has no resumable listing")
	}

	firstIssued, _, err := callMeasured[obsidian.LSOutput](ctx, session, obsidian.ToolLS, map[string]any{
		"path":  representative,
		"limit": 1,
	})
	if err != nil || !validFirstPerformancePage(firstIssued) {
		return performanceReport{}, errors.New("current vault has no resumable directory")
	}
	issuedCursor := firstIssued.Coverage.NextCursor
	firstIdentity := firstIssued.Entries[0].Path
	if err := tracker.recordSetupCalls(); err != nil {
		return performanceReport{}, err
	}

	resolveOperation := func() (operationSample, error) {
		out, measured, callErr := callMeasured[obsidian.ResolveOutput](ctx, session, obsidian.ToolResolve, map[string]any{"path": "."})
		if callErr != nil || !out.OK || !out.Exists || out.Type != "directory" {
			return operationSample{}, errors.New("current-vault performance call failed")
		}
		return operationSample{measurement: measured}, nil
	}
	firstOperation := func() (operationSample, error) {
		out, measured, callErr := callMeasured[obsidian.LSOutput](ctx, session, obsidian.ToolLS, map[string]any{
			"path":  representative,
			"limit": 1,
		})
		if callErr != nil || !validFirstPerformancePage(out) {
			return operationSample{}, errors.New("current-vault performance call failed")
		}
		return sampleFromLS(measured, out), nil
	}
	continuedOperation := func() (operationSample, error) {
		out, measured, callErr := callMeasured[obsidian.LSOutput](ctx, session, obsidian.ToolLS, map[string]any{
			"path":   representative,
			"limit":  1,
			"cursor": issuedCursor,
		})
		if callErr != nil || !validContinuedPerformancePage(out, firstIdentity) {
			return operationSample{}, errors.New("current-vault performance call failed")
		}
		return sampleFromLS(measured, out), nil
	}
	limit100Operation := func() (operationSample, error) {
		out, measured, callErr := callMeasured[obsidian.LSOutput](ctx, session, obsidian.ToolLS, map[string]any{
			"path":  representative,
			"limit": 100,
		})
		if callErr != nil || !validPerformancePage(out) {
			return operationSample{}, errors.New("current-vault performance call failed")
		}
		return sampleFromLS(measured, out), nil
	}
	operations := []performanceOperation{resolveOperation, firstOperation, continuedOperation, limit100Operation}
	for warmup := 0; warmup < performanceWarmups; warmup++ {
		for _, operation := range operations {
			if _, err := operation(); err != nil {
				return performanceReport{}, err
			}
		}
	}

	resolveMetrics, err := collectPerformanceMetrics(resolveOperation, performanceSamples)
	if err != nil {
		return performanceReport{}, err
	}
	firstMetrics, err := collectPerformanceMetrics(firstOperation, performanceSamples)
	if err != nil {
		return performanceReport{}, err
	}
	continuedMetrics, err := collectPerformanceMetrics(continuedOperation, performanceSamples)
	if err != nil {
		return performanceReport{}, err
	}
	limit100Metrics, err := collectPerformanceMetrics(limit100Operation, performanceSamples)
	if err != nil {
		return performanceReport{}, err
	}
	for _, metrics := range []performanceMetrics{resolveMetrics, firstMetrics, continuedMetrics, limit100Metrics} {
		if metrics.P95Microseconds > performanceP95LimitUS || metrics.MaxSDKResultBytes > obsidian.MaxSDKResultBytes {
			return performanceReport{}, errors.New("current-vault performance gate failed")
		}
	}
	expectedMeasured := (performanceWarmups + performanceSamples) * len(operations)
	if err := tracker.recordMeasuredBurst(expectedMeasured); err != nil {
		return performanceReport{}, err
	}
	if err := session.Close(); err != nil {
		return performanceReport{}, errors.New("current-vault candidate close failed")
	}
	sessionOpen = false
	sqliteProof, err := tracker.finalize()
	if err != nil {
		return performanceReport{}, err
	}

	return performanceReport{
		SchemaVersion:     smokeReportVersion,
		DescriptorCount:   toolCount,
		CardinalityBucket: cardinalityBucket(cardinality),
		ResolveCached:     resolveMetrics,
		LSFirstLimit1:     firstMetrics,
		LSContinuedLimit1: continuedMetrics,
		LSFirstLimit100:   limit100Metrics,
		CurrentSQLite:     sqliteProof,
	}, nil
}

func probeStratifiedPerformance(ctx context.Context, gatewayBin string) ([]stratifiedMetrics, sqliteTelemetryProof, cancellationObservation, error) {
	fixture, err := newStratifiedFixture()
	if err != nil {
		return nil, sqliteTelemetryProof{}, cancellationObservation{}, errors.New("stratified fixture setup failed")
	}
	defer os.RemoveAll(fixture.root)
	dbPath, cleanup, err := newPrivateSQLiteStore()
	if err != nil {
		return nil, sqliteTelemetryProof{}, cancellationObservation{}, err
	}
	defer cleanup()

	session, err := connectCandidateDefaultSQLite(ctx, gatewayBin, fixture.root, dbPath, io.Discard)
	if err != nil {
		return nil, sqliteTelemetryProof{}, cancellationObservation{}, err
	}
	sessionOpen := true
	defer func() {
		if sessionOpen {
			_ = session.Close()
		}
	}()
	if _, err := requireExactToolList(ctx, session); err != nil {
		return nil, sqliteTelemetryProof{}, cancellationObservation{}, err
	}
	tracker, err := newSQLiteBurstTracker(ctx, dbPath)
	if err != nil {
		return nil, sqliteTelemetryProof{}, cancellationObservation{}, err
	}

	report := make([]stratifiedMetrics, 0, len(fixture.directories))
	for _, directory := range fixture.directories {
		measured, measureErr := measureStratifiedDirectory(ctx, session, tracker, directory)
		if measureErr != nil {
			return nil, sqliteTelemetryProof{}, cancellationObservation{}, measureErr
		}
		report = append(report, measured)
	}
	if err := session.Close(); err != nil {
		return nil, sqliteTelemetryProof{}, cancellationObservation{}, errors.New("stratified candidate close failed")
	}
	sessionOpen = false
	sqliteProof, err := tracker.finalize()
	if err != nil {
		return nil, sqliteTelemetryProof{}, cancellationObservation{}, err
	}
	if sqliteProof.SetupToolCallRows != len(fixture.directories)*3 {
		return nil, sqliteTelemetryProof{}, cancellationObservation{}, errors.New("stratified SQLite setup count was invalid")
	}
	cancellation, err := observeBoundedCancellation(ctx, gatewayBin, fixture.root, fixture.directories[len(fixture.directories)-1])
	if err != nil {
		return nil, sqliteTelemetryProof{}, cancellationObservation{}, err
	}
	return report, sqliteProof, cancellation, nil
}

func measureStratifiedDirectory(ctx context.Context, session *sdk.ClientSession, tracker *sqliteBurstTracker, directory stratifiedDirectory) (stratifiedMetrics, error) {
	out := stratifiedMetrics{EntryCount: directory.entryCount}
	for _, limit := range []int{1, 100, 500} {
		first, continued, err := measureStratifiedLimit(ctx, session, tracker, directory, limit)
		if err != nil {
			return stratifiedMetrics{}, err
		}
		switch limit {
		case 1:
			out.FirstLimit1, out.ContinuedLimit1 = first, continued
		case 100:
			out.FirstLimit100, out.ContinuedLimit100 = first, continued
		case 500:
			out.FirstLimit500, out.ContinuedLimit500 = first, continued
		}
	}
	return out, nil
}

func measureStratifiedLimit(ctx context.Context, session *sdk.ClientSession, tracker *sqliteBurstTracker, directory stratifiedDirectory, limit int) (performanceMetrics, *performanceMetrics, error) {
	arguments := map[string]any{"path": directory.path, "limit": limit}
	issued, _, err := callMeasured[obsidian.LSOutput](ctx, session, obsidian.ToolLS, arguments)
	if err != nil || !validStratifiedFirstPage(issued, directory.entryCount, limit) {
		return performanceMetrics{}, nil, errors.New("stratified performance setup failed")
	}
	if err := tracker.recordOneSetupCall(); err != nil {
		return performanceMetrics{}, nil, err
	}

	firstOperation := func() (operationSample, error) {
		page, measured, callErr := callMeasured[obsidian.LSOutput](ctx, session, obsidian.ToolLS, arguments)
		if callErr != nil || !validStratifiedFirstPage(page, directory.entryCount, limit) {
			return operationSample{}, errors.New("stratified performance call failed")
		}
		return sampleFromLS(measured, page), nil
	}
	operations := []performanceOperation{firstOperation}

	var continuedOperation performanceOperation
	if issued.Coverage.Continuation == "cursor" {
		cursor := issued.Coverage.NextCursor
		lastIdentity := issued.Entries[len(issued.Entries)-1].Path
		continuedOperation = func() (operationSample, error) {
			page, measured, callErr := callMeasured[obsidian.LSOutput](ctx, session, obsidian.ToolLS, map[string]any{
				"path": directory.path, "limit": limit, "cursor": cursor,
			})
			if callErr != nil || !validStratifiedContinuedPage(page, lastIdentity, limit) {
				return operationSample{}, errors.New("stratified performance call failed")
			}
			return sampleFromLS(measured, page), nil
		}
		operations = append(operations, continuedOperation)
	}
	for warmup := 0; warmup < stratifiedWarmups; warmup++ {
		for _, operation := range operations {
			if _, err := operation(); err != nil {
				return performanceMetrics{}, nil, err
			}
		}
	}

	first, err := collectPerformanceMetrics(firstOperation, stratifiedSamples)
	if err != nil || !stratifiedMetricsPass(first, directory.entryCount) {
		return performanceMetrics{}, nil, errors.New("stratified performance gate failed")
	}
	if continuedOperation == nil {
		if err := tracker.recordMeasuredBurst((stratifiedWarmups + stratifiedSamples) * len(operations)); err != nil {
			return performanceMetrics{}, nil, err
		}
		return first, nil, nil
	}
	continued, err := collectPerformanceMetrics(continuedOperation, stratifiedSamples)
	if err != nil || !stratifiedMetricsPass(continued, directory.entryCount) {
		return performanceMetrics{}, nil, errors.New("stratified performance gate failed")
	}
	if err := tracker.recordMeasuredBurst((stratifiedWarmups + stratifiedSamples) * len(operations)); err != nil {
		return performanceMetrics{}, nil, err
	}
	return first, &continued, nil
}

func validStratifiedFirstPage(page obsidian.LSOutput, entryCount, limit int) bool {
	if !validPerformancePage(page) || len(page.Entries) == 0 || len(page.Entries) > limit {
		return false
	}
	if len(page.Entries) < entryCount {
		return page.Coverage.Continuation == "cursor"
	}
	return len(page.Entries) == entryCount && page.Coverage.Continuation == "complete"
}

func validStratifiedContinuedPage(page obsidian.LSOutput, lastIdentity string, limit int) bool {
	return validPerformancePage(page) && len(page.Entries) > 0 && len(page.Entries) <= limit && page.Entries[0].Path != lastIdentity
}

func stratifiedMetricsPass(metrics performanceMetrics, entryCount int) bool {
	return performanceMetricsWithin(metrics, stratifiedSamples, performanceP95LimitUS) &&
		metrics.MaxFilesScanned == uint64(entryCount) && metrics.MaxBytesScanned == 0
}

func observeBoundedCancellation(ctx context.Context, gatewayBin, root string, directory stratifiedDirectory) (cancellationObservation, error) {
	observation := cancellationObservation{
		EntryCount:           directory.entryCount,
		DeadlineMicroseconds: cancellationDelay.Microseconds(),
		BoundMicroseconds:    cancellationBound.Microseconds(),
	}
	telemetry, err := os.CreateTemp("", "personal-mcp-gateway-smoke-telemetry-*")
	if err != nil {
		return cancellationObservation{}, errors.New("stratified cancellation telemetry setup failed")
	}
	telemetryPath := telemetry.Name()
	defer os.Remove(telemetryPath)
	defer telemetry.Close()

	session, err := connectCandidateWithTelemetry(ctx, gatewayBin, root, telemetry)
	if err != nil {
		return cancellationObservation{}, err
	}
	defer session.Close()
	if _, err := requireExactToolList(ctx, session); err != nil {
		return cancellationObservation{}, err
	}

	callCtx, cancel := context.WithTimeout(ctx, cancellationDelay)
	started := time.Now()
	_, callErr := session.CallTool(callCtx, &sdk.CallToolParams{
		Name: obsidian.ToolLS,
		Arguments: map[string]any{
			"path":  directory.path,
			"limit": 500,
		},
	})
	observation.ClientReturnMicroseconds = time.Since(started).Microseconds()
	contextErr := callCtx.Err()
	cancel()
	clientCanceled := errors.Is(callErr, context.Canceled) || errors.Is(callErr, context.DeadlineExceeded) ||
		errors.Is(contextErr, context.Canceled) || errors.Is(contextErr, context.DeadlineExceeded)
	if !clientCanceled {
		return cancellationObservation{}, errors.New("stratified cancellation request was not canceled")
	}

	record, completion, err := waitForCancellationTelemetry(ctx, telemetryPath, started, cancellationBound)
	if err != nil {
		return cancellationObservation{}, err
	}
	observation.ServerCompletionMicroseconds = completion.Microseconds()
	observation.FilesScanned = record.Summary.Result.FilesScanned
	observation.ServerCompleted = true
	observation.PartialWork = observation.FilesScanned > 0 && observation.FilesScanned < uint64(directory.entryCount)
	observation.WithinBound = observation.ServerCompletionMicroseconds <= observation.BoundMicroseconds && observation.PartialWork
	if !observation.WithinBound {
		return cancellationObservation{}, errors.New("stratified cancellation gate failed")
	}

	followup, _, err := callMeasured[obsidian.LSOutput](ctx, session, obsidian.ToolLS, map[string]any{
		"path": directory.path, "limit": 1,
	})
	observation.FollowupSucceeded = err == nil && validStratifiedFirstPage(followup, directory.entryCount, 1)
	if !observation.FollowupSucceeded {
		return cancellationObservation{}, errors.New("stratified cancellation follow-up failed")
	}
	return observation, nil
}

func waitForCancellationTelemetry(ctx context.Context, telemetryPath string, started time.Time, bound time.Duration) (cancellationTelemetry, time.Duration, error) {
	deadline := started.Add(bound)
	for {
		record, found, err := readCancellationTelemetry(telemetryPath)
		if err != nil {
			return cancellationTelemetry{}, 0, errors.New("stratified cancellation telemetry was invalid")
		}
		if found {
			return record, time.Since(started), nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return cancellationTelemetry{}, 0, errors.New("stratified cancellation server completion was not observed")
		}
		wait := time.Millisecond
		if remaining < wait {
			wait = remaining
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return cancellationTelemetry{}, 0, errors.New("stratified cancellation observation was interrupted")
		case <-timer.C:
		}
	}
}

func readCancellationTelemetry(telemetryPath string) (cancellationTelemetry, bool, error) {
	data, err := os.ReadFile(telemetryPath)
	if err != nil {
		return cancellationTelemetry{}, false, err
	}
	lines := bytes.Split(data, []byte{'\n'})
	for index, line := range lines {
		if len(line) == 0 {
			continue
		}
		var record cancellationTelemetry
		if err := json.Unmarshal(line, &record); err != nil {
			if index == len(lines)-1 && len(data) > 0 && data[len(data)-1] != '\n' {
				continue
			}
			return cancellationTelemetry{}, false, err
		}
		if record.Event != "tool.call" || record.Tool != obsidian.ToolLS {
			continue
		}
		if record.Outcome != "tool_error" || (record.ErrorCode != "canceled" && record.ErrorCode != "timeout") ||
			(record.Summary.Result.StoppedBy != "canceled" && record.Summary.Result.StoppedBy != "timeout") {
			return cancellationTelemetry{}, false, errors.New("unexpected cancellation telemetry")
		}
		return record, true, nil
	}
	return cancellationTelemetry{}, false, nil
}

func probeSyntheticVault(ctx context.Context, gatewayBin string, report *smokeReport) error {
	fixture, err := newSyntheticFixture()
	if err != nil {
		return errors.New("synthetic fixture setup failed")
	}
	defer os.RemoveAll(fixture.root)
	inventory, err := inspectVaultAggregate(ctx, fixture.root)
	if err != nil {
		return err
	}
	report.SyntheticVault = inventory

	var telemetry bytes.Buffer
	process, err := connectCandidateProcessHandle(ctx, exec.Command(gatewayBin,
		"stdio", "--obsidian-root", fixture.root, "--telemetry", "stderr"), &telemetry)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			_ = process.session.Close()
		}
	}()
	tracker, err := startCandidateProcessTracker(ctx, process, systemResourceSampler{})
	if err != nil {
		return err
	}
	session := process.session

	resolved, err := callStructured[obsidian.ResolveOutput](ctx, session, obsidian.ToolResolve, map[string]any{
		"path": fixture.canonicalInput,
	}, report)
	if err != nil {
		return errors.New("synthetic resolve call failed")
	}
	if !resolved.OK || !resolved.Exists || resolved.Type != "file" || resolved.Path != fixture.canonicalPath {
		return errors.New("synthetic resolve did not return stored canonical identity")
	}
	report.SyntheticCanonicalResolve = true

	var (
		cursor       string
		firstEntry   string
		listedPaths  []string
		seen         = make(map[string]struct{}, len(fixture.expectedListings))
		pageCount    int
		secondMoved  bool
		maxPageCount = len(fixture.expectedListings) + 1
	)
	for {
		if pageCount >= maxPageCount {
			return errors.New("synthetic listing did not complete")
		}
		arguments := map[string]any{
			"path":  fixture.directoryPath,
			"limit": 1,
		}
		if cursor != "" {
			arguments["cursor"] = cursor
		}
		page, callErr := callStructured[obsidian.LSOutput](ctx, session, obsidian.ToolLS, arguments, report)
		if callErr != nil {
			return errors.New("synthetic ls call failed")
		}
		pageCount++
		if !page.OK || len(page.Entries) != 1 || !page.Coverage.ScopeComplete || page.Coverage.Consistency != "stable" {
			return errors.New("synthetic ls returned an invalid page")
		}
		entryPath := page.Entries[0].Path
		if _, duplicate := seen[entryPath]; duplicate {
			report.SyntheticNoDuplicates = false
			return errors.New("synthetic ls repeated an entry")
		}
		seen[entryPath] = struct{}{}
		listedPaths = append(listedPaths, entryPath)
		if pageCount == 1 {
			firstEntry = entryPath
		}
		if pageCount == 2 {
			secondMoved = entryPath != firstEntry
			if !secondMoved {
				return errors.New("synthetic second page did not advance")
			}
		}

		switch page.Coverage.Continuation {
		case "cursor":
			if page.Coverage.ResultComplete || !page.Truncated || page.Coverage.NextCursor == "" {
				return errors.New("synthetic partial page lacked continuation")
			}
			cursor = page.Coverage.NextCursor
		case "complete":
			if !page.Coverage.ResultComplete || page.Truncated || page.Coverage.NextCursor != "" {
				return errors.New("synthetic final page was incomplete")
			}
			cursor = ""
		default:
			return errors.New("synthetic listing required restart")
		}
		if cursor == "" {
			break
		}
	}

	if pageCount < 2 || !secondMoved || !reflect.DeepEqual(listedPaths, fixture.expectedListings) {
		return errors.New("synthetic listing did not match the complete reference")
	}
	report.SyntheticPageCount = pageCount
	report.SyntheticEntryCount = len(listedPaths)
	report.SyntheticSecondProgress = secondMoved
	report.SyntheticFullEquivalence = true

	cursors, err := probeSyntheticRetrieval(ctx, session, fixture, report)
	if err != nil {
		return err
	}
	report.SyntheticProcess, err = tracker.finish(ctx, process)
	if err != nil {
		return err
	}
	closed = true
	if err := verifySyntheticRetrievalTelemetry(telemetry.String(), fixture, cursors); err != nil {
		return err
	}
	report.SyntheticTelemetrySanitized = true
	return nil
}

func probeSyntheticRetrieval(ctx context.Context, session *sdk.ClientSession, fixture syntheticFixture, report *smokeReport) ([]string, error) {
	selected, err := callStructured[obsidian.ReadOutput](ctx, session, obsidian.ToolRead, map[string]any{
		"path":     fixture.canonicalPath,
		"selector": map[string]any{"kind": obsidian.SelectorHeading, "heading": "Summary"},
	}, report)
	if err != nil || !selected.OK || selected.Content == nil || *selected.Content != fixture.expectedContents[fixture.canonicalPath] ||
		selected.Coverage.Continuation != "complete" {
		return nil, errors.New("synthetic read selection failed")
	}
	report.SyntheticReadSelected = true

	zero := 0
	literal := false
	grep, err := callStructured[obsidian.GrepOutput](ctx, session, obsidian.ToolGrep, map[string]any{
		"pattern": "phase2-health-needle", "path": fixture.directoryPath, "regex": literal,
		"context_lines": zero, "limit": 10,
	}, report)
	if err != nil || !grep.OK || grep.Coverage.Continuation != "complete" || len(grep.Matches) != len(fixture.expectedListings) {
		return nil, errors.New("synthetic grep failed")
	}
	for index, match := range grep.Matches {
		if match.Path != fixture.expectedListings[index] || match.Occurrences != 1 || !strings.Contains(match.Text, "phase2-health-needle") {
			return nil, errors.New("synthetic grep ordering or evidence failed")
		}
	}
	report.SyntheticGrepMatchCount = len(grep.Matches)

	requests := make([]any, len(grep.Matches))
	for index, match := range grep.Matches {
		requests[index] = map[string]any{
			"path":      match.Path,
			"selector":  map[string]any{"kind": obsidian.SelectorContent, "start_line": 1},
			"max_bytes": 13,
		}
	}
	arguments := map[string]any{"requests": requests, "max_bytes": 24}
	accumulated := make([]strings.Builder, len(requests))
	cursors := []string{}
	for page := 0; page < 100; page++ {
		batch, callErr := callStructured[obsidian.ReadManyOutput](ctx, session, obsidian.ToolReadMany, arguments, report)
		if callErr != nil || !batch.OK || len(batch.Items) == 0 {
			return nil, errors.New("synthetic read_many call failed")
		}
		report.SyntheticReadManyPages++
		for _, item := range batch.Items {
			if item.Index < 0 || item.Index >= len(accumulated) || !item.OK || item.Content == nil {
				return nil, errors.New("synthetic read_many item failed")
			}
			accumulated[item.Index].WriteString(*item.Content)
		}
		switch batch.Coverage.Continuation {
		case "complete":
			if batch.Coverage.NextCursor != "" || batch.RemainingRequestCount != 0 {
				return nil, errors.New("synthetic read_many completion was invalid")
			}
		case "cursor":
			if batch.Coverage.NextCursor == "" || batch.RemainingRequestCount <= 0 {
				return nil, errors.New("synthetic read_many continuation was invalid")
			}
			cursors = append(cursors, batch.Coverage.NextCursor)
			arguments["cursor"] = batch.Coverage.NextCursor
			continue
		default:
			return nil, errors.New("synthetic read_many required restart")
		}
		break
	}
	if report.SyntheticReadManyPages < 2 || len(cursors) == 0 {
		return nil, errors.New("synthetic read_many did not prove continuation")
	}
	for index, expectedPath := range fixture.expectedListings {
		if accumulated[index].String() != fixture.expectedContents[expectedPath] {
			return nil, errors.New("synthetic read_many evidence was not equivalent")
		}
	}
	report.SyntheticReadManyContinued = true
	report.SyntheticRetrievalEquivalent = true
	return cursors, nil
}

func verifySyntheticRetrievalTelemetry(encoded string, fixture syntheticFixture, cursors []string) error {
	for _, forbidden := range append(append([]string{
		"phase2-health-needle",
		"alpha training evidence",
		fixture.canonicalInput,
		fixture.canonicalPath,
	}, fixture.expectedListings...), cursors...) {
		if strings.Contains(encoded, forbidden) {
			return errors.New("synthetic retrieval telemetry retained private evidence")
		}
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(encoded), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			return errors.New("synthetic retrieval telemetry was invalid")
		}
		tool, _ := record["tool"].(string)
		if record["event"] == "tool.call" && record["outcome"] == "ok" {
			seen[tool] = true
		}
	}
	for _, tool := range []string{obsidian.ToolRead, obsidian.ToolReadMany, obsidian.ToolGrep} {
		if !seen[tool] {
			return errors.New("synthetic retrieval telemetry lacked aggregate tool evidence")
		}
	}
	return nil
}

func connectCandidate(ctx context.Context, gatewayBin, root string) (*sdk.ClientSession, error) {
	return connectCandidateCommand(ctx, gatewayBin, root, "off", nil)
}

func connectCandidateWithTelemetry(ctx context.Context, gatewayBin, root string, stderr io.Writer) (*sdk.ClientSession, error) {
	return connectCandidateCommand(ctx, gatewayBin, root, "stderr", stderr)
}

func connectCandidateDefaultSQLite(ctx context.Context, gatewayBin, root, dbPath string, stderr io.Writer) (*sdk.ClientSession, error) {
	cmd := exec.Command(gatewayBin,
		"stdio",
		"--obsidian-root", root,
		"--telemetry-db", dbPath,
	)
	return connectCandidateProcess(ctx, cmd, stderr)
}

func connectCandidateCommand(ctx context.Context, gatewayBin, root, telemetry string, stderr io.Writer) (*sdk.ClientSession, error) {
	cmd := exec.Command(gatewayBin,
		"stdio",
		"--obsidian-root", root,
		"--telemetry", telemetry,
	)
	return connectCandidateProcess(ctx, cmd, stderr)
}

func connectCandidateProcess(ctx context.Context, cmd *exec.Cmd, stderr io.Writer) (*sdk.ClientSession, error) {
	process, err := connectCandidateProcessHandle(ctx, cmd, stderr)
	if err != nil {
		return nil, err
	}
	return process.session, nil
}

type candidateProcess struct {
	session *sdk.ClientSession
	command *exec.Cmd
}

func connectCandidateProcessHandle(ctx context.Context, cmd *exec.Cmd, stderr io.Writer) (*candidateProcess, error) {
	cmd.Stderr = stderr
	client := sdk.NewClient(&sdk.Implementation{Name: "local-release-smoke", Version: "v1"}, nil)
	session, err := client.Connect(ctx, &sdk.CommandTransport{
		Command:           cmd,
		TerminateDuration: 2 * time.Second,
	}, nil)
	if err != nil {
		return nil, errors.New("candidate MCP connection failed")
	}
	if cmd.Process == nil || cmd.Process.Pid <= 0 {
		_ = session.Close()
		return nil, errors.New("candidate process identity was unavailable")
	}
	return &candidateProcess{session: session, command: cmd}, nil
}

func newPrivateSQLiteStore() (string, func(), error) {
	dir, err := os.MkdirTemp("", "personal-mcp-gateway-smoke-sqlite-")
	if err != nil {
		return "", nil, errors.New("private SQLite setup failed")
	}
	return filepath.Join(dir, "telemetry.sqlite"), func() { _ = os.RemoveAll(dir) }, nil
}

func newSQLiteBurstTracker(ctx context.Context, dbPath string) (*sqliteBurstTracker, error) {
	snapshot, err := inspectSQLite(ctx, dbPath)
	if err != nil || snapshot.toolCallRows != 0 {
		return nil, errors.New("SQLite baseline was invalid")
	}
	return &sqliteBurstTracker{ctx: ctx, dbPath: dbPath, last: snapshot}, nil
}

func (t *sqliteBurstTracker) recordSetupCalls() error {
	next, err := inspectSQLite(t.ctx, t.dbPath)
	if err != nil {
		return err
	}
	delta := next.toolCallRows - t.last.toolCallRows
	if delta <= 0 {
		return errors.New("SQLite setup rows were missing")
	}
	t.setupToolCalls += delta
	t.last = next
	return nil
}

func (t *sqliteBurstTracker) recordOneSetupCall() error {
	next, err := inspectSQLite(t.ctx, t.dbPath)
	if err != nil {
		return err
	}
	delta := next.toolCallRows - t.last.toolCallRows
	if delta != 1 {
		return errors.New("SQLite setup row count was invalid")
	}
	t.setupToolCalls++
	t.last = next
	return nil
}

func (t *sqliteBurstTracker) recordMeasuredBurst(expected int) error {
	next, err := inspectSQLite(t.ctx, t.dbPath)
	if err != nil {
		return err
	}
	delta := next.toolCallRows - t.last.toolCallRows
	if delta != expected {
		return errors.New("SQLite measured row count was invalid")
	}
	t.expectedMeasured += expected
	t.measured += delta
	t.last = next
	return nil
}

func (t *sqliteBurstTracker) finalize() (sqliteTelemetryProof, error) {
	final, err := inspectSQLite(t.ctx, t.dbPath)
	if err != nil {
		return sqliteTelemetryProof{}, err
	}
	expectedTotal := t.setupToolCalls + t.expectedMeasured
	validated := t.measured == t.expectedMeasured && final.toolCallRows == expectedTotal &&
		final.parsedBodyRows == final.persistedRows && final.persistedRows >= final.toolCallRows
	if !validated {
		return sqliteTelemetryProof{}, errors.New("SQLite telemetry proof failed")
	}
	return sqliteTelemetryProof{
		SetupToolCallRows:            t.setupToolCalls,
		ExpectedMeasuredToolCallRows: t.expectedMeasured,
		MeasuredToolCallRows:         t.measured,
		TotalToolCallRows:            final.toolCallRows,
		PersistedRows:                final.persistedRows,
		ParsedBodyRows:               final.parsedBodyRows,
		Validated:                    true,
	}, nil
}

func inspectSQLite(ctx context.Context, dbPath string) (sqliteSnapshot, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return sqliteSnapshot{}, errors.New("SQLite telemetry readback failed")
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT event, body_json FROM audit_events ORDER BY id`)
	if err != nil {
		return sqliteSnapshot{}, errors.New("SQLite telemetry readback failed")
	}
	defer rows.Close()

	var snapshot sqliteSnapshot
	for rows.Next() {
		var event, bodyJSON string
		if err := rows.Scan(&event, &bodyJSON); err != nil {
			return sqliteSnapshot{}, errors.New("SQLite telemetry row was invalid")
		}
		snapshot.persistedRows++
		var body map[string]any
		if err := json.Unmarshal([]byte(bodyJSON), &body); err != nil || body["event"] != event {
			return sqliteSnapshot{}, errors.New("SQLite telemetry body was invalid")
		}
		snapshot.parsedBodyRows++
		if event == "tool.call" {
			snapshot.toolCallRows++
		}
	}
	if err := rows.Err(); err != nil {
		return sqliteSnapshot{}, errors.New("SQLite telemetry readback failed")
	}
	return snapshot, nil
}

func probeSQLiteDegradation(ctx context.Context, gatewayBin string) (sqliteDegradationProof, error) {
	root, err := os.MkdirTemp("", "personal-mcp-gateway-degradation-vault-")
	if err != nil {
		return sqliteDegradationProof{}, errors.New("SQLite degradation fixture setup failed")
	}
	defer os.RemoveAll(root)
	notePath := filepath.Join(root, "degradation.md")
	noteBody := []byte("# Degradation\nretrieval remains available while telemetry is degraded\n")
	if err := os.WriteFile(notePath, noteBody, 0o600); err != nil {
		return sqliteDegradationProof{}, errors.New("SQLite degradation fixture setup failed")
	}
	noteBefore, err := os.Lstat(notePath)
	if err != nil {
		return sqliteDegradationProof{}, errors.New("SQLite degradation fixture setup failed")
	}
	dbPath, cleanup, err := newPrivateSQLiteStore()
	if err != nil {
		return sqliteDegradationProof{}, err
	}
	defer cleanup()
	stderrPath := filepath.Join(filepath.Dir(dbPath), "candidate.stderr")
	stderr, err := os.OpenFile(stderrPath, os.O_CREATE|os.O_RDWR|os.O_EXCL, 0o600)
	if err != nil {
		return sqliteDegradationProof{}, errors.New("SQLite degradation observation setup failed")
	}
	defer stderr.Close()

	session, err := connectCandidateDefaultSQLite(ctx, gatewayBin, root, dbPath, stderr)
	if err != nil {
		return sqliteDegradationProof{}, err
	}
	defer session.Close()
	if _, err := requireExactToolList(ctx, session); err != nil {
		return sqliteDegradationProof{}, err
	}
	if err := dropSQLiteEventsTable(dbPath); err != nil {
		return sqliteDegradationProof{}, err
	}

	out, measured, err := callMeasured[obsidian.ReadOutput](ctx, session, obsidian.ToolRead, map[string]any{
		"path": "degradation.md", "max_bytes": 4,
	})
	if err != nil {
		return sqliteDegradationProof{}, errors.New("SQLite degradation tool call failed")
	}
	noteAfterBody, bodyErr := os.ReadFile(notePath)
	noteAfter, statErr := os.Lstat(notePath)
	noteUnchanged := bodyErr == nil && statErr == nil && bytes.Equal(noteAfterBody, noteBody) &&
		noteAfter.Mode() == noteBefore.Mode() && noteAfter.Size() == noteBefore.Size() && noteAfter.ModTime().Equal(noteBefore.ModTime())
	stderrBody, err := os.ReadFile(stderrPath)
	if err != nil {
		return sqliteDegradationProof{}, errors.New("SQLite degradation observation failed")
	}
	proof := sqliteDegradationProof{
		FailureInjected:     true,
		DegradationObserved: bytes.Contains(stderrBody, []byte("runtime warning: telemetry degraded")),
		ToolCallSucceeded: out.OK && out.Error == nil && out.Content != nil && *out.Content == "# De" && out.Truncated &&
			out.Coverage.Continuation == "cursor" && out.Coverage.NextCursor != "" && noteUnchanged,
		SDKResultBytes:       measured.sdkResultBytes,
		LatencyMicroseconds:  measured.latency.Microseconds(),
		BoundMicroseconds:    performanceP95LimitUS,
		WithinResponseBudget: measured.sdkResultBytes > 0 && measured.sdkResultBytes <= obsidian.MaxSDKResultBytes,
		WithinLatencyBudget:  measured.latency.Microseconds() <= performanceP95LimitUS,
	}
	if !proof.DegradationObserved || !proof.ToolCallSucceeded || !proof.WithinResponseBudget || !proof.WithinLatencyBudget {
		return sqliteDegradationProof{}, errors.New("SQLite degradation gate failed")
	}
	return proof, nil
}

func dropSQLiteEventsTable(dbPath string) error {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return errors.New("SQLite degradation injection failed")
	}
	defer db.Close()
	if _, err := db.Exec(`DROP TABLE audit_events`); err != nil {
		return errors.New("SQLite degradation injection failed")
	}
	return nil
}

func requireExactToolList(ctx context.Context, session *sdk.ClientSession) (int, error) {
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		return 0, errors.New("candidate tool list failed")
	}
	if !exactCandidateToolGrammar(listed.Tools) {
		return 0, errors.New("candidate tool grammar did not match the exact five-tool contract")
	}
	names := make([]string, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	want := []string{obsidian.ToolGrep, obsidian.ToolLS, obsidian.ToolRead, obsidian.ToolReadMany, obsidian.ToolResolve}
	if !reflect.DeepEqual(names, want) {
		return 0, errors.New("candidate tool list was not exactly grep, ls, read, read_many, and resolve")
	}
	return len(names), nil
}

func callStructured[Out any](ctx context.Context, session *sdk.ClientSession, name string, arguments map[string]any, report *smokeReport) (Out, error) {
	out, measured, err := callMeasured[Out](ctx, session, name, arguments)
	if err != nil {
		return out, err
	}
	report.SDKResultCount++
	report.ToolCalls.add(name)
	if measured.sdkResultBytes > report.MaxSDKResultBytes {
		report.MaxSDKResultBytes = measured.sdkResultBytes
	}
	if measured.structuredBytes > report.MaxStructuredResultBytes {
		report.MaxStructuredResultBytes = measured.structuredBytes
	}
	if latency := measured.latency.Microseconds(); latency > report.MaxClientLatencyMicroseconds {
		report.MaxClientLatencyMicroseconds = latency
	}
	coverage := functionalCoverage(out)
	report.TotalFilesScanned += coverage.FilesScanned
	report.TotalBytesScanned += coverage.BytesScanned
	report.TotalSourceEntriesValidated += coverage.SourceEntriesValidated
	return out, nil
}

func callMeasured[Out any](ctx context.Context, session *sdk.ClientSession, name string, arguments map[string]any) (Out, callMeasurement, error) {
	var out Out
	started := time.Now()
	result, err := session.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: arguments})
	latency := time.Since(started)
	if err != nil || result == nil || result.IsError {
		return out, callMeasurement{}, errors.New("candidate tool call failed")
	}
	encodedResult, err := json.Marshal(result)
	if err != nil || len(encodedResult) > obsidian.MaxSDKResultBytes {
		return out, callMeasurement{}, errors.New("candidate SDK result exceeded maximum size")
	}
	structured, err := json.Marshal(result.StructuredContent)
	if err != nil || json.Unmarshal(structured, &out) != nil {
		return out, callMeasurement{}, errors.New("candidate structured result was invalid")
	}
	return out, callMeasurement{
		latency:         latency,
		sdkResultBytes:  len(encodedResult),
		structuredBytes: len(structured),
	}, nil
}

func validPerformancePage(page obsidian.LSOutput) bool {
	return page.OK && page.Coverage.ScopeComplete && page.Coverage.Consistency == "stable" &&
		(page.Coverage.Continuation == "complete" ||
			(page.Coverage.Continuation == "cursor" && page.Coverage.NextCursor != ""))
}

func validFirstPerformancePage(page obsidian.LSOutput) bool {
	return validPerformancePage(page) && len(page.Entries) == 1 &&
		page.Coverage.Continuation == "cursor" && page.Coverage.NextCursor != ""
}

func validContinuedPerformancePage(page obsidian.LSOutput, firstIdentity string) bool {
	return validPerformancePage(page) && len(page.Entries) == 1 && page.Entries[0].Path != firstIdentity
}

func sampleFromLS(measured callMeasurement, page obsidian.LSOutput) operationSample {
	return operationSample{
		measurement:            measured,
		filesScanned:           page.Coverage.FilesScanned,
		bytesScanned:           page.Coverage.BytesScanned,
		sourceEntriesValidated: page.Coverage.SourceEntriesValidated,
	}
}

func collectPerformanceMetrics(operation performanceOperation, samples int) (performanceMetrics, error) {
	latencies := make([]int64, 0, samples)
	metrics := performanceMetrics{N: samples}
	for sampleIndex := 0; sampleIndex < samples; sampleIndex++ {
		sample, err := operation()
		if err != nil {
			return performanceMetrics{}, err
		}
		microseconds := sample.measurement.latency.Microseconds()
		latencies = append(latencies, microseconds)
		if microseconds > metrics.MaxMicroseconds {
			metrics.MaxMicroseconds = microseconds
		}
		if sample.measurement.sdkResultBytes > metrics.MaxSDKResultBytes {
			metrics.MaxSDKResultBytes = sample.measurement.sdkResultBytes
		}
		if sample.measurement.structuredBytes > metrics.MaxStructuredBytes {
			metrics.MaxStructuredBytes = sample.measurement.structuredBytes
		}
		if sample.filesScanned > metrics.MaxFilesScanned {
			metrics.MaxFilesScanned = sample.filesScanned
		}
		if sample.bytesScanned > metrics.MaxBytesScanned {
			metrics.MaxBytesScanned = sample.bytesScanned
		}
		if sample.sourceEntriesValidated > metrics.MaxSourceEntriesValidated {
			metrics.MaxSourceEntriesValidated = sample.sourceEntriesValidated
		}
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	metrics.P50Microseconds = nearestRank(latencies, 50)
	metrics.P95Microseconds = nearestRank(latencies, 95)
	return metrics, nil
}

func nearestRank(sorted []int64, percentile int) int64 {
	if len(sorted) == 0 {
		return 0
	}
	index := (len(sorted)*percentile+99)/100 - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}

func cardinalityBucket(cardinality int) string {
	switch {
	case cardinality <= 10:
		return "2_10"
	case cardinality <= 100:
		return "11_100"
	case cardinality <= 1000:
		return "101_1000"
	default:
		return "1001_plus"
	}
}

func newSyntheticFixture() (syntheticFixture, error) {
	root, err := os.MkdirTemp("", "personal-mcp-gateway-smoke-")
	if err != nil {
		return syntheticFixture{}, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(root)
		}
	}()

	directory := "Caf\u00e9"
	storedNames := []string{"Alpha.md", "Beta.md", "R\u00e9sum\u00e9.md"}
	contents := map[string]string{
		"Alpha.md":            "# Workout\nphase2-health-needle alpha\n" + strings.Repeat("alpha training evidence ", 8) + "\n",
		"Beta.md":             "# Recovery\nphase2-health-needle beta\nrecovery evidence\n",
		"R\u00e9sum\u00e9.md": "# Summary\nphase2-health-needle summary\nsummary evidence\n",
	}
	if err := os.Mkdir(pathOnHost(root, directory), 0o700); err != nil {
		return syntheticFixture{}, err
	}
	for _, name := range storedNames {
		if err := os.WriteFile(pathOnHost(root, directory, name), []byte(contents[name]), 0o600); err != nil {
			return syntheticFixture{}, err
		}
	}
	if err := os.WriteFile(pathOnHost(root, directory, ".hidden.md"), []byte("phase2-health-needle hidden\n"), 0o600); err != nil {
		return syntheticFixture{}, err
	}

	expected := make([]string, 0, len(storedNames))
	for _, name := range storedNames {
		canonical := path.Join(directory, name)
		expected = append(expected, canonical)
		contents[canonical] = contents[name]
		delete(contents, name)
	}
	sort.Strings(expected)
	cleanup = false
	return syntheticFixture{
		root:             root,
		canonicalInput:   "cafe\u0301/re\u0301sume\u0301.md",
		canonicalPath:    path.Join(directory, "R\u00e9sum\u00e9.md"),
		directoryPath:    directory,
		expectedListings: expected,
		expectedContents: contents,
	}, nil
}

func newStratifiedFixture() (stratifiedFixture, error) {
	root, err := os.MkdirTemp("", "personal-mcp-gateway-performance-")
	if err != nil {
		return stratifiedFixture{}, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(root)
		}
	}()

	directories := make([]stratifiedDirectory, 0, len(stratifiedEntryCounts))
	for stratumIndex, entryCount := range stratifiedEntryCounts {
		directoryName := fmt.Sprintf("fixture-%02d", stratumIndex)
		directoryRoot := pathOnHost(root, directoryName)
		if err := os.Mkdir(directoryRoot, 0o700); err != nil {
			return stratifiedFixture{}, err
		}
		for entryIndex := 0; entryIndex < entryCount; entryIndex++ {
			name := fmt.Sprintf("entry-%05d.md", entryIndex)
			if err := os.WriteFile(pathOnHost(directoryRoot, name), nil, 0o600); err != nil {
				return stratifiedFixture{}, err
			}
		}
		directories = append(directories, stratifiedDirectory{path: directoryName, entryCount: entryCount})
	}
	cleanup = false
	return stratifiedFixture{root: root, directories: directories}, nil
}

func pathOnHost(root string, segments ...string) string {
	parts := append([]string{root}, segments...)
	return filepath.Join(parts...)
}
