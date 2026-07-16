package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"personal-mcp-gateway/internal/releaseactivation"
)

const (
	testID         = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testHash       = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	testDependency = "9999999999999999999999999999999999999999999999999999999999999999"
)

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
	withClear      func(context.Context, func(context.Context, releaseactivation.Runtime) error) error
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
	if f.withClear != nil {
		return f.withClear(ctx, effect)
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
	if _, err := gitOutput(ctx, t.TempDir(), "status"); err == nil {
		t.Fatal("blocking git unexpectedly succeeded")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("git child exceeded parent deadline: %v", elapsed)
	}
}

type fakeRuntime struct {
	releaseactivation.Runtime
	installPath string
	installArgs []string
	restart     releaseactivation.Manifest
}

func (f *fakeRuntime) InvokeInstallAdapter(_ context.Context, path string, args ...string) error {
	f.installPath = path
	f.installArgs = append([]string(nil), args...)
	return nil
}

func (f *fakeRuntime) Restart(_ context.Context, manifest releaseactivation.Manifest) error {
	f.restart = manifest
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

func TestPrepareAndAdminRejectUnsafeLaunchAgentLabels(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "repo")
	args := validPrepareArgs(home, repo)
	for i := range args {
		if args[i] == "--label" {
			args[i+1] = "../private/sentinel"
		}
	}
	if _, err := execute(context.Background(), append([]string{"prepare"}, args...), dependencies{manager: &fakeManager{}, uid: 501, home: home}); err == nil {
		t.Fatal("prepare accepted an unsafe LaunchAgent label")
	}
	if _, err := execute(context.Background(), []string{"install-launchagent", "--repo-root", repo, "--label", "a/b"}, dependencies{manager: &fakeManager{}, uid: 501, home: home}); err == nil {
		t.Fatal("admin command accepted an unsafe LaunchAgent label")
	}
}

func TestPrepareDerivesPrivateBinding(t *testing.T) {
	manager := &fakeManager{manifest: &releaseactivation.Manifest{State: releaseactivation.StatePrepared, ID: testID, Commit: strings.Repeat("a", 40), CandidateSHA256: testHash, DependencySHA256: testDependency}}
	home := t.TempDir()
	repo := filepath.Join(home, "repo")
	args := []string{
		"prepare", "--commit", strings.Repeat("a", 40), "--candidate-sha256", testHash, "--dependency-sha256", testDependency, "--candidate", filepath.Join(repo, "candidate"),
		"--authority", filepath.Join(repo, "authority"), "--target", filepath.Join(home, "bin", "gateway"),
		"--label", "test.label", "--repo-root", repo, "--environment", filepath.Join(repo, ".env.local"),
		"--health-url-file", filepath.Join(home, "health.url"),
	}
	if _, err := execute(context.Background(), args, dependencies{manager: manager, uid: 501, home: home}); err != nil {
		t.Fatal(err)
	}
	got := manager.prepared
	if got.CandidateSHA256 != testHash || got.EffectiveUID != 501 || got.PlistPath != filepath.Join(home, "Library", "LaunchAgents", "test.label.plist") ||
		got.WrapperPath != filepath.Join(repo, "scripts", "run-obsidian-tunnel.sh") ||
		got.MCPWrapperPath != filepath.Join(repo, "scripts", "run-obsidian-mcp-stdio.sh") {
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
		"--dependency-sha256", testDependency,
		"--candidate", filepath.Join(repo, "candidate"),
		"--authority", filepath.Join(repo, "authority"),
		"--target", filepath.Join(home, "bin", "gateway"),
		"--label", "test.label", "--repo-root", repo,
		"--environment", filepath.Join(repo, ".env.local"),
		"--health-url-file", filepath.Join(home, "health.url"),
	}
}

func TestInstallLaunchAgentUsesPrivateAdapter(t *testing.T) {
	runtime := &fakeRuntime{}
	manager := &fakeManager{runtime: runtime}
	home := t.TempDir()
	repo := filepath.Join(home, "repo")
	_, err := execute(context.Background(), []string{"install-launchagent", "--repo-root", repo, "--label", "test.label"}, dependencies{
		manager: manager, uid: 501, home: home,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(repo, "scripts", "internal", "install-obsidian-tunnel-launchagent.sh")
	wantArgs := []string{repo, home, "501", "test.label"}
	if runtime.installPath != wantPath || !reflect.DeepEqual(runtime.installArgs, wantArgs) {
		t.Fatalf("path=%q args=%q", runtime.installPath, runtime.installArgs)
	}
}

func TestRestartRequiresAndPassesAbsoluteHealthURLFile(t *testing.T) {
	runtime := &fakeRuntime{}
	manager := &fakeManager{runtime: runtime}
	home := t.TempDir()
	repo := filepath.Join(home, "repo")
	healthURLFile := filepath.Join(home, "health.url")
	_, err := execute(context.Background(), []string{
		"restart", "--repo-root", repo, "--label", "test.label", "--health-url-file", healthURLFile,
	}, dependencies{manager: manager, uid: 501, home: home})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.restart.EffectiveUID != 501 || runtime.restart.LaunchAgentLabel != "test.label" || runtime.restart.HealthURLFile != healthURLFile {
		t.Fatalf("restart manifest = %+v", runtime.restart)
	}
	if _, err := execute(context.Background(), []string{
		"restart", "--repo-root", repo, "--label", "test.label",
	}, dependencies{manager: manager, uid: 501, home: home}); err == nil {
		t.Fatal("restart accepted a missing health URL file")
	}
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
