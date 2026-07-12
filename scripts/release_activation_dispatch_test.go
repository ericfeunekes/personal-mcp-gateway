package scripts_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestReleaseActivationDispatcherClearStatus(t *testing.T) {
	repo, binDir, _ := newDispatcherRepo(t)
	cmd := exec.Command(filepath.Join(repo, "scripts", "release-activation.sh"), "status")
	cmd.Env = testEnv(binDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("status failed: %v\n%s", err, output)
	}
	if string(output) != "state=clear\n" {
		t.Fatalf("status output = %q", output)
	}
}

func TestReleaseActivationDispatcherDoesNotUsePasswdToolsFromPath(t *testing.T) {
	repo, binDir, _ := newDispatcherRepo(t)
	sentinel := filepath.Join(repo, "hostile-path-passwd-tool-ran")
	writeExecutable(t, filepath.Join(binDir, "id"), "#!/bin/sh\ntouch \"$HOSTILE_PASSWD_SENTINEL\"\nprintf '%s\\n' attacker\n")
	writeExecutable(t, filepath.Join(binDir, "dscl"), "#!/bin/sh\ntouch \"$HOSTILE_PASSWD_SENTINEL\"\nprintf '%s\\n' 'NFSHomeDirectory: /private/attacker'\n")

	cmd := exec.Command(filepath.Join(repo, "scripts", "release-activation.sh"), "status")
	cmd.Env = append(testEnv(binDir), "HOSTILE_PASSWD_SENTINEL="+sentinel)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("status failed: %v\n%s", err, output)
	}
	if string(output) != "state=clear\n" {
		t.Fatalf("status output = %q", output)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("dispatcher selected passwd tool from PATH: %v", err)
	}
}

func TestReleaseActivationDispatcherExecsPinnedResume(t *testing.T) {
	repo, binDir, home := newDispatcherRepo(t)
	authority := filepath.Join(home, "Library", "Application Support", "personal-mcp-gateway", "release", "obsidian", "active", "authority")
	writeExecutable(t, authority, "#!/bin/sh\nprintf 'pinned:%s\\n' \"$*\"\n")

	cmd := exec.Command(filepath.Join(repo, "scripts", "release-activation.sh"), "resume-if-active")
	cmd.Env = testEnv(binDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("resume failed: %v\n%s", err, output)
	}
	if string(output) != "pinned:release\n" {
		t.Fatalf("resume output = %q", output)
	}
}

func TestReleaseActivationDispatcherFailsWithLiteralOnMalformedActive(t *testing.T) {
	repo, binDir, home := newDispatcherRepo(t)
	active := filepath.Join(home, "Library", "Application Support", "personal-mcp-gateway", "release", "obsidian", "active")
	if err := os.MkdirAll(active, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(home, "private-sentinel")
	cmd := exec.Command(filepath.Join(repo, "scripts", "release-activation.sh"), "status")
	cmd.Env = append(testEnv(binDir), "SENTINEL="+sentinel)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("malformed active succeeded: %s", output)
	}
	want := "error=authority_missing message=the release controller is unavailable\n"
	if string(output) != want {
		t.Fatalf("malformed output = %q, want %q", output, want)
	}
	if strings.Contains(string(output), sentinel) {
		t.Fatalf("output leaked sentinel: %s", output)
	}
}

func TestReleaseActivationDispatcherTreatsDanglingActiveAsActive(t *testing.T) {
	repo, binDir, home := newDispatcherRepo(t)
	active := filepath.Join(home, "Library", "Application Support", "personal-mcp-gateway", "release", "obsidian", "active")
	if err := os.MkdirAll(filepath.Dir(active), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, "missing-private-target"), active); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, exit := runDispatcher(t, repo, binDir, "status")
	if exit != 1 || stdout != "" || stderr != "error=authority_missing message=the release controller is unavailable\n" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exit, stdout, stderr)
	}
}

func TestReleaseActivationDispatcherRelaysChannelsByteExactly(t *testing.T) {
	repo, binDir, _ := newDispatcherRepo(t)
	controller := filepath.Join(repo, "controller")
	writeExecutable(t, controller, "#!/bin/sh\nprintf 'out-without-newline'\nprintf 'error=busy message=release state is busy\\n' >&2\nexit 1\n")
	stdout, stderr, exit := runDispatcherEnv(t, repo, binDir, []string{"RELEASE_ACTIVATION_CANDIDATE=" + controller}, "status-private")
	if exit != 1 || stdout != "out-without-newline" || stderr != "error=busy message=release state is busy\n" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exit, stdout, stderr)
	}
}

func TestReleaseActivationDispatcherRetriesClearToActiveOnce(t *testing.T) {
	repo, binDir, home := newDispatcherRepo(t)
	controller := filepath.Join(repo, "controller")
	active := filepath.Join(home, "Library", "Application Support", "personal-mcp-gateway", "release", "obsidian", "active")
	logFile := filepath.Join(repo, "attempts")
	pinnedSource := filepath.Join(repo, "pinned-source")
	writeExecutable(t, pinnedSource, "#!/bin/sh\nprintf 'pinned:%s\\n' \"$*\"\n")
	writeExecutable(t, controller, `#!/bin/sh
printf 'current\n' >>"$ATTEMPT_LOG"
mkdir -p "$ACTIVE_PATH"
cp "$PINNED_SOURCE" "$ACTIVE_PATH/authority"
chmod 755 "$ACTIVE_PATH/authority"
printf '%s\n' 'error=authority_mismatch message=release controller identity does not match' >&2
exit 1
`)
	stdout, stderr, exit := runDispatcherEnv(t, repo, binDir, []string{
		"RELEASE_ACTIVATION_CANDIDATE=" + controller,
		"ATTEMPT_LOG=" + logFile,
		"ACTIVE_PATH=" + active,
		"PINNED_SOURCE=" + pinnedSource,
	}, "release", "--private-prepare-arg")
	if exit != 0 || stdout != "pinned:release --private-prepare-arg\n" || stderr != "" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exit, stdout, stderr)
	}
	assertFileContains(t, logFile, "current\n")
}

func TestReleaseActivationDispatcherRetriesAuthorityAToBOnce(t *testing.T) {
	repo, binDir, home := newDispatcherRepo(t)
	active := filepath.Join(home, "Library", "Application Support", "personal-mcp-gateway", "release", "obsidian", "active")
	authority := filepath.Join(active, "authority")
	replacement := filepath.Join(repo, "replacement")
	writeExecutable(t, replacement, "#!/bin/sh\nprintf 'authority-b:%s\\n' \"$*\"\n")
	writeExecutable(t, authority, `#!/bin/sh
old="$ACTIVE_PATH.old"
mv "$ACTIVE_PATH" "$old"
mkdir -p "$ACTIVE_PATH"
cp "$REPLACEMENT" "$ACTIVE_PATH/authority"
chmod 755 "$ACTIVE_PATH/authority"
printf '%s\n' 'error=authority_mismatch message=release controller identity does not match' >&2
exit 1
`)
	stdout, stderr, exit := runDispatcherEnv(t, repo, binDir, []string{"ACTIVE_PATH=" + active, "REPLACEMENT=" + replacement}, "status")
	if exit != 0 || stdout != "authority-b:status\n" || stderr != "" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exit, stdout, stderr)
	}
}

func TestReleaseActivationDispatcherSuppressesStartFailure(t *testing.T) {
	repo, binDir, home := newDispatcherRepo(t)
	authority := filepath.Join(home, "Library", "Application Support", "personal-mcp-gateway", "release", "obsidian", "active", "authority")
	writeExecutable(t, authority, "#!/private/missing/interpreter\n")
	stdout, stderr, exit := runDispatcher(t, repo, binDir, "status")
	if exit != 1 || stdout != "" || stderr != "error=authority_missing message=the release controller is unavailable\n" || strings.Contains(stderr, "private/missing") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exit, stdout, stderr)
	}
}

func TestReleaseActivationDispatcherRejectsOversizedOutput(t *testing.T) {
	repo, binDir, home := newDispatcherRepo(t)
	authority := filepath.Join(home, "Library", "Application Support", "personal-mcp-gateway", "release", "obsidian", "active", "authority")
	writeExecutable(t, authority, "#!/bin/sh\nyes x | head -c 70000\n")
	stdout, stderr, exit := runDispatcher(t, repo, binDir, "status")
	if exit != 1 || stdout != "" || stderr != "error=authority_missing message=the release controller is unavailable\n" {
		t.Fatalf("exit=%d stdout-len=%d stderr=%q", exit, len(stdout), stderr)
	}
}

func TestReleaseActivationDispatcherDoesNotLimitControllerArtifactWrites(t *testing.T) {
	repo, binDir, home := newDispatcherRepo(t)
	authority := filepath.Join(home, "Library", "Application Support", "personal-mcp-gateway", "release", "obsidian", "active", "authority")
	artifact := filepath.Join(repo, "candidate-artifact")
	writeExecutable(t, authority, "#!/bin/sh\nhead -c 70000 /dev/zero >\"$ARTIFACT_PATH\"\nprintf '%s\\n' 'state=prepared release_id=test-release'\n")
	stdout, stderr, exit := runDispatcherEnv(t, repo, binDir, []string{"ARTIFACT_PATH=" + artifact}, "status")
	if exit != 0 || stdout != "state=prepared release_id=test-release\n" || stderr != "" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exit, stdout, stderr)
	}
	info, err := os.Stat(artifact)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 70000 {
		t.Fatalf("artifact size = %d, want 70000", info.Size())
	}
}

func TestReleaseActivationDispatcherDoesNotHangOnDescendantHeldChannels(t *testing.T) {
	repo, binDir, home := newDispatcherRepo(t)
	authority := filepath.Join(home, "Library", "Application Support", "personal-mcp-gateway", "release", "obsidian", "active", "authority")
	descendantPID := filepath.Join(repo, "descendant.pid")
	writeExecutable(t, authority, "#!/bin/sh\nsleep 30 &\nprintf '%s' \"$!\" >\"$DESCENDANT_PID\"\nprintf '%s\\n' 'state=prepared release_id=test-release'\n")

	started := time.Now()
	stdout, stderr, exit := runDispatcherEnv(t, repo, binDir, []string{"DESCENDANT_PID=" + descendantPID}, "status")
	elapsed := time.Since(started)
	pIDBytes, err := os.ReadFile(descendantPID)
	if err != nil {
		t.Fatal(err)
	}
	pID, err := strconv.Atoi(string(pIDBytes))
	if err != nil {
		t.Fatal(err)
	}
	if exit != 1 || stdout != "" || stderr != "error=authority_missing message=the release controller is unavailable\n" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exit, stdout, stderr)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("dispatcher waited %s for descendant-held output channels", elapsed)
	}
	process, err := os.FindProcess(pID)
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Signal(syscall.Signal(0)); err == nil {
		t.Fatalf("dispatcher left controller descendant %d running", pID)
	}
}

func TestReleaseActivationDispatcherWatchdogBoundsNonTerminatingController(t *testing.T) {
	repo, binDir, home := newDispatcherRepo(t)
	rewriteDispatcherControllerTimeout(t, repo, "1")
	authority := filepath.Join(home, "Library", "Application Support", "personal-mcp-gateway", "release", "obsidian", "active", "authority")
	writeExecutable(t, authority, "#!/bin/sh\nwhile :; do printf 'unbounded-output'; done\n")

	started := time.Now()
	stdout, stderr, exit := runDispatcher(t, repo, binDir, "status")
	elapsed := time.Since(started)
	if exit != 1 || stdout != "" || stderr != "error=authority_missing message=the release controller is unavailable\n" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exit, stdout, stderr)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("dispatcher watchdog took %s", elapsed)
	}
}

func TestReleaseActivationDispatcherRetriesAuthorityMismatchOnlyOnce(t *testing.T) {
	repo, binDir, home := newDispatcherRepo(t)
	logFile := filepath.Join(repo, "attempts")
	authority := filepath.Join(home, "Library", "Application Support", "personal-mcp-gateway", "release", "obsidian", "active", "authority")
	writeExecutable(t, authority, "#!/bin/sh\nprintf 'attempt\\n' >>\"$ATTEMPT_LOG\"\nprintf '%s\\n' 'error=authority_mismatch message=release controller identity does not match' >&2\nexit 1\n")
	stdout, stderr, exit := runDispatcherEnv(t, repo, binDir, []string{"ATTEMPT_LOG=" + logFile}, "status")
	if exit != 1 || stdout != "" || stderr != "error=authority_missing message=the release controller is unavailable\n" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exit, stdout, stderr)
	}
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "attempt\nattempt\n" {
		t.Fatalf("attempt log = %q", data)
	}
}

func runDispatcher(t *testing.T, repo, binDir string, args ...string) (stdout, stderr string, exit int) {
	t.Helper()
	return runDispatcherEnv(t, repo, binDir, nil, args...)
}

func runDispatcherEnv(t *testing.T, repo, binDir string, extraEnv []string, args ...string) (stdout, stderr string, exit int) {
	t.Helper()
	cmd := exec.Command(filepath.Join(repo, "scripts", "release-activation.sh"), args...)
	cmd.Env = append(testEnv(binDir), extraEnv...)
	var stdoutBuffer, stderrBuffer bytes.Buffer
	cmd.Stdout = &stdoutBuffer
	cmd.Stderr = &stderrBuffer
	err := cmd.Run()
	exit = 0
	if err != nil {
		var exitError *exec.ExitError
		if !strings.Contains(err.Error(), "exit status") || !errors.As(err, &exitError) {
			t.Fatalf("dispatcher start failed: %v", err)
		}
		exit = exitError.ExitCode()
	}
	return stdoutBuffer.String(), stderrBuffer.String(), exit
}

func newDispatcherRepo(t *testing.T) (repo, binDir, home string) {
	t.Helper()
	repo = newScriptRepo(t, "release-activation.sh")
	binDir = filepath.Join(repo, "bin")
	home = filepath.Join(repo, "passwd-home")
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
	t.Setenv("PASSWD_HOME", home)
	return repo, binDir, home
}

func rewriteDispatcherPasswdTools(t *testing.T, repo, trustedID, trustedDSCL string) {
	t.Helper()
	path := filepath.Join(repo, "scripts", "release-activation.sh")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ReplaceAll(string(data), "/usr/bin/id", trustedID)
	text = strings.ReplaceAll(text, "/usr/bin/dscl", trustedDSCL)
	if text == string(data) {
		t.Fatal("dispatcher fixture did not contain trusted passwd-tool literals")
	}
	writeExecutable(t, path, text)
}

func rewriteDispatcherControllerTimeout(t *testing.T, repo, seconds string) {
	t.Helper()
	path := filepath.Join(repo, "scripts", "release-activation.sh")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	const productionTimeout = "readonly controller_timeout_seconds=720"
	text := strings.Replace(string(data), productionTimeout, "readonly controller_timeout_seconds="+seconds, 1)
	if text == string(data) {
		t.Fatal("dispatcher fixture did not contain controller timeout literal")
	}
	writeExecutable(t, path, text)
}
