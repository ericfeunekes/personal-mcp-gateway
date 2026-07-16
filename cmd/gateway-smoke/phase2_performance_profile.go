package main

import (
	"context"
	"debug/buildinfo"
	"errors"
	"runtime"
	"strings"

	"personal-mcp-gateway/internal/tools/obsidian"
)

const markdownInventoryPolicy = "obsidian_markdown_v1"

// ps(1) reports process CPU at centisecond precision on the supported local
// host, while wait4 rusage is finer grained. Allow one display quantum when
// comparing the independently sampled values.
const processCPUSampleToleranceMicroseconds = int64(10_000)

type candidateRuntimeProfile struct {
	GoVersion string `json:"go_version"`
	GOOS      string `json:"goos"`
	GOARCH    string `json:"goarch"`
}

type machineProfile struct {
	LogicalCPUCount int `json:"logical_cpu_count"`
	GOMAXPROCS      int `json:"gomaxprocs"`
}

type vaultAggregateProfile struct {
	InventoryPolicy   string `json:"inventory_policy"`
	InventoryComplete bool   `json:"inventory_complete"`
	MarkdownFileCount uint64 `json:"markdown_file_count"`
	MarkdownByteCount uint64 `json:"markdown_byte_count"`
	StoppedBy         string `json:"stopped_by"`
}

type candidateProcessProfile struct {
	BaselineCPUMicroseconds int64 `json:"baseline_cpu_microseconds"`
	FinalCPUMicroseconds    int64 `json:"final_cpu_microseconds"`
	CPUDeltaMicroseconds    int64 `json:"cpu_delta_microseconds"`
	LifetimeCPUMicroseconds int64 `json:"lifetime_cpu_microseconds"`
	BaselineRSSBytes        int64 `json:"baseline_rss_bytes"`
	FinalRSSBytes           int64 `json:"final_rss_bytes"`
	MaxObservedRSSBytes     int64 `json:"max_observed_rss_bytes"`
	HighWaterRSSBytes       int64 `json:"high_water_rss_bytes"`
	BaselineFDCount         int   `json:"baseline_fd_count"`
	FinalFDCount            int   `json:"final_fd_count"`
	MaxObservedFDCount      int   `json:"max_observed_fd_count"`
	FDsRecovered            bool  `json:"fds_recovered"`
}

type candidateProcessTracker struct {
	pid      int
	sampler  resourceSampler
	baseline processResourceSample
	latest   processResourceSample
	maxRSS   int64
	maxFD    int
}

func inspectCandidateRuntime(candidatePath string) (candidateRuntimeProfile, error) {
	info, err := buildinfo.ReadFile(candidatePath)
	if err != nil || info == nil || !boundedProfileValue(info.GoVersion, 64) {
		return candidateRuntimeProfile{}, errors.New("candidate runtime profile was unavailable")
	}
	settings := make(map[string]string, len(info.Settings))
	for _, setting := range info.Settings {
		settings[setting.Key] = setting.Value
	}
	goos, goarch := settings["GOOS"], settings["GOARCH"]
	if !boundedProfileToken(goos, 32) || !boundedProfileToken(goarch, 32) {
		return candidateRuntimeProfile{}, errors.New("candidate runtime profile was unavailable")
	}
	return candidateRuntimeProfile{GoVersion: info.GoVersion, GOOS: goos, GOARCH: goarch}, nil
}

func inspectMachineProfile() (machineProfile, error) {
	logicalCPUs := runtime.NumCPU()
	gomaxprocs := runtime.GOMAXPROCS(0)
	if logicalCPUs < 1 || logicalCPUs > 4096 || gomaxprocs < 1 || gomaxprocs > 4096 {
		return machineProfile{}, errors.New("machine profile was unavailable")
	}
	return machineProfile{LogicalCPUCount: logicalCPUs, GOMAXPROCS: gomaxprocs}, nil
}

func startCandidateProcessTracker(ctx context.Context, process *candidateProcess, sampler resourceSampler) (*candidateProcessTracker, error) {
	if process == nil || process.command == nil || process.command.Process == nil || process.command.Process.Pid <= 0 || sampler == nil {
		return nil, errors.New("candidate process profile was unavailable")
	}
	baseline, err := sampler.Sample(ctx, process.command.Process.Pid, true)
	if err != nil {
		return nil, errors.New("candidate process profile was unavailable")
	}
	return &candidateProcessTracker{
		pid: process.command.Process.Pid, sampler: sampler, baseline: baseline, latest: baseline,
		maxRSS: baseline.rssBytes, maxFD: baseline.fdCount,
	}, nil
}

func (t *candidateProcessTracker) sample(ctx context.Context) error {
	if t == nil || t.sampler == nil || t.pid <= 0 {
		return errors.New("candidate process profile was unavailable")
	}
	sample, err := t.sampler.Sample(ctx, t.pid, true)
	if err != nil {
		return errors.New("candidate process profile was unavailable")
	}
	t.latest = sample
	if sample.rssBytes > t.maxRSS {
		t.maxRSS = sample.rssBytes
	}
	if sample.fdCount > t.maxFD {
		t.maxFD = sample.fdCount
	}
	return nil
}

func (t *candidateProcessTracker) finish(ctx context.Context, process *candidateProcess) (candidateProcessProfile, error) {
	if t == nil || process == nil || process.session == nil || process.command == nil {
		return candidateProcessProfile{}, errors.New("candidate process profile was unavailable")
	}
	if err := t.sample(ctx); err != nil {
		return candidateProcessProfile{}, err
	}
	if err := process.session.Close(); err != nil {
		return candidateProcessProfile{}, errors.New("candidate process close failed")
	}
	usage, err := waitedUsageFromProcessState(process.command.ProcessState)
	if err != nil {
		return candidateProcessProfile{}, err
	}
	profile := candidateProcessProfile{
		BaselineCPUMicroseconds: t.baseline.cpuMicros,
		FinalCPUMicroseconds:    t.latest.cpuMicros,
		CPUDeltaMicroseconds:    t.latest.cpuMicros - t.baseline.cpuMicros,
		LifetimeCPUMicroseconds: usage.cpuMicros,
		BaselineRSSBytes:        t.baseline.rssBytes,
		FinalRSSBytes:           t.latest.rssBytes,
		MaxObservedRSSBytes:     t.maxRSS,
		HighWaterRSSBytes:       usage.highWaterRSSBytes,
		BaselineFDCount:         t.baseline.fdCount,
		FinalFDCount:            t.latest.fdCount,
		MaxObservedFDCount:      t.maxFD,
		FDsRecovered:            t.latest.fdCount == t.baseline.fdCount,
	}
	if err := candidateProcessProfileValidationError(profile); err != nil {
		return profile, err
	}
	return profile, nil
}

func candidateRuntimeProfilePasses(profile candidateRuntimeProfile) bool {
	return boundedProfileValue(profile.GoVersion, 64) && boundedProfileToken(profile.GOOS, 32) && boundedProfileToken(profile.GOARCH, 32)
}

func machineProfilePasses(profile machineProfile) bool {
	return profile.LogicalCPUCount >= 1 && profile.LogicalCPUCount <= 4096 &&
		profile.GOMAXPROCS >= 1 && profile.GOMAXPROCS <= 4096
}

func vaultAggregateProfilePasses(profile vaultAggregateProfile) bool {
	if profile.InventoryPolicy != markdownInventoryPolicy || profile.MarkdownFileCount > uint64(obsidian.MaxGrepMaxFiles) ||
		profile.MarkdownByteCount > uint64(obsidian.MaxGrepMaxBytes) {
		return false
	}
	if profile.InventoryComplete {
		return profile.StoppedBy == "scope"
	}
	return profile.StoppedBy == "file_limit" || profile.StoppedBy == "byte_limit" || profile.StoppedBy == "timeout" || profile.StoppedBy == "source_change"
}

func candidateProcessProfilePasses(profile candidateProcessProfile) bool {
	return candidateProcessProfileValidationError(profile) == nil
}

func candidateProcessProfileValidationError(profile candidateProcessProfile) error {
	switch {
	case profile.BaselineCPUMicroseconds < 0 || profile.FinalCPUMicroseconds < profile.BaselineCPUMicroseconds ||
		profile.CPUDeltaMicroseconds != profile.FinalCPUMicroseconds-profile.BaselineCPUMicroseconds:
		return errors.New("candidate process CPU samples were invalid")
	case profile.LifetimeCPUMicroseconds+processCPUSampleToleranceMicroseconds < profile.FinalCPUMicroseconds:
		return errors.New("candidate lifetime CPU sample was invalid")
	case profile.BaselineRSSBytes <= 0 || profile.FinalRSSBytes <= 0 ||
		profile.MaxObservedRSSBytes < profile.BaselineRSSBytes || profile.MaxObservedRSSBytes < profile.FinalRSSBytes:
		return errors.New("candidate process RSS samples were invalid")
	case profile.HighWaterRSSBytes < profile.MaxObservedRSSBytes:
		return errors.New("candidate process high-water RSS sample was invalid")
	case profile.BaselineFDCount <= 0 || profile.FinalFDCount <= 0 ||
		profile.MaxObservedFDCount < profile.BaselineFDCount || profile.MaxObservedFDCount < profile.FinalFDCount:
		return errors.New("candidate process descriptor samples were invalid")
	case profile.FDsRecovered != (profile.FinalFDCount == profile.BaselineFDCount) || !profile.FDsRecovered:
		return errors.New("candidate process descriptors did not recover")
	default:
		return nil
	}
}

func boundedProfileValue(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n\x00")
}

func boundedProfileToken(value string, maximum int) bool {
	if !boundedProfileValue(value, maximum) {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return true
}
