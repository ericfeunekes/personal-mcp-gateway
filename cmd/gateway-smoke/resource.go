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
	resourceColdProcesses = 10
	resourceBatchCount    = 3
	resourceBatchCalls    = 100
	resourceRSSLimitBytes = int64(64 * 1024 * 1024)
	resourceStabilize5    = 5 * time.Second
	resourceStabilize30   = 30 * time.Second
	resourceIdleDuration  = 60 * time.Second
	resourceControlTime   = 5 * time.Second
	resourceAckMaxBytes   = 64
)

type resourceReport struct {
	SchemaVersion          int                   `json:"schema_version"`
	Passed                 bool                  `json:"passed"`
	DescriptorCount        int                   `json:"descriptor_count"`
	Cold                   coldResourceReport    `json:"cold"`
	BaselineRSSBytes       int64                 `json:"baseline_rss_bytes"`
	BaselineFDCount        int                   `json:"baseline_fd_count"`
	HighWaterRSSBytes      int64                 `json:"high_water_rss_bytes"`
	HighWaterRSSDeltaBytes int64                 `json:"high_water_rss_delta_bytes"`
	HighWaterWithinBound   bool                  `json:"high_water_within_bound"`
	BatchEndsDoNotGrow     bool                  `json:"batch_ends_do_not_grow_monotonically"`
	AllBatchFDsRecovered   bool                  `json:"all_batch_fds_recovered"`
	Batches                []resourceBatchReport `json:"batches"`
	Idle                   idleResourceReport    `json:"idle"`
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

type resourceBatchReport struct {
	CallCount                int   `json:"call_count"`
	CPUTimeDeltaMicroseconds int64 `json:"cpu_time_delta_microseconds"`
	RSSImmediateBytes        int64 `json:"rss_immediate_bytes"`
	RSSAfter5SecondsBytes    int64 `json:"rss_after_5_seconds_bytes"`
	RSSAfter30SecondsBytes   int64 `json:"rss_after_30_seconds_bytes"`
	FDImmediateCount         int   `json:"fd_immediate_count"`
	FDAfter5SecondsCount     int   `json:"fd_after_5_seconds_count"`
	FDAfter30SecondsCount    int   `json:"fd_after_30_seconds_count"`
	FDRecoveredAtEverySample bool  `json:"fd_recovered_at_every_sample"`
	GCAcknowledged           bool  `json:"gc_acknowledged"`
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

func (c *resourceControl) gc(ctx context.Context, timeout time.Duration) error {
	line, err := c.request(ctx, "gc", "gc ", timeout)
	if err != nil || line != "gc ok\n" {
		return errors.New("candidate GC acknowledgement failed")
	}
	return nil
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
	baseline, err := sampler.Sample(ctx, pid, true)
	if err != nil {
		return resourceReport{}, err
	}
	report := resourceReport{
		SchemaVersion:    smokeReportVersion,
		DescriptorCount:  descriptorCount,
		Cold:             cold,
		BaselineRSSBytes: baseline.rssBytes,
		BaselineFDCount:  baseline.fdCount,
		Batches:          make([]resourceBatchReport, 0, resourceBatchCount),
	}
	allFDRecovered := true
	for batchIndex := 0; batchIndex < resourceBatchCount; batchIndex++ {
		batch, batchErr := observeResourceBatch(ctx, pid, baseline, operations, options, sampler, longLived.control)
		if batchErr != nil {
			return resourceReport{}, batchErr
		}
		allFDRecovered = allFDRecovered && batch.FDRecoveredAtEverySample
		report.Batches = append(report.Batches, batch)
	}
	report.AllBatchFDsRecovered = allFDRecovered
	report.BatchEndsDoNotGrow = batchEndsDoNotGrow(report.Batches)
	report.Idle, err = observeResourceIdle(ctx, longLived.process.session, pid, descriptorCount, baseline.fdCount, longLived.dbPath, options, sampler, longLived.control)
	if err != nil {
		return resourceReport{}, err
	}
	usage, err := longLived.closeWithUsage()
	if err != nil {
		return resourceReport{}, err
	}
	report.HighWaterRSSBytes = usage.highWaterRSSBytes
	report.HighWaterRSSDeltaBytes = nonnegativeDelta(usage.highWaterRSSBytes, baseline.rssBytes)
	report.HighWaterWithinBound = highWaterWithinBound(usage.highWaterRSSBytes, baseline.rssBytes)
	report.Passed = resourceReportPasses(report, options.ColdProcesses)
	if !report.Passed {
		return report, errors.New("candidate resource gate failed")
	}
	return report, nil
}

func highWaterWithinBound(highWater, baseline int64) bool {
	// ru_maxrss is lifetime-wide while baseline is sampled later. Comparing the
	// final lifetime high-water to that current baseline is intentionally
	// conservative: a pre-baseline spike can fail but can never false-pass.
	return baseline >= 0 && highWater >= baseline && highWater-baseline <= resourceRSSLimitBytes
}

func idleCPUWithinBound(cpuMicros int64, duration time.Duration) bool {
	return cpuMicros >= 0 && duration > 0 && cpuMicros <= duration.Microseconds()/100
}

func noVaultActivity(before, after resourceActivitySnapshot) bool {
	return before.active == 0 && after.active == 0 && before.total == after.total
}

func resourceReportPasses(report resourceReport, expectedColdProcesses int) bool {
	if expectedColdProcesses <= 0 || report.Cold.FreshProcessCount != expectedColdProcesses ||
		report.DescriptorCount != 2 || len(report.Batches) != resourceBatchCount ||
		!report.HighWaterWithinBound || !report.AllBatchFDsRecovered || !report.BatchEndsDoNotGrow ||
		!report.Idle.CPUWithinBound || !report.Idle.FDsRecovered || !report.Idle.NoExtraToolCalls ||
		!report.Idle.NoVaultActivity || !report.Idle.DescriptorsUnchanged {
		return false
	}
	for _, batch := range report.Batches {
		if batch.CallCount != resourceBatchCalls || !batch.GCAcknowledged || !batch.FDRecoveredAtEverySample {
			return false
		}
	}
	return true
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

func observeResourceBatch(ctx context.Context, pid int, baseline processResourceSample, operations []performanceOperation, options resourceProbeOptions, sampler resourceSampler, control *resourceControl) (resourceBatchReport, error) {
	started, err := sampler.Sample(ctx, pid, false)
	if err != nil {
		return resourceBatchReport{}, err
	}
	for callIndex := 0; callIndex < resourceBatchCalls; callIndex++ {
		if _, err := operations[callIndex%len(operations)](); err != nil {
			return resourceBatchReport{}, err
		}
	}
	if err := control.gc(ctx, options.ControlTime); err != nil {
		return resourceBatchReport{}, err
	}
	immediate, err := sampler.Sample(ctx, pid, true)
	if err != nil {
		return resourceBatchReport{}, err
	}
	if err := waitResource(ctx, options.Stabilize5); err != nil {
		return resourceBatchReport{}, err
	}
	after5, err := sampler.Sample(ctx, pid, true)
	if err != nil {
		return resourceBatchReport{}, err
	}
	if err := waitResource(ctx, options.Stabilize30-options.Stabilize5); err != nil {
		return resourceBatchReport{}, err
	}
	after30, err := sampler.Sample(ctx, pid, true)
	if err != nil {
		return resourceBatchReport{}, err
	}
	fdRecovered := immediate.fdCount == baseline.fdCount && after5.fdCount == baseline.fdCount && after30.fdCount == baseline.fdCount
	return resourceBatchReport{
		CallCount:                resourceBatchCalls,
		CPUTimeDeltaMicroseconds: nonnegativeDelta(immediate.cpuMicros, started.cpuMicros),
		RSSImmediateBytes:        immediate.rssBytes,
		RSSAfter5SecondsBytes:    after5.rssBytes,
		RSSAfter30SecondsBytes:   after30.rssBytes,
		FDImmediateCount:         immediate.fdCount,
		FDAfter5SecondsCount:     after5.fdCount,
		FDAfter30SecondsCount:    after30.fdCount,
		FDRecoveredAtEverySample: fdRecovered,
		GCAcknowledged:           true,
	}, nil
}

func batchEndsDoNotGrow(batches []resourceBatchReport) bool {
	if len(batches) != resourceBatchCount {
		return false
	}
	first := batches[0].RSSAfter30SecondsBytes
	second := batches[1].RSSAfter30SecondsBytes
	third := batches[2].RSSAfter30SecondsBytes
	nonDecreasing := first <= second && second <= third
	containsGrowth := first < second || second < third
	return !(nonDecreasing && containsGrowth)
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
