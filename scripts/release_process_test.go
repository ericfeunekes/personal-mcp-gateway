package scripts_test

import (
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
	repo, target, logFile, verifyCount, output, err := runLocalRelease(t, releaseOptions{previous: true})
	if err != nil {
		t.Fatalf("release failed: %v\n%s", err, output)
	}
	assertFileContains(t, target, "candidate-binary")
	if strings.Contains(string(output), "runtime-secret") {
		t.Fatalf("release output leaked configured secret: %s", output)
	}
	if strings.Contains(string(output), "parent-runtime-secret") {
		t.Fatalf("release output leaked inherited secret: %s", output)
	}
	if !strings.Contains(string(output), "release complete: commit=0123456789ab") {
		t.Fatalf("release output = %q", output)
	}
	assertFileContains(t, logFile, "make:test\nmake:build\ngo:run ./cmd/gateway-smoke")
	assertFileContains(t, logFile, "launchctl:kickstart -k")
	assertFileContains(t, verifyCount, "1")
	rollbacks, err := filepath.Glob(filepath.Join(repo, "installed", "gateway.rollback.*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rollbacks) != 0 {
		t.Fatalf("successful release retained rollback files: %v", rollbacks)
	}
}

func TestLocalReleaseRestoresPreviousBinaryWhenReadinessFails(t *testing.T) {
	_, target, logFile, verifyCount, output, err := runLocalRelease(t, releaseOptions{
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
	if !strings.Contains(string(output), "previous binary restored") ||
		!strings.Contains(string(output), "rollback runtime is live and ready") {
		t.Fatalf("rollback output = %q", output)
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
	rollbacks, globErr := filepath.Glob(filepath.Join(repo, "installed", "gateway.rollback.*"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(rollbacks) != 1 {
		t.Fatalf("rollback copies = %v, want one retained copy", rollbacks)
	}
	assertFileContains(t, rollbacks[0], "previous-binary")
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
	rollbacks, globErr := filepath.Glob(filepath.Join(repo, "installed", "gateway.rollback.*"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(rollbacks) != 1 {
		t.Fatalf("rollback copies = %v, want one retained copy", rollbacks)
	}
}

func TestLocalReleaseRemovesFailedFirstInstallation(t *testing.T) {
	_, target, _, verifyCount, output, err := runLocalRelease(t, releaseOptions{failFirstVerification: true})
	if err == nil {
		t.Fatalf("release unexpectedly succeeded: %s", output)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("first failed installation still exists: %v", statErr)
	}
	assertFileContains(t, verifyCount, "1")
	if !strings.Contains(string(output), "removed the failed first installation") {
		t.Fatalf("first-install rollback output = %q", output)
	}
}

func TestLocalReleaseRejectsCandidateInstalledPathAlias(t *testing.T) {
	_, target, logFile, _, output, err := runLocalRelease(t, releaseOptions{
		previous:       true,
		candidateAlias: true,
	})
	if err == nil || !strings.Contains(string(output), "must use distinct paths") {
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
	if err == nil || !strings.Contains(string(output), "changed during its MCP smoke") {
		t.Fatalf("mutated-candidate release err=%v output=%q", err, output)
	}
	assertFileContains(t, target, "previous-binary")
}

func TestLocalReleaseRejectsCommitChangedDuringRelease(t *testing.T) {
	_, target, _, _, output, err := runLocalRelease(t, releaseOptions{
		previous:              true,
		changeCommitOnRecheck: true,
	})
	if err == nil || !strings.Contains(string(output), "HEAD changed during release") {
		t.Fatalf("changed-commit release err=%v output=%q", err, output)
	}
	assertFileContains(t, target, "previous-binary")
}

func TestLocalReleaseRejectsDirtyTreeOnSecondPreflight(t *testing.T) {
	_, target, _, _, output, err := runLocalRelease(t, releaseOptions{
		previous:            true,
		failSecondPreflight: true,
	})
	if err == nil || !strings.Contains(string(output), "working tree must be clean") {
		t.Fatalf("second-preflight release err=%v output=%q", err, output)
	}
	assertFileContains(t, target, "previous-binary")
}

func TestLocalReleaseRejectsAlternateEnvironmentFile(t *testing.T) {
	_, target, _, _, output, err := runLocalRelease(t, releaseOptions{
		previous:     true,
		alternateEnv: true,
	})
	if err == nil || !strings.Contains(string(output), "uses the repo-local .env.local") {
		t.Fatalf("alternate-env release err=%v output=%q", err, output)
	}
	assertFileContains(t, target, "previous-binary")
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
	repo := newScriptRepo(t, "update-local.sh")
	binDir := filepath.Join(repo, "bin")
	logFile := filepath.Join(repo, "calls.log")
	writeExecutable(t, filepath.Join(binDir, "git"), `#!/bin/sh
case "$*" in
  *"branch --show-current"*) printf '%s\n' "${FAKE_BRANCH:-main}" ;;
  *"status --porcelain"*)
    if [ "${FAKE_GIT_STATUS_ERROR:-0}" = 1 ]; then exit 3; fi
    printf '%s' "${FAKE_GIT_STATUS:-}"
    ;;
  *"rev-parse HEAD"*) printf '%s\n' "${FAKE_HEAD:-same-commit}" ;;
  *"rev-parse origin/main"*) printf '%s\n' "${FAKE_ORIGIN_HEAD:-same-commit}" ;;
  *"merge --ff-only origin/main"*)
    printf 'git:%s\n' "$*" >>"$CALL_LOG"
    if [ "${FAKE_MERGE_ERROR:-0}" = 1 ]; then exit 4; fi
    ;;
  *) printf 'git:%s\n' "$*" >>"$CALL_LOG" ;;
esac
`)
	writeExecutable(t, filepath.Join(binDir, "make"), "#!/bin/sh\nprintf 'make:%s\\n' \"$*\" >>\"$CALL_LOG\"\n")

	cmd := exec.Command(filepath.Join(repo, "scripts", "update-local.sh"))
	cmd.Env = append(testEnv(binDir), "CALL_LOG="+logFile, "MAKE=make")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("update failed: %v\n%s", err, output)
	}
	logData, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	logText := string(logData)
	fetch := strings.Index(logText, "fetch origin")
	merge := strings.Index(logText, "merge --ff-only origin/main")
	release := strings.Index(logText, "make:--no-print-directory -C "+repo+" release")
	if fetch < 0 || merge <= fetch || release <= merge {
		t.Fatalf("update call order = %q", logText)
	}

	cmd = exec.Command(filepath.Join(repo, "scripts", "update-local.sh"))
	cmd.Env = append(testEnv(binDir), "CALL_LOG="+logFile, "MAKE=make", "FAKE_BRANCH=feature")
	output, err = cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "main branch") {
		t.Fatalf("non-main update err=%v output=%q", err, output)
	}

	cmd = exec.Command(filepath.Join(repo, "scripts", "update-local.sh"))
	cmd.Env = append(testEnv(binDir), "CALL_LOG="+logFile, "MAKE=make", "FAKE_GIT_STATUS= M README.md")
	output, err = cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "working tree must be clean") {
		t.Fatalf("dirty update err=%v output=%q", err, output)
	}

	cmd = exec.Command(filepath.Join(repo, "scripts", "update-local.sh"))
	cmd.Env = append(testEnv(binDir),
		"CALL_LOG="+logFile,
		"MAKE=make",
		"FAKE_HEAD=local-only-commit",
		"FAKE_ORIGIN_HEAD=landed-commit",
	)
	output, err = cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "exactly match origin/main") {
		t.Fatalf("ahead-main update err=%v output=%q", err, output)
	}

	cmd = exec.Command(filepath.Join(repo, "scripts", "update-local.sh"))
	cmd.Env = append(testEnv(binDir), "CALL_LOG="+logFile, "MAKE=make", "FAKE_GIT_STATUS_ERROR=1")
	output, err = cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "unable to inspect the working tree") {
		t.Fatalf("failed-status update err=%v output=%q", err, output)
	}

	cmd = exec.Command(filepath.Join(repo, "scripts", "update-local.sh"))
	cmd.Env = append(testEnv(binDir), "CALL_LOG="+logFile, "MAKE=make", "FAKE_MERGE_ERROR=1")
	output, err = cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("failed merge update succeeded: %s", output)
	}
}

type releaseOptions struct {
	previous                   bool
	failFirstVerification      bool
	failAllVerification        bool
	failRestore                bool
	candidateAlias             bool
	mutateCandidateDuringSmoke bool
	changeCommitOnRecheck      bool
	failSecondPreflight        bool
	corruptStage               bool
	alternateEnv               bool
}

func runLocalRelease(t *testing.T, options releaseOptions) (repo, target, logFile, verifyCount string, output []byte, runErr error) {
	t.Helper()
	repo = newScriptRepo(t, "release-local.sh", "release-preflight.sh")
	binDir := filepath.Join(repo, "bin")
	logFile = filepath.Join(repo, "calls.log")
	verifyCount = filepath.Join(repo, "verify-count")
	candidate := filepath.Join(repo, ".build", "gateway")
	target = filepath.Join(repo, "installed", "gateway")
	if options.candidateAlias {
		candidate = target
	}
	vault := filepath.Join(repo, "vault")
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
	writeExecutable(t, filepath.Join(binDir, "go"), `#!/bin/sh
if [ -n "${CONTROL_PLANE_API_KEY:-}" ]; then printf '%s\n' "$CONTROL_PLANE_API_KEY"; exit 9; fi
printf 'go:%s\n' "$*" >>"$CALL_LOG"
expected="run ./cmd/gateway-smoke --gateway-bin $EXPECTED_CANDIDATE --obsidian-root $EXPECTED_VAULT"
if [ "$*" != "$expected" ]; then printf 'unexpected smoke args\n'; exit 7; fi
if [ "${MUTATE_CANDIDATE:-0}" = 1 ]; then printf '# changed\n' >>"$GATEWAY_CANDIDATE"; fi
exit 0
`)
	makeScript := fmt.Sprintf(`#!/bin/sh
if [ -n "${CONTROL_PLANE_API_KEY:-}" ]; then printf '%%s\n' "$CONTROL_PLANE_API_KEY"; exit 9; fi
target=""
for arg in "$@"; do target="$arg"; done
printf 'make:%%s\n' "$target" >>"$CALL_LOG"
if [ "$target" = build ]; then
  mkdir -p %q
  printf '#!/bin/sh\n# candidate-binary\n' >%q
  chmod 755 %q
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
if [ "${FAIL_RESTORE:-0}" = 1 ] && echo "$source_path" | grep -q '\.rollback\.'; then exit 8; fi
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
	cmd := exec.Command(filepath.Join(repo, "scripts", "release-local.sh"))
	cmd.Env = append(testEnv(binDir),
		"CALL_LOG="+logFile,
		"VERIFY_COUNT="+verifyCount,
		"FAIL_FIRST_VERIFY="+failValue,
		"FAIL_ALL_VERIFY="+failAllValue,
		"FAIL_RESTORE="+boolString(options.failRestore),
		"CORRUPT_STAGE="+boolString(options.corruptStage),
		"MUTATE_CANDIDATE="+boolString(options.mutateCandidateDuringSmoke),
		"CHANGE_COMMIT_ON_RECHECK="+boolString(options.changeCommitOnRecheck),
		"FAIL_SECOND_PREFLIGHT="+boolString(options.failSecondPreflight),
		"REVPARSE_COUNT="+filepath.Join(repo, "revparse-count"),
		"STATUS_COUNT="+filepath.Join(repo, "status-count"),
		"REAL_INSTALL=/usr/bin/install",
		"CONTROL_PLANE_API_KEY=parent-runtime-secret",
		"MCP_GATEWAY_ENV_FILE="+alternateEnv,
		"GATEWAY_CANDIDATE="+candidate,
		"EXPECTED_CANDIDATE="+expectedCandidate,
		"EXPECTED_VAULT="+vault,
		"MAKE=make",
		"GO=go",
		"TUNNEL_HEALTH_URL_FILE="+filepath.Join(repo, "health.url"),
	)
	output, runErr = cmd.CombinedOutput()
	return repo, target, logFile, verifyCount, output, runErr
}

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
