package scripts_test

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"personal-mcp-gateway/internal/releaseactivation"
)

// These are realistic-local composition tests, not actual LaunchAgent tests:
// the shell dispatcher, production CLI, Manager, Store, OSRuntime, child-process
// capture, and loopback HTTP probes are real; only launchctl and the supervised
// process that publishes its loopback URL are deterministic local fakes.
func TestReleaseActivationRealisticLocalComposition(t *testing.T) {
	controller := buildRealReleaseController(t)

	t.Run("prepared resume pending accept and rollback", func(t *testing.T) {
		fixture := newCompositionFixture(t, controller)
		defer fixture.server.Close()

		accepted := fixture.seedPrepared(t, "accept-candidate")
		stdout, stderr, exit := fixture.dispatch(t, "resume-if-active")
		assertCompositionResult(t, stdout, stderr, exit, pendingRecords(accepted), "", 0)
		assertInstalledHash(t, fixture.target, accepted.CandidateSHA256)

		stdout, stderr, exit = fixture.dispatch(t, "accept", "--release-id", string(accepted.ID))
		assertCompositionResult(t, stdout, stderr, exit, "state=clear\n", "", 0)
		fixture.assertClear(t)
		assertInstalledHash(t, fixture.target, accepted.CandidateSHA256)

		rolledBack := fixture.seedPrepared(t, "rollback-candidate")
		stdout, stderr, exit = fixture.dispatch(t, "resume-if-active")
		assertCompositionResult(t, stdout, stderr, exit, pendingRecords(rolledBack), "", 0)
		assertInstalledHash(t, fixture.target, rolledBack.CandidateSHA256)

		stdout, stderr, exit = fixture.dispatch(t, "rollback", "--release-id", string(rolledBack.ID))
		assertCompositionResult(t, stdout, stderr, exit, "state=clear\n", "", 0)
		fixture.assertClear(t)
		assertInstalledHash(t, fixture.target, rolledBack.PreviousSHA256)
	})

	t.Run("first install rollback unloads and removes candidate", func(t *testing.T) {
		fixture := newCompositionFixture(t, controller)
		defer fixture.server.Close()

		manifest := fixture.seedFirstInstallPrepared(t, "first-install-candidate")
		stdout, stderr, exit := fixture.dispatch(t, "resume-if-active")
		assertCompositionResult(t, stdout, stderr, exit, pendingRecords(manifest), "", 0)
		assertInstalledHash(t, fixture.target, manifest.CandidateSHA256)

		stdout, stderr, exit = fixture.dispatch(t, "rollback", "--release-id", string(manifest.ID))
		assertCompositionResult(t, stdout, stderr, exit, "state=clear\n", "", 0)
		fixture.assertClear(t)
		if _, err := os.Lstat(fixture.target); !os.IsNotExist(err) {
			t.Fatalf("first-install target remains after rollback: %v", err)
		}
		if _, err := os.Lstat(fixture.launchState); !os.IsNotExist(err) {
			t.Fatalf("first-install supervisor remains loaded after rollback: %v", err)
		}
	})

	t.Run("malformed state is rejected on exact channels", func(t *testing.T) {
		fixture := newCompositionFixture(t, controller)
		defer fixture.server.Close()
		fixture.seedPrepared(t, "malformed-candidate")
		manifestPath := filepath.Join(fixture.stateRoot, "active", "manifest.json")
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			t.Fatal(err)
		}
		data = []byte(strings.Replace(string(data), `"state": "prepared"`, `"state": "hostile/private/sentinel"`, 1))
		if err := os.WriteFile(manifestPath, data, 0o600); err != nil {
			t.Fatal(err)
		}
		stdout, stderr, exit := fixture.dispatch(t, "status")
		assertCompositionResult(t, stdout, stderr, exit, "", "error=state_malformed message=release state is malformed\n", 1)
		assertNoSentinel(t, stdout, stderr)
	})

	t.Run("busy store is rejected on exact channels", func(t *testing.T) {
		fixture := newCompositionFixture(t, controller)
		defer fixture.server.Close()
		fixture.seedPrepared(t, "busy-candidate")
		store, err := releaseactivation.NewStoreAt(fixture.stateRoot, os.Geteuid())
		if err != nil {
			t.Fatal(err)
		}
		locked, err := store.Acquire()
		if err != nil {
			t.Fatal(err)
		}
		defer locked.Close()
		stdout, stderr, exit := fixture.dispatch(t, "status")
		assertCompositionResult(t, stdout, stderr, exit, "", "error=busy message=release state is busy\n", 1)
	})

	t.Run("runtime drift remains pending and is rejected", func(t *testing.T) {
		fixture := newCompositionFixture(t, controller)
		defer fixture.server.Close()
		manifest := fixture.seedPrepared(t, "drift-candidate")
		stdout, stderr, exit := fixture.dispatch(t, "resume-if-active")
		assertCompositionResult(t, stdout, stderr, exit, pendingRecords(manifest), "", 0)
		if err := os.WriteFile(fixture.wrapper, []byte("#!/bin/sh\n# drifted\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		stdout, stderr, exit = fixture.dispatch(t, "accept", "--release-id", string(manifest.ID))
		assertCompositionResult(t, stdout, stderr, exit, "", "error=runtime_drift message=supervised runtime configuration changed\n", 1)
		active, inspectErr := fixture.store(t).Inspect()
		if inspectErr != nil || active == nil || active.State != releaseactivation.StatePending || active.ID != manifest.ID {
			t.Fatalf("active after drift = %#v, %v", active, inspectErr)
		}
	})

	t.Run("hostile launchctl output is bounded and never relayed", func(t *testing.T) {
		fixture := newCompositionFixture(t, controller)
		defer fixture.server.Close()
		fixture.extraEnv = append(fixture.extraEnv, "LAUNCHCTL_HOSTILE=1")
		fixture.seedPrepared(t, "hostile-candidate")
		stdout, stderr, exit := fixture.dispatch(t, "resume-if-active")
		assertCompositionResult(t, stdout, stderr, exit, "", "error=recovery_unconfirmed message=recovery could not be confirmed\n", 1)
		assertNoSentinel(t, stdout, stderr)
	})
}

type compositionFixture struct {
	repo, binDir, home, stateRoot, target, wrapper, mcpWrapper, plist, environment, healthFile string
	controller, launchState, candidate                                                         string
	extraEnv                                                                                   []string
	server                                                                                     *httptest.Server
}

func newCompositionFixture(t *testing.T, controller string) *compositionFixture {
	t.Helper()
	repo, binDir, home := newDispatcherRepo(t)
	root := t.TempDir()
	fixture := &compositionFixture{
		repo: repo, binDir: binDir, home: home,
		stateRoot: filepath.Join(home, "Library", "Application Support", "personal-mcp-gateway", "release", "obsidian"),
		target:    filepath.Join(root, "installed", "gateway"), wrapper: filepath.Join(root, "bindings", "run-tunnel.sh"),
		mcpWrapper: filepath.Join(root, "bindings", "run-mcp.sh"), plist: filepath.Join(root, "bindings", "agent.plist"),
		environment: filepath.Join(root, "bindings", "gateway.env"), healthFile: filepath.Join(root, "runtime", "health-url"),
		controller: controller, launchState: filepath.Join(root, "runtime", "loaded"), candidate: filepath.Join(root, "sources", "candidate"),
	}
	for _, path := range []string{fixture.target, fixture.wrapper, fixture.mcpWrapper, fixture.plist, fixture.environment, fixture.healthFile, fixture.candidate} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeExecutable(t, fixture.wrapper, "#!/bin/sh\nexit 0\n")
	writeExecutable(t, fixture.mcpWrapper, "#!/bin/sh\nexit 0\n")
	if err := os.WriteFile(fixture.plist, []byte("<plist/>\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.environment, []byte("BOUND=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fixture.server = httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/healthz" && request.URL.Path != "/readyz" {
			http.NotFound(writer, request)
			return
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("ok"))
	}))
	fixture.server.Listener = listener
	fixture.server.Start()
	writeExecutable(t, filepath.Join(binDir, "launchctl"), `#!/bin/sh
set -eu
if [ "${LAUNCHCTL_HOSTILE:-}" = 1 ]; then
  printf '%070000d/private/sentinel/runtime-secret\n' 0 >&2
  exit 9
fi
case "$1" in
  print)
    if [ -f "$LAUNCHCTL_STATE" ]; then
      printf 'program = %s\n' "$WRAPPER_PATH"
      exit 0
    fi
    exit 113
    ;;
  kickstart)
    printf '%s\n' "$HEALTH_BASE" >"$HEALTH_FILE"
    : >"$LAUNCHCTL_STATE"
    ;;
  bootout)
    rm -f -- "$LAUNCHCTL_STATE"
    ;;
  *) exit 9 ;;
esac
`)
	fixture.extraEnv = []string{
		"PASSWD_HOME=" + home,
		"LAUNCHCTL_STATE=" + fixture.launchState,
		"WRAPPER_PATH=" + fixture.wrapper,
		"HEALTH_FILE=" + fixture.healthFile,
		"HEALTH_BASE=" + fixture.server.URL,
	}
	return fixture
}

func (f *compositionFixture) seedPrepared(t *testing.T, candidateName string) *releaseactivation.Manifest {
	t.Helper()
	_ = os.Remove(f.launchState)
	_ = os.Remove(f.healthFile)
	writeExecutable(t, f.candidate, "#!/bin/sh\n# "+candidateName+"\nexit 0\n")
	writeExecutable(t, f.target, "#!/bin/sh\n# previous for "+candidateName+"\nexit 0\n")
	id, err := releaseactivation.NewReleaseID()
	if err != nil {
		t.Fatal(err)
	}
	manifest := releaseactivation.Manifest{
		State: releaseactivation.StatePrepared, ID: id, Commit: strings.Repeat("b", 40), DependencySHA256: strings.Repeat("9", 64), PreviousPresent: true,
		CandidateSHA256: mustHash(t, f.candidate), AuthoritySHA256: mustHash(t, f.controller), PreviousSHA256: mustHash(t, f.target),
		TargetPath: f.target, EffectiveUID: os.Geteuid(), LaunchAgentLabel: "com.example.realistic-local",
		PlistPath: f.plist, PlistSHA256: mustHash(t, f.plist), WrapperPath: f.wrapper, WrapperSHA256: mustHash(t, f.wrapper),
		MCPWrapperPath: f.mcpWrapper, MCPWrapperSHA256: mustHash(t, f.mcpWrapper),
		StdoutPath: filepath.Join(f.home, "Library", "Logs", "gateway.out.log"), StderrPath: filepath.Join(f.home, "Library", "Logs", "gateway.err.log"),
		EnvironmentPath: f.environment, EnvironmentSHA256: mustHash(t, f.environment), HealthURLFile: f.healthFile,
		ReadyTimeoutSeconds: 5, ReadyPollMilliseconds: 25,
	}
	store := f.store(t)
	locked, err := store.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer locked.Close()
	prepared, err := locked.Prepare(manifest, releaseactivation.ArtifactSources{Candidate: f.candidate, Authority: f.controller, Previous: f.target})
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}

func (f *compositionFixture) seedFirstInstallPrepared(t *testing.T, candidateName string) *releaseactivation.Manifest {
	t.Helper()
	_ = os.Remove(f.launchState)
	_ = os.Remove(f.healthFile)
	_ = os.Remove(f.target)
	writeExecutable(t, f.candidate, "#!/bin/sh\n# "+candidateName+"\nexit 0\n")
	id, err := releaseactivation.NewReleaseID()
	if err != nil {
		t.Fatal(err)
	}
	manifest := releaseactivation.Manifest{
		State: releaseactivation.StatePrepared, ID: id, Commit: strings.Repeat("c", 40), DependencySHA256: strings.Repeat("9", 64),
		CandidateSHA256: mustHash(t, f.candidate), AuthoritySHA256: mustHash(t, f.controller),
		TargetPath: f.target, EffectiveUID: os.Geteuid(), LaunchAgentLabel: "com.example.realistic-local",
		PlistPath: f.plist, PlistSHA256: mustHash(t, f.plist), WrapperPath: f.wrapper, WrapperSHA256: mustHash(t, f.wrapper),
		MCPWrapperPath: f.mcpWrapper, MCPWrapperSHA256: mustHash(t, f.mcpWrapper),
		StdoutPath: filepath.Join(f.home, "Library", "Logs", "gateway.out.log"), StderrPath: filepath.Join(f.home, "Library", "Logs", "gateway.err.log"),
		EnvironmentPath: f.environment, EnvironmentSHA256: mustHash(t, f.environment), HealthURLFile: f.healthFile,
		ReadyTimeoutSeconds: 5, ReadyPollMilliseconds: 25,
	}
	store := f.store(t)
	locked, err := store.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer locked.Close()
	prepared, err := locked.Prepare(manifest, releaseactivation.ArtifactSources{Candidate: f.candidate, Authority: f.controller})
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}

func (f *compositionFixture) store(t *testing.T) *releaseactivation.Store {
	t.Helper()
	store, err := releaseactivation.NewStoreAt(f.stateRoot, os.Geteuid())
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func (f *compositionFixture) dispatch(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	return runDispatcherEnv(t, f.repo, f.binDir, f.extraEnv, args...)
}

func (f *compositionFixture) assertClear(t *testing.T) {
	t.Helper()
	active, err := f.store(t).Inspect()
	if err != nil || active != nil {
		t.Fatalf("active = %#v, %v; want clear", active, err)
	}
}

func buildRealReleaseController(t *testing.T) string {
	t.Helper()
	root := repoRoot(t)
	output := filepath.Join(t.TempDir(), "release-activation")
	command := exec.Command("go", "build", "-o", output, "./cmd/release-activation")
	command.Dir = root
	cache := os.Getenv("GOCACHE")
	if cache == "" {
		cache = filepath.Join(t.TempDir(), "gocache")
	}
	command.Env = append(os.Environ(), "GOCACHE="+cache)
	if result, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build real controller: %v\n%s", err, result)
	}
	return output
}

func pendingRecords(manifest *releaseactivation.Manifest) string {
	return fmt.Sprintf("state=pending id=%s commit=%s sha256=%s dependency_sha256=%s\naccept=make release-accept RELEASE_ID=%s\nrollback=make release-rollback RELEASE_ID=%s\n",
		manifest.ID, manifest.Commit[:12], manifest.CandidateSHA256[:12], manifest.DependencySHA256[:12], manifest.ID, manifest.ID)
}

func mustHash(t *testing.T, path string) string {
	t.Helper()
	hash, err := releaseactivation.HashRegular(path)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func assertInstalledHash(t *testing.T, path, want string) {
	t.Helper()
	if got := mustHash(t, path); got != want {
		t.Fatalf("installed hash = %s, want %s", got, want)
	}
}

func assertCompositionResult(t *testing.T, stdout, stderr string, exit int, wantStdout, wantStderr string, wantExit int) {
	t.Helper()
	if stdout != wantStdout || stderr != wantStderr || exit != wantExit {
		t.Fatalf("exit=%d stdout=%q stderr=%q, want exit=%d stdout=%q stderr=%q", exit, stdout, stderr, wantExit, wantStdout, wantStderr)
	}
	if len(stdout) > 1024 || len(stderr) > 1024 {
		t.Fatalf("public channels exceeded composition bound: stdout=%d stderr=%d", len(stdout), len(stderr))
	}
}

func assertNoSentinel(t *testing.T, streams ...string) {
	t.Helper()
	joined := strings.Join(streams, "")
	for _, sentinel := range []string{"/private/sentinel", "runtime-secret", "hostile"} {
		if strings.Contains(joined, sentinel) {
			t.Fatalf("public output leaked %q: %q", sentinel, joined)
		}
	}
}
