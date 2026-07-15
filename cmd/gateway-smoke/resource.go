package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
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
	resourceReportVersion             = 2
	resourceColdProcesses             = 10
	resourceBatchCount                = 3
	resourceBatchCalls                = 100
	resourceHeapAllocGrowthLimitBytes = uint64(256 * 1024)
	resourceRSSGrowthLimitBytes       = int64(8 * 1024 * 1024)
	resourceRSSLimitBytes             = int64(64 * 1024 * 1024)
	resourceStabilize5                = 5 * time.Second
	resourceStabilize30               = 30 * time.Second
	resourceIdleDuration              = 60 * time.Second
	resourceControlTime               = 5 * time.Second
	resourceAckMaxBytes               = 128
)

type resourceReport struct {
	SchemaVersion                      int                    `json:"schema_version"`
	Passed                             bool                   `json:"passed"`
	DescriptorCount                    int                    `json:"descriptor_count"`
	Cold                               coldResourceReport     `json:"cold"`
	Baseline                           resourceBaselineReport `json:"baseline"`
	HighWaterRSSBytes                  int64                  `json:"high_water_rss_bytes"`
	HighWaterRSSDeltaBytes             int64                  `json:"high_water_rss_delta_bytes"`
	HighWaterWithinBound               bool                   `json:"high_water_within_bound"`
	MaxHeapAllocGrowthBytes            uint64                 `json:"max_heap_alloc_growth_bytes"`
	HeapAllocGrowthWithinBound         bool                   `json:"heap_alloc_growth_within_bound"`
	MaxRSSAfter30SecondsGrowthBytes    int64                  `json:"max_rss_after_30_seconds_growth_bytes"`
	RSSAfter30SecondsGrowthWithinBound bool                   `json:"rss_after_30_seconds_growth_within_bound"`
	GCAcknowledgementCount             int                    `json:"gc_acknowledgement_count"`
	AllFDsRecovered                    bool                   `json:"all_fds_recovered"`
	Batches                            []resourceBatchReport  `json:"batches"`
	Idle                               idleResourceReport     `json:"idle"`
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
	CallCount                int                  `json:"call_count"`
	CPUTimeDeltaMicroseconds int64                `json:"cpu_time_delta_microseconds"`
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

type postGCResourceObservation struct {
	memory    resourceMemoryReport
	immediate processResourceSample
	after5    processResourceSample
	after30   processResourceSample
}

type idleResourceReport struct {
	DurationMicroseconds      int64  `json:"duration_microseconds"`
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

func probeCandidateResources(ctx context.Context, gatewayBin, root string, options resourceProbeOptions, sampler resourceSampler) (resourceReport, error) {
	if options.ColdProcesses <= 0 || options.Stabilize5 < 0 || options.Stabilize30 < options.Stabilize5 ||
		options.IdleDuration <= 0 || options.ControlTime <= 0 || sampler == nil {
		return resourceReport{}, errors.New("candidate resource probe configuration was invalid")
	}
	representative, err := discoverResourceRepresentative(ctx, gatewayBin, root)
	if err != nil {
		return resourceReport{}, err
	}
	cold, err := observeFreshProcesses(ctx, gatewayBin, root, representative, options.ColdProcesses, sampler)
	if err != nil {
		return resourceReport{}, err
	}
	longLived, err := connectResourceCandidate(ctx, gatewayBin, root)
	if err != nil {
		return resourceReport{}, err
	}
	defer longLived.closeDiscard()
	descriptorCount, err := requireExactToolList(ctx, longLived.process.session)
	if err != nil {
		return resourceReport{}, err
	}
	operations, err := resourceOperations(ctx, longLived.process.session, representative)
	if err != nil {
		return resourceReport{}, err
	}
	pid := longLived.process.command.Process.Pid
	baseline, err := observeResourceBaseline(ctx, pid, options, sampler, longLived.control)
	if err != nil {
		return resourceReport{}, err
	}
	report := resourceReport{
		SchemaVersion:   resourceReportVersion,
		DescriptorCount: descriptorCount,
		Cold:            cold,
		Baseline:        baseline,
		Batches:         make([]resourceBatchReport, 0, resourceBatchCount),
	}
	for batchIndex := 0; batchIndex < resourceBatchCount; batchIndex++ {
		batch, batchErr := observeResourceBatch(ctx, pid, baseline, operations, options, sampler, longLived.control)
		if batchErr != nil {
			return resourceReport{}, batchErr
		}
		report.Batches = append(report.Batches, batch)
	}
	report.Idle, err = observeResourceIdle(ctx, longLived.process.session, pid, descriptorCount, baseline.FDImmediateCount, longLived.dbPath, options, sampler, longLived.control)
	if err != nil {
		return resourceReport{}, err
	}
	usage, err := longLived.closeWithUsage()
	if err != nil {
		return resourceReport{}, err
	}
	report.HighWaterRSSBytes = usage.highWaterRSSBytes
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
	if report.SchemaVersion != resourceReportVersion ||
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
		report.DescriptorCount != 2 || len(report.Batches) != resourceBatchCount ||
		report.Baseline.MeasuredCallCount != 0 || !report.Baseline.GCAcknowledged ||
		!report.Baseline.FDRecoveredAtEverySample || report.GCAcknowledgementCount != resourceBatchCount+1 ||
		!report.HighWaterWithinBound || !report.HeapAllocGrowthWithinBound ||
		!report.RSSAfter30SecondsGrowthWithinBound || !report.AllFDsRecovered ||
		!report.Idle.CPUWithinBound || !report.Idle.FDsRecovered || !report.Idle.NoExtraToolCalls ||
		!report.Idle.NoVaultActivity || !report.Idle.DescriptorsUnchanged {
		return false
	}
	for _, batch := range report.Batches {
		if batch.CallCount != resourceBatchCalls || !batch.GCAcknowledged ||
			batch.FDRecoveredAtEverySample != batchFDsMatchBaseline(batch, report.Baseline.FDImmediateCount) ||
			!batch.FDRecoveredAtEverySample {
			return false
		}
	}
	return true
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

func discoverResourceRepresentative(ctx context.Context, gatewayBin, root string) (string, error) {
	candidate, err := connectResourceCandidate(ctx, gatewayBin, root)
	if err != nil {
		return "", err
	}
	defer candidate.closeDiscard()
	if _, err := requireExactToolList(ctx, candidate.process.session); err != nil {
		return "", err
	}
	representative, _, err := selectRepresentativeDirectory(ctx, candidate.process.session)
	if err != nil {
		return "", err
	}
	if _, err := candidate.closeWithUsage(); err != nil {
		return "", errors.New("candidate resource setup close failed")
	}
	return representative, nil
}

func observeFreshProcesses(ctx context.Context, gatewayBin, root, representative string, count int, sampler resourceSampler) (coldResourceReport, error) {
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
		page, measured, err := callMeasured[obsidian.LSOutput](ctx, candidate.process.session, obsidian.ToolLS, map[string]any{"path": representative, "limit": 1})
		if err != nil || !validFirstPerformancePage(page) {
			candidate.closeDiscard()
			return coldResourceReport{}, errors.New("fresh-process candidate call failed")
		}
		firstCalls = append(firstCalls, measured.latency.Microseconds())
		sample, err := sampler.Sample(ctx, candidate.process.command.Process.Pid, true)
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
		report.MaxSDKResultBytes = maxInt(report.MaxSDKResultBytes, measured.sdkResultBytes)
		report.MaxStructuredBytes = maxInt(report.MaxStructuredBytes, measured.structuredBytes)
		if page.Coverage.FilesScanned > report.MaxFilesScanned {
			report.MaxFilesScanned = page.Coverage.FilesScanned
		}
		report.MaxFDCount = maxInt(report.MaxFDCount, sample.fdCount)
	}
	report.StartupP50Microseconds, report.StartupP95Microseconds, report.StartupMaxMicroseconds = durationSummary(startup)
	report.FirstCallP50Microseconds, report.FirstCallP95Microseconds, report.FirstCallMaxMicroseconds = durationSummary(firstCalls)
	report.ProcessCPUP50Microseconds, report.ProcessCPUP95Microseconds, report.ProcessCPUMaxMicroseconds = durationSummary(processCPU)
	return report, nil
}

func resourceOperations(ctx context.Context, session *sdk.ClientSession, representative string) ([]performanceOperation, error) {
	first, _, err := callMeasured[obsidian.LSOutput](ctx, session, obsidian.ToolLS, map[string]any{"path": representative, "limit": 1})
	if err != nil || !validFirstPerformancePage(first) {
		return nil, errors.New("candidate resource setup failed")
	}
	cursor := first.Coverage.NextCursor
	firstIdentity := first.Entries[0].Path
	resolve := func() (operationSample, error) {
		out, measured, callErr := callMeasured[obsidian.ResolveOutput](ctx, session, obsidian.ToolResolve, map[string]any{"path": "."})
		if callErr != nil || !out.OK || !out.Exists || out.Type != "directory" {
			return operationSample{}, errors.New("candidate resource call failed")
		}
		return operationSample{measurement: measured}, nil
	}
	firstPage := func() (operationSample, error) {
		out, measured, callErr := callMeasured[obsidian.LSOutput](ctx, session, obsidian.ToolLS, map[string]any{"path": representative, "limit": 1})
		if callErr != nil || !validFirstPerformancePage(out) {
			return operationSample{}, errors.New("candidate resource call failed")
		}
		return sampleFromLS(measured, out), nil
	}
	continued := func() (operationSample, error) {
		out, measured, callErr := callMeasured[obsidian.LSOutput](ctx, session, obsidian.ToolLS, map[string]any{"path": representative, "limit": 1, "cursor": cursor})
		if callErr != nil || !validContinuedPerformancePage(out, firstIdentity) {
			return operationSample{}, errors.New("candidate resource call failed")
		}
		return sampleFromLS(measured, out), nil
	}
	limit100 := func() (operationSample, error) {
		out, measured, callErr := callMeasured[obsidian.LSOutput](ctx, session, obsidian.ToolLS, map[string]any{"path": representative, "limit": 100})
		if callErr != nil || !validPerformancePage(out) {
			return operationSample{}, errors.New("candidate resource call failed")
		}
		return sampleFromLS(measured, out), nil
	}
	return []performanceOperation{resolve, firstPage, continued, limit100}, nil
}

func observeResourceBaseline(ctx context.Context, pid int, options resourceProbeOptions, sampler resourceSampler, control *resourceControl) (resourceBaselineReport, error) {
	observation, err := observePostGCResources(ctx, pid, options, sampler, control)
	if err != nil {
		return resourceBaselineReport{}, err
	}
	return resourceBaselineReport{
		MeasuredCallCount:        0,
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

func observeResourceBatch(ctx context.Context, pid int, baseline resourceBaselineReport, operations []performanceOperation, options resourceProbeOptions, sampler resourceSampler, control *resourceControl) (resourceBatchReport, error) {
	started, err := sampler.Sample(ctx, pid, false)
	if err != nil {
		return resourceBatchReport{}, err
	}
	for callIndex := 0; callIndex < resourceBatchCalls; callIndex++ {
		if _, err := operations[callIndex%len(operations)](); err != nil {
			return resourceBatchReport{}, err
		}
	}
	observation, err := observePostGCResources(ctx, pid, options, sampler, control)
	if err != nil {
		return resourceBatchReport{}, err
	}
	fdRecovered := observation.immediate.fdCount == baseline.FDImmediateCount && observation.after5.fdCount == baseline.FDImmediateCount && observation.after30.fdCount == baseline.FDImmediateCount
	return resourceBatchReport{
		CallCount:                resourceBatchCalls,
		CPUTimeDeltaMicroseconds: nonnegativeDelta(observation.immediate.cpuMicros, started.cpuMicros),
		Memory:                   observation.memory,
		RSSImmediateBytes:        observation.immediate.rssBytes,
		RSSAfter5SecondsBytes:    observation.after5.rssBytes,
		RSSAfter30SecondsBytes:   observation.after30.rssBytes,
		FDImmediateCount:         observation.immediate.fdCount,
		FDAfter5SecondsCount:     observation.after5.fdCount,
		FDAfter30SecondsCount:    observation.after30.fdCount,
		FDRecoveredAtEverySample: fdRecovered,
		GCAcknowledged:           true,
	}, nil
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
	if descriptorCount != 2 {
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
