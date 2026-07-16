package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"personal-mcp-gateway/internal/resourceprobe"
	"personal-mcp-gateway/internal/tools/obsidian"
)

const (
	resourceReportSchema              = "personal-mcp-gateway.resource.v5"
	resourceReportVersion             = 5
	resourceColdProcesses             = 10
	resourceBatchCount                = 3
	resourceBatchCalls                = 100
	resourceCallsPerToolPerBatch      = 20
	resourceBoundaryCalls             = 12
	resourceMeasuredCalls             = resourceBatchCount*resourceBatchCalls + resourceBoundaryCalls
	resourceHeapAllocGrowthLimitBytes = uint64(256 * 1024)
	resourceRSSGrowthLimitBytes       = int64(8 * 1024 * 1024)
	resourceRSSLimitBytes             = int64(64 * 1024 * 1024)
	resourceCallTimeLimit             = 2 * time.Second
	resourceStabilize5                = 5 * time.Second
	resourceStabilize30               = 30 * time.Second
	resourceIdleDuration              = 60 * time.Second
	resourceControlTime               = 5 * time.Second
	resourceAckMaxBytes               = 128
	resourceGrepZeroWidthPattern      = "a*"
)

type resourceReport struct {
	ReportKind                         string                  `json:"report_kind"`
	ReportSchema                       string                  `json:"report_schema"`
	SchemaVersion                      int                     `json:"schema_version"`
	Passed                             bool                    `json:"passed"`
	CandidateCommit                    string                  `json:"candidate_commit"`
	CandidateSHA256                    string                  `json:"candidate_sha256"`
	DependencySHA256                   string                  `json:"dependency_sha256"`
	DescriptorCount                    int                     `json:"descriptor_count"`
	CandidateRuntime                   candidateRuntimeProfile `json:"candidate_runtime"`
	Machine                            machineProfile          `json:"machine"`
	Vault                              vaultAggregateProfile   `json:"vault"`
	Fixture                            resourceVaultReport     `json:"fixture"`
	Process                            candidateProcessProfile `json:"process"`
	Workload                           resourceWorkloadReport  `json:"workload"`
	Boundaries                         resourceBoundaryReport  `json:"boundaries"`
	Cold                               coldResourceReport      `json:"cold"`
	Baseline                           resourceBaselineReport  `json:"baseline"`
	HighWaterRSSBytes                  int64                   `json:"high_water_rss_bytes"`
	HighWaterRSSDeltaBytes             int64                   `json:"high_water_rss_delta_bytes"`
	HighWaterWithinBound               bool                    `json:"high_water_within_bound"`
	MaxHeapAllocGrowthBytes            uint64                  `json:"max_heap_alloc_growth_bytes"`
	HeapAllocGrowthWithinBound         bool                    `json:"heap_alloc_growth_within_bound"`
	MaxRSSAfter30SecondsGrowthBytes    int64                   `json:"max_rss_after_30_seconds_growth_bytes"`
	RSSAfter30SecondsGrowthWithinBound bool                    `json:"rss_after_30_seconds_growth_within_bound"`
	GCAcknowledgementCount             int                     `json:"gc_acknowledgement_count"`
	AllFDsRecovered                    bool                    `json:"all_fds_recovered"`
	Batches                            []resourceBatchReport   `json:"batches"`
	Idle                               idleResourceReport      `json:"idle"`
}

type resourceVaultReport struct {
	GeneratedMarkdownFiles int   `json:"generated_markdown_files"`
	GeneratedBytes         int64 `json:"generated_bytes"`
	InventoryMarkdownFiles int   `json:"inventory_markdown_files"`
	InventoryBytes         int64 `json:"inventory_bytes"`
	InventoryComplete      bool  `json:"inventory_complete"`
	InventoryReconciled    bool  `json:"inventory_reconciled"`
}

type resourceToolCallCounts struct {
	Resolve  int `json:"resolve"`
	LS       int `json:"ls"`
	Read     int `json:"read"`
	ReadMany int `json:"read_many"`
	Grep     int `json:"grep"`
}

func (c resourceToolCallCounts) total() int {
	return c.Resolve + c.LS + c.Read + c.ReadMany + c.Grep
}

type resourceWorkloadReport struct {
	BatchCount                   int                    `json:"batch_count"`
	CallsPerBatch                int                    `json:"calls_per_batch"`
	CallsPerToolPerBatch         int                    `json:"calls_per_tool_per_batch"`
	MixedCallCount               int                    `json:"mixed_call_count"`
	BoundaryCallCount            int                    `json:"boundary_call_count"`
	MeasuredCallCount            int                    `json:"measured_call_count"`
	ToolCalls                    resourceToolCallCounts `json:"tool_calls"`
	MaxClientLatencyMicroseconds int64                  `json:"max_client_latency_microseconds"`
	MaxSDKResultBytes            int                    `json:"max_sdk_result_bytes"`
	MaxStructuredBytes           int                    `json:"max_structured_bytes"`
	MaxFilesScanned              uint64                 `json:"max_files_scanned"`
	MaxBytesScanned              uint64                 `json:"max_bytes_scanned"`
	MaxSourceEntriesValidated    uint64                 `json:"max_source_entries_validated"`
	EveryCallWithinTwoSeconds    bool                   `json:"every_call_within_two_seconds"`
	EverySDKResultWithin64KiB    bool                   `json:"every_sdk_result_within_64kib"`
}

type resourceBoundaryReport struct {
	CallCount                     int    `json:"call_count"`
	BatchNumber                   int    `json:"batch_number"`
	RanAfterBaseline              bool   `json:"ran_after_baseline"`
	RanBeforeBlockingGC           bool   `json:"ran_before_blocking_gc"`
	Near8MiBStructuralAccepted    bool   `json:"near_8mib_structural_accepted"`
	Dense50000DecoyRejected       bool   `json:"dense_50000_decoy_rejected"`
	Dense50000BlockAccepted       bool   `json:"dense_50000_block_accepted"`
	Over8MiBErrorCode             string `json:"over_8mib_error_code"`
	Over50000LinesErrorCode       string `json:"over_50000_lines_error_code"`
	GrepExactMatchingErrorCode    string `json:"grep_exact_matching_error_code"`
	GrepExactNonmatchingAccepted  bool   `json:"grep_exact_nonmatching_accepted"`
	GrepExactContextErrorCode     string `json:"grep_exact_context_error_code"`
	GrepExactUnicodeErrorCode     string `json:"grep_exact_unicode_error_code"`
	GrepExactZeroWidthErrorCode   string `json:"grep_exact_zero_width_error_code"`
	GrepExactInvalidUTF8ErrorCode string `json:"grep_exact_invalid_utf8_error_code"`
	GrepOver1MiBErrorCode         string `json:"grep_over_1mib_error_code"`
	EveryCallWithinTwoSeconds     bool   `json:"every_call_within_two_seconds"`
	EverySDKResultWithin64KiB     bool   `json:"every_sdk_result_within_64kib"`
}

type coldResourceReport struct {
	FreshProcessCount         int    `json:"fresh_process_count"`
	StartupP50Microseconds    int64  `json:"startup_p50_microseconds"`
	StartupP95Microseconds    int64  `json:"startup_p95_microseconds"`
	StartupMaxMicroseconds    int64  `json:"startup_max_microseconds"`
	FirstCallP50Microseconds  int64  `json:"first_call_p50_microseconds"`
	FirstCallP95Microseconds  int64  `json:"first_call_p95_microseconds"`
	FirstCallMaxMicroseconds  int64  `json:"first_call_max_microseconds"`
	ProcessCPUP50Microseconds int64  `json:"process_cpu_p50_microseconds"`
	ProcessCPUP95Microseconds int64  `json:"process_cpu_p95_microseconds"`
	ProcessCPUMaxMicroseconds int64  `json:"process_cpu_max_microseconds"`
	MaxHighWaterRSSBytes      int64  `json:"max_high_water_rss_bytes"`
	MaxSDKResultBytes         int    `json:"max_sdk_result_bytes"`
	MaxStructuredBytes        int    `json:"max_structured_bytes"`
	MaxFilesScanned           uint64 `json:"max_files_scanned"`
	MaxFDCount                int    `json:"max_fd_count"`
}

type resourceMemoryReport struct {
	HeapAllocBytes    uint64 `json:"heap_alloc_bytes"`
	HeapInuseBytes    uint64 `json:"heap_inuse_bytes"`
	HeapObjects       uint64 `json:"heap_objects"`
	HeapReleasedBytes uint64 `json:"heap_released_bytes"`
	HeapSysBytes      uint64 `json:"heap_sys_bytes"`
}

type resourceBaselineReport struct {
	MeasuredCallCount        int                  `json:"measured_call_count"`
	CPUTimeMicroseconds      int64                `json:"cpu_time_microseconds"`
	Memory                   resourceMemoryReport `json:"memory"`
	RSSImmediateBytes        int64                `json:"rss_immediate_bytes"`
	RSSAfter5SecondsBytes    int64                `json:"rss_after_5_seconds_bytes"`
	RSSAfter30SecondsBytes   int64                `json:"rss_after_30_seconds_bytes"`
	FDImmediateCount         int                  `json:"fd_immediate_count"`
	FDAfter5SecondsCount     int                  `json:"fd_after_5_seconds_count"`
	FDAfter30SecondsCount    int                  `json:"fd_after_30_seconds_count"`
	FDRecoveredAtEverySample bool                 `json:"fd_recovered_at_every_sample"`
	GCAcknowledged           bool                 `json:"gc_acknowledged"`
}

type resourceBatchReport struct {
	CallCount                    int                    `json:"call_count"`
	BoundaryCallCount            int                    `json:"boundary_call_count"`
	ToolCalls                    resourceToolCallCounts `json:"tool_calls"`
	MaxClientLatencyMicroseconds int64                  `json:"max_client_latency_microseconds"`
	MaxSDKResultBytes            int                    `json:"max_sdk_result_bytes"`
	MaxStructuredBytes           int                    `json:"max_structured_bytes"`
	MaxFilesScanned              uint64                 `json:"max_files_scanned"`
	MaxBytesScanned              uint64                 `json:"max_bytes_scanned"`
	MaxSourceEntriesValidated    uint64                 `json:"max_source_entries_validated"`
	EveryCallWithinTwoSeconds    bool                   `json:"every_call_within_two_seconds"`
	EverySDKResultWithin64KiB    bool                   `json:"every_sdk_result_within_64kib"`
	CPUTimeDeltaMicroseconds     int64                  `json:"cpu_time_delta_microseconds"`
	Memory                       resourceMemoryReport   `json:"memory"`
	RSSImmediateBytes            int64                  `json:"rss_immediate_bytes"`
	RSSAfter5SecondsBytes        int64                  `json:"rss_after_5_seconds_bytes"`
	RSSAfter30SecondsBytes       int64                  `json:"rss_after_30_seconds_bytes"`
	FDImmediateCount             int                    `json:"fd_immediate_count"`
	FDAfter5SecondsCount         int                    `json:"fd_after_5_seconds_count"`
	FDAfter30SecondsCount        int                    `json:"fd_after_30_seconds_count"`
	FDRecoveredAtEverySample     bool                   `json:"fd_recovered_at_every_sample"`
	GCAcknowledged               bool                   `json:"gc_acknowledged"`
}

type postGCResourceObservation struct {
	memory    resourceMemoryReport
	immediate processResourceSample
	after5    processResourceSample
	after30   processResourceSample
}

type idleResourceReport struct {
	DurationMicroseconds      int64  `json:"duration_microseconds"`
	CPUTimeBeforeMicroseconds int64  `json:"cpu_time_before_microseconds"`
	CPUTimeAfterMicroseconds  int64  `json:"cpu_time_after_microseconds"`
	CPUTimeDeltaMicroseconds  int64  `json:"cpu_time_delta_microseconds"`
	CPUTimeBoundMicroseconds  int64  `json:"cpu_time_bound_microseconds"`
	CPUWithinBound            bool   `json:"cpu_within_bound"`
	RSSBeforeBytes            int64  `json:"rss_before_bytes"`
	RSSAfterBytes             int64  `json:"rss_after_bytes"`
	FDBeforeCount             int    `json:"fd_before_count"`
	FDAfterCount              int    `json:"fd_after_count"`
	FDsRecovered              bool   `json:"fds_recovered"`
	ToolCallRowsBefore        int    `json:"tool_call_rows_before"`
	ToolCallRowsAfter         int    `json:"tool_call_rows_after"`
	ExpectedToolCallRows      int    `json:"expected_tool_call_rows"`
	NoExtraToolCalls          bool   `json:"no_extra_tool_calls"`
	VaultActivityTotalBefore  uint64 `json:"vault_activity_total_before"`
	VaultActivityTotalAfter   uint64 `json:"vault_activity_total_after"`
	VaultActivityActiveBefore uint64 `json:"vault_activity_active_before"`
	VaultActivityActiveAfter  uint64 `json:"vault_activity_active_after"`
	NoVaultActivity           bool   `json:"no_vault_activity"`
	DescriptorCountAfter      int    `json:"descriptor_count_after"`
	DescriptorsUnchanged      bool   `json:"descriptors_unchanged"`
}

type resourceProbeOptions struct {
	ColdProcesses int
	Stabilize5    time.Duration
	Stabilize30   time.Duration
	IdleDuration  time.Duration
	ControlTime   time.Duration
}

func defaultResourceProbeOptions() resourceProbeOptions {
	return resourceProbeOptions{
		ColdProcesses: resourceColdProcesses,
		Stabilize5:    resourceStabilize5,
		Stabilize30:   resourceStabilize30,
		IdleDuration:  resourceIdleDuration,
		ControlTime:   resourceControlTime,
	}
}

type processResourceSample struct {
	rssBytes  int64
	cpuMicros int64
	fdCount   int
}

type resourceActivitySnapshot struct {
	total  uint64
	active uint64
}

type resourceMemorySnapshot struct {
	heapAlloc    uint64
	heapInuse    uint64
	heapObjects  uint64
	heapReleased uint64
	heapSys      uint64
}

type waitedProcessUsage struct {
	highWaterRSSBytes int64
	cpuMicros         int64
}

type resourceFixture struct {
	root          string
	smallReadPath string
	smallBatch    []string
	boundary      resourceBoundaryFixture
	generated     resourceVaultReport
}

type resourceBoundaryFixture struct {
	near8MiB       string
	dense50000     string
	over8MiB       string
	over50000Lines string
	grepMatching   string
	grepNonmatch   string
	grepContext    string
	grepUnicode    string
	grepZeroWidth  string
	grepInvalid    string
	grepOver       string
}

type resourceCallSample struct {
	measurement callMeasurement
	coverage    obsidian.Coverage
}

type resourceOperation struct {
	tool string
	call func() (resourceCallSample, error)
}

type resourceWorkloadAccumulator struct {
	report resourceWorkloadReport
}

func newResourceWorkloadAccumulator() *resourceWorkloadAccumulator {
	return &resourceWorkloadAccumulator{report: resourceWorkloadReport{
		BatchCount:                resourceBatchCount,
		CallsPerBatch:             resourceBatchCalls,
		CallsPerToolPerBatch:      resourceCallsPerToolPerBatch,
		MixedCallCount:            resourceBatchCount * resourceBatchCalls,
		BoundaryCallCount:         resourceBoundaryCalls,
		MeasuredCallCount:         resourceMeasuredCalls,
		EveryCallWithinTwoSeconds: true,
		EverySDKResultWithin64KiB: true,
	}}
}

func (a *resourceWorkloadAccumulator) add(tool string, sample resourceCallSample) error {
	if a == nil {
		return errors.New("candidate resource workload accounting failed")
	}
	switch tool {
	case obsidian.ToolResolve:
		a.report.ToolCalls.Resolve++
	case obsidian.ToolLS:
		a.report.ToolCalls.LS++
	case obsidian.ToolRead:
		a.report.ToolCalls.Read++
	case obsidian.ToolReadMany:
		a.report.ToolCalls.ReadMany++
	case obsidian.ToolGrep:
		a.report.ToolCalls.Grep++
	default:
		return errors.New("candidate resource workload accounting failed")
	}
	observeResourceCall(&a.report.MaxClientLatencyMicroseconds, &a.report.MaxSDKResultBytes,
		&a.report.MaxStructuredBytes, &a.report.MaxFilesScanned, &a.report.MaxBytesScanned,
		&a.report.MaxSourceEntriesValidated, &a.report.EveryCallWithinTwoSeconds,
		&a.report.EverySDKResultWithin64KiB, sample)
	return nil
}

func observeResourceCall(maxLatency *int64, maxSDK, maxStructured *int, maxFiles, maxBytes, maxValidated *uint64,
	withinTime, withinSDK *bool, sample resourceCallSample,
) {
	latencyMicros := sample.measurement.latency.Microseconds()
	*maxLatency = maxInt64(*maxLatency, latencyMicros)
	*maxSDK = maxInt(*maxSDK, sample.measurement.sdkResultBytes)
	*maxStructured = maxInt(*maxStructured, sample.measurement.structuredBytes)
	*maxFiles = maxUint64(*maxFiles, sample.coverage.FilesScanned)
	*maxBytes = maxUint64(*maxBytes, sample.coverage.BytesScanned)
	*maxValidated = maxUint64(*maxValidated, sample.coverage.SourceEntriesValidated)
	*withinTime = *withinTime && sample.measurement.latency < resourceCallTimeLimit
	*withinSDK = *withinSDK && sample.measurement.sdkResultBytes > 0 && sample.measurement.sdkResultBytes <= obsidian.MaxSDKResultBytes
}

type resourceSampler interface {
	Sample(context.Context, int, bool) (processResourceSample, error)
}

type systemResourceSampler struct{}

func (systemResourceSampler) Sample(ctx context.Context, pid int, includeFD bool) (processResourceSample, error) {
	if pid <= 0 {
		return processResourceSample{}, errors.New("candidate resource sampling failed")
	}
	ps := exec.CommandContext(ctx, "/bin/ps", "-o", "rss=,time=", "-p", strconv.Itoa(pid))
	psOutput, err := ps.Output()
	if err != nil {
		return processResourceSample{}, errors.New("candidate resource sampling failed")
	}
	fields := strings.Fields(string(psOutput))
	if len(fields) != 2 {
		return processResourceSample{}, errors.New("candidate resource sampling failed")
	}
	rssKB, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || rssKB < 0 || rssKB > (1<<63-1)/1024 {
		return processResourceSample{}, errors.New("candidate resource sampling failed")
	}
	cpuMicros, err := parseProcessCPU(fields[1])
	if err != nil {
		return processResourceSample{}, errors.New("candidate resource sampling failed")
	}
	sample := processResourceSample{rssBytes: rssKB * 1024, cpuMicros: cpuMicros}
	if !includeFD {
		return sample, nil
	}
	lsof := exec.CommandContext(ctx, "/usr/sbin/lsof", "-a", "-p", strconv.Itoa(pid), "-Fn")
	lsofOutput, err := lsof.Output()
	if err != nil {
		return processResourceSample{}, errors.New("candidate descriptor sampling failed")
	}
	for _, line := range bytes.Split(lsofOutput, []byte{'\n'}) {
		if len(line) > 1 && line[0] == 'f' {
			sample.fdCount++
		}
	}
	if sample.fdCount <= 0 {
		return processResourceSample{}, errors.New("candidate descriptor sampling failed")
	}
	return sample, nil
}

func parseProcessCPU(value string) (int64, error) {
	days := int64(0)
	clock := value
	if dash := strings.IndexByte(value, '-'); dash >= 0 {
		parsed, err := strconv.ParseInt(value[:dash], 10, 64)
		if err != nil || parsed < 0 {
			return 0, errors.New("invalid process CPU time")
		}
		days = parsed
		clock = value[dash+1:]
	}
	parts := strings.Split(clock, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, errors.New("invalid process CPU time")
	}
	var hours, minutes int64
	var secondsPart string
	var err error
	if len(parts) == 3 {
		hours, err = strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return 0, errors.New("invalid process CPU time")
		}
		minutes, err = strconv.ParseInt(parts[1], 10, 64)
		secondsPart = parts[2]
	} else {
		minutes, err = strconv.ParseInt(parts[0], 10, 64)
		secondsPart = parts[1]
	}
	if err != nil || hours < 0 || minutes < 0 || minutes >= 60 {
		return 0, errors.New("invalid process CPU time")
	}
	secondsText, fractionText, _ := strings.Cut(secondsPart, ".")
	seconds, err := strconv.ParseInt(secondsText, 10, 64)
	if err != nil || seconds < 0 || seconds >= 60 || len(fractionText) > 6 {
		return 0, errors.New("invalid process CPU time")
	}
	for len(fractionText) < 6 {
		fractionText += "0"
	}
	fraction := int64(0)
	if fractionText != "" {
		fraction, err = strconv.ParseInt(fractionText, 10, 64)
		if err != nil {
			return 0, errors.New("invalid process CPU time")
		}
	}
	totalSeconds := days*24*60*60 + hours*60*60 + minutes*60 + seconds
	return totalSeconds*1_000_000 + fraction, nil
}

type resourceControl struct {
	command *os.File
	ack     *os.File
	reader  *bufio.Reader
	mu      sync.Mutex
}

func (c *resourceControl) request(ctx context.Context, command, expected string, timeout time.Duration) (string, error) {
	if c == nil || c.command == nil || c.ack == nil || timeout <= 0 {
		return "", errors.New("candidate resource control was unavailable")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	deadline := time.Now().Add(timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := c.command.SetWriteDeadline(deadline); err != nil {
		return "", errors.New("candidate resource control failed")
	}
	if err := c.ack.SetReadDeadline(deadline); err != nil {
		return "", errors.New("candidate resource control failed")
	}
	if _, err := io.WriteString(c.command, command+"\n"); err != nil {
		return "", errors.New("candidate resource control failed")
	}
	lineBytes, err := c.reader.ReadSlice('\n')
	if err != nil || len(lineBytes) > resourceAckMaxBytes {
		return "", errors.New("candidate resource acknowledgement was invalid")
	}
	line := string(lineBytes)
	if !strings.HasPrefix(line, expected) {
		return "", errors.New("candidate resource acknowledgement was invalid")
	}
	return line, nil
}

func (c *resourceControl) gc(ctx context.Context, timeout time.Duration) (resourceMemorySnapshot, error) {
	line, err := c.request(ctx, "gc", "gc ", timeout)
	if err != nil {
		return resourceMemorySnapshot{}, errors.New("candidate GC acknowledgement failed")
	}
	fields := strings.Fields(strings.TrimSuffix(line, "\n"))
	if len(fields) != 6 || fields[0] != "gc" {
		return resourceMemorySnapshot{}, errors.New("candidate GC acknowledgement failed")
	}
	values := make([]uint64, 5)
	canonical := "gc"
	for index := range values {
		value, parseErr := strconv.ParseUint(fields[index+1], 10, 64)
		if parseErr != nil {
			return resourceMemorySnapshot{}, errors.New("candidate GC acknowledgement failed")
		}
		values[index] = value
		canonical += " " + strconv.FormatUint(value, 10)
	}
	if line != canonical+"\n" {
		return resourceMemorySnapshot{}, errors.New("candidate GC acknowledgement failed")
	}
	return resourceMemorySnapshot{
		heapAlloc:    values[0],
		heapInuse:    values[1],
		heapObjects:  values[2],
		heapReleased: values[3],
		heapSys:      values[4],
	}, nil
}

func (c *resourceControl) snapshot(ctx context.Context, timeout time.Duration) (resourceActivitySnapshot, error) {
	line, err := c.request(ctx, "snapshot", "snapshot ", timeout)
	if err != nil {
		return resourceActivitySnapshot{}, err
	}
	fields := strings.Fields(strings.TrimSuffix(line, "\n"))
	if len(fields) != 3 || fields[0] != "snapshot" {
		return resourceActivitySnapshot{}, errors.New("candidate snapshot acknowledgement was invalid")
	}
	total, totalErr := strconv.ParseUint(fields[1], 10, 64)
	active, activeErr := strconv.ParseUint(fields[2], 10, 64)
	if totalErr != nil || activeErr != nil || active > total || line != "snapshot "+strconv.FormatUint(total, 10)+" "+strconv.FormatUint(active, 10)+"\n" {
		return resourceActivitySnapshot{}, errors.New("candidate snapshot acknowledgement was invalid")
	}
	return resourceActivitySnapshot{total: total, active: active}, nil
}

func (c *resourceControl) close() {
	if c == nil {
		return
	}
	if c.command != nil {
		_ = c.command.Close()
	}
	if c.ack != nil {
		_ = c.ack.Close()
	}
}

type resourceCandidate struct {
	process *candidateProcess
	dbPath  string
	cleanup func()
	control *resourceControl
	closed  bool
}

func connectResourceCandidate(ctx context.Context, gatewayBin, root string) (*resourceCandidate, error) {
	dbPath, cleanup, err := newPrivateSQLiteStore()
	if err != nil {
		return nil, err
	}
	commandRead, commandWrite, err := os.Pipe()
	if err != nil {
		cleanup()
		return nil, errors.New("candidate resource control setup failed")
	}
	ackRead, ackWrite, err := os.Pipe()
	if err != nil {
		_ = commandRead.Close()
		_ = commandWrite.Close()
		cleanup()
		return nil, errors.New("candidate resource control setup failed")
	}
	closeAll := func() {
		_ = commandRead.Close()
		_ = commandWrite.Close()
		_ = ackRead.Close()
		_ = ackWrite.Close()
	}
	cmd := exec.Command(gatewayBin, "stdio", "--obsidian-root", root, "--telemetry-db", dbPath)
	cmd.ExtraFiles = []*os.File{commandRead, ackWrite}
	cmd.Env = environmentWithOverride(os.Environ(), resourceprobe.Environment, "3,4")
	process, err := connectCandidateProcessHandle(ctx, cmd, io.Discard)
	_ = commandRead.Close()
	_ = ackWrite.Close()
	if err != nil {
		closeAll()
		cleanup()
		return nil, err
	}
	return &resourceCandidate{
		process: process,
		dbPath:  dbPath,
		cleanup: cleanup,
		control: &resourceControl{command: commandWrite, ack: ackRead, reader: bufio.NewReaderSize(ackRead, resourceAckMaxBytes)},
	}, nil
}

func environmentWithOverride(environment []string, name, value string) []string {
	prefix := name + "="
	out := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			out = append(out, entry)
		}
	}
	return append(out, prefix+value)
}

func (c *resourceCandidate) closeWithUsage() (waitedProcessUsage, error) {
	if c == nil || c.closed {
		return waitedProcessUsage{}, errors.New("candidate resource process was already closed")
	}
	c.closed = true
	closeErr := c.process.session.Close()
	c.control.close()
	c.cleanup()
	if closeErr != nil {
		return waitedProcessUsage{}, errors.New("candidate resource process close failed")
	}
	return waitedUsageFromProcessState(c.process.command.ProcessState)
}

func (c *resourceCandidate) closeDiscard() {
	if c != nil && !c.closed {
		_, _ = c.closeWithUsage()
	}
}

func waitedUsageFromProcessState(state *os.ProcessState) (waitedProcessUsage, error) {
	if state == nil || !state.Exited() || !state.Success() {
		return waitedProcessUsage{}, errors.New("candidate waited process state was invalid")
	}
	rusage, ok := state.SysUsage().(*syscall.Rusage)
	if !ok || rusage == nil {
		return waitedProcessUsage{}, errors.New("candidate waited process usage was unavailable")
	}
	return waitedUsageFromRusage(rusage, runtime.GOOS)
}

func waitedUsageFromRusage(rusage *syscall.Rusage, goos string) (waitedProcessUsage, error) {
	if rusage == nil || rusage.Maxrss < 0 {
		return waitedProcessUsage{}, errors.New("candidate waited process usage was invalid")
	}
	highWater := rusage.Maxrss
	if goos != "darwin" {
		if highWater > (1<<63-1)/1024 {
			return waitedProcessUsage{}, errors.New("candidate waited process usage was invalid")
		}
		highWater *= 1024
	}
	user := int64(rusage.Utime.Sec)*1_000_000 + int64(rusage.Utime.Usec)
	system := int64(rusage.Stime.Sec)*1_000_000 + int64(rusage.Stime.Usec)
	if user < 0 || system < 0 {
		return waitedProcessUsage{}, errors.New("candidate waited process usage was invalid")
	}
	return waitedProcessUsage{highWaterRSSBytes: highWater, cpuMicros: user + system}, nil
}

func newResourceFixture() (resourceFixture, error) {
	root, err := os.MkdirTemp("", "gateway-resource-")
	if err != nil {
		return resourceFixture{}, errors.New("candidate resource fixture setup failed")
	}
	fixture := resourceFixture{root: root}
	failed := true
	defer func() {
		if failed {
			_ = os.RemoveAll(root)
		}
	}()
	write := func(name string, content []byte) (string, error) {
		if err := os.WriteFile(filepath.Join(root, name), content, 0o600); err != nil {
			return "", errors.New("candidate resource fixture setup failed")
		}
		fixture.generated.GeneratedMarkdownFiles++
		fixture.generated.GeneratedBytes += int64(len(content))
		return name, nil
	}
	fixture.smallReadPath, err = write("small-a.md", []byte("# Small\nresource-workload-needle\nsmall evidence\n"))
	if err != nil {
		return resourceFixture{}, err
	}
	smallB, err := write("small-b.md", []byte("# Batch\nsecond small evidence\n"))
	if err != nil {
		return resourceFixture{}, err
	}
	fixture.smallBatch = []string{fixture.smallReadPath, smallB}

	near := bytes.Repeat([]byte{'a'}, obsidian.MaxMarkdownSourceBytes)
	copy(near, []byte("# Boundary\n"))
	fixture.boundary.near8MiB, err = write("near.md", near)
	if err != nil {
		return resourceFixture{}, err
	}
	dense := []byte("decoy ^decoy\n~ continuation\n\naccepted ^accepted\n- x\n" +
		strings.Repeat("- x\n", obsidian.MaxMarkdownSourceLines-5))
	fixture.boundary.dense50000, err = write("dense.md", dense)
	if err != nil {
		return resourceFixture{}, err
	}
	overBytes := bytes.Repeat([]byte{'b'}, obsidian.MaxMarkdownSourceBytes+1)
	copy(overBytes, []byte("# Beyond\n"))
	fixture.boundary.over8MiB, err = write("over-bytes.md", overBytes)
	if err != nil {
		return resourceFixture{}, err
	}
	fixture.boundary.over50000Lines, err = write("over-lines.md", []byte("# Beyond\n"+strings.Repeat("x\n", obsidian.MaxMarkdownSourceLines)))
	if err != nil {
		return resourceFixture{}, err
	}

	exactLine := func(token string) []byte {
		line := bytes.Repeat([]byte{'z'}, obsidian.MaxGrepPhysicalLineBytes)
		copy(line[len(line)-1-len(token):], token)
		line[len(line)-1] = '\n'
		return line
	}
	fixture.boundary.grepMatching, err = write("grep-match.md", exactLine("resource-boundary-match"))
	if err != nil {
		return resourceFixture{}, err
	}
	fixture.boundary.grepNonmatch, err = write("grep-nonmatch.md", exactLine("resource-boundary-absent-source"))
	if err != nil {
		return resourceFixture{}, err
	}
	contextSource := append(exactLine("resource-boundary-context-source"), []byte("resource-boundary-context-match\n")...)
	fixture.boundary.grepContext, err = write("grep-context.md", contextSource)
	if err != nil {
		return resourceFixture{}, err
	}
	fixture.boundary.grepUnicode, err = write("grep-unicode.md", exactLine("resource-boundary-\u00e9"))
	if err != nil {
		return resourceFixture{}, err
	}
	fixture.boundary.grepZeroWidth, err = write("grep-zero.md", exactLine("resource-boundary-zero"))
	if err != nil {
		return resourceFixture{}, err
	}
	invalid := bytes.Repeat([]byte{'i'}, obsidian.MaxGrepPhysicalLineBytes)
	invalid[len(invalid)-2] = 0xff
	invalid[len(invalid)-1] = '\n'
	fixture.boundary.grepInvalid, err = write("grep-invalid.md", invalid)
	if err != nil {
		return resourceFixture{}, err
	}
	fixture.boundary.grepOver, err = write("grep-over.md", append(bytes.Repeat([]byte{'o'}, obsidian.MaxGrepPhysicalLineBytes), '\n'))
	if err != nil {
		return resourceFixture{}, err
	}

	inventoryFiles, inventoryBytes, err := inventoryResourceFixture(root)
	if err != nil {
		return resourceFixture{}, err
	}
	fixture.generated.InventoryMarkdownFiles = inventoryFiles
	fixture.generated.InventoryBytes = inventoryBytes
	fixture.generated.InventoryComplete = true
	fixture.generated.InventoryReconciled = inventoryFiles == fixture.generated.GeneratedMarkdownFiles && inventoryBytes == fixture.generated.GeneratedBytes
	if !fixture.generated.InventoryReconciled {
		return resourceFixture{}, errors.New("candidate resource fixture inventory did not reconcile")
	}
	failed = false
	return fixture, nil
}

func inventoryResourceFixture(root string) (int, int64, error) {
	profile, err := inspectVaultAggregate(context.Background(), root)
	if err != nil || !profile.InventoryComplete || profile.MarkdownFileCount > uint64(^uint(0)>>1) || profile.MarkdownByteCount > uint64(^uint64(0)>>1) {
		return 0, 0, errors.New("candidate resource fixture inventory failed")
	}
	return int(profile.MarkdownFileCount), int64(profile.MarkdownByteCount), nil
}

func callResourceCandidate[Out any](ctx context.Context, session *sdk.ClientSession, name string, arguments map[string]any) (Out, resourceCallSample, bool, error) {
	var out Out
	callCtx, cancel := context.WithTimeout(ctx, resourceCallTimeLimit)
	defer cancel()
	started := time.Now()
	result, err := session.CallTool(callCtx, &sdk.CallToolParams{Name: name, Arguments: arguments})
	latency := time.Since(started)
	if err != nil || result == nil || latency >= resourceCallTimeLimit {
		return out, resourceCallSample{}, false, errors.New("candidate resource call exceeded the time bound")
	}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) == 0 || len(encoded) > obsidian.MaxSDKResultBytes {
		return out, resourceCallSample{}, false, errors.New("candidate resource SDK result exceeded the size bound")
	}
	structured, err := json.Marshal(result.StructuredContent)
	if err != nil || len(structured) == 0 || json.Unmarshal(structured, &out) != nil {
		return out, resourceCallSample{}, false, errors.New("candidate resource structured result was invalid")
	}
	return out, resourceCallSample{measurement: callMeasurement{
		latency: latency, sdkResultBytes: len(encoded), structuredBytes: len(structured),
	}}, result.IsError, nil
}

func coverageResourceCall(sample resourceCallSample, coverage obsidian.Coverage) resourceCallSample {
	sample.coverage = coverage
	return sample
}

func probeCandidateResources(ctx context.Context, gatewayBin, root string, options resourceProbeOptions, sampler resourceSampler) (resourceReport, error) {
	if options.ColdProcesses <= 0 || options.Stabilize5 < 0 || options.Stabilize30 < options.Stabilize5 ||
		options.IdleDuration <= 0 || options.ControlTime <= 0 || sampler == nil {
		return resourceReport{}, errors.New("candidate resource probe configuration was invalid")
	}
	if root == "" {
		return resourceReport{}, errors.New("candidate resource vault was invalid")
	}
	fixture, err := newResourceFixture()
	if err != nil {
		return resourceReport{}, err
	}
	defer os.RemoveAll(fixture.root)
	runtimeProfile, err := inspectCandidateRuntime(gatewayBin)
	if err != nil {
		return resourceReport{}, err
	}
	machineProfile, err := inspectMachineProfile()
	if err != nil {
		return resourceReport{}, err
	}
	vaultProfile, err := inspectVaultAggregate(ctx, root)
	if err != nil {
		return resourceReport{}, err
	}
	// Cold-start proof uses the configured vault root so the report still binds
	// startup behavior to the actual release surface. The adversarial repeated
	// workload uses the private fixture below and never writes into that vault.
	cold, err := observeFreshProcesses(ctx, gatewayBin, root, options.ColdProcesses, sampler)
	if err != nil {
		return resourceReport{}, err
	}
	longLived, err := connectResourceCandidate(ctx, gatewayBin, fixture.root)
	if err != nil {
		return resourceReport{}, err
	}
	defer longLived.closeDiscard()
	descriptorCount, err := requireExactToolList(ctx, longLived.process.session)
	if err != nil {
		return resourceReport{}, err
	}
	operations, err := resourceOperations(ctx, longLived.process.session, fixture)
	if err != nil {
		return resourceReport{}, err
	}
	pid := longLived.process.command.Process.Pid
	baseline, err := observeResourceBaseline(ctx, pid, options, sampler, longLived.control)
	if err != nil {
		return resourceReport{}, err
	}
	report := resourceReport{
		ReportKind:       reportKindResource,
		ReportSchema:     resourceReportSchema,
		SchemaVersion:    resourceReportVersion,
		DescriptorCount:  descriptorCount,
		CandidateRuntime: runtimeProfile,
		Machine:          machineProfile,
		Vault:            vaultProfile,
		Fixture:          fixture.generated,
		Cold:             cold,
		Baseline:         baseline,
		Batches:          make([]resourceBatchReport, 0, resourceBatchCount),
	}
	workload := newResourceWorkloadAccumulator()
	for batchIndex := 0; batchIndex < resourceBatchCount; batchIndex++ {
		var batchStart *processResourceSample
		if batchIndex == 0 {
			started, sampleErr := sampler.Sample(ctx, pid, false)
			if sampleErr != nil {
				return resourceReport{}, sampleErr
			}
			batchStart = &started
			report.Boundaries, err = probeResourceBoundaries(ctx, longLived.process.session, fixture.boundary, workload)
			if err != nil {
				return resourceReport{}, err
			}
		}
		batch, batchErr := observeResourceBatch(ctx, pid, baseline, operations, workload, batchStart, options, sampler, longLived.control)
		if batchErr != nil {
			return resourceReport{}, batchErr
		}
		report.Batches = append(report.Batches, batch)
	}
	report.Workload = workload.report
	report.Idle, err = observeResourceIdle(ctx, longLived.process.session, pid, descriptorCount, baseline.FDImmediateCount, longLived.dbPath, options, sampler, longLived.control)
	if err != nil {
		return resourceReport{}, err
	}
	usage, err := longLived.closeWithUsage()
	if err != nil {
		return resourceReport{}, err
	}
	report.HighWaterRSSBytes = usage.highWaterRSSBytes
	report.Process = candidateProcessProfile{
		BaselineCPUMicroseconds: report.Baseline.CPUTimeMicroseconds,
		FinalCPUMicroseconds:    report.Idle.CPUTimeAfterMicroseconds,
		CPUDeltaMicroseconds:    report.Idle.CPUTimeAfterMicroseconds - report.Baseline.CPUTimeMicroseconds,
		LifetimeCPUMicroseconds: usage.cpuMicros,
		BaselineRSSBytes:        report.Baseline.RSSAfter30SecondsBytes,
		FinalRSSBytes:           report.Idle.RSSAfterBytes,
		MaxObservedRSSBytes:     maxResourceRSS(report),
		HighWaterRSSBytes:       usage.highWaterRSSBytes,
		BaselineFDCount:         report.Baseline.FDImmediateCount,
		FinalFDCount:            report.Idle.FDAfterCount,
		MaxObservedFDCount:      maxResourceFD(report),
		FDsRecovered:            report.Idle.FDAfterCount == report.Baseline.FDImmediateCount,
	}
	report = deriveResourceReport(report)
	report.Passed = resourceReportPasses(report, options.ColdProcesses)
	if !report.Passed {
		return report, errors.New("candidate resource gate failed")
	}
	return report, nil
}

func highWaterWithinBound(highWater, baseline int64, current ...int64) bool {
	// ru_maxrss is lifetime-wide while baseline is sampled later. Comparing the
	// final lifetime high-water to that current baseline is intentionally
	// conservative: a pre-baseline spike can fail but can never false-pass.
	if baseline < 0 || highWater < baseline || highWater-baseline > resourceRSSLimitBytes {
		return false
	}
	for _, rss := range current {
		if rss < 0 || highWater < rss {
			return false
		}
	}
	return true
}

func idleCPUWithinBound(cpuMicros int64, duration time.Duration) bool {
	return cpuMicros >= 0 && duration > 0 && cpuMicros <= duration.Microseconds()/100
}

func noVaultActivity(before, after resourceActivitySnapshot) bool {
	return before.active == 0 && after.active == 0 && before.total == after.total
}

func resourceReportPasses(report resourceReport, expectedColdProcesses int) bool {
	derived := deriveResourceReport(report)
	if !reportSchemaTuplePasses(report.ReportKind, report.ReportSchema, report.SchemaVersion) ||
		report.HighWaterRSSDeltaBytes != derived.HighWaterRSSDeltaBytes ||
		report.HighWaterWithinBound != derived.HighWaterWithinBound ||
		report.MaxHeapAllocGrowthBytes != derived.MaxHeapAllocGrowthBytes ||
		report.HeapAllocGrowthWithinBound != derived.HeapAllocGrowthWithinBound ||
		report.MaxRSSAfter30SecondsGrowthBytes != derived.MaxRSSAfter30SecondsGrowthBytes ||
		report.RSSAfter30SecondsGrowthWithinBound != derived.RSSAfter30SecondsGrowthWithinBound ||
		report.GCAcknowledgementCount != derived.GCAcknowledgementCount ||
		report.AllFDsRecovered != derived.AllFDsRecovered {
		return false
	}
	if expectedColdProcesses <= 0 || report.Cold.FreshProcessCount != expectedColdProcesses ||
		report.DescriptorCount != 5 || len(report.Batches) != resourceBatchCount ||
		!candidateRuntimeProfilePasses(report.CandidateRuntime) || !machineProfilePasses(report.Machine) ||
		!vaultAggregateProfilePasses(report.Vault) || !candidateProcessProfilePasses(report.Process) ||
		!report.Fixture.InventoryComplete || !report.Fixture.InventoryReconciled || report.Fixture.GeneratedMarkdownFiles <= 0 ||
		report.Fixture.GeneratedMarkdownFiles != report.Fixture.InventoryMarkdownFiles || report.Fixture.GeneratedBytes <= 0 ||
		report.Fixture.GeneratedBytes != report.Fixture.InventoryBytes || !validResourceWorkload(report.Workload) ||
		!validResourceBoundaries(report.Boundaries) ||
		report.Baseline.MeasuredCallCount != 0 || !report.Baseline.GCAcknowledged ||
		!report.Baseline.FDRecoveredAtEverySample || report.GCAcknowledgementCount != resourceBatchCount+1 ||
		!report.HighWaterWithinBound || !report.HeapAllocGrowthWithinBound ||
		!report.RSSAfter30SecondsGrowthWithinBound || !report.AllFDsRecovered ||
		!validColdResourceMetrics(report.Cold) ||
		!idleResourceReportPasses(report.Idle, report.Baseline.FDImmediateCount, report.DescriptorCount) {
		return false
	}
	if report.Process.BaselineCPUMicroseconds != report.Baseline.CPUTimeMicroseconds ||
		report.Process.FinalCPUMicroseconds != report.Idle.CPUTimeAfterMicroseconds ||
		report.Process.BaselineRSSBytes != report.Baseline.RSSAfter30SecondsBytes ||
		report.Process.FinalRSSBytes != report.Idle.RSSAfterBytes ||
		report.Process.HighWaterRSSBytes != report.HighWaterRSSBytes || report.Process.BaselineFDCount != report.Baseline.FDImmediateCount ||
		report.Process.FinalFDCount != report.Idle.FDAfterCount {
		return false
	}
	for index, batch := range report.Batches {
		expectedBoundaryCalls := 0
		if index == 0 {
			expectedBoundaryCalls = resourceBoundaryCalls
		}
		if batch.CallCount != resourceBatchCalls || !batch.GCAcknowledged ||
			batch.BoundaryCallCount != expectedBoundaryCalls || !validMixedBatch(batch) ||
			batch.FDRecoveredAtEverySample != batchFDsMatchBaseline(batch, report.Baseline.FDImmediateCount) ||
			!batch.FDRecoveredAtEverySample {
			return false
		}
	}
	return true
}

func validColdResourceMetrics(report coldResourceReport) bool {
	return orderedNonnegativeDurations(report.StartupP50Microseconds, report.StartupP95Microseconds, report.StartupMaxMicroseconds) &&
		orderedNonnegativeDurations(report.FirstCallP50Microseconds, report.FirstCallP95Microseconds, report.FirstCallMaxMicroseconds) &&
		orderedNonnegativeDurations(report.ProcessCPUP50Microseconds, report.ProcessCPUP95Microseconds, report.ProcessCPUMaxMicroseconds) &&
		report.FirstCallMaxMicroseconds < resourceCallTimeLimit.Microseconds() &&
		report.MaxSDKResultBytes > 0 && report.MaxSDKResultBytes <= obsidian.MaxSDKResultBytes &&
		report.MaxStructuredBytes > 0 && report.MaxStructuredBytes <= report.MaxSDKResultBytes
}

func orderedNonnegativeDurations(p50, p95, maximum int64) bool {
	return p50 >= 0 && p95 >= p50 && maximum >= p95
}

func idleResourceReportPasses(report idleResourceReport, baselineFD, descriptorCount int) bool {
	cpuDelta := report.CPUTimeAfterMicroseconds - report.CPUTimeBeforeMicroseconds
	cpuBound := report.DurationMicroseconds / 100
	cpuWithinBound := report.DurationMicroseconds > 0 && report.CPUTimeBeforeMicroseconds >= 0 &&
		report.CPUTimeAfterMicroseconds >= report.CPUTimeBeforeMicroseconds && cpuDelta >= 0 && cpuDelta <= cpuBound
	fdsRecovered := baselineFD > 0 && report.FDBeforeCount == baselineFD && report.FDAfterCount == baselineFD
	noExtraToolCalls := report.ToolCallRowsBefore == report.ToolCallRowsAfter
	noActivity := report.VaultActivityActiveBefore == 0 && report.VaultActivityActiveAfter == 0 &&
		report.VaultActivityTotalBefore == report.VaultActivityTotalAfter
	descriptorsUnchanged := descriptorCount == 5 && report.DescriptorCountAfter == descriptorCount
	return report.CPUTimeDeltaMicroseconds == cpuDelta && report.CPUTimeBoundMicroseconds == cpuBound &&
		report.CPUWithinBound == cpuWithinBound && report.CPUWithinBound && report.RSSBeforeBytes > 0 && report.RSSAfterBytes > 0 &&
		report.FDsRecovered == fdsRecovered && report.FDsRecovered &&
		report.ExpectedToolCallRows == resourceMeasuredCalls && report.ToolCallRowsBefore == resourceMeasuredCalls &&
		report.NoExtraToolCalls == noExtraToolCalls && report.NoExtraToolCalls &&
		report.NoVaultActivity == noActivity && report.NoVaultActivity &&
		report.DescriptorsUnchanged == descriptorsUnchanged && report.DescriptorsUnchanged
}

func validResourceWorkload(report resourceWorkloadReport) bool {
	wantCalls := resourceToolCallCounts{
		Resolve:  resourceBatchCount * resourceCallsPerToolPerBatch,
		LS:       resourceBatchCount * resourceCallsPerToolPerBatch,
		Read:     resourceBatchCount*resourceCallsPerToolPerBatch + 5,
		ReadMany: resourceBatchCount * resourceCallsPerToolPerBatch,
		Grep:     resourceBatchCount*resourceCallsPerToolPerBatch + 7,
	}
	return report.BatchCount == resourceBatchCount && report.CallsPerBatch == resourceBatchCalls &&
		report.CallsPerToolPerBatch == resourceCallsPerToolPerBatch &&
		report.MixedCallCount == resourceBatchCount*resourceBatchCalls && report.BoundaryCallCount == resourceBoundaryCalls &&
		report.MeasuredCallCount == resourceMeasuredCalls && report.ToolCalls == wantCalls && report.ToolCalls.total() == resourceMeasuredCalls &&
		report.MaxClientLatencyMicroseconds > 0 && report.MaxClientLatencyMicroseconds < resourceCallTimeLimit.Microseconds() &&
		report.MaxSDKResultBytes > 0 && report.MaxSDKResultBytes <= obsidian.MaxSDKResultBytes && report.MaxStructuredBytes > 0 &&
		report.MaxBytesScanned > 0 && report.EveryCallWithinTwoSeconds && report.EverySDKResultWithin64KiB
}

func validResourceBoundaries(report resourceBoundaryReport) bool {
	return report.CallCount == resourceBoundaryCalls && report.BatchNumber == 1 && report.RanAfterBaseline && report.RanBeforeBlockingGC &&
		report.Near8MiBStructuralAccepted && report.Dense50000DecoyRejected && report.Dense50000BlockAccepted &&
		report.Over8MiBErrorCode == "input_too_large" && report.Over50000LinesErrorCode == "input_too_large" &&
		report.GrepExactMatchingErrorCode == obsidian.ResponseTooLargeCode && report.GrepExactNonmatchingAccepted &&
		report.GrepExactContextErrorCode == obsidian.ResponseTooLargeCode && report.GrepExactUnicodeErrorCode == obsidian.ResponseTooLargeCode &&
		report.GrepExactZeroWidthErrorCode == obsidian.ResponseTooLargeCode && report.GrepExactInvalidUTF8ErrorCode == obsidian.InvalidUTF8Code &&
		report.GrepOver1MiBErrorCode == "input_too_large" && report.EveryCallWithinTwoSeconds && report.EverySDKResultWithin64KiB
}

func validMixedBatch(batch resourceBatchReport) bool {
	want := resourceToolCallCounts{
		Resolve: resourceCallsPerToolPerBatch, LS: resourceCallsPerToolPerBatch, Read: resourceCallsPerToolPerBatch,
		ReadMany: resourceCallsPerToolPerBatch, Grep: resourceCallsPerToolPerBatch,
	}
	return batch.ToolCalls == want && batch.ToolCalls.total() == resourceBatchCalls &&
		batch.MaxClientLatencyMicroseconds > 0 && batch.MaxClientLatencyMicroseconds < resourceCallTimeLimit.Microseconds() &&
		batch.MaxSDKResultBytes > 0 && batch.MaxSDKResultBytes <= obsidian.MaxSDKResultBytes && batch.MaxStructuredBytes > 0 &&
		batch.EveryCallWithinTwoSeconds && batch.EverySDKResultWithin64KiB
}

func deriveResourceReport(report resourceReport) resourceReport {
	report.HighWaterRSSDeltaBytes = nonnegativeDelta(report.HighWaterRSSBytes, report.Baseline.RSSAfter30SecondsBytes)
	report.MaxHeapAllocGrowthBytes = maxHeapAllocGrowth(report.Baseline, report.Batches)
	report.HeapAllocGrowthWithinBound = report.MaxHeapAllocGrowthBytes <= resourceHeapAllocGrowthLimitBytes
	report.MaxRSSAfter30SecondsGrowthBytes = maxRSSAfter30SecondsGrowth(report.Baseline, report.Batches)
	report.RSSAfter30SecondsGrowthWithinBound = report.MaxRSSAfter30SecondsGrowthBytes <= resourceRSSGrowthLimitBytes
	report.GCAcknowledgementCount = 0
	if report.Baseline.GCAcknowledged {
		report.GCAcknowledgementCount++
	}
	baselineFDsRecovered := baselineFDsMatch(report.Baseline)
	report.AllFDsRecovered = baselineFDsRecovered
	for _, batch := range report.Batches {
		if batch.GCAcknowledged {
			report.GCAcknowledgementCount++
		}
		report.AllFDsRecovered = report.AllFDsRecovered && batchFDsMatchBaseline(batch, report.Baseline.FDImmediateCount)
	}
	report.HighWaterWithinBound = highWaterWithinBound(
		report.HighWaterRSSBytes,
		report.Baseline.RSSAfter30SecondsBytes,
		resourceCurrentRSS(report)...,
	)
	return report
}

func maxHeapAllocGrowth(baseline resourceBaselineReport, batches []resourceBatchReport) uint64 {
	var maximum uint64
	for _, batch := range batches {
		if batch.Memory.HeapAllocBytes > baseline.Memory.HeapAllocBytes {
			maximum = maxUint64(maximum, batch.Memory.HeapAllocBytes-baseline.Memory.HeapAllocBytes)
		}
	}
	return maximum
}

func maxRSSAfter30SecondsGrowth(baseline resourceBaselineReport, batches []resourceBatchReport) int64 {
	var maximum int64
	for _, batch := range batches {
		maximum = maxInt64(maximum, nonnegativeDelta(batch.RSSAfter30SecondsBytes, baseline.RSSAfter30SecondsBytes))
	}
	return maximum
}

func baselineFDsMatch(baseline resourceBaselineReport) bool {
	return baseline.FDImmediateCount > 0 && baseline.FDImmediateCount == baseline.FDAfter5SecondsCount &&
		baseline.FDImmediateCount == baseline.FDAfter30SecondsCount
}

func batchFDsMatchBaseline(batch resourceBatchReport, baselineFD int) bool {
	return baselineFD > 0 && batch.FDImmediateCount == baselineFD && batch.FDAfter5SecondsCount == baselineFD &&
		batch.FDAfter30SecondsCount == baselineFD
}

func resourceCurrentRSS(report resourceReport) []int64 {
	values := []int64{
		report.Baseline.RSSImmediateBytes,
		report.Baseline.RSSAfter5SecondsBytes,
		report.Baseline.RSSAfter30SecondsBytes,
	}
	for _, batch := range report.Batches {
		values = append(values, batch.RSSImmediateBytes, batch.RSSAfter5SecondsBytes, batch.RSSAfter30SecondsBytes)
	}
	values = append(values, report.Idle.RSSBeforeBytes, report.Idle.RSSAfterBytes)
	return values
}

func maxResourceRSS(report resourceReport) int64 {
	var maximum int64
	for _, value := range resourceCurrentRSS(report) {
		maximum = maxInt64(maximum, value)
	}
	return maximum
}

func maxResourceFD(report resourceReport) int {
	maximum := maxInt(report.Baseline.FDImmediateCount, report.Baseline.FDAfter5SecondsCount)
	maximum = maxInt(maximum, report.Baseline.FDAfter30SecondsCount)
	for _, batch := range report.Batches {
		maximum = maxInt(maximum, batch.FDImmediateCount)
		maximum = maxInt(maximum, batch.FDAfter5SecondsCount)
		maximum = maxInt(maximum, batch.FDAfter30SecondsCount)
	}
	maximum = maxInt(maximum, report.Idle.FDBeforeCount)
	return maxInt(maximum, report.Idle.FDAfterCount)
}

func observeFreshProcesses(ctx context.Context, gatewayBin, root string, count int, sampler resourceSampler) (coldResourceReport, error) {
	startup := make([]int64, 0, count)
	firstCalls := make([]int64, 0, count)
	processCPU := make([]int64, 0, count)
	report := coldResourceReport{FreshProcessCount: count}
	for index := 0; index < count; index++ {
		started := time.Now()
		candidate, err := connectResourceCandidate(ctx, gatewayBin, root)
		if err != nil {
			return coldResourceReport{}, err
		}
		startup = append(startup, time.Since(started).Microseconds())
		if _, err := requireExactToolList(ctx, candidate.process.session); err != nil {
			candidate.closeDiscard()
			return coldResourceReport{}, err
		}
		resolved, callSample, isError, err := callResourceCandidate[obsidian.ResolveOutput](ctx, candidate.process.session, obsidian.ToolResolve, map[string]any{"path": "."})
		if err != nil || isError || !resolved.OK || !resolved.Exists || resolved.Type != "directory" {
			candidate.closeDiscard()
			return coldResourceReport{}, errors.New("fresh-process candidate call failed")
		}
		firstCalls = append(firstCalls, callSample.measurement.latency.Microseconds())
		processSample, err := sampler.Sample(ctx, candidate.process.command.Process.Pid, true)
		if err != nil {
			candidate.closeDiscard()
			return coldResourceReport{}, err
		}
		usage, err := candidate.closeWithUsage()
		if err != nil {
			return coldResourceReport{}, err
		}
		processCPU = append(processCPU, usage.cpuMicros)
		report.MaxHighWaterRSSBytes = maxInt64(report.MaxHighWaterRSSBytes, usage.highWaterRSSBytes)
		report.MaxSDKResultBytes = maxInt(report.MaxSDKResultBytes, callSample.measurement.sdkResultBytes)
		report.MaxStructuredBytes = maxInt(report.MaxStructuredBytes, callSample.measurement.structuredBytes)
		report.MaxFDCount = maxInt(report.MaxFDCount, processSample.fdCount)
	}
	report.StartupP50Microseconds, report.StartupP95Microseconds, report.StartupMaxMicroseconds = durationSummary(startup)
	report.FirstCallP50Microseconds, report.FirstCallP95Microseconds, report.FirstCallMaxMicroseconds = durationSummary(firstCalls)
	report.ProcessCPUP50Microseconds, report.ProcessCPUP95Microseconds, report.ProcessCPUMaxMicroseconds = durationSummary(processCPU)
	return report, nil
}

func resourceOperations(ctx context.Context, session *sdk.ClientSession, fixture resourceFixture) ([]resourceOperation, error) {
	if session == nil || fixture.smallReadPath == "" || len(fixture.smallBatch) != 2 {
		return nil, errors.New("candidate resource setup failed")
	}
	resolve := resourceOperation{tool: obsidian.ToolResolve, call: func() (resourceCallSample, error) {
		out, sample, isError, err := callResourceCandidate[obsidian.ResolveOutput](ctx, session, obsidian.ToolResolve, map[string]any{"path": "."})
		if err != nil || isError || !out.OK || !out.Exists || out.Type != "directory" {
			return resourceCallSample{}, errors.New("candidate resource resolve failed")
		}
		return sample, nil
	}}
	ls := resourceOperation{tool: obsidian.ToolLS, call: func() (resourceCallSample, error) {
		out, sample, isError, err := callResourceCandidate[obsidian.LSOutput](ctx, session, obsidian.ToolLS, map[string]any{"path": ".", "limit": 20})
		if err != nil || isError || !out.OK || len(out.Entries) == 0 || out.Coverage.Continuation != "complete" {
			return resourceCallSample{}, errors.New("candidate resource ls failed")
		}
		return coverageResourceCall(sample, out.Coverage), nil
	}}
	read := resourceOperation{tool: obsidian.ToolRead, call: func() (resourceCallSample, error) {
		out, sample, isError, err := callResourceCandidate[obsidian.ReadOutput](ctx, session, obsidian.ToolRead, map[string]any{
			"path": fixture.smallReadPath, "selector": map[string]any{"kind": obsidian.SelectorHeading, "heading": "Small"}, "max_bytes": 1024,
		})
		if err != nil || isError || !out.OK || out.Content == nil || out.Coverage.Continuation != "complete" {
			return resourceCallSample{}, errors.New("candidate resource read failed")
		}
		return coverageResourceCall(sample, out.Coverage), nil
	}}
	readMany := resourceOperation{tool: obsidian.ToolReadMany, call: func() (resourceCallSample, error) {
		requests := []any{
			map[string]any{"path": fixture.smallBatch[0], "selector": map[string]any{"kind": obsidian.SelectorContent, "start_line": 1}, "max_bytes": 1024},
			map[string]any{"path": fixture.smallBatch[1], "selector": map[string]any{"kind": obsidian.SelectorContent, "start_line": 1}, "max_bytes": 1024},
		}
		out, sample, isError, err := callResourceCandidate[obsidian.ReadManyOutput](ctx, session, obsidian.ToolReadMany, map[string]any{"requests": requests, "max_bytes": 2048})
		if err != nil || isError || !out.OK || len(out.Items) != 2 || out.Coverage.Continuation != "complete" || !out.Items[0].OK || !out.Items[1].OK {
			return resourceCallSample{}, errors.New("candidate resource read_many failed")
		}
		return coverageResourceCall(sample, out.Coverage), nil
	}}
	grep := resourceOperation{tool: obsidian.ToolGrep, call: func() (resourceCallSample, error) {
		out, sample, isError, err := callResourceCandidate[obsidian.GrepOutput](ctx, session, obsidian.ToolGrep, map[string]any{
			"pattern": "resource-workload-needle", "path": fixture.smallReadPath, "regex": false, "context_lines": 0, "limit": 1,
		})
		if err != nil || isError || !out.OK || len(out.Matches) != 1 {
			return resourceCallSample{}, errors.New("candidate resource grep failed")
		}
		return coverageResourceCall(sample, out.Coverage), nil
	}}
	return []resourceOperation{resolve, ls, read, readMany, grep}, nil
}

func probeResourceBoundaries(ctx context.Context, session *sdk.ClientSession, fixture resourceBoundaryFixture, workload *resourceWorkloadAccumulator) (resourceBoundaryReport, error) {
	report := resourceBoundaryReport{
		CallCount:                 resourceBoundaryCalls,
		BatchNumber:               1,
		RanAfterBaseline:          true,
		RanBeforeBlockingGC:       true,
		EveryCallWithinTwoSeconds: true,
		EverySDKResultWithin64KiB: true,
	}
	add := func(tool string, sample resourceCallSample) error {
		if err := workload.add(tool, sample); err != nil {
			return err
		}
		report.EveryCallWithinTwoSeconds = report.EveryCallWithinTwoSeconds && sample.measurement.latency < resourceCallTimeLimit
		report.EverySDKResultWithin64KiB = report.EverySDKResultWithin64KiB && sample.measurement.sdkResultBytes > 0 && sample.measurement.sdkResultBytes <= obsidian.MaxSDKResultBytes
		return nil
	}
	readSuccess := func(path string, selector map[string]any) (obsidian.ReadOutput, resourceCallSample, error) {
		out, sample, isError, err := callResourceCandidate[obsidian.ReadOutput](ctx, session, obsidian.ToolRead, map[string]any{
			"path": path, "selector": selector, "max_bytes": 32,
		})
		if err != nil || isError || !out.OK || out.Content == nil || out.Error != nil {
			return out, sample, errors.New("candidate resource read boundary failed")
		}
		return out, coverageResourceCall(sample, out.Coverage), nil
	}
	readError := func(path string, selector map[string]any, want string) (string, resourceCallSample, error) {
		out, sample, isError, err := callResourceCandidate[obsidian.ReadOutput](ctx, session, obsidian.ToolRead, map[string]any{
			"path": path, "selector": selector, "max_bytes": 32,
		})
		if err != nil || !isError || out.OK || out.Error == nil || out.Error.Code != want || out.Coverage.Continuation != "restart" {
			return "", sample, errors.New("candidate resource read rejection boundary failed")
		}
		return out.Error.Code, coverageResourceCall(sample, out.Coverage), nil
	}
	grepCall := func(path, pattern string, regex bool, contextLines int, wantError string) (bool, string, resourceCallSample, error) {
		out, sample, isError, err := callResourceCandidate[obsidian.GrepOutput](ctx, session, obsidian.ToolGrep, map[string]any{
			"pattern": pattern, "path": path, "regex": regex, "context_lines": contextLines, "limit": 1,
		})
		sample = coverageResourceCall(sample, out.Coverage)
		if err != nil {
			return false, "", sample, err
		}
		if wantError == "" {
			if isError || !out.OK || out.Error != nil || len(out.Matches) != 0 || out.Coverage.Continuation != "complete" {
				return false, "", sample, errors.New("candidate resource grep acceptance boundary failed")
			}
			return true, "", sample, nil
		}
		if !isError || out.OK || out.Error == nil || out.Error.Code != wantError || out.Coverage.Continuation != "restart" {
			return false, "", sample, errors.New("candidate resource grep rejection boundary failed")
		}
		return false, out.Error.Code, sample, nil
	}

	near, sample, err := readSuccess(fixture.near8MiB, map[string]any{"kind": obsidian.SelectorHeading, "heading": "Boundary"})
	if err != nil || near.Coverage.Continuation != "cursor" || near.Coverage.NextCursor == "" {
		return resourceBoundaryReport{}, errors.New("candidate resource near-8MiB structural boundary failed")
	}
	if err := add(obsidian.ToolRead, sample); err != nil {
		return resourceBoundaryReport{}, err
	}
	report.Near8MiBStructuralAccepted = true

	report.Dense50000DecoyRejected, sample, err = func() (bool, resourceCallSample, error) {
		code, measured, callErr := readError(fixture.dense50000, map[string]any{"kind": obsidian.SelectorBlock, "block_id": "decoy"}, "selector_not_found")
		return code == "selector_not_found", measured, callErr
	}()
	if err != nil || !report.Dense50000DecoyRejected {
		return resourceBoundaryReport{}, errors.New("candidate resource 50,000-line decoy boundary failed")
	}
	if err := add(obsidian.ToolRead, sample); err != nil {
		return resourceBoundaryReport{}, err
	}
	dense, sample, err := readSuccess(fixture.dense50000, map[string]any{"kind": obsidian.SelectorBlock, "block_id": "accepted"})
	if err != nil || dense.Content == nil || *dense.Content != "accepted ^accepted\n" || dense.Coverage.Continuation != "complete" {
		return resourceBoundaryReport{}, errors.New("candidate resource 50,000-line accepted boundary failed")
	}
	if err := add(obsidian.ToolRead, sample); err != nil {
		return resourceBoundaryReport{}, err
	}
	report.Dense50000BlockAccepted = true

	report.Over8MiBErrorCode, sample, err = readError(fixture.over8MiB, map[string]any{"kind": obsidian.SelectorHeading, "heading": "Beyond"}, "input_too_large")
	if err != nil {
		return resourceBoundaryReport{}, err
	}
	if err := add(obsidian.ToolRead, sample); err != nil {
		return resourceBoundaryReport{}, err
	}
	report.Over50000LinesErrorCode, sample, err = readError(fixture.over50000Lines, map[string]any{"kind": obsidian.SelectorHeading, "heading": "Beyond"}, "input_too_large")
	if err != nil {
		return resourceBoundaryReport{}, err
	}
	if err := add(obsidian.ToolRead, sample); err != nil {
		return resourceBoundaryReport{}, err
	}

	_, report.GrepExactMatchingErrorCode, sample, err = grepCall(fixture.grepMatching, "resource-boundary-match", false, 0, obsidian.ResponseTooLargeCode)
	if err != nil {
		return resourceBoundaryReport{}, err
	}
	if err := add(obsidian.ToolGrep, sample); err != nil {
		return resourceBoundaryReport{}, err
	}
	report.GrepExactNonmatchingAccepted, _, sample, err = grepCall(fixture.grepNonmatch, "resource-boundary-missing", false, 0, "")
	if err != nil {
		return resourceBoundaryReport{}, err
	}
	if err := add(obsidian.ToolGrep, sample); err != nil {
		return resourceBoundaryReport{}, err
	}
	_, report.GrepExactContextErrorCode, sample, err = grepCall(fixture.grepContext, "resource-boundary-context-match", false, 1, obsidian.ResponseTooLargeCode)
	if err != nil {
		return resourceBoundaryReport{}, err
	}
	if err := add(obsidian.ToolGrep, sample); err != nil {
		return resourceBoundaryReport{}, err
	}
	_, report.GrepExactUnicodeErrorCode, sample, err = grepCall(fixture.grepUnicode, "\u00e9", false, 0, obsidian.ResponseTooLargeCode)
	if err != nil {
		return resourceBoundaryReport{}, err
	}
	if err := add(obsidian.ToolGrep, sample); err != nil {
		return resourceBoundaryReport{}, err
	}
	_, report.GrepExactZeroWidthErrorCode, sample, err = grepCall(fixture.grepZeroWidth, resourceGrepZeroWidthPattern, true, 0, obsidian.ResponseTooLargeCode)
	if err != nil {
		return resourceBoundaryReport{}, err
	}
	if err := add(obsidian.ToolGrep, sample); err != nil {
		return resourceBoundaryReport{}, err
	}
	_, report.GrepExactInvalidUTF8ErrorCode, sample, err = grepCall(fixture.grepInvalid, "invalid", false, 0, obsidian.InvalidUTF8Code)
	if err != nil {
		return resourceBoundaryReport{}, err
	}
	if err := add(obsidian.ToolGrep, sample); err != nil {
		return resourceBoundaryReport{}, err
	}
	_, report.GrepOver1MiBErrorCode, sample, err = grepCall(fixture.grepOver, "over", false, 0, "input_too_large")
	if err != nil {
		return resourceBoundaryReport{}, err
	}
	if err := add(obsidian.ToolGrep, sample); err != nil {
		return resourceBoundaryReport{}, err
	}
	return report, nil
}

func observeResourceBaseline(ctx context.Context, pid int, options resourceProbeOptions, sampler resourceSampler, control *resourceControl) (resourceBaselineReport, error) {
	observation, err := observePostGCResources(ctx, pid, options, sampler, control)
	if err != nil {
		return resourceBaselineReport{}, err
	}
	return resourceBaselineReport{
		MeasuredCallCount:        0,
		CPUTimeMicroseconds:      observation.after30.cpuMicros,
		Memory:                   observation.memory,
		RSSImmediateBytes:        observation.immediate.rssBytes,
		RSSAfter5SecondsBytes:    observation.after5.rssBytes,
		RSSAfter30SecondsBytes:   observation.after30.rssBytes,
		FDImmediateCount:         observation.immediate.fdCount,
		FDAfter5SecondsCount:     observation.after5.fdCount,
		FDAfter30SecondsCount:    observation.after30.fdCount,
		FDRecoveredAtEverySample: observation.immediate.fdCount > 0 && observation.immediate.fdCount == observation.after5.fdCount && observation.immediate.fdCount == observation.after30.fdCount,
		GCAcknowledged:           true,
	}, nil
}

func observeResourceBatch(ctx context.Context, pid int, baseline resourceBaselineReport, operations []resourceOperation, workload *resourceWorkloadAccumulator, started *processResourceSample, options resourceProbeOptions, sampler resourceSampler, control *resourceControl) (resourceBatchReport, error) {
	if len(operations) != 5 || workload == nil {
		return resourceBatchReport{}, errors.New("candidate resource batch configuration was invalid")
	}
	includedBoundary := started != nil
	if started == nil {
		sample, err := sampler.Sample(ctx, pid, false)
		if err != nil {
			return resourceBatchReport{}, err
		}
		started = &sample
	}
	batch := resourceBatchReport{CallCount: resourceBatchCalls, EveryCallWithinTwoSeconds: true, EverySDKResultWithin64KiB: true}
	if includedBoundary {
		batch.BoundaryCallCount = resourceBoundaryCalls
	}
	for callIndex := 0; callIndex < resourceBatchCalls; callIndex++ {
		operation := operations[callIndex%len(operations)]
		sample, err := operation.call()
		if err != nil {
			return resourceBatchReport{}, err
		}
		if err := workload.add(operation.tool, sample); err != nil {
			return resourceBatchReport{}, err
		}
		switch operation.tool {
		case obsidian.ToolResolve:
			batch.ToolCalls.Resolve++
		case obsidian.ToolLS:
			batch.ToolCalls.LS++
		case obsidian.ToolRead:
			batch.ToolCalls.Read++
		case obsidian.ToolReadMany:
			batch.ToolCalls.ReadMany++
		case obsidian.ToolGrep:
			batch.ToolCalls.Grep++
		}
		observeResourceCall(&batch.MaxClientLatencyMicroseconds, &batch.MaxSDKResultBytes, &batch.MaxStructuredBytes,
			&batch.MaxFilesScanned, &batch.MaxBytesScanned, &batch.MaxSourceEntriesValidated,
			&batch.EveryCallWithinTwoSeconds, &batch.EverySDKResultWithin64KiB, sample)
	}
	observation, err := observePostGCResources(ctx, pid, options, sampler, control)
	if err != nil {
		return resourceBatchReport{}, err
	}
	fdRecovered := observation.immediate.fdCount == baseline.FDImmediateCount && observation.after5.fdCount == baseline.FDImmediateCount && observation.after30.fdCount == baseline.FDImmediateCount
	batch.CPUTimeDeltaMicroseconds = nonnegativeDelta(observation.immediate.cpuMicros, started.cpuMicros)
	batch.Memory = observation.memory
	batch.RSSImmediateBytes = observation.immediate.rssBytes
	batch.RSSAfter5SecondsBytes = observation.after5.rssBytes
	batch.RSSAfter30SecondsBytes = observation.after30.rssBytes
	batch.FDImmediateCount = observation.immediate.fdCount
	batch.FDAfter5SecondsCount = observation.after5.fdCount
	batch.FDAfter30SecondsCount = observation.after30.fdCount
	batch.FDRecoveredAtEverySample = fdRecovered
	batch.GCAcknowledged = true
	return batch, nil
}

func observePostGCResources(ctx context.Context, pid int, options resourceProbeOptions, sampler resourceSampler, control *resourceControl) (postGCResourceObservation, error) {
	memory, err := control.gc(ctx, options.ControlTime)
	if err != nil {
		return postGCResourceObservation{}, err
	}
	immediate, err := sampler.Sample(ctx, pid, true)
	if err != nil {
		return postGCResourceObservation{}, err
	}
	if err := waitResource(ctx, options.Stabilize5); err != nil {
		return postGCResourceObservation{}, err
	}
	after5, err := sampler.Sample(ctx, pid, true)
	if err != nil {
		return postGCResourceObservation{}, err
	}
	if err := waitResource(ctx, options.Stabilize30-options.Stabilize5); err != nil {
		return postGCResourceObservation{}, err
	}
	after30, err := sampler.Sample(ctx, pid, true)
	if err != nil {
		return postGCResourceObservation{}, err
	}
	return postGCResourceObservation{
		memory: resourceMemoryReport{
			HeapAllocBytes:    memory.heapAlloc,
			HeapInuseBytes:    memory.heapInuse,
			HeapObjects:       memory.heapObjects,
			HeapReleasedBytes: memory.heapReleased,
			HeapSysBytes:      memory.heapSys,
		},
		immediate: immediate,
		after5:    after5,
		after30:   after30,
	}, nil
}

func observeResourceIdle(ctx context.Context, session *sdk.ClientSession, pid, descriptorCount, baselineFD int, dbPath string, options resourceProbeOptions, sampler resourceSampler, control *resourceControl) (idleResourceReport, error) {
	if descriptorCount != 5 {
		return idleResourceReport{}, errors.New("candidate descriptor count changed")
	}
	activityBefore, err := control.snapshot(ctx, options.ControlTime)
	if err != nil {
		return idleResourceReport{}, err
	}
	beforeSQLite, err := inspectSQLite(ctx, dbPath)
	if err != nil {
		return idleResourceReport{}, err
	}
	before, err := sampler.Sample(ctx, pid, true)
	if err != nil {
		return idleResourceReport{}, err
	}
	if err := waitResource(ctx, options.IdleDuration); err != nil {
		return idleResourceReport{}, err
	}
	after, err := sampler.Sample(ctx, pid, true)
	if err != nil {
		return idleResourceReport{}, err
	}
	afterSQLite, err := inspectSQLite(ctx, dbPath)
	if err != nil {
		return idleResourceReport{}, err
	}
	activityAfter, err := control.snapshot(ctx, options.ControlTime)
	if err != nil {
		return idleResourceReport{}, err
	}
	descriptorCountAfter, err := requireExactToolList(ctx, session)
	if err != nil {
		return idleResourceReport{}, err
	}
	cpuDelta := nonnegativeDelta(after.cpuMicros, before.cpuMicros)
	cpuBound := options.IdleDuration.Microseconds() / 100
	return idleResourceReport{
		DurationMicroseconds:      options.IdleDuration.Microseconds(),
		CPUTimeBeforeMicroseconds: before.cpuMicros,
		CPUTimeAfterMicroseconds:  after.cpuMicros,
		CPUTimeDeltaMicroseconds:  cpuDelta,
		CPUTimeBoundMicroseconds:  cpuBound,
		CPUWithinBound:            idleCPUWithinBound(cpuDelta, options.IdleDuration),
		RSSBeforeBytes:            before.rssBytes,
		RSSAfterBytes:             after.rssBytes,
		FDBeforeCount:             before.fdCount,
		FDAfterCount:              after.fdCount,
		FDsRecovered:              before.fdCount == baselineFD && after.fdCount == baselineFD,
		ToolCallRowsBefore:        beforeSQLite.toolCallRows,
		ToolCallRowsAfter:         afterSQLite.toolCallRows,
		ExpectedToolCallRows:      resourceMeasuredCalls,
		NoExtraToolCalls:          beforeSQLite.toolCallRows == afterSQLite.toolCallRows,
		VaultActivityTotalBefore:  activityBefore.total,
		VaultActivityTotalAfter:   activityAfter.total,
		VaultActivityActiveBefore: activityBefore.active,
		VaultActivityActiveAfter:  activityAfter.active,
		NoVaultActivity:           noVaultActivity(activityBefore, activityAfter),
		DescriptorCountAfter:      descriptorCountAfter,
		DescriptorsUnchanged:      descriptorCountAfter == descriptorCount,
	}, nil
}

func waitResource(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return errors.New("candidate resource observation was interrupted")
	case <-timer.C:
		return nil
	}
}

func durationSummary(values []int64) (int64, int64, int64) {
	ordered := append([]int64(nil), values...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	return nearestRank(ordered, 50), nearestRank(ordered, 95), ordered[len(ordered)-1]
}

func nonnegativeDelta(after, before int64) int64 {
	if after <= before {
		return 0
	}
	return after - before
}

func maxInt(a, b int) int {
	if b > a {
		return b
	}
	return a
}

func maxInt64(a, b int64) int64 {
	if b > a {
		return b
	}
	return a
}

func maxUint64(a, b uint64) uint64 {
	if b > a {
		return b
	}
	return a
}
