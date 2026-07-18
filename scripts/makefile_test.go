package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCanonicalMakeTestIsolatesTimingSensitivePackages(t *testing.T) {
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

	callLog := filepath.Join(repo, "calls.log")
	callLock := filepath.Join(repo, "call.lock")
	fakeGo := filepath.Join(repo, "fake-go")
	writeExecutable(t, fakeGo, `#!/usr/bin/env bash
set -euo pipefail

if [[ "$*" == "list -m" ]]; then
  printf '%s\n' example.test/gateway
  exit 0
fi
if [[ "$*" == "list ./..." ]]; then
  printf '%s\n' \
    example.test/gateway/cmd/gateway \
    example.test/gateway/cmd/gateway-smoke \
    example.test/gateway/internal/fsx \
    example.test/gateway/scripts
  exit 0
fi

case "$*" in
  "test -count=1 example.test/gateway/cmd/gateway example.test/gateway/internal/fsx") lane=ordinary ;;
  "test -count=1 ./cmd/gateway-smoke") lane=gateway-smoke ;;
  "test -count=1 ./scripts") lane=scripts ;;
  *) printf 'unexpected fake go invocation: %s\n' "$*" >&2; exit 9 ;;
esac

if ! mkdir "$TEST_CALL_LOCK" 2>/dev/null; then
  printf 'overlapping test lane: %s\n' "$lane" >&2
  exit 10
fi
trap 'rmdir "$TEST_CALL_LOCK"' EXIT
printf 'start:%s:%s\n' "$lane" "$*" >>"$TEST_CALL_LOG"
sleep 0.05
printf 'end:%s:%s\n' "$lane" "$*" >>"$TEST_CALL_LOG"
`)

	cmd := exec.Command("make", "--no-print-directory", "-C", repo, "test", "GO="+fakeGo, "GOCACHE="+filepath.Join(repo, ".gocache"))
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "MAKELEVEL=") && !strings.HasPrefix(value, "MAKEFLAGS=") && !strings.HasPrefix(value, "MFLAGS=") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Env = append(cmd.Env, "TEST_CALL_LOG="+callLog, "TEST_CALL_LOCK="+callLock)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("canonical make test failed: %v\n%s", err, output)
	}

	logData, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatal(err)
	}
	want := "" +
		"start:ordinary:test -count=1 example.test/gateway/cmd/gateway example.test/gateway/internal/fsx\n" +
		"end:ordinary:test -count=1 example.test/gateway/cmd/gateway example.test/gateway/internal/fsx\n" +
		"start:gateway-smoke:test -count=1 ./cmd/gateway-smoke\n" +
		"end:gateway-smoke:test -count=1 ./cmd/gateway-smoke\n" +
		"start:scripts:test -count=1 ./scripts\n" +
		"end:scripts:test -count=1 ./scripts\n"
	if string(logData) != want {
		t.Fatalf("canonical test schedule = %q, want %q", logData, want)
	}
}
