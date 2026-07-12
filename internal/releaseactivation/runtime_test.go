package releaseactivation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOSRuntimeObserveCollectsBoundFactsAndReadiness(t *testing.T) {
	dir := t.TempDir()
	m, artifacts, controller := runtimeFixture(t, dir, true)
	writeRuntimeFile(t, m.TargetPath, "candidate", 0o755)
	m.CandidateSHA256 = hashText("candidate")
	m.PreviousSHA256 = hashText("previous")

	runner := &recordingRunner{results: []CommandResult{{Stdout: []byte("program = " + m.WrapperPath + "\n")}}}
	httpClient := &sequenceHTTPClient{statuses: []int{http.StatusOK, http.StatusOK}}
	runtime := &OSRuntime{Runner: runner, HTTPClient: httpClient}

	observed, err := runtime.Observe(context.Background(), m, controller, artifacts)
	if err != nil {
		t.Fatal(err)
	}
	if observed.CandidateSHA256 != m.CandidateSHA256 || observed.AuthorityArtifactSHA256 != m.AuthoritySHA256 || observed.ControllerSHA256 != m.AuthoritySHA256 {
		t.Fatalf("artifact observations = %#v", observed)
	}
	if observed.PreviousSHA256 != m.PreviousSHA256 || !observed.InstalledPresent || observed.InstalledSHA256 != m.CandidateSHA256 {
		t.Fatalf("target/recovery observations = %#v", observed)
	}
	if observed.PlistSHA256 != m.PlistSHA256 || observed.WrapperSHA256 != m.WrapperSHA256 || observed.MCPWrapperSHA256 != m.MCPWrapperSHA256 || observed.EnvironmentSHA256 != m.EnvironmentSHA256 {
		t.Fatalf("runtime fingerprints = %#v", observed)
	}
	if !observed.RuntimeReady || observed.SupervisorUnloaded {
		t.Fatalf("supervisor observations = %#v", observed)
	}
	if got := httpClient.paths(); strings.Join(got, ",") != "/healthz,/readyz" {
		t.Fatalf("probe paths = %v", got)
	}
}

func TestOSRuntimeObserveRejectsNonExecutableOrSymlinkTarget(t *testing.T) {
	for _, tt := range []struct {
		name  string
		setup func(t *testing.T, target string)
	}{
		{name: "non executable", setup: func(t *testing.T, target string) { writeRuntimeFile(t, target, "old", 0o600) }},
		{name: "symlink", setup: func(t *testing.T, target string) {
			real := writeRuntimeFile(t, filepath.Join(filepath.Dir(target), "real"), "old", 0o755)
			if err := os.Symlink(real, target); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			m, artifacts, controller := runtimeFixture(t, dir, false)
			tt.setup(t, m.TargetPath)
			runtime := &OSRuntime{Runner: &recordingRunner{}}
			_, err := runtime.Observe(context.Background(), m, controller, artifacts)
			if err == nil || strings.Contains(err.Error(), dir) {
				t.Fatalf("Observe error = %q; want sanitized failure", err)
			}
		})
	}
}

func TestOSRuntimeInstallRestoreAndRemoveTarget(t *testing.T) {
	dir := t.TempDir()
	m, artifacts, _ := runtimeFixture(t, dir, true)
	writeRuntimeFile(t, m.TargetPath, "old installed", 0o755)
	runtime := NewOSRuntime()

	if err := runtime.InstallCandidate(context.Background(), m, artifacts); err != nil {
		t.Fatal(err)
	}
	assertRuntimeTarget(t, m.TargetPath, "candidate", m.CandidateSHA256)
	assertNoRuntimeTemps(t, filepath.Dir(m.TargetPath), filepath.Base(m.TargetPath))

	if err := runtime.RestorePrevious(context.Background(), m, artifacts); err != nil {
		t.Fatal(err)
	}
	assertRuntimeTarget(t, m.TargetPath, "previous", m.PreviousSHA256)

	if err := runtime.RemoveTarget(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(m.TargetPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target remains after remove: %v", err)
	}
	if err := runtime.RemoveTarget(context.Background(), m); err != nil {
		t.Fatalf("repeated remove = %v", err)
	}
}

func TestOSRuntimeInstallRejectsHashMismatchWithoutMutation(t *testing.T) {
	dir := t.TempDir()
	m, artifacts, _ := runtimeFixture(t, dir, false)
	writeRuntimeFile(t, m.TargetPath, "proven", 0o755)
	m.CandidateSHA256 = strings.Repeat("f", 64)

	err := NewOSRuntime().InstallCandidate(context.Background(), m, artifacts)
	if err == nil || strings.Contains(err.Error(), dir) || err.Error() != "release candidate installation failed" {
		t.Fatalf("InstallCandidate error = %q; want fixed sanitized error", err)
	}
	data, readErr := os.ReadFile(m.TargetPath)
	if readErr != nil || string(data) != "proven" {
		t.Fatalf("installed target changed: %q, %v", data, readErr)
	}
	assertNoRuntimeTemps(t, filepath.Dir(m.TargetPath), filepath.Base(m.TargetPath))
}

func TestOSRuntimeRestartBootoutAndUnloadedObservation(t *testing.T) {
	dir := t.TempDir()
	healthURLFile := writeRuntimeFile(t, filepath.Join(dir, "health.url"), "http://127.0.0.1:12345\n", 0o600)
	m := Manifest{EffectiveUID: 501, LaunchAgentLabel: "example.agent", WrapperPath: "/private/wrapper", PlistPath: "/private/agent.plist", HealthURLFile: healthURLFile}
	runner := &recordingRunner{results: []CommandResult{
		{ExitCode: 0},
		{Stdout: []byte("program = /private/wrapper\n")},
		{ExitCode: 0},
		{ExitCode: launchctlNotFound},
	}}
	runtime := &OSRuntime{Runner: runner}
	if err := runtime.Restart(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(healthURLFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale health URL file remains after restart: %v", err)
	}
	if err := runtime.Bootout(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	unloaded, err := runtime.ConfirmUnloaded(context.Background(), m)
	if err != nil || !unloaded {
		t.Fatalf("ConfirmUnloaded = %v, %v", unloaded, err)
	}
	want := []string{
		"launchctl kickstart -k gui/501/example.agent",
		"launchctl print gui/501/example.agent",
		"launchctl bootout gui/501 /private/agent.plist",
		"launchctl print gui/501/example.agent",
	}
	if got := runner.calls(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("calls = %v, want %v", got, want)
	}
}

func TestOSRuntimeSupervisorObservationRejectsUnexpectedLaunchctlFailure(t *testing.T) {
	m := Manifest{EffectiveUID: 501, LaunchAgentLabel: "example.agent", WrapperPath: "/private/wrapper"}
	runtime := &OSRuntime{Runner: &recordingRunner{results: []CommandResult{{ExitCode: 64}}}}
	if _, err := runtime.ConfirmUnloaded(context.Background(), m); err == nil || err.Error() != "release supervisor observation failed" {
		t.Fatalf("ConfirmUnloaded error = %v", err)
	}
}

func TestOSRuntimeRestartRejectsUnsafeHealthMarkerBeforeLaunchctl(t *testing.T) {
	dir := t.TempDir()
	target := writeRuntimeFile(t, filepath.Join(dir, "target"), "health", 0o600)
	link := filepath.Join(dir, "health.url")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{}
	runtime := &OSRuntime{Runner: runner}
	m := Manifest{EffectiveUID: 501, LaunchAgentLabel: "example.agent", HealthURLFile: link}
	if err := runtime.Restart(context.Background(), m); err == nil || err.Error() != "release runtime restart failed" {
		t.Fatalf("Restart error = %v", err)
	}
	if got := runner.calls(); len(got) != 0 {
		t.Fatalf("unsafe marker caused launchctl calls: %v", got)
	}
}

func TestOSRuntimeWaitReadyIsBoundedAndInjected(t *testing.T) {
	dir := t.TempDir()
	m, _, _ := runtimeFixture(t, dir, false)
	m.ReadyTimeoutSeconds = 1
	m.ReadyPollMilliseconds = 250
	runner := &recordingRunner{results: []CommandResult{
		{Stdout: []byte("program = " + m.WrapperPath + "\n")},
		{Stdout: []byte("program = " + m.WrapperPath + "\n")},
		{Stdout: []byte("program = " + m.WrapperPath + "\n")},
	}}
	httpClient := &sequenceHTTPClient{statuses: []int{http.StatusServiceUnavailable, http.StatusOK, http.StatusServiceUnavailable, http.StatusOK, http.StatusOK}}
	var sleeps []time.Duration
	runtime := &OSRuntime{
		Runner:     runner,
		HTTPClient: httpClient,
		Sleep: func(_ context.Context, duration time.Duration) error {
			sleeps = append(sleeps, duration)
			return nil
		},
	}
	if err := runtime.WaitReady(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if got := len(runner.calls()); got != 3 {
		t.Fatalf("launch observations = %d, want 3", got)
	}
	if len(sleeps) != 2 || sleeps[0] != 250*time.Millisecond || sleeps[1] != 250*time.Millisecond {
		t.Fatalf("sleeps = %v", sleeps)
	}

	runner = &recordingRunner{fallback: CommandResult{Stdout: []byte("program = " + m.WrapperPath + "\n")}}
	httpClient = &sequenceHTTPClient{fallback: http.StatusServiceUnavailable}
	sleeps = nil
	runtime = &OSRuntime{Runner: runner, HTTPClient: httpClient, Sleep: func(_ context.Context, d time.Duration) error { sleeps = append(sleeps, d); return nil }}
	err := runtime.WaitReady(context.Background(), m)
	if err == nil || len(runner.calls()) != 4 || len(sleeps) != 3 {
		t.Fatalf("bounded failure err=%v calls=%d sleeps=%d", err, len(runner.calls()), len(sleeps))
	}
}

func TestOSRuntimeReadinessRejectsNonLoopbackURLWithoutHTTP(t *testing.T) {
	dir := t.TempDir()
	m, _, _ := runtimeFixture(t, dir, false)
	if err := os.WriteFile(m.HealthURLFile, []byte("https://example.com:443\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.ReadyTimeoutSeconds = 1
	m.ReadyPollMilliseconds = 1000
	httpClient := &sequenceHTTPClient{}
	runtime := &OSRuntime{
		Runner:     &recordingRunner{fallback: CommandResult{Stdout: []byte("program = " + m.WrapperPath + "\n")}},
		HTTPClient: httpClient,
		Sleep:      func(context.Context, time.Duration) error { return nil },
	}
	if err := runtime.WaitReady(context.Background(), m); err == nil {
		t.Fatal("WaitReady accepted non-loopback URL")
	}
	if got := len(httpClient.paths()); got != 0 {
		t.Fatalf("HTTP requests = %d, want zero", got)
	}
}

func TestOSRuntimeReadinessRejectsUnboundedManifestValuesBeforeEffects(t *testing.T) {
	dir := t.TempDir()
	m, _, _ := runtimeFixture(t, dir, false)
	runner := &recordingRunner{}
	runtime := &OSRuntime{Runner: runner}
	for _, mutate := range []func(*Manifest){
		func(m *Manifest) { m.ReadyTimeoutSeconds = int(maxReadyTimeout/time.Second) + 1 },
		func(m *Manifest) { m.ReadyPollMilliseconds = int(maxReadyPoll/time.Millisecond) + 1 },
	} {
		candidate := m
		mutate(&candidate)
		if err := runtime.WaitReady(context.Background(), candidate); err == nil {
			t.Fatal("WaitReady accepted an unbounded manifest value")
		}
	}
	if got := len(runner.calls()); got != 0 {
		t.Fatalf("unbounded manifests ran %d child effects", got)
	}
}

func TestOSRuntimeAdaptersCaptureOutputAndSanitizeFailure(t *testing.T) {
	dir := t.TempDir()
	adapter := writeRuntimeFile(t, filepath.Join(dir, "adapter"), "#!/bin/sh\nexit 1\n", 0o755)
	sensitive := "SECRET_CHILD_DIAGNOSTIC_" + dir
	runner := &recordingRunner{results: []CommandResult{{Stdout: []byte(sensitive), Stderr: []byte(sensitive), ExitCode: 9}}}
	runtime := &OSRuntime{Runner: runner}
	err := runtime.InvokeInstallAdapter(context.Background(), adapter, "resolved-home", "501")
	if err == nil || strings.Contains(err.Error(), sensitive) || strings.Contains(err.Error(), adapter) || err.Error() != "release launch agent installation failed" {
		t.Fatalf("adapter error = %q", err)
	}
	if got := runner.calls(); len(got) != 1 || got[0] != adapter+" resolved-home 501" {
		t.Fatalf("adapter calls = %v", got)
	}
}

func TestExecRunnerCapsCapturedOutputAndReturnsExitStatus(t *testing.T) {
	result, err := (execRunner{}).Run(context.Background(), "/bin/sh", "-c", "yes x | head -c 40000; yes y | head -c 40000 >&2; exit 7")
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 7 || !result.Truncated || len(result.Stdout) != maxChildOutput || len(result.Stderr) != maxChildOutput {
		t.Fatalf("result = exit:%d truncated:%v stdout:%d stderr:%d", result.ExitCode, result.Truncated, len(result.Stdout), len(result.Stderr))
	}
}

func TestExecRunnerBoundsInheritedOutputPipes(t *testing.T) {
	started := time.Now()
	result, err := (execRunner{maxDuration: 200 * time.Millisecond}).Run(
		context.Background(),
		"/bin/sh",
		"-c",
		"sleep 1 & printf 'parent stdout'; printf 'parent stderr' >&2",
	)
	elapsed := time.Since(started)

	if !errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("Run error = %v, want bounded inherited-pipe error", err)
	}
	if result.ExitCode != 0 || result.Truncated {
		t.Fatalf("result = exit:%d truncated:%v", result.ExitCode, result.Truncated)
	}
	if got := string(result.Stdout); got != "parent stdout" {
		t.Fatalf("stdout = %q", got)
	}
	if got := string(result.Stderr); got != "parent stderr" {
		t.Fatalf("stderr = %q", got)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("inherited-pipe child elapsed = %v, want a bounded return", elapsed)
	}
}

func TestOSRuntimeWaitReadyHonorsOneTotalDeadline(t *testing.T) {
	dir := t.TempDir()
	m, _, _ := runtimeFixture(t, dir, false)
	m.ReadyTimeoutSeconds = 1
	m.ReadyPollMilliseconds = 100
	runtime := &OSRuntime{
		Runner:     &recordingRunner{fallback: CommandResult{Stdout: []byte("program = " + m.WrapperPath + "\n")}},
		HTTPClient: blockingHTTPClient{},
		Sleep:      contextSleep,
	}
	started := time.Now()
	err := runtime.WaitReady(context.Background(), m)
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("WaitReady accepted a permanently stalled loopback probe")
	}
	if elapsed < 800*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("WaitReady elapsed = %v, want one 1s manifest deadline with scheduler tolerance", elapsed)
	}
}

type blockingHTTPClient struct{}

func (blockingHTTPClient) Do(request *http.Request) (*http.Response, error) {
	<-request.Context().Done()
	return nil, request.Context().Err()
}

func TestExecRunnerBoundsBlockingChildrenAndRespectsEarlierDeadline(t *testing.T) {
	for _, tt := range []struct {
		name       string
		parent     time.Duration
		child      time.Duration
		upperBound time.Duration
	}{
		{name: "fixed child maximum", child: 80 * time.Millisecond, upperBound: time.Second},
		{name: "earlier caller deadline", parent: 40 * time.Millisecond, child: time.Second, upperBound: time.Second},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			cancel := func() {}
			if tt.parent > 0 {
				ctx, cancel = context.WithTimeout(ctx, tt.parent)
			}
			defer cancel()
			started := time.Now()
			_, err := (execRunner{maxDuration: tt.child}).Run(ctx, "/bin/sh", "-c", "sleep 10")
			elapsed := time.Since(started)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("Run error = %v, want context deadline", err)
			}
			if elapsed > tt.upperBound {
				t.Fatalf("blocking child elapsed = %v, want <= %v", elapsed, tt.upperBound)
			}
		})
	}
}

func runtimeFixture(t *testing.T, dir string, previous bool) (Manifest, RuntimeArtifacts, string) {
	t.Helper()
	artifacts := RuntimeArtifacts{
		Candidate: writeRuntimeFile(t, filepath.Join(dir, "candidate"), "candidate", 0o500),
		Authority: writeRuntimeFile(t, filepath.Join(dir, "authority"), "authority", 0o500),
	}
	if previous {
		artifacts.Previous = writeRuntimeFile(t, filepath.Join(dir, "previous"), "previous", 0o500)
	}
	controller := writeRuntimeFile(t, filepath.Join(dir, "controller"), "authority", 0o500)
	m := Manifest{
		CandidateSHA256:       hashText("candidate"),
		AuthoritySHA256:       hashText("authority"),
		PreviousPresent:       previous,
		TargetPath:            filepath.Join(dir, "bin", "gateway"),
		EffectiveUID:          501,
		LaunchAgentLabel:      "example.agent",
		PlistPath:             writeRuntimeFile(t, filepath.Join(dir, "agent.plist"), "plist", 0o600),
		WrapperPath:           writeRuntimeFile(t, filepath.Join(dir, "wrapper"), "wrapper", 0o700),
		MCPWrapperPath:        writeRuntimeFile(t, filepath.Join(dir, "mcp-wrapper"), "mcp wrapper", 0o700),
		EnvironmentPath:       writeRuntimeFile(t, filepath.Join(dir, "environment"), "environment", 0o600),
		HealthURLFile:         writeRuntimeFile(t, filepath.Join(dir, "health.url"), "http://127.0.0.1:12345\n", 0o600),
		ReadyTimeoutSeconds:   2,
		ReadyPollMilliseconds: 100,
	}
	m.PlistSHA256 = hashText("plist")
	m.WrapperSHA256 = hashText("wrapper")
	m.MCPWrapperSHA256 = hashText("mcp wrapper")
	m.EnvironmentSHA256 = hashText("environment")
	if previous {
		m.PreviousSHA256 = hashText("previous")
	}
	if err := os.MkdirAll(filepath.Dir(m.TargetPath), 0o700); err != nil {
		t.Fatal(err)
	}
	return m, artifacts, controller
}

func writeRuntimeFile(t *testing.T, path, contents string, mode os.FileMode) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func hashText(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func assertRuntimeTarget(t *testing.T, path, wantContents, wantHash string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != wantContents {
		t.Fatalf("target = %q, %v; want %q", data, err, wantContents)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("target mode = %v, %v; want 0755", info.Mode().Perm(), err)
	}
	gotHash, err := HashRegular(path)
	if err != nil || gotHash != wantHash {
		t.Fatalf("target hash = %q, %v; want %q", gotHash, err, wantHash)
	}
}

func assertNoRuntimeTemps(t *testing.T, dir, targetBase string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "."+targetBase+".release.") {
			t.Fatalf("temporary target remains: %s", entry.Name())
		}
	}
}

type recordingRunner struct {
	mu       sync.Mutex
	results  []CommandResult
	fallback CommandResult
	err      error
	called   []string
}

func (r *recordingRunner) Run(_ context.Context, name string, args ...string) (CommandResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.called = append(r.called, strings.Join(append([]string{name}, args...), " "))
	if r.err != nil {
		return CommandResult{}, r.err
	}
	if len(r.results) == 0 {
		return r.fallback, nil
	}
	result := r.results[0]
	r.results = r.results[1:]
	return result, nil
}

func (r *recordingRunner) calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.called...)
}

type sequenceHTTPClient struct {
	mu       sync.Mutex
	statuses []int
	fallback int
	called   []string
}

func (c *sequenceHTTPClient) Do(request *http.Request) (*http.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.called = append(c.called, request.URL.Path)
	status := c.fallback
	if len(c.statuses) > 0 {
		status = c.statuses[0]
		c.statuses = c.statuses[1:]
	}
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("ok")), Header: make(http.Header)}, nil
}

func (c *sequenceHTTPClient) paths() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.called...)
}
