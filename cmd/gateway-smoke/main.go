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
	"time"

	_ "github.com/mattn/go-sqlite3"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"personal-mcp-gateway/internal/tools/obsidian"
)

const (
	smokeTimeout          = 10 * time.Second
	performanceTimeout    = 90 * time.Second
	performanceWarmups    = 10
	performanceSamples    = 100
	stratifiedWarmups     = 2
	stratifiedSamples     = 20
	performanceP95LimitUS = 100_000
	cancellationDelay     = 2 * time.Millisecond
	cancellationBound     = 100 * time.Millisecond
	defaultSuccessMessage = "gateway smoke passed: resolve(.) returned an existing directory"
	smokeReportVersion    = 1
)

var stratifiedEntryCounts = [...]int{1, 100, 1_000, 10_000}

type smokeReport struct {
	SchemaVersion             int  `json:"schema_version"`
	Passed                    bool `json:"passed"`
	ToolCount                 int  `json:"tool_count"`
	SDKResultCount            int  `json:"sdk_result_count"`
	MaxSDKResultBytes         int  `json:"max_sdk_result_bytes"`
	CurrentResolveExistingDir bool `json:"current_resolve_existing_directory"`
	SyntheticCanonicalResolve bool `json:"synthetic_canonical_resolve"`
	SyntheticPageCount        int  `json:"synthetic_page_count"`
	SyntheticEntryCount       int  `json:"synthetic_entry_count"`
	SyntheticSecondProgress   bool `json:"synthetic_second_page_progress"`
	SyntheticNoDuplicates     bool `json:"synthetic_no_duplicates"`
	SyntheticFullEquivalence  bool `json:"synthetic_full_equivalence"`
}

type performanceReport struct {
	SchemaVersion     int                     `json:"schema_version"`
	Passed            bool                    `json:"passed"`
	DescriptorCount   int                     `json:"descriptor_count"`
	CardinalityBucket string                  `json:"cardinality_bucket"`
	ResolveCached     performanceMetrics      `json:"resolve_cached"`
	LSFirstLimit1     performanceMetrics      `json:"ls_first_limit_1"`
	LSContinuedLimit1 performanceMetrics      `json:"ls_continued_limit_1"`
	LSFirstLimit100   performanceMetrics      `json:"ls_first_limit_100"`
	Stratified        []stratifiedMetrics     `json:"stratified"`
	CurrentSQLite     sqliteTelemetryProof    `json:"current_sqlite"`
	StratifiedSQLite  sqliteTelemetryProof    `json:"stratified_sqlite"`
	SQLiteDegradation sqliteDegradationProof  `json:"sqlite_degradation"`
	Cancellation      cancellationObservation `json:"cancellation"`
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
	N                  int    `json:"n"`
	P50Microseconds    int64  `json:"p50_microseconds"`
	P95Microseconds    int64  `json:"p95_microseconds"`
	MaxMicroseconds    int64  `json:"max_microseconds"`
	MaxSDKResultBytes  int    `json:"max_sdk_result_bytes"`
	MaxStructuredBytes int    `json:"max_structured_bytes"`
	MaxFilesScanned    uint64 `json:"max_files_scanned"`
	MaxBytesScanned    uint64 `json:"max_bytes_scanned"`
}

type callMeasurement struct {
	latency         time.Duration
	sdkResultBytes  int
	structuredBytes int
}

type operationSample struct {
	measurement  callMeasurement
	filesScanned uint64
	bytesScanned uint64
}

type performanceOperation func() (operationSample, error)

type syntheticFixture struct {
	root             string
	canonicalInput   string
	canonicalPath    string
	directoryPath    string
	expectedListings []string
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
	flags := flag.NewFlagSet("gateway-smoke", flag.ContinueOnError)
	flags.SetOutput(stderr)
	gatewayBin := flags.String("gateway-bin", "", "gateway executable to probe")
	obsidianRoot := flags.String("obsidian-root", "", "vault root used for the probe")
	reportJSON := flags.Bool("report-json", false, "emit one sanitized aggregate JSON report")
	performanceJSON := flags.Bool("performance-json", false, "emit one sanitized current-vault and stratified candidate performance report")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *gatewayBin == "" || *obsidianRoot == "" {
		return errors.New("--gateway-bin and --obsidian-root are required")
	}
	if *reportJSON && *performanceJSON {
		return errors.New("--report-json and --performance-json are mutually exclusive")
	}

	timeout := smokeTimeout
	if *performanceJSON {
		timeout = performanceTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if *performanceJSON {
		report, err := probeCurrentVaultPerformance(ctx, *gatewayBin, *obsidianRoot)
		if err != nil {
			return err
		}
		report.Stratified, report.StratifiedSQLite, report.Cancellation, err = probeStratifiedPerformance(ctx, *gatewayBin)
		if err != nil {
			return err
		}
		report.SQLiteDegradation, err = probeSQLiteDegradation(ctx, *gatewayBin)
		if err != nil {
			return err
		}
		if err := json.NewEncoder(stdout).Encode(report); err != nil {
			return errors.New("encode performance report failed")
		}
		return nil
	}

	report := smokeReport{SchemaVersion: smokeReportVersion, SyntheticNoDuplicates: true}
	if err := probeCurrentVault(ctx, *gatewayBin, *obsidianRoot, &report); err != nil {
		return err
	}
	if err := probeSyntheticVault(ctx, *gatewayBin, &report); err != nil {
		return err
	}
	report.Passed = true

	if *reportJSON {
		if err := json.NewEncoder(stdout).Encode(report); err != nil {
			return errors.New("encode smoke report failed")
		}
		return nil
	}
	_, err := fmt.Fprintln(stdout, defaultSuccessMessage)
	return err
}

func probeCurrentVault(ctx context.Context, gatewayBin, root string, report *smokeReport) error {
	session, err := connectCandidate(ctx, gatewayBin, root)
	if err != nil {
		return err
	}
	defer session.Close()

	toolCount, err := requireExactToolList(ctx, session)
	if err != nil {
		return err
	}
	report.ToolCount = toolCount

	out, err := callStructured[obsidian.ResolveOutput](ctx, session, obsidian.ToolResolve, map[string]any{"path": "."}, report)
	if err != nil {
		return errors.New("candidate resolve call failed")
	}
	if !out.OK || !out.Exists || out.Type != "directory" {
		return errors.New("resolve did not return an existing directory")
	}
	report.CurrentResolveExistingDir = true
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
	representative, cardinality, err := selectRepresentativeDirectory(ctx, session)
	if err != nil {
		return performanceReport{}, err
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
		Passed:            true,
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
	return metrics.N == stratifiedSamples && metrics.P95Microseconds <= performanceP95LimitUS &&
		metrics.MaxSDKResultBytes > 0 && metrics.MaxSDKResultBytes <= obsidian.MaxSDKResultBytes &&
		metrics.MaxStructuredBytes > 0 && metrics.MaxFilesScanned == uint64(entryCount) && metrics.MaxBytesScanned == 0
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

	session, err := connectCandidate(ctx, gatewayBin, fixture.root)
	if err != nil {
		return err
	}
	defer session.Close()

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
	cmd.Stderr = stderr
	client := sdk.NewClient(&sdk.Implementation{Name: "local-release-smoke", Version: "v1"}, nil)
	session, err := client.Connect(ctx, &sdk.CommandTransport{
		Command:           cmd,
		TerminateDuration: 2 * time.Second,
	}, nil)
	if err != nil {
		return nil, errors.New("candidate MCP connection failed")
	}
	return session, nil
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

	out, measured, err := callMeasured[obsidian.ResolveOutput](ctx, session, obsidian.ToolResolve, map[string]any{"path": "."})
	if err != nil {
		return sqliteDegradationProof{}, errors.New("SQLite degradation tool call failed")
	}
	stderrBody, err := os.ReadFile(stderrPath)
	if err != nil {
		return sqliteDegradationProof{}, errors.New("SQLite degradation observation failed")
	}
	proof := sqliteDegradationProof{
		FailureInjected:      true,
		DegradationObserved:  bytes.Contains(stderrBody, []byte("runtime warning: telemetry degraded")),
		ToolCallSucceeded:    out.OK && out.Exists && out.Type == "directory",
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
	names := make([]string, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	want := []string{obsidian.ToolLS, obsidian.ToolResolve}
	if !reflect.DeepEqual(names, want) {
		return 0, errors.New("candidate tool list was not exactly ls and resolve")
	}
	return len(names), nil
}

func callStructured[Out any](ctx context.Context, session *sdk.ClientSession, name string, arguments map[string]any, report *smokeReport) (Out, error) {
	out, measured, err := callMeasured[Out](ctx, session, name, arguments)
	if err != nil {
		return out, err
	}
	report.SDKResultCount++
	if measured.sdkResultBytes > report.MaxSDKResultBytes {
		report.MaxSDKResultBytes = measured.sdkResultBytes
	}
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

func selectRepresentativeDirectory(ctx context.Context, session *sdk.ClientSession) (string, int, error) {
	var cursor string
	seenCursors := make(map[string]struct{})
	for {
		arguments := map[string]any{"path": ".", "limit": 100}
		if cursor != "" {
			arguments["cursor"] = cursor
		}
		page, _, err := callMeasured[obsidian.LSOutput](ctx, session, obsidian.ToolLS, arguments)
		if err != nil || !validPerformancePage(page) {
			return "", 0, errors.New("current-vault representative selection failed")
		}
		for _, entry := range page.Entries {
			if entry.Type != "directory" {
				continue
			}
			candidate, _, candidateErr := callMeasured[obsidian.LSOutput](ctx, session, obsidian.ToolLS, map[string]any{
				"path":  entry.Path,
				"limit": 2,
			})
			if candidateErr != nil {
				return "", 0, errors.New("current-vault representative selection failed")
			}
			if len(candidate.Entries) < 2 || !validPerformancePage(candidate) {
				continue
			}
			cardinality, countErr := countDirectoryCardinality(ctx, session, entry.Path)
			if countErr != nil {
				return "", 0, countErr
			}
			return entry.Path, cardinality, nil
		}

		switch page.Coverage.Continuation {
		case "complete":
			return "", 0, errors.New("current vault has no resumable directory")
		case "cursor":
			cursor = page.Coverage.NextCursor
			if cursor == "" {
				return "", 0, errors.New("current-vault representative selection failed")
			}
			if _, duplicate := seenCursors[cursor]; duplicate {
				return "", 0, errors.New("current-vault representative selection failed")
			}
			seenCursors[cursor] = struct{}{}
		default:
			return "", 0, errors.New("current-vault representative selection failed")
		}
	}
}

func countDirectoryCardinality(ctx context.Context, session *sdk.ClientSession, directory string) (int, error) {
	const cardinalityCeiling = 1001
	var (
		cursor string
		count  int
	)
	for {
		arguments := map[string]any{"path": directory, "limit": 100}
		if cursor != "" {
			arguments["cursor"] = cursor
		}
		page, _, err := callMeasured[obsidian.LSOutput](ctx, session, obsidian.ToolLS, arguments)
		if err != nil || !validPerformancePage(page) {
			return 0, errors.New("current-vault cardinality measurement failed")
		}
		count += len(page.Entries)
		if count >= cardinalityCeiling {
			return cardinalityCeiling, nil
		}
		switch page.Coverage.Continuation {
		case "complete":
			return count, nil
		case "cursor":
			cursor = page.Coverage.NextCursor
			if cursor == "" {
				return 0, errors.New("current-vault cardinality measurement failed")
			}
		default:
			return 0, errors.New("current-vault cardinality measurement failed")
		}
	}
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
		measurement:  measured,
		filesScanned: page.Coverage.FilesScanned,
		bytesScanned: page.Coverage.BytesScanned,
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
	if err := os.Mkdir(pathOnHost(root, directory), 0o700); err != nil {
		return syntheticFixture{}, err
	}
	for _, name := range storedNames {
		if err := os.WriteFile(pathOnHost(root, directory, name), []byte("synthetic\n"), 0o600); err != nil {
			return syntheticFixture{}, err
		}
	}
	if err := os.WriteFile(pathOnHost(root, directory, ".hidden.md"), []byte("synthetic\n"), 0o600); err != nil {
		return syntheticFixture{}, err
	}

	expected := make([]string, 0, len(storedNames))
	for _, name := range storedNames {
		expected = append(expected, path.Join(directory, name))
	}
	sort.Strings(expected)
	cleanup = false
	return syntheticFixture{
		root:             root,
		canonicalInput:   "cafe\u0301/re\u0301sume\u0301.md",
		canonicalPath:    path.Join(directory, "R\u00e9sum\u00e9.md"),
		directoryPath:    directory,
		expectedListings: expected,
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
