package releaseactivation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	maxChildOutput    = 32 << 10
	maxHealthURLBytes = 512
	launchctlNotFound = 113
	maxReadyTimeout   = 10 * time.Minute
	maxReadyPoll      = 30 * time.Second
	httpProbeTimeout  = 2 * time.Second
	maxHostChildTime  = 30 * time.Second
	maxChildWaitDelay = 100 * time.Millisecond
)

// RuntimeArtifacts names the immutable transaction artifacts. Paths stay
// explicit because Manifest deliberately records fixed relative artifact names.
type RuntimeArtifacts struct {
	Candidate string
	Authority string
	Previous  string
}

// Runtime owns bounded host observations and effects. Lifecycle policy and
// locking remain Manager responsibilities.
type Runtime interface {
	Observe(context.Context, Manifest, string, RuntimeArtifacts) (Observed, error)
	InstallCandidate(context.Context, Manifest, RuntimeArtifacts) error
	Restart(context.Context, Manifest) error
	WaitReady(context.Context, Manifest) error
	RestorePrevious(context.Context, Manifest, RuntimeArtifacts) error
	Bootout(context.Context, Manifest) error
	ConfirmUnloaded(context.Context, Manifest) (bool, error)
	RemoveTarget(context.Context, Manifest) error
	InvokeInstallAdapter(context.Context, string, ...string) error
	InvokeUninstallAdapter(context.Context, string, ...string) error
}

// CommandResult is captured in memory and is never written by Runtime.
type CommandResult struct {
	Stdout    []byte
	Stderr    []byte
	ExitCode  int
	Truncated bool
}

// Runner executes a child without giving it a caller-owned output writer.
type Runner interface {
	Run(context.Context, string, ...string) (CommandResult, error)
}

// HTTPClient is the narrow readiness-probe dependency.
type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

// SleepFunc makes readiness polling deterministic in tests.
type SleepFunc func(context.Context, time.Duration) error

// OSRuntime is the production host adapter. Its dependencies are exported only
// so isolated tests and alternate frontends can inject no-process fakes.
type OSRuntime struct {
	Runner     Runner
	HTTPClient HTTPClient
	Sleep      SleepFunc
}

// NewOSRuntime returns a bounded, writer-free production runtime.
func NewOSRuntime() *OSRuntime {
	return &OSRuntime{
		Runner:     execRunner{},
		HTTPClient: defaultHTTPClient(),
		Sleep:      contextSleep,
	}
}

// Observe gathers all facts required by the pure lifecycle model. A target
// that exists but is not a regular executable is an observation failure.
func (r *OSRuntime) Observe(ctx context.Context, m Manifest, controllerPath string, artifacts RuntimeArtifacts) (Observed, error) {
	var observed Observed
	var err error
	if observed.CandidateSHA256, err = hashRuntimeFile(artifacts.Candidate, false); err != nil {
		return Observed{}, runtimeFailure("observation", err)
	}
	if observed.AuthorityArtifactSHA256, err = hashRuntimeFile(artifacts.Authority, true); err != nil {
		return Observed{}, runtimeFailure("observation", err)
	}
	if filepath.Clean(controllerPath) == filepath.Clean(artifacts.Authority) {
		observed.ControllerSHA256 = observed.AuthorityArtifactSHA256
	} else {
		if observed.ControllerSHA256, err = hashRuntimeFile(controllerPath, true); err != nil {
			return Observed{}, runtimeFailure("observation", err)
		}
	}
	if m.PreviousPresent {
		if observed.PreviousSHA256, err = hashRuntimeFile(artifacts.Previous, false); err != nil {
			return Observed{}, runtimeFailure("observation", err)
		}
	}
	if observed.PlistSHA256, err = hashRuntimeFile(m.PlistPath, false); err != nil {
		return Observed{}, runtimeFailure("observation", err)
	}
	if observed.WrapperSHA256, err = hashRuntimeFile(m.WrapperPath, true); err != nil {
		return Observed{}, runtimeFailure("observation", err)
	}
	if observed.MCPWrapperSHA256, err = hashRuntimeFile(m.MCPWrapperPath, true); err != nil {
		return Observed{}, runtimeFailure("observation", err)
	}
	if observed.EnvironmentSHA256, err = hashRuntimeFile(m.EnvironmentPath, false); err != nil {
		return Observed{}, runtimeFailure("observation", err)
	}

	present, targetHash, err := observeTarget(m.TargetPath)
	if err != nil {
		return Observed{}, runtimeFailure("observation", err)
	}
	observed.InstalledPresent = present
	observed.InstalledSHA256 = targetHash

	loaded, err := r.launchAgentLoaded(ctx, m)
	if err != nil {
		return Observed{}, err
	}
	observed.SupervisorUnloaded = !loaded
	if loaded {
		observed.RuntimeReady = r.readyOnce(ctx, m)
	}
	return observed, nil
}

func (r *OSRuntime) InstallCandidate(ctx context.Context, m Manifest, artifacts RuntimeArtifacts) error {
	if err := ctx.Err(); err != nil {
		return runtimeFailure("candidate installation", err)
	}
	if err := replaceTarget(artifacts.Candidate, m.TargetPath, m.CandidateSHA256); err != nil {
		return runtimeFailure("candidate installation", err)
	}
	return nil
}

func (r *OSRuntime) RestorePrevious(ctx context.Context, m Manifest, artifacts RuntimeArtifacts) error {
	if err := ctx.Err(); err != nil {
		return runtimeFailure("previous runtime restoration", err)
	}
	if !m.PreviousPresent || artifacts.Previous == "" {
		return runtimeFailure("previous runtime restoration", errors.New("previous artifact is absent"))
	}
	if err := replaceTarget(artifacts.Previous, m.TargetPath, m.PreviousSHA256); err != nil {
		return runtimeFailure("previous runtime restoration", err)
	}
	return nil
}

func (r *OSRuntime) RemoveTarget(ctx context.Context, m Manifest) error {
	if err := ctx.Err(); err != nil {
		return runtimeFailure("target removal", err)
	}
	info, err := os.Lstat(m.TargetPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return runtimeFailure("target removal", errors.New("target is not a regular file"))
	}
	if err := os.Remove(m.TargetPath); err != nil {
		return runtimeFailure("target removal", err)
	}
	if err := syncDir(filepath.Dir(m.TargetPath)); err != nil {
		return runtimeFailure("target removal", err)
	}
	return nil
}

func (r *OSRuntime) Restart(ctx context.Context, m Manifest) error {
	if err := removeHealthMarker(m.HealthURLFile); err != nil {
		return runtimeFailure("runtime restart", err)
	}
	result, err := r.run(ctx, "launchctl", "kickstart", "-k", launchService(m))
	if err != nil || result.ExitCode != 0 {
		return runtimeFailure("runtime restart", errors.Join(err, exitError(result.ExitCode)))
	}
	return nil
}

func removeHealthMarker(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("health URL file path is not absolute")
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("health URL file is not a regular file")
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

func (r *OSRuntime) Bootout(ctx context.Context, m Manifest) error {
	loaded, err := r.launchAgentLoaded(ctx, m)
	if err != nil {
		return err
	}
	if !loaded {
		return nil
	}
	result, err := r.run(ctx, "launchctl", "bootout", "gui/"+strconv.Itoa(m.EffectiveUID), m.PlistPath)
	if err != nil || result.ExitCode != 0 {
		return runtimeFailure("runtime bootout", errors.Join(err, exitError(result.ExitCode)))
	}
	return nil
}

func (r *OSRuntime) ConfirmUnloaded(ctx context.Context, m Manifest) (bool, error) {
	loaded, err := r.launchAgentLoaded(ctx, m)
	if err != nil {
		return false, err
	}
	return !loaded, nil
}

// WaitReady uses one manifest-derived deadline for every launchctl call, HTTP
// probe, and sleep. The attempt cap is a second bound for deterministic fakes;
// it does not extend the wall-clock deadline.
func (r *OSRuntime) WaitReady(ctx context.Context, m Manifest) error {
	if m.ReadyTimeoutSeconds <= 0 || m.ReadyTimeoutSeconds > int(maxReadyTimeout/time.Second) ||
		m.ReadyPollMilliseconds <= 0 || m.ReadyPollMilliseconds > int(maxReadyPoll/time.Millisecond) {
		return runtimeFailure("readiness", errors.New("invalid readiness bounds"))
	}
	timeout := time.Duration(m.ReadyTimeoutSeconds) * time.Second
	poll := time.Duration(m.ReadyPollMilliseconds) * time.Millisecond
	readyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	attempts := int((timeout + poll - 1) / poll)
	for attempt := 0; attempt < attempts; attempt++ {
		if err := readyCtx.Err(); err != nil {
			return runtimeFailure("readiness", err)
		}
		loaded, err := r.launchAgentLoaded(readyCtx, m)
		if err != nil {
			return err
		}
		if loaded && r.readyOnce(readyCtx, m) {
			return nil
		}
		if err := readyCtx.Err(); err != nil {
			return runtimeFailure("readiness", err)
		}
		if attempt+1 < attempts {
			if err := r.sleep(readyCtx, poll); err != nil {
				return runtimeFailure("readiness", err)
			}
		}
	}
	return runtimeFailure("readiness", errors.New("bounded readiness exhausted"))
}

func (r *OSRuntime) InvokeInstallAdapter(ctx context.Context, adapterPath string, args ...string) error {
	return r.invokeAdapter(ctx, adapterPath, "launch agent installation", args...)
}

func (r *OSRuntime) InvokeUninstallAdapter(ctx context.Context, adapterPath string, args ...string) error {
	return r.invokeAdapter(ctx, adapterPath, "launch agent uninstallation", args...)
}

func (r *OSRuntime) invokeAdapter(ctx context.Context, adapterPath, operation string, args ...string) error {
	if !filepath.IsAbs(adapterPath) {
		return runtimeFailure(operation, errors.New("adapter path is not absolute"))
	}
	if _, err := hashRuntimeFile(adapterPath, true); err != nil {
		return runtimeFailure(operation, err)
	}
	result, err := r.run(ctx, adapterPath, args...)
	if err != nil || result.ExitCode != 0 {
		return runtimeFailure(operation, errors.Join(err, exitError(result.ExitCode)))
	}
	return nil
}

func (r *OSRuntime) launchAgentLoaded(ctx context.Context, m Manifest) (bool, error) {
	result, err := r.run(ctx, "launchctl", "print", launchService(m))
	if err != nil {
		return false, runtimeFailure("supervisor observation", err)
	}
	if result.ExitCode != 0 {
		if result.ExitCode == launchctlNotFound {
			return false, nil
		}
		return false, runtimeFailure("supervisor observation", exitError(result.ExitCode))
	}
	if !programMatches(result.Stdout, m.WrapperPath) {
		return false, lifecycleError(ErrorRuntimeDrift)
	}
	return true, nil
}

func (r *OSRuntime) readyOnce(ctx context.Context, m Manifest) bool {
	base, err := readHealthURL(m.HealthURLFile)
	if err != nil {
		return false
	}
	for _, path := range []string{"/healthz", "/readyz"} {
		probeCtx, cancel := context.WithTimeout(ctx, httpProbeTimeout)
		req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, base+path, nil)
		if err != nil {
			cancel()
			return false
		}
		response, err := r.httpClient().Do(req)
		if err != nil {
			cancel()
			return false
		}
		if response == nil || response.Body == nil {
			cancel()
			return false
		}
		_, _ = io.CopyN(io.Discard, response.Body, 4096)
		closeErr := response.Body.Close()
		cancel()
		if response.StatusCode != http.StatusOK || closeErr != nil {
			return false
		}
	}
	return true
}

func (r *OSRuntime) run(ctx context.Context, name string, args ...string) (CommandResult, error) {
	if r == nil || r.Runner == nil {
		return CommandResult{}, errors.New("runtime runner is not configured")
	}
	return r.Runner.Run(ctx, name, args...)
}

func (r *OSRuntime) httpClient() HTTPClient {
	if r != nil && r.HTTPClient != nil {
		return r.HTTPClient
	}
	return defaultHTTPClient()
}

func defaultHTTPClient() *http.Client {
	return &http.Client{
		Timeout: httpProbeTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func (r *OSRuntime) sleep(ctx context.Context, duration time.Duration) error {
	if r != nil && r.Sleep != nil {
		return r.Sleep(ctx, duration)
	}
	return contextSleep(ctx, duration)
}

func launchService(m Manifest) string {
	return "gui/" + strconv.Itoa(m.EffectiveUID) + "/" + m.LaunchAgentLabel
}

func programMatches(output []byte, expected string) bool {
	for _, line := range strings.Split(string(output), "\n") {
		if strings.TrimSpace(line) == "program = "+expected {
			return true
		}
	}
	return false
}

func readHealthURL(path string) (string, error) {
	data, err := readRegularAnyMode(path, maxHealthURLBytes)
	if err != nil {
		return "", err
	}
	raw := strings.TrimSpace(strings.SplitN(string(data), "\n", 2)[0])
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" && parsed.Path != "/" {
		return "", errors.New("health URL is not strict loopback HTTP")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 {
		return "", errors.New("health URL has invalid port")
	}
	return "http://127.0.0.1:" + strconv.Itoa(port), nil
}

func observeTarget(path string) (bool, string, error) {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	hash, err := hashRuntimeFile(path, true)
	if err != nil {
		return false, "", err
	}
	return true, hash, nil
}

func hashRuntimeFile(path string, executable bool) (string, error) {
	if path == "" || !filepath.IsAbs(path) {
		return "", errors.New("runtime path is invalid")
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return "", err
	}
	linked, err := os.Lstat(path)
	if err != nil || !opened.Mode().IsRegular() || linked.Mode()&os.ModeSymlink != 0 || !linked.Mode().IsRegular() || !os.SameFile(opened, linked) {
		return "", errors.New("runtime path is not a regular file")
	}
	if executable && opened.Mode().Perm()&0o111 == 0 {
		return "", errors.New("runtime file is not executable")
	}
	if opened.Size() > maxArtifactSize {
		return "", errors.New("runtime file exceeds size limit")
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, maxArtifactSize+1))
	if err != nil || n > maxArtifactSize {
		return "", errors.New("runtime file could not be hashed")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func replaceTarget(source, target, wantHash string) error {
	if !filepath.IsAbs(source) || !filepath.IsAbs(target) || !validSHA256(wantHash) {
		return errors.New("replacement inputs are invalid")
	}
	parent := filepath.Dir(target)
	suffix, err := randomHex(12)
	if err != nil {
		return err
	}
	temporary := filepath.Join(parent, "."+filepath.Base(target)+".release."+suffix)
	hash, err := copyRegular(source, temporary, 0o755)
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(temporary)
		}
	}()
	if hash != wantHash {
		return errors.New("replacement source hash does not match")
	}
	if err := os.Rename(temporary, target); err != nil {
		return err
	}
	keep = true
	if err := syncDir(parent); err != nil {
		return err
	}
	installedHash, err := hashRuntimeFile(target, true)
	if err != nil || installedHash != wantHash {
		return errors.New("replacement could not be confirmed")
	}
	return nil
}

func readRegularAnyMode(path string, limit int64) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	linked, err := os.Lstat(path)
	if err != nil || !opened.Mode().IsRegular() || linked.Mode()&os.ModeSymlink != 0 || !linked.Mode().IsRegular() || !os.SameFile(opened, linked) {
		return nil, errors.New("path is not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("file exceeds read limit")
	}
	return data, nil
}

type runtimeError struct {
	operation string
	cause     error
}

func (e *runtimeError) Error() string { return "release " + e.operation + " failed" }
func (e *runtimeError) Unwrap() error { return e.cause }

func runtimeFailure(operation string, cause error) error {
	return &runtimeError{operation: operation, cause: cause}
}

func exitError(code int) error {
	if code == 0 {
		return nil
	}
	return fmt.Errorf("child exited with status %d", code)
}

type execRunner struct {
	maxDuration time.Duration
}

func (r execRunner) Run(ctx context.Context, name string, args ...string) (CommandResult, error) {
	maximum := r.maxDuration
	if maximum <= 0 || maximum > maxHostChildTime {
		maximum = maxHostChildTime
	}
	if err := ctx.Err(); err != nil {
		return CommandResult{}, err
	}

	// Reserve a bounded portion of the total command budget for Cmd.Wait to
	// close inherited output pipes. Without WaitDelay, a successfully exited
	// adapter can leave Run blocked indefinitely when one of its descendants
	// retains stdout or stderr.
	deadline := time.Now().Add(maximum)
	if callerDeadline, ok := ctx.Deadline(); ok && callerDeadline.Before(deadline) {
		deadline = callerDeadline
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return CommandResult{}, context.DeadlineExceeded
	}
	waitDelay := min(maxChildWaitDelay, remaining/4)
	if waitDelay <= 0 {
		waitDelay = time.Nanosecond
	}
	childCtx, cancel := context.WithDeadline(ctx, deadline.Add(-waitDelay))
	defer cancel()
	command := exec.CommandContext(childCtx, name, args...)
	command.WaitDelay = waitDelay
	var stdout, stderr cappedBuffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	result := CommandResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), ExitCode: 0, Truncated: stdout.truncated || stderr.truncated}
	if err == nil {
		return result, nil
	}
	if childCtx.Err() != nil {
		return result, childCtx.Err()
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		result.ExitCode = exit.ExitCode()
		return result, nil
	}
	return result, err
}

type cappedBuffer struct {
	buffer    bytes.Buffer
	truncated bool
}

func (b *cappedBuffer) Write(data []byte) (int, error) {
	original := len(data)
	remaining := maxChildOutput - b.buffer.Len()
	if remaining <= 0 {
		b.truncated = b.truncated || original > 0
		return original, nil
	}
	if len(data) > remaining {
		data = data[:remaining]
		b.truncated = true
	}
	_, _ = b.buffer.Write(data)
	return original, nil
}

func (b *cappedBuffer) Bytes() []byte { return bytes.Clone(b.buffer.Bytes()) }

func contextSleep(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
