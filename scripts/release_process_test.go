package scripts_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestReleasePreflightRequiresCleanCommittedTree(t *testing.T) {
	repo := newScriptRepo(t, "release-preflight.sh")
	binDir := filepath.Join(repo, "bin")
	writeExecutable(t, filepath.Join(binDir, "git"), `#!/bin/sh
case "$*" in
  *"status --porcelain"*)
    if [ "${FAKE_GIT_STATUS_ERROR:-0}" = 1 ]; then exit 3; fi
    printf '%s' "${FAKE_GIT_STATUS:-}"
    ;;
  *"rev-parse --verify HEAD"*) printf '%s\n' 0123456789abcdef0123456789abcdef01234567 ;;
  *) exit 2 ;;
esac
`)

	cmd := exec.Command(filepath.Join(repo, "scripts", "release-preflight.sh"))
	cmd.Env = testEnv(binDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("clean preflight failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "release preflight passed for commit 0123456789ab") {
		t.Fatalf("clean preflight output = %q", output)
	}

	cmd = exec.Command(filepath.Join(repo, "scripts", "release-preflight.sh"))
	cmd.Env = append(testEnv(binDir), "FAKE_GIT_STATUS= M README.md")
	output, err = cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("dirty preflight succeeded: %s", output)
	}
	if !strings.Contains(string(output), "working tree must be clean") {
		t.Fatalf("dirty preflight output = %q", output)
	}

	cmd = exec.Command(filepath.Join(repo, "scripts", "release-preflight.sh"))
	cmd.Env = append(testEnv(binDir), "FAKE_GIT_STATUS_ERROR=1")
	output, err = cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "unable to inspect the working tree") {
		t.Fatalf("failed-status preflight err=%v output=%q", err, output)
	}
}

func TestMakeReleaseCommandsPreserveScriptRecordsAndUsageSuffix(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	sourceRoot := filepath.Dir(filepath.Dir(currentFile))
	repo := t.TempDir()
	makefile, err := os.ReadFile(filepath.Join(sourceRoot, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "Makefile"), makefile, 0o600); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(repo, "scripts", "release-activation.sh"), `#!/bin/sh
case "$1" in
  status) printf '%s\n' state=clear ;;
  accept|rollback)
    if [ -z "$3" ]; then printf '%s\n' 'error=usage message=invalid release command' >&2; exit 2; fi
    printf '%s\n' state=clear
    ;;
  install-launchagent) printf '%s\n' called >>"$TEST_ADAPTER_LOG" ;;
  *) exit 2 ;;
esac
`)
	writeExecutable(t, filepath.Join(repo, "scripts", "update-local.sh"), `#!/bin/sh
printf '%s\n' 'error=release_test_failed message=release tests failed' >&2
exit 1
`)
	writeExecutable(t, filepath.Join(repo, "scripts", "release-local.sh"), `#!/bin/sh
printf '%s\n' 'error=state_conflict message=event conflicts with release state' >&2
printf '%s\n' 'state=pending id=release-0001 commit=0123456789ab sha256=candidate0000' >&2
printf '%s\n' 'accept=make release-accept RELEASE_ID=release-0001' >&2
printf '%s\n' 'rollback=make release-rollback RELEASE_ID=release-0001' >&2
exit 1
`)
	configHelper, err := os.ReadFile(filepath.Join(sourceRoot, "scripts", "internal", "release-config.sh"))
	if err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(repo, "scripts", "internal", "release-config.sh"), string(configHelper))

	run := func(args ...string) (string, string, int) {
		t.Helper()
		cmd := exec.Command("make", append([]string{"--no-print-directory", "-C", repo}, args...)...)
		for _, value := range os.Environ() {
			if !strings.HasPrefix(value, "MAKELEVEL=") && !strings.HasPrefix(value, "MAKEFLAGS=") && !strings.HasPrefix(value, "MFLAGS=") {
				cmd.Env = append(cmd.Env, value)
			}
		}
		cmd.Env = append(cmd.Env, "TEST_ADAPTER_LOG="+filepath.Join(repo, "adapter.log"))
		var stdout, stderr strings.Builder
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		runErr := cmd.Run()
		exit := 0
		if runErr != nil {
			var exitError *exec.ExitError
			if !errors.As(runErr, &exitError) {
				t.Fatalf("make start failed: %v", runErr)
			}
			exit = exitError.ExitCode()
		}
		return stdout.String(), stderr.String(), exit
	}

	stdout, stderr, exit := run("release-status")
	if exit != 0 || stdout != "state=clear\n" || stderr != "" {
		t.Fatalf("status exit=%d stdout=%q stderr=%q", exit, stdout, stderr)
	}
	stdout, stderr, exit = run("release-accept", "RELEASE_ID="+strings.Repeat("a", 64))
	if exit != 0 || stdout != "state=clear\n" || stderr != "" {
		t.Fatalf("accept exit=%d stdout=%q stderr=%q", exit, stdout, stderr)
	}
	stdout, stderr, exit = run("release-rollback")
	if exit != 2 || stdout != "" || stderr != "error=usage message=invalid release command\nmake: *** [release-rollback] Error 2\n" {
		t.Fatalf("usage exit=%d stdout=%q stderr=%q", exit, stdout, stderr)
	}
	stdout, stderr, exit = run("update")
	if exit != 2 || stdout != "" || stderr != "error=release_test_failed message=release tests failed\nmake: *** [update] Error 1\n" {
		t.Fatalf("update exit=%d stdout=%q stderr=%q", exit, stdout, stderr)
	}
	stdout, stderr, exit = run("release")
	wantActiveRelease := "error=state_conflict message=event conflicts with release state\n" +
		"state=pending id=release-0001 commit=0123456789ab sha256=candidate0000\n" +
		"accept=make release-accept RELEASE_ID=release-0001\n" +
		"rollback=make release-rollback RELEASE_ID=release-0001\n" +
		"make: *** [release] Error 1\n"
	if exit != 2 || stdout != "" || stderr != wantActiveRelease {
		t.Fatalf("active release exit=%d stdout=%q stderr=%q want stderr=%q", exit, stdout, stderr, wantActiveRelease)
	}

	fakeGo := filepath.Join(repo, "fake-go")
	writeExecutable(t, fakeGo, "#!/bin/sh\nexit 0\n")
	sentinel := filepath.Join(repo, "restart-env-executed")
	if err := os.WriteFile(filepath.Join(repo, ".env.local"), []byte("TUNNEL_HEALTH_URL_FILE=$(touch "+sentinel+")\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, exit = run("restart", "GO="+fakeGo)
	if exit != 2 || stdout != "" || stderr != "error=release_config message=release configuration is invalid\nmake: *** [restart] Error 1\n" {
		t.Fatalf("restart exit=%d stdout=%q stderr=%q", exit, stdout, stderr)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("restart executed environment-file content: %v", err)
	}
	stdout, stderr, exit = run("install-launchagent", "GO="+fakeGo)
	if exit != 2 || stdout != "" || stderr != "error=release_config message=release configuration is invalid\nmake: *** [install-launchagent] Error 1\n" {
		t.Fatalf("install exit=%d stdout=%q stderr=%q", exit, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(repo, "adapter.log")); !os.IsNotExist(err) {
		t.Fatalf("invalid install configuration reached adapter: %v", err)
	}
}

func TestVerifyLiveChecksLaunchAgentLivenessAndReadiness(t *testing.T) {
	var paths []string
	var pathsMu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pathsMu.Lock()
		paths = append(paths, r.URL.Path)
		pathsMu.Unlock()
		switch r.URL.Path {
		case "/healthz", "/readyz":
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	repo := newScriptRepo(t, "verify-live.sh")
	binDir := filepath.Join(repo, "bin")
	launchctlLog := filepath.Join(repo, "launchctl.log")
	writeExecutable(t, filepath.Join(binDir, "launchctl"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >>\"$LAUNCHCTL_LOG\"\nprintf 'program = %s/scripts/run-obsidian-tunnel.sh\\n' \"$TEST_REPO\"\nexit 0\n")
	healthFile := filepath.Join(t.TempDir(), "health.url")
	if err := os.WriteFile(healthFile, []byte(server.URL+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(filepath.Join(repo, "scripts", "verify-live.sh"))
	cmd.Env = append(testEnv(binDir),
		"MCP_GATEWAY_ENV_FILE="+filepath.Join(t.TempDir(), "missing.env"),
		"TUNNEL_HEALTH_URL_FILE="+healthFile,
		"RELEASE_READY_TIMEOUT_SECONDS=2",
		"RELEASE_READY_POLL_SECONDS=0.01",
		"LAUNCHCTL_LOG="+launchctlLog,
		"TEST_REPO="+repo,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("live verification failed: %v\n%s", err, output)
	}
	pathsMu.Lock()
	got := strings.Join(paths, ",")
	pathsMu.Unlock()
	if got != "/healthz,/readyz" {
		t.Fatalf("probed paths = %q, want healthz and readyz", got)
	}
	assertFileContains(t, launchctlLog, "print gui/")
}

func TestVerifyLiveTreatsEnvironmentFileAsData(t *testing.T) {
	repo := newScriptRepo(t, "verify-live.sh")
	envFile := filepath.Join(repo, ".env.local")
	sentinel := filepath.Join(repo, "verify-env-executed")
	if err := os.WriteFile(envFile, []byte("TUNNEL_HEALTH_URL_FILE=$(touch "+sentinel+")\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join(repo, "scripts", "verify-live.sh"))
	cmd.Env = append(testEnv(filepath.Join(repo, "bin")), "MCP_GATEWAY_ENV_FILE="+envFile)
	output, err := cmd.CombinedOutput()
	if err == nil || string(output) != "personal-mcp-gateway verification: local environment configuration is invalid.\n" {
		t.Fatalf("verify hostile config err=%v output=%q", err, output)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("verify-live executed environment-file content: %v", err)
	}
}

func TestVerifyLiveRejectsDegradedReadiness(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Error(w, "not ready", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	repo := newScriptRepo(t, "verify-live.sh")
	binDir := filepath.Join(repo, "bin")
	writeExecutable(t, filepath.Join(binDir, "launchctl"), "#!/bin/sh\nprintf 'program = %s/scripts/run-obsidian-tunnel.sh\\n' \"$TEST_REPO\"\nexit 0\n")
	healthFile := filepath.Join(t.TempDir(), "health.url")
	if err := os.WriteFile(healthFile, []byte(server.URL+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(filepath.Join(repo, "scripts", "verify-live.sh"))
	cmd.Env = append(testEnv(binDir),
		"MCP_GATEWAY_ENV_FILE="+filepath.Join(t.TempDir(), "missing.env"),
		"TUNNEL_HEALTH_URL_FILE="+healthFile,
		"RELEASE_READY_TIMEOUT_SECONDS=1",
		"RELEASE_READY_POLL_SECONDS=0.01",
		"TEST_REPO="+repo,
	)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("verification accepted degraded readiness: %s", output)
	}
	if !strings.Contains(string(output), "did not become live and ready") {
		t.Fatalf("degraded verification output = %q", output)
	}
}

func TestVerifyLiveRejectsUnloadedLaunchAgent(t *testing.T) {
	repo := newScriptRepo(t, "verify-live.sh")
	binDir := filepath.Join(repo, "bin")
	writeExecutable(t, filepath.Join(binDir, "launchctl"), "#!/bin/sh\nexit 3\n")
	cmd := exec.Command(filepath.Join(repo, "scripts", "verify-live.sh"))
	cmd.Env = append(testEnv(binDir),
		"MCP_GATEWAY_ENV_FILE="+filepath.Join(t.TempDir(), "missing.env"),
		"RELEASE_READY_TIMEOUT_SECONDS=1",
	)
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "LaunchAgent is not loaded") {
		t.Fatalf("unloaded verification err=%v output=%q", err, output)
	}
}

func TestVerifyLiveRejectsLaunchAgentFromAnotherCheckout(t *testing.T) {
	repo := newScriptRepo(t, "verify-live.sh")
	binDir := filepath.Join(repo, "bin")
	writeExecutable(t, filepath.Join(binDir, "launchctl"), "#!/bin/sh\nprintf 'program = /tmp/other-checkout/scripts/run-obsidian-tunnel.sh\\n'\n")
	cmd := exec.Command(filepath.Join(repo, "scripts", "verify-live.sh"))
	cmd.Env = append(testEnv(binDir),
		"MCP_GATEWAY_ENV_FILE="+filepath.Join(t.TempDir(), "missing.env"),
		"RELEASE_READY_TIMEOUT_SECONDS=1",
	)
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "does not use this repo's tunnel wrapper") {
		t.Fatalf("wrong-checkout verification err=%v output=%q", err, output)
	}
}

func TestLocalReleaseInstallsExactCandidate(t *testing.T) {
	harness := newLocalReleaseHarness(t, releaseOptions{previous: true})
	stdout, stderr, exit := harness.runChannels()
	if exit != 0 || stderr != "" {
		t.Fatalf("release exit=%d stdout=%q stderr=%q", exit, stdout, stderr)
	}
	repo, target, logFile, verifyCount := harness.repo, harness.target, harness.logFile, harness.verifyCount
	output := []byte(stdout)
	assertFileContains(t, target, "candidate-binary")
	if strings.Contains(string(output), "runtime-secret") {
		t.Fatalf("release output leaked configured secret: %s", output)
	}
	if strings.Contains(string(output), "parent-runtime-secret") {
		t.Fatalf("release output leaked inherited secret: %s", output)
	}
	if strings.Contains(string(output), repo) {
		t.Fatalf("release output leaked a private path: %s", output)
	}
	active := releaseActiveDir(repo)
	candidateIdentity, err := os.ReadFile(filepath.Join(active, "candidate-sha256"))
	if err != nil {
		t.Fatal(err)
	}
	dependencyIdentity, err := os.ReadFile(filepath.Join(active, "dependency-sha256"))
	if err != nil {
		t.Fatal(err)
	}
	commitIdentity, err := os.ReadFile(filepath.Join(active, "commit"))
	if err != nil {
		t.Fatal(err)
	}
	reportTuple, err := os.ReadFile(filepath.Join(repo, "report-set-tuple"))
	if err != nil {
		t.Fatal(err)
	}
	wantTuple := string(commitIdentity) + "\n" + string(candidateIdentity) + "\n" + string(dependencyIdentity) + "\n"
	if string(reportTuple) != wantTuple {
		t.Fatalf("validated report tuple = %q, pending manifest tuple = %q", reportTuple, wantTuple)
	}
	wantOutput := "state=pending id=release-0001 commit=0123456789ab sha256=" + string(candidateIdentity[:12]) +
		" dependency_sha256=" + string(dependencyIdentity[:12]) + "\n" +
		"accept=make release-accept RELEASE_ID=release-0001\n" +
		"rollback=make release-rollback RELEASE_ID=release-0001\n"
	if string(output) != wantOutput {
		t.Fatalf("release output = %q, want %q", output, wantOutput)
	}
	assertFileContains(t, logFile, "make:test\nmake:build\nmake:build-release-controller\ngo:run ./cmd/gateway-smoke")
	logData, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(logData), "go:run ./cmd/gateway-smoke"); got != 4 ||
		!strings.Contains(string(logData), "--report-json") || !strings.Contains(string(logData), "--performance-json") ||
		!strings.Contains(string(logData), "--resource-json") || !strings.Contains(string(logData), "--validate-report-set") ||
		strings.Count(string(logData), "--candidate-commit 0123456789abcdef0123456789abcdef01234567") != 4 ||
		strings.Count(string(logData), "--candidate-sha256 "+string(candidateIdentity)) != 4 ||
		strings.Count(string(logData), "--dependency-sha256 "+string(dependencyIdentity)) != 4 {
		t.Fatalf("candidate smoke calls = %q", logData)
	}
	assertFileContains(t, logFile, "launchctl:kickstart -k")
	assertFileContains(t, verifyCount, "1")
	assertFileContains(t, filepath.Join(active, "state"), "pending")
	assertFileContains(t, filepath.Join(active, "previous"), "previous-binary")
	assertFileContains(t, filepath.Join(active, "candidate"), "candidate-binary")
	if info, statErr := os.Stat(filepath.Join(active, "authority")); statErr != nil || info.Mode()&0o111 == 0 {
		t.Fatalf("pinned authority is not executable: info=%v err=%v", info, statErr)
	}
	snapshotPath, err := os.ReadFile(harness.smokeCandidateRecord)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(string(snapshotPath)); !os.IsNotExist(err) {
		t.Fatalf("private smoke candidate survived release cleanup: %v", err)
	}
}

func TestLocalReleaseBoundsReportsWithoutLimitingPrivateGoArtifacts(t *testing.T) {
	harness := newLocalReleaseHarness(t, releaseOptions{previous: true, largePrivateSmokeArtifact: true})
	stdout, stderr, exit := harness.runChannels()
	if exit != 0 || stderr != "" || !strings.HasPrefix(stdout, "state=pending id=release-0001") {
		t.Fatalf("release exit=%d stdout=%q stderr=%q", exit, stdout, stderr)
	}
	info, err := os.Stat(filepath.Join(harness.repo, "private-smoke-artifact"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 4<<20 {
		t.Fatalf("private smoke artifact size = %d, want %d", info.Size(), 4<<20)
	}
}

func TestLocalReleasePinsDefaultHealthMarkerOutsideCallerTMPDIR(t *testing.T) {
	harness := newLocalReleaseHarness(t, releaseOptions{previous: true})
	callerTMP := filepath.Join(harness.repo, "caller-tmp")
	if err := os.MkdirAll(callerTMP, 0o700); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, exit := harness.runChannels(
		"TUNNEL_HEALTH_URL_FILE=",
		"TMPDIR="+callerTMP,
	)
	if exit != 0 || stderr != "" || !strings.HasPrefix(stdout, "state=pending id=release-0001") {
		t.Fatalf("release exit=%d stdout=%q stderr=%q", exit, stdout, stderr)
	}
	assertFileContains(t, harness.logFile, "health:/tmp/personal-mcp-gateway/tunnel-health.url\n")
	entries, err := os.ReadDir(callerTMP)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("private report capture was not cleaned: %v", entries)
	}
}

func TestLocalReleaseSuppressesHostileGateOutput(t *testing.T) {
	harness := newLocalReleaseHarness(t, releaseOptions{previous: true, failTests: true})
	stdout, stderr, exit := harness.runChannels()
	if exit != 1 || stdout != "" || stderr != "error=release_test_failed message=release tests failed\n" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exit, stdout, stderr)
	}
	for _, sentinel := range []string{"runtime-secret", "private-sentinel", harness.repo} {
		if strings.Contains(stdout+stderr, sentinel) {
			t.Fatalf("public output leaked %q: stdout=%q stderr=%q", sentinel, stdout, stderr)
		}
	}
}

func TestLocalReleaseStopsBeforeActivationWhenPerformanceSmokeFails(t *testing.T) {
	harness := newLocalReleaseHarness(t, releaseOptions{previous: true, failPerformanceSmoke: true})
	stdout, stderr, exit := harness.runChannels()
	if exit != 1 || stdout != "" || stderr != "error=release_smoke_failed message=release candidate performance smoke failed\n" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exit, stdout, stderr)
	}
	assertFileContains(t, harness.target, "previous-binary")
	if _, err := os.Stat(releaseActiveDir(harness.repo)); !os.IsNotExist(err) {
		t.Fatalf("failed performance smoke created release state: %v", err)
	}
	logData, err := os.ReadFile(harness.logFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(logData), "go:run ./cmd/gateway-smoke"); got != 2 ||
		!strings.Contains(string(logData), "--performance-json") {
		t.Fatalf("candidate smoke calls = %q", logData)
	}
}

func TestLocalReleaseStopsBeforeActivationWhenResourceSmokeFails(t *testing.T) {
	harness := newLocalReleaseHarness(t, releaseOptions{previous: true, failResourceSmoke: true})
	stdout, stderr, exit := harness.runChannels()
	if exit != 1 || stdout != "" || stderr != "error=release_smoke_failed message=release candidate resource smoke failed\n" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exit, stdout, stderr)
	}
	assertFileContains(t, harness.target, "previous-binary")
	if _, err := os.Stat(releaseActiveDir(harness.repo)); !os.IsNotExist(err) {
		t.Fatalf("failed resource smoke created release state: %v", err)
	}
	logData, err := os.ReadFile(harness.logFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(logData), "go:run ./cmd/gateway-smoke"); got != 3 ||
		!strings.Contains(string(logData), "--performance-json") || !strings.Contains(string(logData), "--resource-json") {
		t.Fatalf("candidate smoke calls = %q", logData)
	}
}

func TestLocalReleaseRestoresPreviousBinaryWhenReadinessFails(t *testing.T) {
	repo, target, logFile, verifyCount, output, err := runLocalRelease(t, releaseOptions{
		previous:              true,
		failFirstVerification: true,
	})
	if err == nil {
		t.Fatalf("release unexpectedly succeeded: %s", output)
	}
	assertFileContains(t, target, "previous-binary")
	assertFileContains(t, verifyCount, "2")
	logData, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(logData), "launchctl:kickstart -k"); got != 2 {
		t.Fatalf("kickstart count = %d, want candidate restart plus rollback restart; log=%s", got, logData)
	}
	if !strings.Contains(string(output), "error=deployment_failed") {
		t.Fatalf("rollback output = %q", output)
	}
	if _, statErr := os.Stat(releaseActiveDir(repo)); !os.IsNotExist(statErr) {
		t.Fatalf("successful recovery retained active state: %v", statErr)
	}
}

func TestLocalReleaseRetainsRollbackCopyWhenRecoveryCannotBeConfirmed(t *testing.T) {
	repo, target, _, _, output, err := runLocalRelease(t, releaseOptions{
		previous:            true,
		failAllVerification: true,
	})
	if err == nil {
		t.Fatalf("release unexpectedly succeeded: %s", output)
	}
	assertFileContains(t, target, "previous-binary")
	if !strings.Contains(string(output), "runtime recovery is unconfirmed") {
		t.Fatalf("unconfirmed recovery output = %q", output)
	}
	active := releaseActiveDir(repo)
	assertFileContains(t, filepath.Join(active, "state"), "rolling_back")
	assertFileContains(t, filepath.Join(active, "previous"), "previous-binary")
}

func TestLocalReleaseRetainsRollbackCopyWhenRestoreFails(t *testing.T) {
	repo, target, _, _, output, err := runLocalRelease(t, releaseOptions{
		previous:              true,
		failFirstVerification: true,
		failRestore:           true,
	})
	if err == nil {
		t.Fatalf("release unexpectedly succeeded: %s", output)
	}
	assertFileContains(t, target, "candidate-binary")
	if !strings.Contains(string(output), "automatic binary restoration failed") {
		t.Fatalf("restore-failure output = %q", output)
	}
	active := releaseActiveDir(repo)
	assertFileContains(t, filepath.Join(active, "state"), "rolling_back")
	assertFileContains(t, filepath.Join(active, "previous"), "previous-binary")
}

func TestLocalReleaseRemovesFailedFirstInstallation(t *testing.T) {
	repo, target, logFile, verifyCount, output, err := runLocalRelease(t, releaseOptions{failFirstVerification: true})
	if err == nil {
		t.Fatalf("release unexpectedly succeeded: %s", output)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("first failed installation still exists: %v", statErr)
	}
	assertFileContains(t, verifyCount, "1")
	if !strings.Contains(string(output), "error=deployment_failed") {
		t.Fatalf("first-install rollback output = %q", output)
	}
	assertFileContains(t, logFile, "launchctl:bootout")
	if _, statErr := os.Stat(releaseActiveDir(repo)); !os.IsNotExist(statErr) {
		t.Fatalf("successful first-install recovery retained active state: %v", statErr)
	}
}

func TestLocalReleaseResumesPreparedTransactionBeforePreflight(t *testing.T) {
	harness := newLocalReleaseHarness(t, releaseOptions{previous: true})
	output, err := harness.run("STOP_AFTER_PREPARE=1")
	if err == nil || !strings.Contains(string(output), "prepared test stop") {
		t.Fatalf("prepared setup err=%v output=%q", err, output)
	}
	active := releaseActiveDir(harness.repo)
	assertFileContains(t, filepath.Join(active, "state"), "prepared")
	if err := os.WriteFile(harness.logFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	output, err = harness.run()
	if err != nil {
		t.Fatalf("prepared resume failed: %v\n%s", err, output)
	}
	assertFileContains(t, filepath.Join(active, "state"), "pending")
	logData, readErr := os.ReadFile(harness.logFile)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.Contains(string(logData), "make:") || strings.Contains(string(logData), "go:") {
		t.Fatalf("prepared resume rebuilt or reprobed: %s", logData)
	}
}

func TestLocalReleaseGuidesEveryNonResumableActiveStateOnStderr(t *testing.T) {
	harness := newLocalReleaseHarness(t, releaseOptions{previous: true})
	if output, err := harness.run(); err != nil {
		t.Fatalf("release failed: %v\n%s", err, output)
	}
	active := releaseActiveDir(harness.repo)
	tests := []struct {
		state    string
		guidance string
	}{
		{"pending", "accept=make release-accept RELEASE_ID=release-0001\nrollback=make release-rollback RELEASE_ID=release-0001\n"},
		{"accepting", "resume=make release-accept RELEASE_ID=release-0001\n"},
		{"rolling_back", "resume=make release-rollback RELEASE_ID=release-0001\n"},
	}
	for _, test := range tests {
		t.Run(test.state, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(active, "state"), []byte(test.state), 0o600); err != nil {
				t.Fatal(err)
			}
			stdout, stderr, exit := harness.runChannels()
			candidateIdentity, err := os.ReadFile(filepath.Join(active, "candidate-sha256"))
			if err != nil {
				t.Fatal(err)
			}
			dependencyIdentity, err := os.ReadFile(filepath.Join(active, "dependency-sha256"))
			if err != nil {
				t.Fatal(err)
			}
			want := "error=state_conflict message=event conflicts with release state\n" +
				"state=" + test.state + " id=release-0001 commit=0123456789ab sha256=" + string(candidateIdentity[:12]) +
				" dependency_sha256=" + string(dependencyIdentity[:12]) + "\n" + test.guidance
			if exit != 1 || stdout != "" || stderr != want {
				t.Fatalf("exit=%d stdout=%q stderr=%q want=%q", exit, stdout, stderr, want)
			}
		})
	}
}

func TestPendingReleaseDispatchesExactAccept(t *testing.T) {
	harness := newLocalReleaseHarness(t, releaseOptions{previous: true})
	if output, err := harness.run(); err != nil {
		t.Fatalf("release failed: %v\n%s", err, output)
	}

	output, err := harness.dispatch("accept", "--release-id", "release-0001")
	if err != nil || string(output) != "state=clear\n" {
		t.Fatalf("accept err=%v output=%q", err, output)
	}
	assertFileContains(t, harness.target, "candidate-binary")
	if _, statErr := os.Stat(releaseActiveDir(harness.repo)); !os.IsNotExist(statErr) {
		t.Fatalf("accepted transaction remains active: %v", statErr)
	}
}

func TestPendingReleaseDispatchesExactRollbackAndRejectsStaleID(t *testing.T) {
	harness := newLocalReleaseHarness(t, releaseOptions{previous: true})
	if output, err := harness.run(); err != nil {
		t.Fatalf("release failed: %v\n%s", err, output)
	}

	output, err := harness.dispatch("rollback", "--release-id", "release-000")
	if err == nil || !strings.Contains(string(output), "error=identity_mismatch") {
		t.Fatalf("stale rollback err=%v output=%q", err, output)
	}
	assertFileContains(t, harness.target, "candidate-binary")
	assertFileContains(t, filepath.Join(releaseActiveDir(harness.repo), "state"), "pending")

	output, err = harness.dispatch("rollback", "--release-id", "release-0001")
	if err != nil || !strings.Contains(string(output), "state=clear") {
		t.Fatalf("rollback err=%v output=%q", err, output)
	}
	assertFileContains(t, harness.target, "previous-binary")
	if _, statErr := os.Stat(releaseActiveDir(harness.repo)); !os.IsNotExist(statErr) {
		t.Fatalf("rolled-back transaction remains active: %v", statErr)
	}
}

func TestLocalReleaseRejectsCandidateInstalledPathAlias(t *testing.T) {
	_, target, logFile, _, output, err := runLocalRelease(t, releaseOptions{
		previous:       true,
		candidateAlias: true,
	})
	if err == nil || string(output) != "error=release_config message=release configuration is invalid\n" {
		t.Fatalf("aliased release err=%v output=%q", err, output)
	}
	assertFileContains(t, target, "previous-binary")
	if data, readErr := os.ReadFile(logFile); readErr == nil && strings.Contains(string(data), "make:build") {
		t.Fatalf("aliased release reached build: %s", data)
	}
}

func TestLocalReleaseRejectsCandidateChangedDuringSmoke(t *testing.T) {
	_, target, _, _, output, err := runLocalRelease(t, releaseOptions{
		previous:                   true,
		mutateCandidateDuringSmoke: true,
	})
	if err == nil || string(output) != "error=release_changed message=release inputs changed during validation\n" {
		t.Fatalf("mutated-candidate release err=%v output=%q", err, output)
	}
	assertFileContains(t, target, "previous-binary")
}

func TestLocalReleasePinsOneSnapshotAcrossOriginalReplaceAndRestore(t *testing.T) {
	harness := newLocalReleaseHarness(t, releaseOptions{previous: true, replaceRestoreCandidate: true})
	stdout, stderr, exit := harness.runChannels()
	if exit != 0 || stderr != "" {
		t.Fatalf("replace-and-restore release exit=%d stdout=%q stderr=%q", exit, stdout, stderr)
	}
	assertFileContains(t, filepath.Join(releaseActiveDir(harness.repo), "candidate"), "candidate-binary")
	snapshotPath, err := os.ReadFile(harness.smokeCandidateRecord)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(string(snapshotPath)); !os.IsNotExist(err) {
		t.Fatalf("shared smoke snapshot survived cleanup: %v", err)
	}
}

func TestLocalReleasePinsExecutingControllerAcrossOriginalReplaceAndRestore(t *testing.T) {
	harness := newLocalReleaseHarness(t, releaseOptions{previous: true, replaceRestoreController: true})
	stdout, stderr, exit := harness.runChannels()
	if exit != 0 || stderr != "" || !strings.HasPrefix(stdout, "state=pending id=release-0001") {
		t.Fatalf("replace-and-restore controller release exit=%d stdout=%q stderr=%q", exit, stdout, stderr)
	}
	values := readKeyValueFile(t, harness.controllerRaceRecord)
	if values["self"] == harness.controller || values["self"] == "" || values["authority"] != values["self"] {
		t.Fatalf("controller authority was not the executing snapshot: %#v", values)
	}
	if values["self_hash"] != mustHash(t, harness.controllerSource) || values["selected_hash_during_swap"] != mustHash(t, harness.controllerReplacement) {
		t.Fatalf("controller replace/restore retargeted Prepare: %#v", values)
	}
	if mustHash(t, harness.controller) != mustHash(t, harness.controllerSource) ||
		mustHash(t, filepath.Join(releaseActiveDir(harness.repo), "authority")) != mustHash(t, harness.controllerSource) {
		t.Fatal("release did not restore and pin controller A")
	}
	if _, err := os.Lstat(values["self"]); !os.IsNotExist(err) {
		t.Fatalf("executing controller snapshot survived release: %v", err)
	}
}

func TestLocalReleaseRejectsCandidateChangedAfterReportValidation(t *testing.T) {
	harness := newLocalReleaseHarness(t, releaseOptions{previous: true, mutateCandidateAfterReports: true})
	stdout, stderr, exit := harness.runChannels()
	if exit != 1 || stdout != "" || stderr != "error=artifact_mismatch message=release artifact does not match\n" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exit, stdout, stderr)
	}
	assertFileContains(t, harness.target, "previous-binary")
	if _, err := os.Stat(releaseActiveDir(harness.repo)); !os.IsNotExist(err) {
		t.Fatalf("post-report candidate drift created release state: %v", err)
	}
}

func TestLocalReleaseRejectsDependencyChangedDuringSmoke(t *testing.T) {
	harness := newLocalReleaseHarness(t, releaseOptions{previous: true, mutateDependencyDuringSmoke: true})
	stdout, stderr, exit := harness.runChannels()
	if exit != 1 || stdout != "" || stderr != "error=release_changed message=release inputs changed during validation\n" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exit, stdout, stderr)
	}
	assertFileContains(t, harness.target, "previous-binary")
	if _, err := os.Stat(releaseActiveDir(harness.repo)); !os.IsNotExist(err) {
		t.Fatalf("dependency drift created release state: %v", err)
	}
}

func TestLocalReleaseRejectsMalformedOrDriftedReportSetBeforeActivation(t *testing.T) {
	for _, test := range []struct {
		name    string
		options releaseOptions
	}{
		{name: "malformed", options: releaseOptions{previous: true, malformedReport: true}},
		{name: "cross-report drift", options: releaseOptions{previous: true, crossReportDrift: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newLocalReleaseHarness(t, test.options)
			stdout, stderr, exit := harness.runChannels()
			if exit != 1 || stdout != "" || stderr != "error=release_smoke_failed message=release candidate report set is invalid\n" {
				t.Fatalf("exit=%d stdout=%q stderr=%q", exit, stdout, stderr)
			}
			assertFileContains(t, harness.target, "previous-binary")
			if _, err := os.Stat(releaseActiveDir(harness.repo)); !os.IsNotExist(err) {
				t.Fatalf("invalid report set created release state: %v", err)
			}
		})
	}
}

func TestLocalReleaseRejectsCommitChangedDuringRelease(t *testing.T) {
	_, target, _, _, output, err := runLocalRelease(t, releaseOptions{
		previous:              true,
		changeCommitOnRecheck: true,
	})
	if err == nil || string(output) != "error=release_changed message=release inputs changed during validation\n" {
		t.Fatalf("changed-commit release err=%v output=%q", err, output)
	}
	assertFileContains(t, target, "previous-binary")
}

func TestLocalReleaseRejectsDirtyTreeOnSecondPreflight(t *testing.T) {
	_, target, _, _, output, err := runLocalRelease(t, releaseOptions{
		previous:            true,
		failSecondPreflight: true,
	})
	if err == nil || string(output) != "error=release_preflight_failed message=release preflight failed\n" {
		t.Fatalf("second-preflight release err=%v output=%q", err, output)
	}
	assertFileContains(t, target, "previous-binary")
}

func TestLocalReleaseRejectsAlternateEnvironmentFile(t *testing.T) {
	_, target, _, _, output, err := runLocalRelease(t, releaseOptions{
		previous:     true,
		alternateEnv: true,
	})
	if err == nil || string(output) != "error=release_config message=release configuration is invalid\n" {
		t.Fatalf("alternate-env release err=%v output=%q", err, output)
	}
	assertFileContains(t, target, "previous-binary")
}

func TestLocalReleaseTreatsEnvironmentFileAsBoundedData(t *testing.T) {
	harness := newLocalReleaseHarness(t, releaseOptions{previous: true})
	envFile := filepath.Join(harness.repo, ".env.local")
	sentinel := filepath.Join(harness.repo, "must-not-exist")
	tests := []struct {
		name    string
		content string
	}{
		{"command substitution", "GATEWAY_BIN=$(touch " + sentinel + ")\n"},
		{"backtick command", "GATEWAY_BIN=`touch " + sentinel + "`\n"},
		{"exit command", "exit 7\n"},
		{"output command", "GATEWAY_BIN=/tmp/gateway; printf leaked-output\n"},
		{"unknown key", "GATEWAY_BIN=/tmp/gateway\nUNRECOGNIZED_RELEASE_KEY=value\n"},
		{"duplicate key", "GATEWAY_BIN=/tmp/one\nGATEWAY_BIN=/tmp/two\n"},
		{"oversized", "#" + strings.Repeat("x", 65536) + "\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(envFile, []byte(test.content), 0o600); err != nil {
				t.Fatal(err)
			}
			output, runErr := harness.run()
			if runErr == nil || string(output) != "error=release_config message=release configuration is invalid\n" {
				t.Fatalf("err=%v output=%q", runErr, output)
			}
			if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
				t.Fatalf("environment data executed a command: %v", err)
			}
		})
	}
}

func TestLocalReleaseParsesQuotedValuesAndLeadingHome(t *testing.T) {
	harness := newLocalReleaseHarness(t, releaseOptions{previous: true})
	envText := fmt.Sprintf("GATEWAY_BIN=\"$HOME/%s\"\nOBSIDIAN_ROOT='%s'\nCONTROL_PLANE_API_KEY=runtime-secret\n", filepath.Base(harness.target), harness.vault)
	if err := os.WriteFile(filepath.Join(harness.repo, ".env.local"), []byte(envText), 0o600); err != nil {
		t.Fatal(err)
	}
	output, runErr := harness.run("HOME=" + filepath.Dir(harness.target))
	if runErr != nil || !strings.HasPrefix(string(output), "state=pending id=release-0001") {
		t.Fatalf("err=%v output=%q", runErr, output)
	}
}

func TestRuntimeWrappersTreatEnvironmentFileAsBoundedData(t *testing.T) {
	wrappers := []string{"run-obsidian-tunnel.sh", "run-obsidian-mcp-stdio.sh"}
	tests := []struct {
		name    string
		content func(string) string
	}{
		{"command substitution", func(sentinel string) string { return "OBSIDIAN_ROOT=$(touch " + sentinel + ")\n" }},
		{"backtick command", func(sentinel string) string { return "OBSIDIAN_ROOT=`touch " + sentinel + "`\n" }},
		{"exit command", func(string) string { return "exit 0\n" }},
		{"output command", func(string) string { return "printf hostile-output\n" }},
	}
	for _, wrapper := range wrappers {
		for _, test := range tests {
			t.Run(wrapper+"/"+test.name, func(t *testing.T) {
				repo := newScriptRepo(t, wrapper)
				envFile := filepath.Join(repo, ".env.local")
				sentinel := filepath.Join(repo, "must-not-exist")
				if err := os.WriteFile(envFile, []byte(test.content(sentinel)), 0o600); err != nil {
					t.Fatal(err)
				}
				cmd := exec.Command(filepath.Join(repo, "scripts", wrapper))
				cmd.Env = envWithOverrides(testEnv(filepath.Join(repo, "bin")), "MCP_GATEWAY_ENV_FILE="+envFile)
				var stdout, stderr strings.Builder
				cmd.Stdout = &stdout
				cmd.Stderr = &stderr
				if err := cmd.Run(); err == nil {
					t.Fatalf("hostile configuration succeeded: stdout=%q stderr=%q", stdout.String(), stderr.String())
				}
				if stdout.String() != "" || stderr.String() != "personal-mcp-gateway: local environment configuration is invalid.\n" {
					t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
				}
				if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
					t.Fatalf("environment data executed a command: %v", err)
				}
			})
		}
	}
}

func TestRuntimeWrappersParseQuotedAndHomePrefixedValues(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home with space")
	vault := filepath.Join(home, "vault")
	if err := os.MkdirAll(filepath.Join(home, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(vault, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("stdio", func(t *testing.T) {
		repo := newScriptRepo(t, "run-obsidian-mcp-stdio.sh")
		logFile := filepath.Join(repo, "stdio.log")
		gateway := filepath.Join(home, "bin", "personal-mcp-gateway")
		writeExecutable(t, gateway, "#!/bin/sh\nprintf '%s\\n' \"$*\" >\"$WRAPPER_LOG\"\n")
		envFile := filepath.Join(repo, ".env.local")
		config := "OBSIDIAN_ROOT=\"$HOME/vault\"\n" +
			"GATEWAY_BIN=\"${HOME}/bin/personal-mcp-gateway\"\n" +
			"MCP_GATEWAY_TELEMETRY_DB=\"$HOME/state/telemetry.sqlite\"\n"
		if err := os.WriteFile(envFile, []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(filepath.Join(repo, "scripts", "run-obsidian-mcp-stdio.sh"))
		cmd.Env = envWithOverrides(testEnv(filepath.Join(repo, "bin")),
			"HOME="+home,
			"MCP_GATEWAY_ENV_FILE="+envFile,
			"WRAPPER_LOG="+logFile,
			"OBSIDIAN_ROOT=",
			"GATEWAY_BIN=",
			"MCP_GATEWAY_TELEMETRY_DB=",
		)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("stdio wrapper failed: %v\n%s", err, output)
		}
		assertFileContains(t, logFile, "stdio --obsidian-root "+vault+" --telemetry-db "+filepath.Join(home, "state", "telemetry.sqlite"))
	})

	t.Run("tunnel", func(t *testing.T) {
		repo := newScriptRepo(t, "run-obsidian-tunnel.sh")
		logFile := filepath.Join(repo, "tunnel.log")
		writeExecutable(t, filepath.Join(repo, "scripts", "run-obsidian-mcp-stdio.sh"), "#!/bin/sh\nexit 0\n")
		writeExecutable(t, filepath.Join(repo, "tools", "tunnel-client", "tunnel-client"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >>\"$WRAPPER_LOG\"\n")
		envFile := filepath.Join(repo, ".env.local")
		config := "CONTROL_PLANE_TUNNEL_ID=\"tunnel-quoted\"\n" +
			"CONTROL_PLANE_API_KEY='key with spaces'\n" +
			"OBSIDIAN_ROOT=\"$HOME/vault\"\n" +
			"TUNNEL_HEALTH_URL_FILE=\"${HOME}/state/health.url\"\n"
		if err := os.WriteFile(envFile, []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(filepath.Join(repo, "scripts", "run-obsidian-tunnel.sh"))
		cmd.Env = envWithOverrides(testEnv(filepath.Join(repo, "bin")),
			"HOME="+home,
			"MCP_GATEWAY_ENV_FILE="+envFile,
			"WRAPPER_LOG="+logFile,
			"CONTROL_PLANE_TUNNEL_ID=",
			"CONTROL_PLANE_API_KEY=",
			"OBSIDIAN_ROOT=",
		)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("tunnel wrapper failed: %v\n%s", err, output)
		}
		assertFileContains(t, logFile, "init --sample sample_mcp_stdio_local")
		assertFileContains(t, logFile, "--tunnel-id tunnel-quoted")
		assertFileContains(t, logFile, "run --profile obsidian-stdio")
		if data, err := os.ReadFile(logFile); err != nil {
			t.Fatal(err)
		} else if strings.Contains(string(data), "key with spaces") {
			t.Fatalf("tunnel command leaked API key: %q", data)
		}
	})
}

func TestLocalReleaseRollsBackOnStagedHashMismatch(t *testing.T) {
	_, target, _, _, output, err := runLocalRelease(t, releaseOptions{
		previous:     true,
		corruptStage: true,
	})
	if err == nil || !strings.Contains(string(output), "staged binary does not match") {
		t.Fatalf("hash-mismatch release err=%v output=%q", err, output)
	}
	assertFileContains(t, target, "previous-binary")
}

func TestUpdateFastForwardsMainBeforeRelease(t *testing.T) {
	repo := newScriptRepo(t, "update-local.sh", "release-activation.sh")
	localHead := "0123456789abcdef0123456789abcdef01234567"
	remoteHead := "89abcdef0123456789abcdef0123456789abcdef"
	binDir := filepath.Join(repo, "bin")
	logFile := filepath.Join(repo, "calls.log")
	controllerSource := filepath.Join(repo, "fake-release-controller")
	writeExecutable(t, controllerSource, fakeReleaseController)
	home := installFakePasswdTools(t, repo, binDir)
	writeExecutable(t, filepath.Join(binDir, "git"), `#!/bin/sh
case "$*" in
  *"branch --show-current"*) printf '%s\n' "${FAKE_BRANCH:-main}" ;;
  *"status --porcelain"*)
    if [ "${FAKE_GIT_STATUS_ERROR:-0}" = 1 ]; then exit 3; fi
    printf '%s' "${FAKE_GIT_STATUS:-}"
    ;;
	  *"rev-parse HEAD"*)
    count=0
    if [ -f "$UPDATE_HEAD_COUNT" ]; then count=$(cat "$UPDATE_HEAD_COUNT"); fi
    count=$((count + 1)); printf '%s' "$count" >"$UPDATE_HEAD_COUNT"
	    if [ "$count" -gt 2 ] && [ "${FAKE_MERGE_ERROR:-0}" != 1 ] && [ "${FAKE_FINAL_HEAD_MISMATCH:-0}" != 1 ]; then printf '%s\n' "$FAKE_ORIGIN_HEAD"; else printf '%s\n' "$FAKE_HEAD"; fi
    ;;
	  *"rev-parse --verify FETCH_HEAD^{commit}"*)
	    if [ -f "$FETCH_REWRITE_MARKER" ]; then printf '%s\n' "$FAKE_REWRITTEN_HEAD"; else printf '%s\n' "$FAKE_ORIGIN_HEAD"; fi
	    ;;
	  *"merge --ff-only "*)
    printf 'git:%s\n' "$*" >>"$CALL_LOG"
    if [ "${FAKE_MERGE_ERROR:-0}" = 1 ]; then exit 4; fi
    ;;
  *) printf 'git:%s\n' "$*" >>"$CALL_LOG" ;;
esac
`)
	writeExecutable(t, filepath.Join(repo, "scripts", "release-local.sh"), `#!/bin/sh
printf '%s\n' release-local >>"$CALL_LOG"
if [ "${FAKE_RELEASE_ERROR:-0}" = 1 ]; then
  printf '%s\n' 'error=release_test_failed message=release tests failed' >&2
  exit 1
fi
`)
	writeExecutable(t, filepath.Join(binDir, "make"), `#!/bin/sh
target=""
for arg in "$@"; do target="$arg"; done
printf 'make:%s\n' "$target" >>"$CALL_LOG"
if [ "$target" = build-release-controller ]; then
  mkdir -p "$(dirname "$RELEASE_ACTIVATION_CANDIDATE")"
  cp "$FAKE_CONTROLLER_SOURCE" "$RELEASE_ACTIVATION_CANDIDATE"
  chmod 755 "$RELEASE_ACTIVATION_CANDIDATE"
  : >"$FETCH_REWRITE_MARKER"
fi
`)

	cmd := exec.Command(filepath.Join(repo, "scripts", "update-local.sh"))
	baseEnv := append(testEnv(binDir),
		"CALL_LOG="+logFile,
		"MAKE=make",
		"PASSWD_HOME="+home,
		"TEST_REPO="+repo,
		"FAKE_CONTROLLER_SOURCE="+controllerSource,
		"RELEASE_ACTIVATION_CANDIDATE="+filepath.Join(repo, ".build", "release-activation"),
		"UPDATE_HEAD_COUNT="+filepath.Join(repo, "update-head-count"),
		"FAKE_HEAD="+localHead,
		"FAKE_ORIGIN_HEAD="+remoteHead,
		"FAKE_REWRITTEN_HEAD="+strings.Repeat("c", 40),
		"FETCH_REWRITE_MARKER="+filepath.Join(repo, "fetch-rewritten"),
	)
	resetHeadCount := func() {
		t.Helper()
		for _, path := range []string{filepath.Join(repo, "update-head-count"), filepath.Join(repo, "fetch-rewritten")} {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
		}
	}
	resetHeadCount()
	cmd.Env = baseEnv
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("update failed: %v\n%s", err, output)
	}
	if len(output) != 0 {
		t.Fatalf("successful update emitted intermediate output: %q", output)
	}
	logData, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	logText := string(logData)
	fetch := strings.Index(logText, "fetch origin")
	buildController := strings.Index(logText, "make:build-release-controller")
	merge := strings.Index(logText, "merge --ff-only "+remoteHead)
	release := strings.Index(logText, "release-local")
	if fetch < 0 || buildController <= fetch || merge <= buildController || release <= merge {
		t.Fatalf("update call order = %q", logText)
	}
	if strings.Contains(logText, "merge --ff-only FETCH_HEAD") || strings.Contains(logText, "make:release") {
		t.Fatalf("update used a mutable ref or nested make: %q", logText)
	}
	if strings.Contains(logText, "merge --ff-only "+strings.Repeat("c", 40)) {
		t.Fatalf("update followed rewritten FETCH_HEAD: %q", logText)
	}

	cmd = exec.Command(filepath.Join(repo, "scripts", "update-local.sh"))
	resetHeadCount()
	cmd.Env = append(baseEnv, "FAKE_RELEASE_ERROR=1")
	output, err = cmd.CombinedOutput()
	if err == nil || string(output) != "error=release_test_failed message=release tests failed\n" {
		t.Fatalf("release failure err=%v output=%q", err, output)
	}

	active := releaseActiveDir(repo)
	if err := os.MkdirAll(active, 0o700); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(active, "authority"), fakeReleaseController)
	if err := os.WriteFile(logFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command(filepath.Join(repo, "scripts", "update-local.sh"))
	resetHeadCount()
	cmd.Env = baseEnv
	output, err = cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "release transaction is active") {
		t.Fatalf("active update err=%v output=%q", err, output)
	}
	logData, err = os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	logText = string(logData)
	if !strings.Contains(logText, "fetch origin") || !strings.Contains(logText, "make:build-release-controller") ||
		strings.Contains(logText, "merge --ff-only") || strings.Contains(logText, "release-local") {
		t.Fatalf("active update call order = %q", logText)
	}
	if err := os.RemoveAll(active); err != nil {
		t.Fatal(err)
	}

	cmd = exec.Command(filepath.Join(repo, "scripts", "update-local.sh"))
	resetHeadCount()
	cmd.Env = append(baseEnv, "FAKE_BRANCH=feature")
	output, err = cmd.CombinedOutput()
	if err == nil || string(output) != "error=update_preflight_failed message=update preflight failed\n" {
		t.Fatalf("non-main update err=%v output=%q", err, output)
	}

	cmd = exec.Command(filepath.Join(repo, "scripts", "update-local.sh"))
	resetHeadCount()
	cmd.Env = append(baseEnv, "FAKE_GIT_STATUS= M README.md")
	output, err = cmd.CombinedOutput()
	if err == nil || string(output) != "error=update_preflight_failed message=update preflight failed\n" {
		t.Fatalf("dirty update err=%v output=%q", err, output)
	}

	cmd = exec.Command(filepath.Join(repo, "scripts", "update-local.sh"))
	resetHeadCount()
	cmd.Env = append(baseEnv,
		"FAKE_FINAL_HEAD_MISMATCH=1",
	)
	output, err = cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "source update failed") {
		t.Fatalf("mismatched-final-head update err=%v output=%q", err, output)
	}

	cmd = exec.Command(filepath.Join(repo, "scripts", "update-local.sh"))
	resetHeadCount()
	cmd.Env = append(baseEnv, "FAKE_GIT_STATUS_ERROR=1")
	output, err = cmd.CombinedOutput()
	if err == nil || string(output) != "error=update_preflight_failed message=update preflight failed\n" {
		t.Fatalf("failed-status update err=%v output=%q", err, output)
	}

	cmd = exec.Command(filepath.Join(repo, "scripts", "update-local.sh"))
	resetHeadCount()
	cmd.Env = append(baseEnv, "FAKE_MERGE_ERROR=1")
	output, err = cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("failed merge update succeeded: %s", output)
	}
}

type releaseOptions struct {
	previous                    bool
	failTests                   bool
	failPerformanceSmoke        bool
	failResourceSmoke           bool
	failFirstVerification       bool
	failAllVerification         bool
	failRestore                 bool
	candidateAlias              bool
	mutateCandidateDuringSmoke  bool
	mutateCandidateAfterReports bool
	replaceRestoreCandidate     bool
	replaceRestoreController    bool
	changeCommitOnRecheck       bool
	failSecondPreflight         bool
	corruptStage                bool
	alternateEnv                bool
	mutateDependencyDuringSmoke bool
	malformedReport             bool
	crossReportDrift            bool
	largePrivateSmokeArtifact   bool
}

type localReleaseHarness struct {
	t                     *testing.T
	repo                  string
	target                string
	vault                 string
	logFile               string
	verifyCount           string
	smokeCandidateRecord  string
	controllerRaceRecord  string
	controller            string
	controllerSource      string
	controllerReplacement string
	env                   []string
}

func runLocalRelease(t *testing.T, options releaseOptions) (repo, target, logFile, verifyCount string, output []byte, runErr error) {
	t.Helper()
	harness := newLocalReleaseHarness(t, options)
	output, runErr = harness.run()
	return harness.repo, harness.target, harness.logFile, harness.verifyCount, output, runErr
}

func newLocalReleaseHarness(t *testing.T, options releaseOptions) *localReleaseHarness {
	t.Helper()
	repo := newScriptRepo(t, "release-local.sh", "release-preflight.sh", "release-activation.sh")
	binDir := filepath.Join(repo, "bin")
	logFile := filepath.Join(repo, "calls.log")
	verifyCount := filepath.Join(repo, "verify-count")
	candidate := filepath.Join(repo, ".build", "gateway")
	controller := filepath.Join(repo, ".build", "release-activation")
	target := filepath.Join(repo, "installed", "gateway")
	if options.candidateAlias {
		candidate = target
	}
	vault := filepath.Join(repo, "vault")
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module release.test\n\ngo 1.25\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.sum"), []byte("synthetic dependency lock\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(vault, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(candidate), 0o755); err != nil {
		t.Fatal(err)
	}
	canonicalCandidateDir, err := filepath.EvalSymlinks(filepath.Dir(candidate))
	if err != nil {
		t.Fatal(err)
	}
	expectedCandidate := filepath.Join(canonicalCandidateDir, filepath.Base(candidate))
	if options.previous {
		writeExecutable(t, target, "#!/bin/sh\n# previous-binary\n")
	}
	home := installFakePasswdTools(t, repo, binDir)
	controllerSource := filepath.Join(repo, "fake-release-controller")
	writeExecutable(t, controllerSource, fakeReleaseController)
	controllerReplacement := filepath.Join(repo, "fake-release-controller-b")
	writeExecutable(t, controllerReplacement, "#!/bin/sh\nprintf '%s\\n' controller-b\n")
	controllerRaceRecord := filepath.Join(repo, "controller-race-record")
	verifyScript := `#!/bin/sh
count=0
if [ -f "$VERIFY_COUNT" ]; then count=$(cat "$VERIFY_COUNT"); fi
count=$((count + 1))
printf '%s' "$count" >"$VERIFY_COUNT"
if [ "${FAIL_ALL_VERIFY:-0}" = 1 ]; then exit 1; fi
if [ "${FAIL_FIRST_VERIFY:-0}" = 1 ] && [ "$count" = 1 ]; then exit 1; fi
exit 0
`
	writeExecutable(t, filepath.Join(repo, "scripts", "verify-live.sh"), verifyScript)
	writeExecutable(t, filepath.Join(binDir, "launchctl"), "#!/bin/sh\nprintf 'launchctl:%s\\n' \"$*\" >>\"$CALL_LOG\"\nexit 0\n")
	writeExecutable(t, filepath.Join(binDir, "git"), `#!/bin/sh
case "$*" in
  *"status --porcelain"*)
    count=0
    if [ -f "$STATUS_COUNT" ]; then count=$(cat "$STATUS_COUNT"); fi
    count=$((count + 1))
    printf '%s' "$count" >"$STATUS_COUNT"
    if [ "${FAIL_SECOND_PREFLIGHT:-0}" = 1 ] && [ "$count" -gt 1 ]; then
      printf '%s' ' M README.md'
    fi
    ;;
  *"rev-parse --verify HEAD"*)
    count=0
    if [ -f "$REVPARSE_COUNT" ]; then count=$(cat "$REVPARSE_COUNT"); fi
    count=$((count + 1))
    printf '%s' "$count" >"$REVPARSE_COUNT"
    if [ "${CHANGE_COMMIT_ON_RECHECK:-0}" = 1 ] && [ "$count" -ge 4 ]; then
      printf '%s\n' fedcba9876543210fedcba9876543210fedcba98
    else
      printf '%s\n' 0123456789abcdef0123456789abcdef01234567
    fi
    ;;
  *) exit 2 ;;
esac
`)
	writeExecutable(t, filepath.Join(binDir, "go"), `#!/bin/bash
set -euo pipefail
if [ -n "${CONTROL_PLANE_API_KEY:-}" ]; then printf '%s\n' "$CONTROL_PLANE_API_KEY"; exit 9; fi
printf 'go:%s\n' "$*" >>"$CALL_LOG"
argument() {
  local wanted="$1"
  shift
  while (( $# > 0 )); do
    if [[ "$1" == "$wanted" && $# -gt 1 ]]; then printf '%s\n' "$2"; return 0; fi
    shift
  done
  return 1
}
has_argument() {
  local wanted="$1"
  shift
  for value in "$@"; do [[ "$value" == "$wanted" ]] && return 0; done
  return 1
}
[[ "${1:-}" == run && "${2:-}" == ./cmd/gateway-smoke ]] || { printf 'unexpected smoke args\n'; exit 7; }
shift 2
	if [[ "${LARGE_PRIVATE_SMOKE_ARTIFACT:-0}" == 1 ]]; then
	  /usr/bin/head -c $((4 * 1024 * 1024)) /dev/zero >"$PRIVATE_SMOKE_ARTIFACT"
	fi
	smoke_candidate="$(argument --gateway-bin "$@")"
	[[ "$smoke_candidate" != "$EXPECTED_CANDIDATE" && -f "$smoke_candidate" && ! -L "$smoke_candidate" && -x "$smoke_candidate" ]] || exit 7
	[[ "$(stat -f %Lp "$smoke_candidate")" == 700 && "$(stat -f %Lp "$(dirname "$smoke_candidate")")" == 700 ]] || exit 7
	if [[ -f "$SMOKE_CANDIDATE_RECORD" ]]; then
	  [[ "$(cat "$SMOKE_CANDIDATE_RECORD")" == "$smoke_candidate" ]] || exit 7
	else
	  printf '%s' "$smoke_candidate" >"$SMOKE_CANDIDATE_RECORD"
	fi
[[ "$(argument --repo-root "$@")" == "$TEST_REPO" ]] || exit 7
[[ "$(argument --candidate-commit "$@")" == 0123456789abcdef0123456789abcdef01234567 ]] || exit 7
candidate_sha="$(argument --candidate-sha256 "$@")"
dependency_sha="$(argument --dependency-sha256 "$@")"
[[ "$candidate_sha" =~ ^[0-9a-f]{64}$ && "$dependency_sha" =~ ^[0-9a-f]{64}$ ]] || exit 7
	[[ "$candidate_sha" == "$(shasum -a 256 "$smoke_candidate" | awk '{print $1}')" ]] || exit 7
go_mod_sha="$(shasum -a 256 "$TEST_REPO/go.mod" | awk '{print $1}')"
go_sum_sha="$(shasum -a 256 "$TEST_REPO/go.sum" | awk '{print $1}')"
actual_dependency_sha="$(printf 'go.mod=%s\ngo.sum=%s\n' "$go_mod_sha" "$go_sum_sha" | shasum -a 256 | awk '{print $1}')"
[[ "$dependency_sha" == "$actual_dependency_sha" ]] || exit 7
if has_argument --validate-report-set "$@"; then
  files=()
  positional=0
  for value in "$@"; do
    if (( positional )); then files+=("$value"); fi
    [[ "$value" == --validate-report-set ]] && positional=1
  done
  [[ ${#files[@]} -eq 3 ]] || exit 7
  grep -q '"report_kind":"functional"' "${files[0]}" || exit 8
  grep -q '"report_schema":"personal-mcp-gateway.functional.v3"' "${files[0]}" || exit 8
  grep -q '"tool_count":5' "${files[0]}" || exit 8
  grep -q '"synthetic_retrieval_equivalent":true' "${files[0]}" || exit 8
  grep -q '"report_kind":"performance"' "${files[1]}" || exit 8
	  grep -q '"report_schema":"personal-mcp-gateway.performance.v3"' "${files[1]}" || exit 8
  grep -q '"descriptor_count":5' "${files[1]}" || exit 8
  grep -q '"synthetic_grep":' "${files[1]}" || exit 8
  grep -q '"current_sqlite":' "${files[1]}" || exit 8
	  grep -q '"report_kind":"resource"' "${files[2]}" || exit 8
	  grep -q '"report_schema":"personal-mcp-gateway.resource.v5"' "${files[2]}" || exit 8
  grep -q '"descriptor_count":5' "${files[2]}" || exit 8
	  grep -q '"measured_call_count":312' "${files[2]}" || exit 8
  grep -q '"fixture":' "${files[2]}" || exit 8
  grep -q '"process":' "${files[2]}" || exit 8
  grep -q '"cold":' "${files[2]}" || exit 8
  grep -q '"boundaries":' "${files[2]}" || exit 8
  grep -q '"batches":' "${files[2]}" || exit 8
  for file in "${files[@]}"; do
    grep -q '"candidate_commit":"0123456789abcdef0123456789abcdef01234567"' "$file" || exit 8
    grep -q "\"candidate_sha256\":\"$candidate_sha\"" "$file" || exit 8
    grep -q "\"dependency_sha256\":\"$dependency_sha\"" "$file" || exit 8
  done
  printf '%s\n%s\n%s\n' 0123456789abcdef0123456789abcdef01234567 "$candidate_sha" "$dependency_sha" >"$REPORT_SET_TUPLE"
  if [[ "${MUTATE_CANDIDATE_AFTER_REPORTS:-0}" == 1 ]]; then printf '# changed after reports\n' >>"$GATEWAY_CANDIDATE"; fi
  exit 0
fi
[[ "$(argument --obsidian-root "$@")" == "$EXPECTED_VAULT" ]] || exit 7
kind=""
schema_version=3
if has_argument --report-json "$@"; then
  kind=functional
  if [[ "${MALFORMED_REPORT:-0}" == 1 ]]; then printf '{'; exit 0; fi
elif has_argument --performance-json "$@"; then
  kind=performance
  if [ "${FAIL_PERFORMANCE_SMOKE:-0}" = 1 ]; then
    printf 'hostile private-sentinel %s\n' "$TEST_REPO"
    printf 'hostile runtime-secret\n' >&2
    exit 8
  fi
elif has_argument --resource-json "$@"; then
  kind=resource
  schema_version=5
  if [[ "${CROSS_REPORT_DRIFT:-0}" == 1 ]]; then dependency_sha=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa; fi
    if [ "${FAIL_PERFORMANCE_SMOKE:-0}" = 1 ]; then
      exit 8
    fi
    if [ "${FAIL_RESOURCE_SMOKE:-0}" = 1 ]; then
      printf 'hostile private-sentinel %s\n' "$TEST_REPO"
      printf 'hostile runtime-secret\n' >&2
      exit 8
    fi
else
  printf 'unexpected smoke args\n'; exit 7
fi
case "$kind" in
  functional)
    proof='"report_schema":"personal-mcp-gateway.functional.v3","candidate_runtime":{"go_version":"go1.0","goos":"darwin","goarch":"arm64"},"machine":{"logical_cpu_count":1,"gomaxprocs":1},"current_vault":{"inventory_policy":"obsidian_markdown_v1","inventory_complete":true,"markdown_file_count":1,"markdown_byte_count":1,"stopped_by":"scope"},"synthetic_vault":{"inventory_policy":"obsidian_markdown_v1","inventory_complete":true,"markdown_file_count":3,"markdown_byte_count":3,"stopped_by":"scope"},"current_process":{},"synthetic_process":{},"tool_calls":{"resolve":2,"ls":3,"read":1,"read_many":2,"grep":1},"tool_count":5,"sdk_result_count":9,"max_sdk_result_bytes":1,"max_structured_result_bytes":1,"max_client_latency_microseconds":1,"total_files_scanned":1,"total_bytes_scanned":1,"total_source_entries_validated":1,"current_resolve_existing_directory":true,"synthetic_canonical_resolve":true,"synthetic_page_count":2,"synthetic_entry_count":3,"synthetic_second_page_progress":true,"synthetic_no_duplicates":true,"synthetic_full_equivalence":true,"synthetic_read_selected":true,"synthetic_grep_match_count":3,"synthetic_read_many_pages":2,"synthetic_read_many_continued":true,"synthetic_retrieval_equivalent":true,"synthetic_telemetry_sanitized":true'
    ;;
  performance)
	    proof='"report_schema":"personal-mcp-gateway.performance.v3","candidate_runtime":{"go_version":"go1.0","goos":"darwin","goarch":"arm64"},"machine":{"logical_cpu_count":1,"gomaxprocs":1},"current_vault":{"inventory_policy":"obsidian_markdown_v1","inventory_complete":true,"markdown_file_count":1,"markdown_byte_count":1,"stopped_by":"scope"},"synthetic_corpus":{"inventory_policy":"obsidian_markdown_v1","inventory_complete":true,"markdown_file_count":50,"markdown_byte_count":256000,"stopped_by":"scope"},"descriptor_count":5,"cardinality_bucket":"2_10","resolve_cached":{},"ls_first_limit_1":{},"ls_continued_limit_1":{},"ls_first_limit_100":{},"synthetic_read":{},"synthetic_grep":{},"broad_current_grep":{},"synthetic_process":{},"current_vault_process":{},"stratified":[],"current_sqlite":{},"stratified_sqlite":{},"sqlite_degradation":{},"cancellation":{}'
    ;;
  resource)
	    proof='"report_schema":"personal-mcp-gateway.resource.v5","descriptor_count":5,"candidate_runtime":{"go_version":"go1.0","goos":"darwin","goarch":"arm64"},"machine":{"logical_cpu_count":1,"gomaxprocs":1},"vault":{"inventory_policy":"obsidian_markdown_v1","inventory_complete":true,"markdown_file_count":1,"markdown_byte_count":1,"stopped_by":"scope"},"fixture":{"generated_markdown_files":1,"generated_bytes":1,"inventory_markdown_files":1,"inventory_bytes":1,"inventory_complete":true,"inventory_reconciled":true},"process":{"baseline_cpu_microseconds":1,"final_cpu_microseconds":2,"cpu_delta_microseconds":1,"lifetime_cpu_microseconds":2,"baseline_rss_bytes":1,"final_rss_bytes":1,"max_observed_rss_bytes":1,"high_water_rss_bytes":1,"baseline_fd_count":1,"final_fd_count":1,"max_observed_fd_count":1,"fds_recovered":true},"workload":{"batch_count":3,"calls_per_batch":100,"calls_per_tool_per_batch":20,"mixed_call_count":300,"boundary_call_count":12,"measured_call_count":312,"tool_calls":{"resolve":60,"ls":60,"read":65,"read_many":60,"grep":67},"max_client_latency_microseconds":1,"max_sdk_result_bytes":1,"max_structured_bytes":1,"max_files_scanned":1,"max_bytes_scanned":1,"max_source_entries_validated":1,"every_call_within_two_seconds":true,"every_sdk_result_within_64kib":true},"boundaries":{"call_count":12,"near_8mib_structural_accepted":true,"dense_50000_decoy_rejected":true,"dense_50000_block_accepted":true,"over_8mib_error_code":"input_too_large","over_50000_lines_error_code":"input_too_large","grep_exact_matching_error_code":"response_too_large","grep_exact_nonmatching_accepted":true,"grep_exact_context_error_code":"response_too_large","grep_exact_unicode_error_code":"response_too_large","grep_exact_zero_width_error_code":"response_too_large","grep_exact_invalid_utf8_error_code":"invalid_utf8","grep_over_1mib_error_code":"input_too_large","every_call_within_two_seconds":true,"every_sdk_result_within_64kib":true},"cold":{},"baseline":{},"high_water_rss_bytes":1,"high_water_rss_delta_bytes":0,"high_water_within_bound":true,"max_heap_alloc_growth_bytes":0,"heap_alloc_growth_within_bound":true,"max_rss_after_30_seconds_growth_bytes":0,"rss_after_30_seconds_growth_within_bound":true,"gc_acknowledgement_count":4,"all_fds_recovered":true,"batches":[],"idle":{}'
    ;;
esac
printf '{"report_kind":"%s","schema_version":%s,"passed":true,"candidate_commit":"%s","candidate_sha256":"%s","dependency_sha256":"%s",%s}\n' "$kind" "$schema_version" 0123456789abcdef0123456789abcdef01234567 "$candidate_sha" "$dependency_sha" "$proof"
if [[ "$kind" == functional && "${REPLACE_RESTORE_CANDIDATE:-0}" == 1 ]]; then
  cp "$GATEWAY_CANDIDATE" "$GATEWAY_CANDIDATE.restore"
  printf '#!/bin/sh\n# distinguishable-candidate-b\n' >"$GATEWAY_CANDIDATE"
  chmod 755 "$GATEWAY_CANDIDATE"
elif [[ "$kind" == performance && "${REPLACE_RESTORE_CANDIDATE:-0}" == 1 ]]; then
  cp "$GATEWAY_CANDIDATE.restore" "$GATEWAY_CANDIDATE"
  chmod 755 "$GATEWAY_CANDIDATE"
  rm -f "$GATEWAY_CANDIDATE.restore"
fi
if [[ "$kind" == resource && "${MUTATE_CANDIDATE:-0}" = 1 ]]; then printf '# changed\n' >>"$GATEWAY_CANDIDATE"; fi
if [[ "$kind" == resource && "${MUTATE_DEPENDENCY:-0}" = 1 ]]; then printf 'changed dependency\n' >>"$TEST_REPO/go.sum"; fi
exit 0
`)
	makeScript := fmt.Sprintf(`#!/bin/sh
if [ -n "${CONTROL_PLANE_API_KEY:-}" ]; then printf '%%s\n' "$CONTROL_PLANE_API_KEY"; exit 9; fi
target=""
for arg in "$@"; do target="$arg"; done
printf 'make:%%s\n' "$target" >>"$CALL_LOG"
if [ "$target" = test ] && [ "${FAIL_TESTS:-0}" = 1 ]; then
  printf 'hostile private-sentinel %%s\n' "$TEST_REPO"
  printf 'hostile runtime-secret\n' >&2
  exit 9
fi
if [ "$target" = build ]; then
  mkdir -p %q
  printf '#!/bin/sh\n# candidate-binary\n' >%q
  chmod 755 %q
fi
if [ "$target" = build-release-controller ]; then
  mkdir -p "$(dirname "$RELEASE_ACTIVATION_CANDIDATE")"
  cp "$FAKE_CONTROLLER_SOURCE" "$RELEASE_ACTIVATION_CANDIDATE"
  chmod 755 "$RELEASE_ACTIVATION_CANDIDATE"
fi
`, filepath.Dir(candidate), candidate, candidate)
	writeExecutable(t, filepath.Join(binDir, "make"), makeScript)
	if options.failRestore || options.corruptStage {
		installScript := `#!/bin/sh
source_path=""
target_path=""
for arg in "$@"; do
  case "$arg" in -*) ;; *) source_path="$target_path"; target_path="$arg" ;; esac
done
if [ "${FAIL_RESTORE:-0}" = 1 ] && echo "$source_path" | grep -q '/previous$'; then exit 8; fi
if [ "${CORRUPT_STAGE:-0}" = 1 ] && echo "$target_path" | grep -q '\.next\.'; then
  printf '#!/bin/sh\n# corrupt-stage\n' >"$target_path"
  chmod 755 "$target_path"
  exit 0
fi
exec "$REAL_INSTALL" "$@"
`
		writeExecutable(t, filepath.Join(binDir, "install"), installScript)
	}

	envFile := filepath.Join(repo, ".env.local")
	envText := fmt.Sprintf("GATEWAY_BIN=%q\nOBSIDIAN_ROOT=%q\nCONTROL_PLANE_API_KEY=runtime-secret\n", target, vault)
	if err := os.WriteFile(envFile, []byte(envText), 0o600); err != nil {
		t.Fatal(err)
	}

	failValue := "0"
	if options.failFirstVerification {
		failValue = "1"
	}
	failAllValue := boolString(options.failAllVerification)
	alternateEnv := ""
	if options.alternateEnv {
		alternateEnv = filepath.Join(repo, "alternate.env")
	}
	env := append(testEnv(binDir),
		"CALL_LOG="+logFile,
		"REPORT_SET_TUPLE="+filepath.Join(repo, "report-set-tuple"),
		"SMOKE_CANDIDATE_RECORD="+filepath.Join(repo, "smoke-candidate-path"),
		"VERIFY_COUNT="+verifyCount,
		"FAIL_FIRST_VERIFY="+failValue,
		"FAIL_TESTS="+boolString(options.failTests),
		"FAIL_PERFORMANCE_SMOKE="+boolString(options.failPerformanceSmoke),
		"FAIL_RESOURCE_SMOKE="+boolString(options.failResourceSmoke),
		"FAIL_ALL_VERIFY="+failAllValue,
		"FAIL_RESTORE="+boolString(options.failRestore),
		"CORRUPT_STAGE="+boolString(options.corruptStage),
		"MUTATE_CANDIDATE="+boolString(options.mutateCandidateDuringSmoke),
		"MUTATE_CANDIDATE_AFTER_REPORTS="+boolString(options.mutateCandidateAfterReports),
		"REPLACE_RESTORE_CANDIDATE="+boolString(options.replaceRestoreCandidate),
		"REPLACE_RESTORE_CONTROLLER="+boolString(options.replaceRestoreController),
		"MUTATE_DEPENDENCY="+boolString(options.mutateDependencyDuringSmoke),
		"MALFORMED_REPORT="+boolString(options.malformedReport),
		"CROSS_REPORT_DRIFT="+boolString(options.crossReportDrift),
		"LARGE_PRIVATE_SMOKE_ARTIFACT="+boolString(options.largePrivateSmokeArtifact),
		"PRIVATE_SMOKE_ARTIFACT="+filepath.Join(repo, "private-smoke-artifact"),
		"CHANGE_COMMIT_ON_RECHECK="+boolString(options.changeCommitOnRecheck),
		"FAIL_SECOND_PREFLIGHT="+boolString(options.failSecondPreflight),
		"REVPARSE_COUNT="+filepath.Join(repo, "revparse-count"),
		"STATUS_COUNT="+filepath.Join(repo, "status-count"),
		"REAL_INSTALL=/usr/bin/install",
		"CONTROL_PLANE_API_KEY=parent-runtime-secret",
		"MCP_GATEWAY_ENV_FILE="+alternateEnv,
		"GATEWAY_CANDIDATE="+candidate,
		"RELEASE_ACTIVATION_CANDIDATE="+controller,
		"EXPECTED_CANDIDATE="+expectedCandidate,
		"EXPECTED_VAULT="+vault,
		"FAKE_CONTROLLER_SOURCE="+controllerSource,
		"CONTROLLER_REPLACEMENT="+controllerReplacement,
		"CONTROLLER_RACE_RECORD="+controllerRaceRecord,
		"PASSWD_HOME="+home,
		"TEST_REPO="+repo,
		"MAKE=make",
		"GO=go",
		"TUNNEL_HEALTH_URL_FILE="+filepath.Join(repo, "health.url"),
	)
	return &localReleaseHarness{t: t, repo: repo, target: target, vault: vault, logFile: logFile, verifyCount: verifyCount,
		smokeCandidateRecord: filepath.Join(repo, "smoke-candidate-path"), controllerRaceRecord: controllerRaceRecord,
		controller: controller, controllerSource: controllerSource, controllerReplacement: controllerReplacement, env: env}
}

func (h *localReleaseHarness) run(extra ...string) ([]byte, error) {
	h.t.Helper()
	cmd := exec.Command(filepath.Join(h.repo, "scripts", "release-local.sh"))
	cmd.Env = envWithOverrides(h.env, extra...)
	return cmd.CombinedOutput()
}

func (h *localReleaseHarness) runChannels(extra ...string) (stdout, stderr string, exit int) {
	h.t.Helper()
	cmd := exec.Command(filepath.Join(h.repo, "scripts", "release-local.sh"))
	cmd.Env = envWithOverrides(h.env, extra...)
	var stdoutBuffer, stderrBuffer strings.Builder
	cmd.Stdout = &stdoutBuffer
	cmd.Stderr = &stderrBuffer
	err := cmd.Run()
	if err != nil {
		var exitError *exec.ExitError
		if !errors.As(err, &exitError) {
			h.t.Fatalf("release start failed: %v", err)
		}
		exit = exitError.ExitCode()
	}
	return stdoutBuffer.String(), stderrBuffer.String(), exit
}

func (h *localReleaseHarness) dispatch(args ...string) ([]byte, error) {
	h.t.Helper()
	cmd := exec.Command(filepath.Join(h.repo, "scripts", "release-activation.sh"), args...)
	cmd.Env = append([]string{}, h.env...)
	return cmd.CombinedOutput()
}

func releaseActiveDir(repo string) string {
	return filepath.Join(repo, "passwd-home", "Library", "Application Support", "personal-mcp-gateway", "release", "obsidian", "active")
}

func installFakePasswdTools(t *testing.T, repo, binDir string) string {
	t.Helper()
	home := filepath.Join(repo, "passwd-home")
	trustedDir := filepath.Join(repo, "trusted-system-tools")
	trustedID := filepath.Join(trustedDir, "id")
	trustedDSCL := filepath.Join(trustedDir, "dscl")
	writeExecutable(t, trustedID, `#!/bin/sh
case "$1" in
  -un) printf '%s\n' testuser ;;
  -u) printf '%s\n' 501 ;;
  *) exit 2 ;;
esac
`)
	writeExecutable(t, trustedDSCL, `#!/bin/sh
printf 'NFSHomeDirectory: %s\n' "$PASSWD_HOME"
`)
	rewriteDispatcherPasswdTools(t, repo, trustedID, trustedDSCL)
	return home
}

const fakeReleaseController = `#!/bin/bash
set -euo pipefail

state_root="$PASSWD_HOME/Library/Application Support/personal-mcp-gateway/release/obsidian"
active="$state_root/active"
printf 'controller:%s\n' "${1:-status}" >>"$CALL_LOG"

error_line() {
  printf 'error=%s message=%s\n' "$1" "$2" >&2
  exit 1
}

argument() {
  local wanted="$1"
  shift
  while (( $# > 0 )); do
    if [[ "$1" == "$wanted" && $# -gt 1 ]]; then printf '%s\n' "$2"; return 0; fi
    shift
  done
  return 1
}

identity() {
  printf 'state=%s id=%s commit=%s sha256=%s dependency_sha256=%s\n' \
    "$(cat "$active/state")" "$(cat "$active/id")" \
    "$(cut -c1-12 "$active/commit")" "$(cut -c1-12 "$active/candidate-sha256")" \
    "$(cut -c1-12 "$active/dependency-sha256")"
}

clear_active() {
  rm -rf -- "$active"
}

recover() {
  printf '%s' rolling_back >"$active/state"
  local target
  target="$(cat "$active/target")"
  if [[ -f "$active/previous" ]]; then
    if ! install -m 755 "$active/previous" "$target"; then
      error_line recovery_unconfirmed 'automatic binary restoration failed'
    fi
    launchctl kickstart -k "gui/$(id -u)/test-label" >/dev/null
    if ! "$TEST_REPO/scripts/verify-live.sh"; then
      error_line recovery_unconfirmed 'runtime recovery is unconfirmed'
    fi
    clear_active
    return 0
  fi
  launchctl bootout "gui/$(id -u)/test-label" >/dev/null
  rm -f -- "$target"
  clear_active
  printf '%s\n' 'removed the failed first installation'
}

guide_active() {
  printf '%s\n' 'error=state_conflict message=event conflicts with release state' >&2
  identity >&2
  case "$(cat "$active/state")" in
    pending)
      printf 'accept=make release-accept RELEASE_ID=%s\n' "$(cat "$active/id")" >&2
      printf 'rollback=make release-rollback RELEASE_ID=%s\n' "$(cat "$active/id")" >&2
      ;;
    accepting)
      printf 'resume=make release-accept RELEASE_ID=%s\n' "$(cat "$active/id")" >&2
      ;;
    rolling_back)
      printf 'resume=make release-rollback RELEASE_ID=%s\n' "$(cat "$active/id")" >&2
      ;;
  esac
  exit 1
}

if [[ "${1:-status}" == release ]]; then
  if [[ ! -d "$active" ]]; then
    "$0" prepare "${@:2}" >/dev/null
    exec "$TEST_REPO/scripts/release-activation.sh" resume
  fi
  case "$(cat "$active/state")" in
    prepared)
      exec "$TEST_REPO/scripts/release-activation.sh" resume
      ;;
    pending|accepting|rolling_back)
      guide_active
      ;;
  esac
fi

case "${1:-status}" in
	prepare)
    mkdir -p -- "$state_root"
    [[ ! -e "$active" ]] || error_line state_conflict 'a release transaction is already active'
    candidate="$(argument --candidate "$@")"
    candidate_sha256="$(argument --candidate-sha256 "$@")"
    actual_candidate_sha256="$(shasum -a 256 "$candidate" | awk '{print $1}')"
    [[ "$actual_candidate_sha256" == "$candidate_sha256" ]] || error_line artifact_mismatch 'release artifact does not match'
    authority="$(argument --authority "$@")"
	    authority_sha256="$(argument --authority-sha256 "$@")"
	    if [[ "${REPLACE_RESTORE_CONTROLLER:-0}" == 1 ]]; then
	      cp "$CONTROLLER_REPLACEMENT" "$RELEASE_ACTIVATION_SELECTED_SOURCE"
	      chmod 755 "$RELEASE_ACTIVATION_SELECTED_SOURCE"
	      printf 'self=%s\nauthority=%s\nself_hash=%s\nselected_hash_during_swap=%s\n' \
	        "$0" "$authority" "$(shasum -a 256 "$0" | awk '{print $1}')" \
	        "$(shasum -a 256 "$RELEASE_ACTIVATION_SELECTED_SOURCE" | awk '{print $1}')" >"$CONTROLLER_RACE_RECORD"
	      cp "$FAKE_CONTROLLER_SOURCE" "$RELEASE_ACTIVATION_SELECTED_SOURCE"
	      chmod 755 "$RELEASE_ACTIVATION_SELECTED_SOURCE"
	    fi
	    [[ "$(shasum -a 256 "$0" | awk '{print $1}')" == "$authority_sha256" ]] || error_line authority_mismatch 'release controller identity does not match'
	    [[ "$(shasum -a 256 "$authority" | awk '{print $1}')" == "$authority_sha256" ]] || error_line authority_mismatch 'release controller identity does not match'
	    target="$(argument --target "$@")"
	    commit="$(argument --commit "$@")"
	    dependency_sha256="$(argument --dependency-sha256 "$@")"
	    printf 'health:%s\n' "$(argument --health-url-file "$@")" >>"$CALL_LOG"
    mkdir -p -- "$active"
    install -m 755 "$candidate" "$active/candidate"
    install -m 755 "$authority" "$active/authority"
    if [[ -f "$target" ]]; then install -m 755 "$target" "$active/previous"; fi
    printf '%s' release-0001 >"$active/id"
    printf '%s' "$target" >"$active/target"
    printf '%s' "$commit" >"$active/commit"
	    printf '%s' "$candidate_sha256" >"$active/candidate-sha256"
    printf '%s' "$dependency_sha256" >"$active/dependency-sha256"
    printf '%s' prepared >"$active/state"
    ;;
  resume)
    [[ -d "$active" ]] || error_line no_pending 'no release transaction is pending'
    [[ "$(cat "$active/state")" == prepared ]] || error_line state_conflict 'release cannot be resumed from its current state'
    if [[ "${STOP_AFTER_PREPARE:-0}" == 1 ]]; then error_line test_stop 'prepared test stop'; fi
    target="$(cat "$active/target")"
    next="$target.next.release-0001"
    install -m 755 "$active/candidate" "$next"
    if [[ "${CORRUPT_STAGE:-0}" == 1 ]]; then printf '#!/bin/sh\n# corrupt-stage\n' >"$next"; chmod 755 "$next"; fi
    if [[ "$(shasum -a 256 "$next" | awk '{print $1}')" != "$(shasum -a 256 "$active/candidate" | awk '{print $1}')" ]]; then
      rm -f -- "$next"
      recover >/dev/null || true
      error_line artifact_mismatch 'staged binary does not match the prepared candidate'
    fi
    mv -f -- "$next" "$target"
    launchctl kickstart -k "gui/$(id -u)/test-label" >/dev/null
    if ! "$TEST_REPO/scripts/verify-live.sh"; then
      recover >/dev/null
      error_line deployment_failed 'candidate readiness failed; previous runtime restored'
    fi
    printf '%s' pending >"$active/state"
    identity
    printf 'accept=make release-accept RELEASE_ID=%s\n' "$(cat "$active/id")"
    printf 'rollback=make release-rollback RELEASE_ID=%s\n' "$(cat "$active/id")"
    ;;
  status)
    if [[ ! -d "$active" ]]; then printf '%s\n' state=clear; exit 0; fi
    identity
    case "$(cat "$active/state")" in
      prepared) printf '%s\n' 'resume=make release'; printf 'rollback=make release-rollback RELEASE_ID=%s\n' "$(cat "$active/id")" ;;
      pending) printf 'accept=make release-accept RELEASE_ID=%s\n' "$(cat "$active/id")"; printf 'rollback=make release-rollback RELEASE_ID=%s\n' "$(cat "$active/id")" ;;
      rolling_back) printf 'resume=make release-rollback RELEASE_ID=%s\n' "$(cat "$active/id")" ;;
    esac
    ;;
  accept)
    id="$(argument --release-id "$@")"
    [[ -d "$active" && "$id" == "$(cat "$active/id")" ]] || error_line identity_mismatch 'release identity does not match'
    [[ "$(cat "$active/state")" == pending ]] || error_line state_conflict 'release is not pending acceptance'
    clear_active
    printf '%s\n' state=clear
    ;;
  rollback)
    id="$(argument --release-id "$@")"
    [[ -d "$active" && "$id" == "$(cat "$active/id")" ]] || error_line identity_mismatch 'release identity does not match'
    recover
    printf '%s\n' state=clear
    ;;
  update-after-fetch)
    [[ ! -d "$active" ]] || error_line state_conflict 'a release transaction is active'
    repo="$(argument --repo "$@")"
    expected="$(argument --expected-head "$@")"
    remote="$(argument --expected-remote-oid "$@")"
    [[ "$(git -C "$repo" branch --show-current)" == main ]] || error_line state_conflict 'source update failed'
    [[ -z "$(git -C "$repo" status --porcelain --untracked-files=all)" ]] || error_line state_conflict 'source update failed'
    [[ "$(git -C "$repo" rev-parse HEAD)" == "$expected" ]] || error_line state_conflict 'source update failed'
    git -C "$repo" cat-file -e "$remote^{commit}" >/dev/null || error_line state_conflict 'source update failed'
    git -C "$repo" merge --ff-only "$remote" >/dev/null || error_line state_conflict 'source update failed'
    [[ "$(git -C "$repo" rev-parse HEAD)" == "$remote" ]] || error_line state_conflict 'source update failed'
    printf '%s\n' 'state=clear action=update-after-fetch'
    ;;
  *) error_line usage 'unsupported fake controller command' ;;
esac
`

func boolString(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

func newScriptRepo(t *testing.T, names ...string) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	sourceDir := filepath.Dir(currentFile)
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(sourceDir, name))
		if err != nil {
			t.Fatal(err)
		}
		writeExecutable(t, filepath.Join(repo, "scripts", name), string(data))
	}
	configHelper, err := os.ReadFile(filepath.Join(sourceDir, "internal", "release-config.sh"))
	if err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(repo, "scripts", "internal", "release-config.sh"), string(configHelper))
	return repo
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func testEnv(binDir string) []string {
	return append(os.Environ(), "PATH="+binDir+":"+os.Getenv("PATH"))
}

func envWithOverrides(base []string, overrides ...string) []string {
	result := append([]string(nil), base...)
	for _, override := range overrides {
		key, _, ok := strings.Cut(override, "=")
		if !ok {
			result = append(result, override)
			continue
		}
		prefix := key + "="
		filtered := result[:0]
		for _, value := range result {
			if !strings.HasPrefix(value, prefix) {
				filtered = append(filtered, value)
			}
		}
		result = append(filtered, override)
	}
	return result
}

func assertFileContains(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), want) {
		t.Fatalf("%s = %q, want substring %q", path, data, want)
	}
}
