package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"personal-mcp-gateway/internal/releaseactivation"
)

const (
	testID             = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testHash           = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	testDependency     = "9999999999999999999999999999999999999999999999999999999999999999"
	testPasswdHomeEnv  = "PERSONAL_MCP_GATEWAY_RELEASE_CONTROLLER_TEST_PASSWD_HOME"
	testHealthRootEnv  = "PERSONAL_MCP_GATEWAY_RELEASE_CONTROLLER_TEST_HEALTH_ROOT"
	testReadyBoundsEnv = "PERSONAL_MCP_GATEWAY_RELEASE_CONTROLLER_TEST_READY_BOUNDS"
)

func TestMain(m *testing.M) {
	home := os.Getenv(testPasswdHomeEnv)
	if home == "" {
		os.Exit(m.Run())
	}
	_ = os.Unsetenv(testPasswdHomeEnv)
	_ = os.Unsetenv("CONTROL_PLANE_API_KEY")
	_ = os.Unsetenv("OPENAI_API_KEY")
	// Test-binary-only seam: serviceTable's healthURLFile entries are fixed
	// absolute /tmp/personal-mcp-gateway paths shared with the real gateway,
	// so a composition test that dispatches a real "restart"/"verify-live"/
	// "install-launchagent" through this test-built controller (see
	// scripts/release_activation_integration_test.go) must never resolve
	// them for real. This env var is read only here, never from production
	// main(), and only relocates the leaf file name under a caller-chosen
	// test root.
	if healthRoot := os.Getenv(testHealthRootEnv); healthRoot != "" {
		_ = os.Unsetenv(testHealthRootEnv)
		relocateServiceTableHealthURLFiles(healthRoot)
	}
	// Test-binary-only seam: readyTimeoutSeconds/readyPollMilliseconds are
	// fixed at 45s/1000ms for every server (decision #2's uniform semantics
	// leaves no per-call override), which makes an "unloaded" or "not-ready"
	// verify-live composition test take the full bound before it can observe
	// the expected failure. This env var (format "<timeoutSeconds>:
	// <pollMilliseconds>", read only here, never from production main())
	// shrinks every entry's readiness bounds so that composition coverage
	// stays fast and local instead of sleeping through the production bound.
	if readyBounds := os.Getenv(testReadyBoundsEnv); readyBounds != "" {
		_ = os.Unsetenv(testReadyBoundsEnv)
		if err := relocateServiceTableReadyBounds(readyBounds); err != nil {
			writeFailure(os.Stderr, releaseactivation.SanitizedError(err))
			os.Exit(1)
		}
	}
	store, err := releaseactivation.NewStoreAt(filepath.Join(home, "Library", "Application Support", "personal-mcp-gateway", "release", "obsidian"), os.Geteuid())
	if err != nil {
		writeFailure(os.Stderr, releaseactivation.SanitizedError(err))
		os.Exit(1)
	}
	deps, err := testControllerDependencies(store, os.Geteuid(), filepath.Clean(home))
	if err != nil {
		writeFailure(os.Stderr, releaseactivation.SanitizedError(err))
		os.Exit(1)
	}
	os.Exit(runWithDependencies(context.Background(), os.Args[1:], os.Stdout, os.Stderr, deps))
}

// relocateServiceTableHealthURLFiles rewrites every serviceTable entry's
// healthURLFile to <root>/<original base name>, preserving the table's
// per-server file names while moving them off the real /tmp health-marker
// path. Only TestMain calls this.
func relocateServiceTableHealthURLFiles(root string) {
	for server, spec := range serviceTable {
		spec.healthURLFile = filepath.Join(root, filepath.Base(spec.healthURLFile))
		serviceTable[server] = spec
	}
}

// relocateServiceTableReadyBounds rewrites every serviceTable entry's
// readyTimeoutSeconds/readyPollMilliseconds to the given "<seconds>:
// <milliseconds>" pair. Only TestMain calls this.
func relocateServiceTableReadyBounds(raw string) error {
	seconds, milliseconds, ok := strings.Cut(raw, ":")
	timeoutSeconds, err1 := strconv.Atoi(seconds)
	pollMilliseconds, err2 := strconv.Atoi(milliseconds)
	if !ok || err1 != nil || err2 != nil || timeoutSeconds <= 0 || pollMilliseconds <= 0 {
		return errors.New("malformed test ready bounds")
	}
	for server, spec := range serviceTable {
		spec.readyTimeoutSeconds = timeoutSeconds
		spec.readyPollMilliseconds = pollMilliseconds
		serviceTable[server] = spec
	}
	return nil
}

func testControllerDependencies(store *releaseactivation.Store, uid int, home string) (dependencies, error) {
	_ = os.Unsetenv("RELEASE_ACTIVATION_SELECTED_SOURCE")
	executable, err := os.Executable()
	if err != nil {
		return dependencies{}, err
	}
	executable = filepath.Clean(executable)
	controllerSHA256, err := releaseactivation.HashRegular(executable)
	if err != nil {
		return dependencies{}, err
	}
	runtime := releaseactivation.NewOSRuntime()
	manager := &releaseactivation.Manager{Store: store, Runtime: runtime, ControllerPath: executable, ControllerSHA256: controllerSHA256}
	return dependencies{manager: manager, uid: uid, home: home}, nil
}

func TestProductionDependenciesCannotRelocateReleaseStore(t *testing.T) {
	expected, err := releaseactivation.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	hostileHome := t.TempDir()
	t.Setenv("HOME", hostileHome)
	t.Setenv("RELEASE_ACTIVATION_SELECTED_SOURCE", filepath.Join(hostileHome, "active", "authority"))

	deps, err := productionDependencies()
	if err != nil {
		t.Fatal(err)
	}
	manager, ok := deps.manager.(*releaseactivation.Manager)
	if !ok {
		t.Fatalf("manager type = %T", deps.manager)
	}
	if manager.Store.Root() != expected.Root() {
		t.Fatalf("store root = %q, want passwd-derived %q", manager.Store.Root(), expected.Root())
	}
	if _, exists := os.LookupEnv("RELEASE_ACTIVATION_SELECTED_SOURCE"); exists {
		t.Fatal("selected source remained visible after dependency construction")
	}
}

type fakeManager struct {
	manifest       *releaseactivation.Manifest
	statusSequence []*releaseactivation.Manifest
	prepareResult  *releaseactivation.Manifest
	resumeResult   *releaseactivation.Manifest
	err            error
	prepareErr     error
	resumeErr      error
	prepared       releaseactivation.PrepareRequest
	runtime        releaseactivation.Runtime
	prepareCalls   int
	resumeCalls    int
	withClearCalls int
	probeCalls     int
	withClear      func(context.Context, func(context.Context, releaseactivation.Runtime) error) error
	probe          func(context.Context, func(context.Context, releaseactivation.Runtime) error) error
}

func (f *fakeManager) Status(context.Context) (*releaseactivation.Manifest, error) {
	if len(f.statusSequence) > 0 {
		manifest := f.statusSequence[0]
		f.statusSequence = f.statusSequence[1:]
		return manifest, f.err
	}
	return f.manifest, f.err
}
func (f *fakeManager) Prepare(_ context.Context, request releaseactivation.PrepareRequest) (*releaseactivation.Manifest, error) {
	f.prepareCalls++
	f.prepared = request
	if f.prepareErr != nil {
		return nil, f.prepareErr
	}
	if f.prepareResult != nil {
		return f.prepareResult, f.err
	}
	return f.manifest, f.err
}
func (f *fakeManager) Resume(context.Context, releaseactivation.ReleaseID) (*releaseactivation.Manifest, error) {
	f.resumeCalls++
	if f.resumeErr != nil {
		return f.resumeResult, f.resumeErr
	}
	if f.resumeResult != nil {
		return f.resumeResult, f.err
	}
	return f.manifest, f.err
}
func (f *fakeManager) Accept(context.Context, releaseactivation.ReleaseID) (*releaseactivation.Manifest, error) {
	return f.manifest, f.err
}
func (f *fakeManager) Rollback(context.Context, releaseactivation.ReleaseID) (*releaseactivation.Manifest, error) {
	return f.manifest, f.err
}
func (f *fakeManager) WithClear(ctx context.Context, effect func(context.Context, releaseactivation.Runtime) error) error {
	f.withClearCalls++
	if f.withClear != nil {
		return f.withClear(ctx, effect)
	}
	if f.err != nil {
		return f.err
	}
	return effect(ctx, f.runtime)
}

// Probe never gates on a clear transaction and never prunes orphans; unlike
// WithClear, it is expected to run even while a release is pending, which is
// why verify-live dispatches through it instead. withClearCalls stays 0 for
// every Probe call so a regression that routes verify-live back through
// WithClear is caught here rather than only against a live LaunchAgent.
func (f *fakeManager) Probe(ctx context.Context, effect func(context.Context, releaseactivation.Runtime) error) error {
	f.probeCalls++
	if f.probe != nil {
		return f.probe(ctx, effect)
	}
	if f.err != nil {
		return f.err
	}
	return effect(ctx, f.runtime)
}

func TestUpdateRequiresAndUsesExactObjectIDs(t *testing.T) {
	valid := strings.Repeat("a", 40)
	request, err := parseUpdate([]string{"--repo", t.TempDir(), "--expected-head", valid, "--expected-remote-oid", strings.Repeat("b", 40)})
	if err != nil || request.expectedRemoteOID != strings.Repeat("b", 40) {
		t.Fatalf("request=%#v err=%v", request, err)
	}
	for _, args := range [][]string{
		{"--repo", t.TempDir(), "--expected-head", valid, "--expected-remote-oid", "origin/main"},
		{"--repo", t.TempDir(), "--expected-head", valid, "--remote-ref", "FETCH_HEAD"},
		{"--repo", t.TempDir(), "--expected-head", "HEAD", "--expected-remote-oid", valid},
	} {
		if _, err := parseUpdate(args); err == nil {
			t.Fatalf("parseUpdate(%q) accepted a mutable or invalid identifier", args)
		}
	}
}

func TestUpdateLockScopeHonorsCallerDeadline(t *testing.T) {
	manager := &fakeManager{withClear: func(ctx context.Context, _ func(context.Context, releaseactivation.Runtime) error) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	var stdout, stderr bytes.Buffer
	exit := runWithDependencies(ctx, []string{"update-after-fetch", "--repo", t.TempDir(), "--expected-head", strings.Repeat("a", 40), "--expected-remote-oid", strings.Repeat("b", 40)}, &stdout, &stderr, dependencies{manager: manager})
	if exit != 1 || stdout.Len() != 0 || stderr.String() != "error=recovery_unconfirmed message=recovery could not be confirmed\n" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
}

func TestUpdateAfterFetchGitCheckFailuresAreUpdateFailed(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	newRepo := func(t *testing.T, branch string) (repo, head string) {
		t.Helper()
		repo = t.TempDir()
		runGit(t, git, "init", "-b", branch, repo)
		runGit(t, git, "-C", repo, "config", "user.email", "test@example.invalid")
		runGit(t, git, "-C", repo, "config", "user.name", "Release Test")
		writeRaceFile(t, filepath.Join(repo, "payload.txt"), "initial\n", 0o600)
		runGit(t, git, "-C", repo, "add", "payload.txt")
		runGit(t, git, "-C", repo, "commit", "-m", "initial")
		return repo, strings.TrimSpace(runGit(t, git, "-C", repo, "rev-parse", "HEAD"))
	}

	t.Run("wrong branch", func(t *testing.T) {
		repo, head := newRepo(t, "not-main")
		err := updateAfterFetch(context.Background(), updateRequest{repo: repo, expectedHead: head, expectedRemoteOID: strings.Repeat("b", 40)}, gitChildTimeout)
		if got := releaseactivation.SanitizedError(err); got == nil || got.Code != releaseactivation.ErrorUpdateFailed {
			t.Fatalf("update error = %#v, %v", got, err)
		}
	})

	t.Run("dirty tree", func(t *testing.T) {
		repo, head := newRepo(t, "main")
		writeRaceFile(t, filepath.Join(repo, "untracked.txt"), "dirty\n", 0o600)
		err := updateAfterFetch(context.Background(), updateRequest{repo: repo, expectedHead: head, expectedRemoteOID: strings.Repeat("b", 40)}, gitChildTimeout)
		if got := releaseactivation.SanitizedError(err); got == nil || got.Code != releaseactivation.ErrorUpdateFailed {
			t.Fatalf("update error = %#v, %v", got, err)
		}
	})

	t.Run("remote object unreachable", func(t *testing.T) {
		repo, head := newRepo(t, "main")
		err := updateAfterFetch(context.Background(), updateRequest{repo: repo, expectedHead: head, expectedRemoteOID: strings.Repeat("b", 40)}, gitChildTimeout)
		if got := releaseactivation.SanitizedError(err); got == nil || got.Code != releaseactivation.ErrorUpdateFailed {
			t.Fatalf("update error = %#v, %v", got, err)
		}
	})
}

func TestGitChildHonorsEarlierContextDeadline(t *testing.T) {
	bin := t.TempDir()
	git := filepath.Join(bin, "git")
	if err := os.WriteFile(git, []byte("#!/bin/sh\nexec sleep 10\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := gitOutputWithTimeout(ctx, gitChildTimeout, t.TempDir(), "status"); err == nil {
		t.Fatal("blocking git unexpectedly succeeded")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("git child exceeded parent deadline: %v", elapsed)
	}
}

func TestInstallAdapterTimeoutIsHostEffectFailed(t *testing.T) {
	repoRoot := t.TempDir()
	adapterDir := filepath.Join(repoRoot, "scripts", "internal")
	if err := os.MkdirAll(adapterDir, 0o755); err != nil {
		t.Fatal(err)
	}
	adapter := filepath.Join(adapterDir, "install-launchagent.sh")
	if err := os.WriteFile(adapter, []byte("#!/bin/sh\nexec sleep 10\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	// install-launchagent must never shell out to launchctl itself; only the
	// adapter it execs may, and this fake adapter never does. PATH-shim
	// launchctl with a sentinel file and a failing exit so a future
	// regression that reintroduces a launchctl call from runAdmin's own
	// install path is caught here, instead of silently reaching the real
	// host under the real LaunchAgent label.
	shimDir := t.TempDir()
	sentinel := filepath.Join(shimDir, "launchctl-invoked")
	shim := filepath.Join(shimDir, "launchctl")
	shimScript := "#!/bin/sh\n: >\"" + sentinel + "\"\nexit 1\n"
	if err := os.WriteFile(shim, []byte(shimScript), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// A real OSRuntime bounds every child to a fixed maximum, but also honors
	// an earlier caller deadline; a short deadline here exercises the same
	// bounded-child-time mechanism as the production 30-second bound without
	// waiting for it.
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	request := adminRequest{command: "install-launchagent", server: "obsidian", repoRoot: repoRoot, home: t.TempDir(), uid: 501}
	err := runAdmin(ctx, releaseactivation.NewOSRuntime(), request)
	got := releaseactivation.SanitizedError(err)
	// Install never runs a cleanup/unload cascade on failure: the adapter's
	// own timeout is reported directly.
	if got == nil || got.Code != releaseactivation.ErrorHostEffectFailed || got.Message != "launch agent installation: timeout" {
		t.Fatalf("install error = %#v, %v; want host_effect_failed launch-agent-installation timeout", got, err)
	}
	if _, statErr := os.Stat(sentinel); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("install invoked launchctl directly instead of only the adapter (sentinel stat err = %v)", statErr)
	}
}

type fakeRuntime struct {
	releaseactivation.Runtime
	installPath string
	installArgs []string
	restart     releaseactivation.Manifest
	restartErr  error
	// bootout records whether install/restart/verify-live ever unloaded the
	// job. Install must never do this (not even on adapter failure); keeping
	// Bootout wired up, rather than deleting it along with the other now-dead
	// ServiceLoaded/ReadyOnce/ConfirmUnloaded fakes, lets tests assert that
	// directly instead of only inferring it from a lack of coverage.
	bootout    releaseactivation.Manifest
	readyErr   error
	adapterErr error
}

func (f *fakeRuntime) InvokeInstallAdapter(_ context.Context, path string, args ...string) error {
	f.installPath = path
	f.installArgs = append([]string(nil), args...)
	return f.adapterErr
}

func (f *fakeRuntime) Restart(_ context.Context, manifest releaseactivation.Manifest) error {
	f.restart = manifest
	return f.restartErr
}
func (f *fakeRuntime) WaitReady(context.Context, releaseactivation.Manifest) error { return f.readyErr }
func (f *fakeRuntime) Bootout(_ context.Context, manifest releaseactivation.Manifest) error {
	f.bootout = manifest
	return nil
}

func TestRunFormatsPendingRecords(t *testing.T) {
	manager := &fakeManager{manifest: &releaseactivation.Manifest{
		State: releaseactivation.StatePending, ID: testID,
		Commit: "fedcba9876543210", CandidateSHA256: testHash, DependencySHA256: testDependency,
	}}
	var stdout, stderr bytes.Buffer
	exit := runWithDependencies(context.Background(), []string{"status"}, &stdout, &stderr, dependencies{manager: manager})
	if exit != 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stderr=%q", exit, stderr.String())
	}
	want := "state=pending id=" + testID + " commit=fedcba987654 sha256=abcdef012345 dependency_sha256=999999999999\n" +
		"accept=make release-accept RELEASE_ID=" + testID + "\n" +
		"rollback=make release-rollback RELEASE_ID=" + testID + "\n"
	if stdout.String() != want {
		t.Fatalf("stdout=%q, want %q", stdout.String(), want)
	}
}

func TestManifestFormatterCoversEveryState(t *testing.T) {
	base := releaseactivation.Manifest{ID: testID, Commit: "fedcba9876543210", CandidateSHA256: testHash, DependencySHA256: testDependency}
	tests := []struct {
		state releaseactivation.State
		want  string
	}{
		{releaseactivation.StatePrepared, "state=prepared id=" + testID + " commit=fedcba987654 sha256=abcdef012345 dependency_sha256=999999999999\nresume=make release\nrollback=make release-rollback RELEASE_ID=" + testID + "\n"},
		{releaseactivation.StatePending, "state=pending id=" + testID + " commit=fedcba987654 sha256=abcdef012345 dependency_sha256=999999999999\naccept=make release-accept RELEASE_ID=" + testID + "\nrollback=make release-rollback RELEASE_ID=" + testID + "\n"},
		{releaseactivation.StateAccepting, "state=accepting id=" + testID + " commit=fedcba987654 sha256=abcdef012345 dependency_sha256=999999999999\nresume=make release-accept RELEASE_ID=" + testID + "\n"},
		{releaseactivation.StateRollingBack, "state=rolling_back id=" + testID + " commit=fedcba987654 sha256=abcdef012345 dependency_sha256=999999999999\nresume=make release-rollback RELEASE_ID=" + testID + "\n"},
	}
	for _, test := range tests {
		t.Run(string(test.state), func(t *testing.T) {
			manifest := base
			manifest.State = test.state
			var stdout, stderr bytes.Buffer
			exit := runWithDependencies(context.Background(), []string{"status"}, &stdout, &stderr, dependencies{manager: &fakeManager{manifest: &manifest}})
			if exit != 0 || stderr.Len() != 0 || stdout.String() != test.want {
				t.Fatalf("exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
			}
			if stdout.Len() > 512 {
				t.Fatalf("formatter output is unexpectedly large: %d", stdout.Len())
			}
		})
	}
}

func TestReleaseOrGuideResumesPreparedWithoutPrepareArguments(t *testing.T) {
	prepared := &releaseactivation.Manifest{State: releaseactivation.StatePrepared, ID: testID, Commit: "fedcba9876543210", CandidateSHA256: testHash, DependencySHA256: testDependency}
	pending := *prepared
	pending.State = releaseactivation.StatePending
	manager := &fakeManager{manifest: prepared, resumeResult: &pending}
	var stdout, stderr bytes.Buffer
	exit := runWithDependencies(context.Background(), []string{"release"}, &stdout, &stderr, dependencies{manager: manager})
	if exit != 0 || stderr.Len() != 0 || !strings.HasPrefix(stdout.String(), "state=pending id="+testID) {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
	if manager.prepareCalls != 0 || manager.resumeCalls != 1 {
		t.Fatalf("prepare=%d resume=%d", manager.prepareCalls, manager.resumeCalls)
	}
}

func TestReleaseOrGuidePreparesThenReturnsOnlyPending(t *testing.T) {
	prepared := &releaseactivation.Manifest{State: releaseactivation.StatePrepared, ID: testID, Commit: "fedcba9876543210", CandidateSHA256: testHash, DependencySHA256: testDependency}
	pending := *prepared
	pending.State = releaseactivation.StatePending
	manager := &fakeManager{prepareResult: prepared, resumeResult: &pending}
	home := t.TempDir()
	repo := filepath.Join(home, "repo")
	args := append([]string{"release"}, validPrepareArgs(home, repo)...)
	var stdout, stderr bytes.Buffer
	exit := runWithDependencies(context.Background(), args, &stdout, &stderr, dependencies{manager: manager, uid: 501, home: home})
	if exit != 0 || stderr.Len() != 0 || !strings.HasPrefix(stdout.String(), "state=pending id="+testID) || strings.Contains(stdout.String(), "state=prepared") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
	if manager.prepareCalls != 1 || manager.resumeCalls != 1 {
		t.Fatalf("prepare=%d resume=%d", manager.prepareCalls, manager.resumeCalls)
	}
}

func TestReleaseOrGuideNeverReportsSuccessWhenConflictingStateClears(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "repo")
	manager := &fakeManager{
		statusSequence: []*releaseactivation.Manifest{nil, nil},
		prepareErr:     releaseactivation.ErrStateConflict,
	}
	var stdout, stderr bytes.Buffer
	exit := runWithDependencies(context.Background(), append([]string{"release"}, validPrepareArgs(home, repo)...), &stdout, &stderr, dependencies{manager: manager, uid: 501, home: home})
	if exit != 1 || stdout.Len() != 0 || stderr.String() != "error=state_conflict message=event conflicts with release state\n" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
}

func TestReleaseOrGuideFormatsActiveRecoveryOnStderr(t *testing.T) {
	tests := []struct {
		state    releaseactivation.State
		guidance string
	}{
		{releaseactivation.StatePending, "accept=make release-accept RELEASE_ID=" + testID + "\nrollback=make release-rollback RELEASE_ID=" + testID + "\n"},
		{releaseactivation.StateAccepting, "resume=make release-accept RELEASE_ID=" + testID + "\n"},
		{releaseactivation.StateRollingBack, "resume=make release-rollback RELEASE_ID=" + testID + "\n"},
	}
	for _, test := range tests {
		t.Run(string(test.state), func(t *testing.T) {
			manifest := &releaseactivation.Manifest{State: test.state, ID: testID, Commit: "fedcba9876543210", CandidateSHA256: testHash, DependencySHA256: testDependency}
			var stdout, stderr bytes.Buffer
			exit := runWithDependencies(context.Background(), []string{"release"}, &stdout, &stderr, dependencies{manager: &fakeManager{manifest: manifest}})
			want := "error=state_conflict message=event conflicts with release state\n" +
				"state=" + string(test.state) + " id=" + testID + " commit=fedcba987654 sha256=abcdef012345 dependency_sha256=999999999999\n" + test.guidance
			if exit != 1 || stdout.Len() != 0 || stderr.String() != want {
				t.Fatalf("exit=%d stdout=%q stderr=%q want=%q", exit, stdout.String(), stderr.String(), want)
			}
		})
	}
}

func TestReleaseOrGuideConvertsPreparedResumeRaceToGuidance(t *testing.T) {
	prepared := &releaseactivation.Manifest{State: releaseactivation.StatePrepared, ID: testID, Commit: "fedcba9876543210", CandidateSHA256: testHash, DependencySHA256: testDependency}
	pending := *prepared
	pending.State = releaseactivation.StatePending
	manager := &fakeManager{manifest: prepared, resumeResult: &pending, resumeErr: releaseactivation.ErrStateConflict}
	var stdout, stderr bytes.Buffer
	exit := runWithDependencies(context.Background(), []string{"release"}, &stdout, &stderr, dependencies{manager: manager})
	want := "error=state_conflict message=event conflicts with release state\n" +
		"state=pending id=" + testID + " commit=fedcba987654 sha256=abcdef012345 dependency_sha256=999999999999\n" +
		"accept=make release-accept RELEASE_ID=" + testID + "\n" +
		"rollback=make release-rollback RELEASE_ID=" + testID + "\n"
	if exit != 1 || stdout.Len() != 0 || stderr.String() != want {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
}

func TestRunSanitizesDynamicFailure(t *testing.T) {
	manager := &fakeManager{err: errors.New("/private/sentinel runtime-secret")}
	var stdout, stderr bytes.Buffer
	exit := runWithDependencies(context.Background(), []string{"status"}, &stdout, &stderr, dependencies{manager: manager})
	if exit != 1 || stdout.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q", exit, stdout.String())
	}
	want := "error=recovery_unconfirmed message=recovery could not be confirmed\n"
	if stderr.String() != want {
		t.Fatalf("stderr=%q, want %q", stderr.String(), want)
	}
}

func TestRunFormatsEveryStableFailureCode(t *testing.T) {
	tests := []struct {
		code    releaseactivation.ErrorCode
		message string
	}{
		{releaseactivation.ErrorNoPending, "no unresolved release exists"},
		{releaseactivation.ErrorBusy, "release state is busy"},
		{releaseactivation.ErrorIdentityMismatch, "release identity does not match"},
		{releaseactivation.ErrorAuthorityMismatch, "release controller identity does not match"},
		{releaseactivation.ErrorInstalledMismatch, "installed target does not match release state"},
		{releaseactivation.ErrorStateMalformed, "release state is malformed"},
		{releaseactivation.ErrorArtifactMismatch, "release artifact does not match"},
		{releaseactivation.ErrorRuntimeDrift, "supervised runtime configuration changed"},
		{releaseactivation.ErrorStateConflict, "event conflicts with release state"},
		{releaseactivation.ErrorRecoveryUnconfirmed, "recovery could not be confirmed"},
	}
	for _, test := range tests {
		t.Run(string(test.code), func(t *testing.T) {
			manager := &fakeManager{err: &releaseactivation.Error{Code: test.code, Message: "hostile /private/path runtime-secret\nsecond"}}
			var stdout, stderr bytes.Buffer
			exit := runWithDependencies(context.Background(), []string{"status"}, &stdout, &stderr, dependencies{manager: manager})
			want := "error=" + string(test.code) + " message=" + test.message + "\n"
			if exit != 1 || stdout.Len() != 0 || stderr.String() != want {
				t.Fatalf("exit=%d stdout=%q stderr=%q want=%q", exit, stdout.String(), stderr.String(), want)
			}
		})
	}
}

func TestRunRejectsMissingReleaseIDAsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exit := runWithDependencies(context.Background(), []string{"accept"}, &stdout, &stderr, dependencies{manager: &fakeManager{}})
	if exit != 2 || stdout.Len() != 0 || stderr.String() != "error=usage message=invalid release command\n" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
}

func TestRunRejectsUnknownCommandAsExactUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exit := runWithDependencies(context.Background(), []string{"unknown-private-command", "/private/sentinel"}, &stdout, &stderr, dependencies{manager: &fakeManager{}})
	if exit != 2 || stdout.Len() != 0 || stderr.String() != "error=usage message=invalid release command\n" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
}

func TestPrepareAndAdminRejectUnknownServer(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "repo")
	if _, err := execute(context.Background(), []string{"install-launchagent", "--repo-root", repo, "--server", "../private/sentinel"}, dependencies{manager: &fakeManager{}, uid: 501, home: home}); err == nil {
		t.Fatal("admin command accepted an unknown server")
	}
}

func TestParsePrepareDerivesFixedAdditionalServiceCandidatesInCanonicalOrder(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()
	envPath := filepath.Join(repo, ".env.local")
	request, err := parsePrepare([]string{
		"--commit", strings.Repeat("a", 40), "--candidate-sha256", strings.Repeat("b", 64),
		"--authority-sha256", strings.Repeat("c", 64), "--dependency-sha256", strings.Repeat("d", 64),
		"--candidate", filepath.Join(repo, "candidate"), "--authority", filepath.Join(repo, "authority"),
		"--target", filepath.Join(home, "bin", "gateway"),
		"--repo-root", repo, "--environment", envPath,
	}, dependencies{uid: 501, home: home})
	if err != nil {
		t.Fatal(err)
	}
	if len(request.ServiceCandidates) != 3 {
		t.Fatalf("service candidates = %#v", request.ServiceCandidates)
	}
	ynab := request.ServiceCandidates[0]
	if ynab.Server != "ynab" || ynab.LaunchAgentLabel != "com.ericfeunekes.personal-mcp-gateway.ynab-tunnel" ||
		ynab.WrapperPath != filepath.Join(repo, "scripts", "run-ynab-tunnel.sh") ||
		ynab.MCPWrapperPath != filepath.Join(repo, "scripts", "run-ynab-mcp-stdio.sh") ||
		ynab.EnvironmentPath != filepath.Join(repo, ".env.ynab.local") ||
		ynab.HealthURLFile != "/tmp/personal-mcp-gateway/ynab-tunnel-health.url" {
		t.Fatalf("ynab candidate = %#v", ynab)
	}
	// obsidian-http execs the gateway binary directly: no MCP wrapper, and it
	// shares the obsidian tunnel's own environment file rather than a copy.
	obsidianHTTP := request.ServiceCandidates[1]
	if obsidianHTTP.Server != "obsidian-http" || obsidianHTTP.LaunchAgentLabel != "com.ericfeunekes.personal-mcp-gateway.obsidian-http" ||
		obsidianHTTP.WrapperPath != filepath.Join(repo, "scripts", "run-obsidian-http.sh") ||
		obsidianHTTP.MCPWrapperPath != "" ||
		obsidianHTTP.EnvironmentPath != envPath ||
		obsidianHTTP.HealthURLFile != "/tmp/personal-mcp-gateway/obsidian-http-health.url" {
		t.Fatalf("obsidian-http candidate = %#v", obsidianHTTP)
	}
	// ynab-http shares the ynab tunnel's own environment file for the same reason.
	ynabHTTP := request.ServiceCandidates[2]
	if ynabHTTP.Server != "ynab-http" || ynabHTTP.LaunchAgentLabel != "com.ericfeunekes.personal-mcp-gateway.ynab-http" ||
		ynabHTTP.WrapperPath != filepath.Join(repo, "scripts", "run-ynab-http.sh") ||
		ynabHTTP.MCPWrapperPath != "" ||
		ynabHTTP.EnvironmentPath != filepath.Join(repo, ".env.ynab.local") ||
		ynabHTTP.HealthURLFile != "/tmp/personal-mcp-gateway/ynab-http-health.url" {
		t.Fatalf("ynab-http candidate = %#v", ynabHTTP)
	}
}

func TestPrepareDerivesPrivateBinding(t *testing.T) {
	manager := &fakeManager{manifest: &releaseactivation.Manifest{State: releaseactivation.StatePrepared, ID: testID, Commit: strings.Repeat("a", 40), CandidateSHA256: testHash, DependencySHA256: testDependency}}
	home := t.TempDir()
	repo := filepath.Join(home, "repo")
	args := []string{
		"prepare", "--commit", strings.Repeat("a", 40), "--candidate-sha256", testHash, "--authority-sha256", testHash, "--dependency-sha256", testDependency, "--candidate", filepath.Join(repo, "candidate"),
		"--authority", filepath.Join(repo, "authority"), "--target", filepath.Join(home, "bin", "gateway"),
		"--repo-root", repo, "--environment", filepath.Join(repo, ".env.local"),
	}
	if _, err := execute(context.Background(), args, dependencies{manager: manager, uid: 501, home: home}); err != nil {
		t.Fatal(err)
	}
	got := manager.prepared
	obsidian := serviceTable["obsidian"]
	if got.CandidateSHA256 != testHash || got.AuthoritySHA256 != testHash || got.EffectiveUID != 501 ||
		got.LaunchAgentLabel != obsidian.label ||
		got.PlistPath != filepath.Join(home, "Library", "LaunchAgents", obsidian.label+".plist") ||
		got.WrapperPath != filepath.Join(repo, "scripts", "run-obsidian-tunnel.sh") ||
		got.MCPWrapperPath != filepath.Join(repo, "scripts", "run-obsidian-mcp-stdio.sh") ||
		got.HealthURLFile != obsidian.healthURLFile {
		t.Fatalf("derived request = %+v", got)
	}
}

func TestPrepareRequiresCanonicalCommitAndDependencyIdentity(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "repo")
	valid := validPrepareArgs(home, repo)
	for _, test := range []struct {
		name   string
		mutate func([]string) []string
	}{
		{name: "missing dependency", mutate: func(args []string) []string {
			for i := range args {
				if args[i] == "--dependency-sha256" {
					return append(args[:i], args[i+2:]...)
				}
			}
			return args
		}},
		{name: "missing candidate digest", mutate: func(args []string) []string {
			for i := range args {
				if args[i] == "--candidate-sha256" {
					return append(args[:i], args[i+2:]...)
				}
			}
			return args
		}},
		{name: "missing authority digest", mutate: func(args []string) []string {
			for i := range args {
				if args[i] == "--authority-sha256" {
					return append(args[:i], args[i+2:]...)
				}
			}
			return args
		}},
		{name: "short commit", mutate: func(args []string) []string {
			for i := range args {
				if args[i] == "--commit" {
					args[i+1] = "0123456789abcdef"
				}
			}
			return args
		}},
		{name: "uppercase dependency", mutate: func(args []string) []string {
			for i := range args {
				if args[i] == "--dependency-sha256" {
					args[i+1] = strings.Repeat("A", 64)
				}
			}
			return args
		}},
		{name: "uppercase candidate digest", mutate: func(args []string) []string {
			for i := range args {
				if args[i] == "--candidate-sha256" {
					args[i+1] = strings.Repeat("A", 64)
				}
			}
			return args
		}},
		{name: "uppercase authority digest", mutate: func(args []string) []string {
			for i := range args {
				if args[i] == "--authority-sha256" {
					args[i+1] = strings.Repeat("A", 64)
				}
			}
			return args
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := test.mutate(append([]string(nil), valid...))
			if _, err := parsePrepare(args, dependencies{uid: 501, home: home}); err == nil {
				t.Fatalf("parsePrepare accepted %s", test.name)
			}
		})
	}
}

func validPrepareArgs(home, repo string) []string {
	return []string{
		"--commit", "0123456789abcdef0123456789abcdef01234567",
		"--candidate-sha256", testHash,
		"--authority-sha256", testHash,
		"--dependency-sha256", testDependency,
		"--candidate", filepath.Join(repo, "candidate"),
		"--authority", filepath.Join(repo, "authority"),
		"--target", filepath.Join(home, "bin", "gateway"),
		"--repo-root", repo,
		"--environment", filepath.Join(repo, ".env.local"),
	}
}

// TestInstallLaunchAgentUsesPrivateAdapterForEveryServer covers decision #6's
// restored per-server coverage: the adapter round-trip's literal runner path
// and log file names for all four servers, and that a successful install
// never unloads the job (install must never bootout, per decision #2).
func TestInstallLaunchAgentUsesPrivateAdapterForEveryServer(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "repo")
	for server, spec := range serviceTable {
		t.Run(server, func(t *testing.T) {
			runtime := &fakeRuntime{}
			manager := &fakeManager{runtime: runtime}
			_, err := execute(context.Background(), []string{"install-launchagent", "--server", server, "--repo-root", repo}, dependencies{
				manager: manager, uid: 501, home: home,
			})
			if err != nil {
				t.Fatal(err)
			}
			wantPath := filepath.Join(repo, "scripts", "internal", "install-launchagent.sh")
			wantArgs := []string{
				home, "501", spec.label, spec.wrapperPath(repo),
				spec.stdoutPath(home), spec.stderrPath(home), repo,
			}
			if runtime.installPath != wantPath || !reflect.DeepEqual(runtime.installArgs, wantArgs) {
				t.Fatalf("path=%q args=%q, want path=%q args=%q", runtime.installPath, runtime.installArgs, wantPath, wantArgs)
			}
			if runtime.bootout.LaunchAgentLabel != "" {
				t.Fatalf("successful install unexpectedly unloaded the job: %+v", runtime.bootout)
			}
		})
	}
}

// TestWrapperScriptsAgreeWithServiceTableHealthURLFiles covers decision #6's
// fourth restored-coverage item: each run-*.sh wrapper hardcodes its own
// health_url_file literal (it has to, since it also writes/serves that file
// at runtime), and serviceTable is the only other place that path is
// written. Nothing enforces agreement between the two at compile time, so
// this reads the real repository's wrapper scripts and asserts each one's
// literal matches its serviceTable entry exactly.
func TestWrapperScriptsAgreeWithServiceTableHealthURLFiles(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// currentFile is <repo>/cmd/release-activation/main_test.go.
	repoRoot := filepath.Dir(filepath.Dir(filepath.Dir(currentFile)))
	for server, spec := range serviceTable {
		t.Run(server, func(t *testing.T) {
			wrapperPath := filepath.Join(repoRoot, "scripts", spec.wrapperName)
			data, err := os.ReadFile(wrapperPath)
			if err != nil {
				t.Fatal(err)
			}
			want := `health_url_file="` + spec.healthURLFile + `"`
			if !strings.Contains(string(data), want) {
				t.Fatalf("%s does not contain %q from the service table", wrapperPath, want)
			}
		})
	}
}

// TestInstallLaunchAgentNeverUnloadsOnAdapterFailure covers decision #2: an
// adapter failure (for example because GATEWAY_BIN does not exist yet) must
// leave any already-running job alone, for every server.
func TestInstallLaunchAgentNeverUnloadsOnAdapterFailure(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "repo")
	for server := range serviceTable {
		t.Run(server, func(t *testing.T) {
			runtime := &fakeRuntime{adapterErr: errors.New("adapter failed")}
			manager := &fakeManager{runtime: runtime}
			_, err := execute(context.Background(), []string{"install-launchagent", "--server", server, "--repo-root", repo}, dependencies{
				manager: manager, uid: 501, home: home,
			})
			if err == nil {
				t.Fatal("adapter failure unexpectedly succeeded")
			}
			if runtime.bootout.LaunchAgentLabel != "" {
				t.Fatalf("adapter failure unexpectedly unloaded the job: %+v", runtime.bootout)
			}
		})
	}
}

// TestRestartHealthURLFileDefaultsFromTable covers decision #3's second
// bullet indirectly: --health-url-file no longer exists as an admin flag, so
// restart's manifest health URL file always comes from the service table.
func TestRestartHealthURLFileDefaultsFromTable(t *testing.T) {
	runtime := &fakeRuntime{}
	manager := &fakeManager{runtime: runtime}
	home := t.TempDir()
	repo := filepath.Join(home, "repo")
	obsidian := serviceTable["obsidian"]

	if _, err := execute(context.Background(), []string{
		"restart", "--repo-root", repo,
	}, dependencies{manager: manager, uid: 501, home: home}); err != nil {
		t.Fatal(err)
	}
	if runtime.restart.EffectiveUID != 501 || runtime.restart.LaunchAgentLabel != obsidian.label || runtime.restart.HealthURLFile != obsidian.healthURLFile {
		t.Fatalf("restart manifest with default health URL file = %+v", runtime.restart)
	}

	if _, err := execute(context.Background(), []string{
		"restart", "--repo-root", repo, "--health-url-file", filepath.Join(home, "custom-health.url"),
	}, dependencies{manager: manager, uid: 501, home: home}); err == nil {
		t.Fatal("restart accepted the removed --health-url-file flag instead of rejecting it as usage")
	}
}

// TestVerifyLiveRunsUnderProbeAndReportsWaitReadyResult covers decisions #1
// and #2: verify-live is a read-only probe (Probe, never WithClear) whose
// result is exactly runAdmin's WaitReady call on the current controller.
func TestVerifyLiveRunsUnderProbeAndReportsWaitReadyResult(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "repo")

	t.Run("ready succeeds and dispatches through Probe, not WithClear", func(t *testing.T) {
		runtime := &fakeRuntime{}
		manager := &fakeManager{runtime: runtime}
		records, err := execute(context.Background(), []string{"verify-live", "--repo-root", repo}, dependencies{manager: manager, uid: 501, home: home})
		if err != nil {
			t.Fatal(err)
		}
		if manager.probeCalls != 1 || manager.withClearCalls != 0 {
			t.Fatalf("probeCalls=%d withClearCalls=%d, want verify-live to run only under Probe", manager.probeCalls, manager.withClearCalls)
		}
		want := []string{"action=verify-live result=ready"}
		if !reflect.DeepEqual(records, want) {
			t.Fatalf("records = %#v, want %#v", records, want)
		}
	})

	t.Run("WaitReady failure propagates as host_effect_failed", func(t *testing.T) {
		runtime := &fakeRuntime{readyErr: releaseactivation.HostEffectFailure("readiness", errors.New("bounded readiness exhausted"))}
		manager := &fakeManager{runtime: runtime}
		_, err := execute(context.Background(), []string{"verify-live", "--repo-root", repo}, dependencies{manager: manager, uid: 501, home: home})
		got := releaseactivation.SanitizedError(err)
		if got == nil || got.Code != releaseactivation.ErrorHostEffectFailed || got.Message != "readiness: failed" {
			t.Fatalf("verify-live error = %#v, %v", got, err)
		}
		if manager.probeCalls != 1 || manager.withClearCalls != 0 {
			t.Fatalf("probeCalls=%d withClearCalls=%d, want verify-live to run only under Probe even on failure", manager.probeCalls, manager.withClearCalls)
		}
	})
}

func TestBoundedBufferCapsHostileChildOutput(t *testing.T) {
	var buffer boundedBuffer
	payload := make([]byte, (32<<10)+4096)
	if n, err := buffer.Write(payload); err != nil || n != len(payload) {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if !buffer.truncated || len(buffer.data) != 32<<10 {
		t.Fatalf("bounded buffer truncated=%v length=%d", buffer.truncated, len(buffer.data))
	}
}
