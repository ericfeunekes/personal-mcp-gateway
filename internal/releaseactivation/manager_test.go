package releaseactivation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestManagerPrepareResumeAccept(t *testing.T) {
	manager, runtime, request := newManagerFixture(t, true)
	ctx := context.Background()

	prepared, err := manager.Prepare(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.State != StatePrepared || !prepared.PreviousPresent || prepared.ID == "" ||
		prepared.Version != ManifestVersion || len(prepared.Services) != 1 || prepared.Services[0].Server != "obsidian" || prepared.Commit != request.Commit || prepared.CandidateSHA256 != request.CandidateSHA256 ||
		prepared.DependencySHA256 != request.DependencySHA256 {
		t.Fatalf("prepared manifest = %#v", prepared)
	}
	pending, err := manager.Resume(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if pending.State != StatePending || pending.ID != prepared.ID || pending.Commit != request.Commit ||
		pending.CandidateSHA256 != request.CandidateSHA256 || pending.DependencySHA256 != request.DependencySHA256 {
		t.Fatalf("resume = %#v, want same pending release", pending)
	}
	if want := []string{"install", "restart", "ready"}; !reflect.DeepEqual(runtime.calls, want) {
		t.Fatalf("runtime calls = %v, want %v", runtime.calls, want)
	}
	cleared, err := manager.Accept(ctx, prepared.ID)
	if err != nil || cleared != nil {
		t.Fatalf("accept = %#v, %v; want clear", cleared, err)
	}
	status, err := manager.Status(ctx)
	if err != nil || status != nil {
		t.Fatalf("status after accept = %#v, %v; want clear", status, err)
	}
}

func TestManagerPrepareRequiresFullCommitAndDependencyIdentity(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*PrepareRequest)
	}{
		{name: "short commit", mutate: func(request *PrepareRequest) { request.Commit = "0123456789abcdef" }},
		{name: "uppercase commit", mutate: func(request *PrepareRequest) { request.Commit = strings.Repeat("A", 40) }},
		{name: "missing candidate digest", mutate: func(request *PrepareRequest) { request.CandidateSHA256 = "" }},
		{name: "uppercase candidate digest", mutate: func(request *PrepareRequest) { request.CandidateSHA256 = strings.Repeat("A", 64) }},
		{name: "missing authority digest", mutate: func(request *PrepareRequest) { request.AuthoritySHA256 = "" }},
		{name: "uppercase authority digest", mutate: func(request *PrepareRequest) { request.AuthoritySHA256 = strings.Repeat("A", 64) }},
		{name: "missing dependency", mutate: func(request *PrepareRequest) { request.DependencySHA256 = "" }},
		{name: "uppercase dependency", mutate: func(request *PrepareRequest) { request.DependencySHA256 = strings.Repeat("A", 64) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, runtime, request := newManagerFixture(t, true)
			test.mutate(&request)
			prepared, err := manager.Prepare(context.Background(), request)
			if got := SanitizedError(err); prepared != nil || got == nil || got.Code != ErrorStateMalformed {
				t.Fatalf("Prepare = %#v, %v; want state_malformed", prepared, err)
			}
			if len(runtime.calls) != 0 {
				t.Fatalf("invalid identity reached runtime: %v", runtime.calls)
			}
			if active, inspectErr := manager.Store.Inspect(); inspectErr != nil || active != nil {
				t.Fatalf("invalid identity published state: %#v, %v", active, inspectErr)
			}
		})
	}
}

func TestManagerPrepareRejectsCandidateOutsideValidatedReportTuple(t *testing.T) {
	manager, runtime, request := newManagerFixture(t, true)
	request.CandidateSHA256 = strings.Repeat("a", 64)
	prepared, err := manager.Prepare(context.Background(), request)
	if got := SanitizedError(err); prepared != nil || got == nil || got.Code != ErrorArtifactMismatch {
		t.Fatalf("Prepare = %#v, %v; want artifact_mismatch", prepared, err)
	}
	if len(runtime.calls) != 0 {
		t.Fatalf("candidate mismatch reached runtime: %v", runtime.calls)
	}
	if active, inspectErr := manager.Store.Inspect(); inspectErr != nil || active != nil {
		t.Fatalf("candidate mismatch published state: %#v, %v", active, inspectErr)
	}
}

func TestManagerPrepareBindsExecutingControllerToExpectedAuthority(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(t *testing.T, manager *Manager, request *PrepareRequest)
	}{
		{name: "executing self differs", mutate: func(_ *testing.T, manager *Manager, _ *PrepareRequest) {
			manager.ControllerSHA256 = strings.Repeat("a", 64)
		}},
		{name: "authority source differs", mutate: func(t *testing.T, _ *Manager, request *PrepareRequest) {
			replacement := filepath.Join(t.TempDir(), "replacement-controller")
			if err := os.WriteFile(replacement, []byte("replacement-controller"), 0o500); err != nil {
				t.Fatal(err)
			}
			request.AuthorityPath = replacement
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, runtime, request := newManagerFixture(t, true)
			test.mutate(t, manager, &request)
			prepared, err := manager.Prepare(context.Background(), request)
			if got := SanitizedError(err); prepared != nil || got == nil || got.Code != ErrorAuthorityMismatch {
				t.Fatalf("Prepare = %#v, %v; want authority_mismatch", prepared, err)
			}
			if len(runtime.calls) != 0 {
				t.Fatalf("authority mismatch reached runtime: %v", runtime.calls)
			}
			if active, inspectErr := manager.Store.Inspect(); inspectErr != nil || active != nil {
				t.Fatalf("authority mismatch published state: %#v, %v", active, inspectErr)
			}
		})
	}
}

func TestManagerActiveOperationsRequireExecutingSelfDigest(t *testing.T) {
	for _, operation := range []string{"status", "resume", "accept", "rollback"} {
		t.Run(operation, func(t *testing.T) {
			manager, runtime, request := newManagerFixture(t, true)
			prepared, err := manager.Prepare(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			runtime.calls = nil
			manager.ControllerSHA256 = strings.Repeat("a", 64)

			var active *Manifest
			switch operation {
			case "status":
				active, err = manager.Status(context.Background())
			case "resume":
				active, err = manager.Resume(context.Background(), prepared.ID)
			case "accept":
				active, err = manager.Accept(context.Background(), prepared.ID)
			case "rollback":
				active, err = manager.Rollback(context.Background(), prepared.ID)
			}
			if got := SanitizedError(err); active == nil || active.ID != prepared.ID || got == nil || got.Code != ErrorAuthorityMismatch {
				t.Fatalf("%s = %#v, %v; want retained authority_mismatch", operation, active, err)
			}
			if len(runtime.calls) != 0 {
				t.Fatalf("%s authority mismatch emitted effects: %v", operation, runtime.calls)
			}
			stored, inspectErr := manager.Store.Inspect()
			if inspectErr != nil || stored == nil || stored.ID != prepared.ID || stored.State != StatePrepared {
				t.Fatalf("stored after %s = %#v, %v", operation, stored, inspectErr)
			}
		})
	}
}

func TestManagerPendingAcceptRetainsRecoveryWhenCandidateIsNotReadyAndLoaded(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(*fakeManagerRuntime)
		code   ErrorCode
	}{
		{name: "candidate absent", mutate: func(runtime *fakeManagerRuntime) {
			runtime.installedPresent, runtime.installedHash = false, ""
		}, code: ErrorInstalledMismatch},
		{name: "candidate not ready", mutate: func(runtime *fakeManagerRuntime) {
			runtime.ready = false
		}, code: ErrorRecoveryUnconfirmed},
		{name: "supervisor unloaded", mutate: func(runtime *fakeManagerRuntime) {
			runtime.unloaded = true
		}, code: ErrorRecoveryUnconfirmed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			manager, runtime, request := newManagerFixture(t, true)
			prepared, err := manager.Prepare(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			pending, err := manager.Resume(context.Background(), prepared.ID)
			if err != nil {
				t.Fatal(err)
			}
			runtime.calls = nil
			tt.mutate(runtime)

			active, err := manager.Accept(context.Background(), prepared.ID)
			if got := SanitizedError(err); got == nil || got.Code != tt.code {
				t.Fatalf("Accept = %#v, %v; want %s", active, err, tt.code)
			}
			if active == nil || active.State != StatePending || active.ID != pending.ID {
				t.Fatalf("retained transaction = %#v, want pending %s", active, pending.ID)
			}
			if len(runtime.calls) != 0 {
				t.Fatalf("rejected accept emitted runtime effects: %v", runtime.calls)
			}
			status, statusErr := manager.Status(context.Background())
			if statusErr != nil || status == nil || status.State != StatePending || status.ID != pending.ID {
				t.Fatalf("durable state = %#v, %v; want retained pending", status, statusErr)
			}
		})
	}
}

func TestManagerAcceptRetainsAcceptingWhenReadinessIsLostBeforeClear(t *testing.T) {
	manager, runtime, request := newManagerFixture(t, true)
	prepared, err := manager.Prepare(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Resume(context.Background(), prepared.ID); err != nil {
		t.Fatal(err)
	}

	// The first observation authorizes pending -> accepting. Losing readiness
	// immediately afterward models a crash/unload in the persisted-accepting
	// window; the fresh observation must prevent transaction clearing.
	runtime.calls = nil
	runtime.afterObserve = func() {
		runtime.ready = false
		runtime.unloaded = true
		runtime.afterObserve = nil
	}
	active, err := manager.Accept(context.Background(), prepared.ID)
	if got := SanitizedError(err); got == nil || got.Code != ErrorRecoveryUnconfirmed {
		t.Fatalf("Accept = %#v, %v; want recovery_unconfirmed", active, err)
	}
	if active == nil || active.State != StateAccepting || active.ID != prepared.ID {
		t.Fatalf("retained transaction = %#v, want accepting %s", active, prepared.ID)
	}
	if len(runtime.calls) != 0 {
		t.Fatalf("failed final confirmation emitted runtime effects: %v", runtime.calls)
	}
	status, statusErr := manager.Status(context.Background())
	if statusErr != nil || status == nil || status.State != StateAccepting || status.ID != prepared.ID {
		t.Fatalf("durable state = %#v, %v; want retained accepting", status, statusErr)
	}

	// A same-direction retry succeeds only after current readiness and loaded
	// supervisor state are observed again.
	runtime.ready, runtime.unloaded = true, false
	cleared, err := manager.Accept(context.Background(), prepared.ID)
	if err != nil || cleared != nil {
		t.Fatalf("resumed Accept = %#v, %v; want clear", cleared, err)
	}
}

func TestManagerResumedAcceptingRequiresCurrentReadyLoadedCandidate(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(*fakeManagerRuntime)
		code   ErrorCode
	}{
		{name: "candidate absent after crash", mutate: func(runtime *fakeManagerRuntime) {
			runtime.installedPresent, runtime.installedHash = false, ""
		}, code: ErrorInstalledMismatch},
		{name: "runtime not ready after crash", mutate: func(runtime *fakeManagerRuntime) {
			runtime.ready = false
		}, code: ErrorRecoveryUnconfirmed},
		{name: "supervisor unloaded after crash", mutate: func(runtime *fakeManagerRuntime) {
			runtime.unloaded = true
		}, code: ErrorRecoveryUnconfirmed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			manager, runtime, request := newManagerFixture(t, true)
			prepared, err := manager.Prepare(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			pending, err := manager.Resume(context.Background(), prepared.ID)
			if err != nil {
				t.Fatal(err)
			}
			locked, err := manager.Store.Acquire()
			if err != nil {
				t.Fatal(err)
			}
			accepting := *pending
			accepting.State = StateAccepting
			if err := locked.Rewrite(accepting); err != nil {
				_ = locked.Close()
				t.Fatal(err)
			}
			if err := locked.Close(); err != nil {
				t.Fatal(err)
			}

			runtime.calls = nil
			tt.mutate(runtime)
			active, err := manager.Accept(context.Background(), prepared.ID)
			if got := SanitizedError(err); got == nil || got.Code != tt.code {
				t.Fatalf("resumed Accept = %#v, %v; want %s", active, err, tt.code)
			}
			if active == nil || active.State != StateAccepting || active.ID != prepared.ID {
				t.Fatalf("retained transaction = %#v, want accepting %s", active, prepared.ID)
			}
			if len(runtime.calls) != 0 {
				t.Fatalf("rejected resumed accept emitted runtime effects: %v", runtime.calls)
			}
			status, statusErr := manager.Status(context.Background())
			if statusErr != nil || status == nil || status.State != StateAccepting {
				t.Fatalf("durable state = %#v, %v; want retained accepting", status, statusErr)
			}
		})
	}
}

func TestManagerPrepareRejectsSourcesChangedDuringObservation(t *testing.T) {
	for _, role := range []string{"candidate", "authority", "previous"} {
		t.Run(role, func(t *testing.T) {
			manager, runtime, request := newManagerFixture(t, true)
			path := map[string]string{
				"candidate": request.CandidatePath,
				"authority": request.AuthorityPath,
				"previous":  request.TargetPath,
			}[role]
			runtime.afterObserve = func() {
				if err := os.WriteFile(path, []byte("changed after selection"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			prepared, err := manager.Prepare(context.Background(), request)
			if got := SanitizedError(err); got == nil || got.Code != ErrorArtifactMismatch {
				t.Fatalf("Prepare = %#v, %v; want artifact_mismatch", prepared, err)
			}
			if got, inspectErr := manager.Store.Inspect(); inspectErr != nil || got != nil {
				t.Fatalf("failed prepare published state: %#v, %v", got, inspectErr)
			}
		})
	}
}

func TestManagerPrepareRejectsOperationalAliasBeforeRuntimeObservation(t *testing.T) {
	manager, runtime, request := newManagerFixture(t, true)
	request.HealthURLFile = request.TargetPath
	runtime.observeCalls = 0
	prepared, err := manager.Prepare(context.Background(), request)
	if got := SanitizedError(err); got == nil || got.Code != ErrorStateMalformed {
		t.Fatalf("Prepare = %#v, %v; want state_malformed", prepared, err)
	}
	if runtime.observeCalls != 0 || len(runtime.calls) != 0 {
		t.Fatalf("operational alias reached runtime: observes=%d calls=%v", runtime.observeCalls, runtime.calls)
	}
	if got, inspectErr := manager.Store.Inspect(); inspectErr != nil || got != nil {
		t.Fatalf("operational alias published state: %#v, %v", got, inspectErr)
	}
}

func TestManagerReadinessFailureDurablyRestoresPrevious(t *testing.T) {
	manager, runtime, request := newManagerFixture(t, true)
	ctx := context.Background()
	prepared, err := manager.Prepare(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	runtime.waitFailures = 1

	// Readiness failed but rollback to the previous runtime was confirmed
	// (asserted below), so this is rolled_back, not recovery_unconfirmed.
	got, err := manager.Resume(ctx, prepared.ID)
	if SanitizedError(err).Code != ErrorRolledBack || got == nil {
		t.Fatalf("resume = %#v, %v; want sanitized rolled-back failure", got, err)
	}
	if runtime.installedHash != prepared.PreviousSHA256 || !runtime.ready {
		t.Fatalf("recovered runtime = hash %q ready=%v", runtime.installedHash, runtime.ready)
	}
	status, statusErr := manager.Status(ctx)
	if statusErr != nil || status != nil {
		t.Fatalf("status after recovered failure = %#v, %v; want clear", status, statusErr)
	}
	want := []string{"install", "restart", "ready", "restore", "restart", "ready"}
	if !reflect.DeepEqual(runtime.calls, want) {
		t.Fatalf("runtime calls = %v, want %v", runtime.calls, want)
	}
}

func TestManagerPreparedCandidateBytesStillRestartBeforePending(t *testing.T) {
	manager, runtime, request := newManagerFixture(t, true)
	prepared, err := manager.Prepare(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	// Model the install-before-restart crash window: candidate bytes are on
	// disk, while the ready observation can still belong to the old process.
	runtime.installedPresent = true
	runtime.installedHash = prepared.CandidateSHA256
	runtime.ready = true
	runtime.calls = nil

	pending, err := manager.Resume(context.Background(), prepared.ID)
	if err != nil || pending == nil || pending.State != StatePending {
		t.Fatalf("Resume = %#v, %v; want pending", pending, err)
	}
	if want := []string{"restart", "ready"}; !reflect.DeepEqual(runtime.calls, want) {
		t.Fatalf("stale-process reconciliation calls = %v, want %v", runtime.calls, want)
	}
}

func TestManagerPreviousBytesStillRestartBeforeRollbackClear(t *testing.T) {
	manager, runtime, request := newManagerFixture(t, true)
	prepared, err := manager.Prepare(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := manager.Resume(context.Background(), prepared.ID)
	if err != nil {
		t.Fatal(err)
	}
	locked, err := manager.Store.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	rolling := *pending
	rolling.State = StateRollingBack
	if err := locked.Rewrite(rolling); err != nil {
		_ = locked.Close()
		t.Fatal(err)
	}
	if err := locked.Close(); err != nil {
		t.Fatal(err)
	}

	// Model the restore-before-restart crash window. Passive readiness cannot
	// prove which executable the still-running process loaded.
	runtime.installedPresent = true
	runtime.installedHash = prepared.PreviousSHA256
	runtime.ready = true
	runtime.calls = nil
	cleared, err := manager.Rollback(context.Background(), prepared.ID)
	if err != nil || cleared != nil {
		t.Fatalf("Rollback = %#v, %v; want clear", cleared, err)
	}
	if want := []string{"restart", "ready"}; !reflect.DeepEqual(runtime.calls, want) {
		t.Fatalf("stale-process rollback calls = %v, want %v", runtime.calls, want)
	}
}

func TestManagerFirstInstallRollbackConfirmsUnloadBeforeRemove(t *testing.T) {
	manager, runtime, request := newManagerFixture(t, false)
	ctx := context.Background()
	prepared, err := manager.Prepare(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Resume(ctx, prepared.ID); err != nil {
		t.Fatal(err)
	}
	runtime.calls = nil
	runtime.confirmOverride = func() (bool, error) { return false, nil }

	active, err := manager.Rollback(ctx, prepared.ID)
	if SanitizedError(err).Code != ErrorRecoveryUnconfirmed || active == nil || active.State != StateRollingBack {
		t.Fatalf("rollback = %#v, %v; want retained rolling_back", active, err)
	}
	if contains(runtime.calls, "remove") {
		t.Fatalf("target removed before unload confirmation: %v", runtime.calls)
	}
	runtime.confirmOverride = nil
	cleared, err := manager.Rollback(ctx, prepared.ID)
	if err != nil || cleared != nil {
		t.Fatalf("resumed rollback = %#v, %v; want clear", cleared, err)
	}
	if !contains(runtime.calls, "remove") || runtime.installedPresent {
		t.Fatalf("confirmed rollback calls = %v present=%v", runtime.calls, runtime.installedPresent)
	}
}

func TestManagerStatusPreservesIdentityAcrossRuntimeDrift(t *testing.T) {
	manager, runtime, request := newManagerFixture(t, true)
	ctx := context.Background()
	prepared, err := manager.Prepare(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Resume(ctx, prepared.ID); err != nil {
		t.Fatal(err)
	}
	runtime.runtimeDrift = true

	status, err := manager.Status(ctx)
	if err != nil || status.ID != prepared.ID {
		t.Fatalf("status under drift = %#v, %v", status, err)
	}
	runtime.calls = nil
	active, err := manager.Accept(ctx, prepared.ID)
	if SanitizedError(err).Code != ErrorRuntimeDrift || active.ID != prepared.ID {
		t.Fatalf("accept under drift = %#v, %v", active, err)
	}
	if len(runtime.calls) != 0 {
		t.Fatalf("runtime drift emitted effects: %v", runtime.calls)
	}
}

func TestManagerExactIDAndPinnedAuthorityFailClosed(t *testing.T) {
	manager, runtime, request := newManagerFixture(t, true)
	ctx := context.Background()
	prepared, err := manager.Prepare(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	runtime.calls = nil
	if _, err := manager.Rollback(ctx, ReleaseID(strings.Repeat("9", 64))); SanitizedError(err).Code != ErrorIdentityMismatch {
		t.Fatalf("stale rollback error = %v", err)
	}
	if len(runtime.calls) != 0 {
		t.Fatalf("stale identity emitted effects: %v", runtime.calls)
	}
	if err := os.WriteFile(manager.ControllerPath, []byte("changed controller"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Status(ctx); SanitizedError(err).Code != ErrorAuthorityMismatch {
		t.Fatalf("changed-controller status error = %v", err)
	}
	if _, err := manager.Rollback(ctx, prepared.ID); SanitizedError(err).Code != ErrorAuthorityMismatch {
		t.Fatalf("changed-controller rollback error = %v", err)
	}
	if len(runtime.calls) != 0 {
		t.Fatalf("wrong authority emitted effects: %v", runtime.calls)
	}
}

func TestManagerGenericResumeCannotCompleteTerminalDirection(t *testing.T) {
	manager, _, request := newManagerFixture(t, true)
	ctx := context.Background()
	prepared, err := manager.Prepare(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := manager.Resume(ctx, prepared.ID)
	if err != nil {
		t.Fatal(err)
	}
	locked, err := manager.Store.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	accepting := *pending
	accepting.State = StateAccepting
	if err := locked.Rewrite(accepting); err != nil {
		locked.Close()
		t.Fatal(err)
	}
	if err := locked.Close(); err != nil {
		t.Fatal(err)
	}

	active, err := manager.Resume(ctx, "")
	if SanitizedError(err).Code != ErrorStateConflict || active.State != StateAccepting {
		t.Fatalf("generic terminal resume = %#v, %v", active, err)
	}
	if cleared, err := manager.Accept(ctx, prepared.ID); err != nil || cleared != nil {
		t.Fatalf("same-direction exact accept = %#v, %v", cleared, err)
	}
}

func TestManagerRejectsAuthorityIdentityAndEventBeforeCleanupOrObservation(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(t *testing.T, manager *Manager, runtime *fakeManagerRuntime, request PrepareRequest) (func() (*Manifest, error), ErrorCode)
	}{
		{
			name: "wrong controller wins before stale identity",
			prepare: func(t *testing.T, manager *Manager, _ *fakeManagerRuntime, request PrepareRequest) (func() (*Manifest, error), ErrorCode) {
				_, err := manager.Prepare(context.Background(), request)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(manager.ControllerPath, []byte("changed controller"), 0o700); err != nil {
					t.Fatal(err)
				}
				return func() (*Manifest, error) {
					return manager.Rollback(context.Background(), ReleaseID(strings.Repeat("9", 64)))
				}, ErrorAuthorityMismatch
			},
		},
		{
			name: "stale identity",
			prepare: func(t *testing.T, manager *Manager, _ *fakeManagerRuntime, request PrepareRequest) (func() (*Manifest, error), ErrorCode) {
				if _, err := manager.Prepare(context.Background(), request); err != nil {
					t.Fatal(err)
				}
				return func() (*Manifest, error) {
					return manager.Rollback(context.Background(), ReleaseID(strings.Repeat("9", 64)))
				}, ErrorIdentityMismatch
			},
		},
		{
			name: "illegal generic resume",
			prepare: func(t *testing.T, manager *Manager, runtime *fakeManagerRuntime, request PrepareRequest) (func() (*Manifest, error), ErrorCode) {
				prepared, err := manager.Prepare(context.Background(), request)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := manager.Resume(context.Background(), prepared.ID); err != nil {
					t.Fatal(err)
				}
				runtime.calls = nil
				return func() (*Manifest, error) { return manager.Resume(context.Background(), prepared.ID) }, ErrorStateConflict
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager, runtime, request := newManagerFixture(t, true)
			invoke, wantCode := tt.prepare(t, manager, runtime, request)
			orphan := filepath.Join(manager.Store.Root(), "cleanup.unauthenticated-orphan")
			if err := os.Mkdir(orphan, 0o700); err != nil {
				t.Fatal(err)
			}
			runtime.observeCalls = 0
			runtime.calls = nil
			active, err := invoke()
			if got := SanitizedError(err); got == nil || got.Code != wantCode {
				t.Fatalf("operation = %#v, %v; want %s", active, err, wantCode)
			}
			if runtime.observeCalls != 0 || len(runtime.calls) != 0 {
				t.Fatalf("rejected request reached runtime: observes=%d calls=%v", runtime.observeCalls, runtime.calls)
			}
			if _, err := os.Lstat(orphan); err != nil {
				t.Fatalf("rejected request cleaned orphan evidence: %v", err)
			}
		})
	}
}

// TestManagerProbeRunsWhileTransactionActiveWithoutOrphanCleanup proves the
// contrast at the center of Probe's contract: unlike WithClear, it must
// succeed while a transaction is active (verify-live has to work while a
// release is pending) and it must never prune orphans.
func TestManagerProbeRunsWhileTransactionActiveWithoutOrphanCleanup(t *testing.T) {
	manager, runtime, request := newManagerFixture(t, true)
	ctx := context.Background()

	orphanCleanupFired := false
	store, err := NewStoreAtWithHook(manager.Store.Root(), 501, func(point StoreHookPoint) error {
		if point == StoreBeforeOrphanCleanup {
			orphanCleanupFired = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	manager.Store = store

	if _, err := manager.Prepare(ctx, request); err != nil {
		t.Fatal(err)
	}
	// Prepare itself prunes orphans before publishing; reset so the assertion
	// below is about Probe alone.
	orphanCleanupFired = false

	called := false
	var gotRuntime Runtime
	if err := manager.Probe(ctx, func(_ context.Context, got Runtime) error {
		called = true
		gotRuntime = got
		return nil
	}); err != nil {
		t.Fatalf("probe during an active transaction failed: %v", err)
	}
	if !called || gotRuntime != runtime {
		t.Fatalf("probe callback = called %v, runtime match %v", called, gotRuntime == runtime)
	}
	if orphanCleanupFired {
		t.Fatal("Probe pruned orphans; it must never do so, unlike WithClear")
	}

	// Contrast: the same active transaction still correctly rejects WithClear.
	if err := manager.WithClear(ctx, func(context.Context, Runtime) error { return nil }); SanitizedError(err).Code != ErrorStateConflict {
		t.Fatalf("WithClear during the same active transaction = %v, want state conflict", err)
	}
}

func TestManagerWithClearAndBusyGate(t *testing.T) {
	manager, runtime, request := newManagerFixture(t, true)
	ctx := context.Background()
	called := false
	if err := manager.WithClear(ctx, func(_ context.Context, got Runtime) error {
		called = got == runtime
		return nil
	}); err != nil || !called {
		t.Fatalf("clear callback = called %v, error %v", called, err)
	}
	if _, err := manager.Prepare(ctx, request); err != nil {
		t.Fatal(err)
	}
	called = false
	if err := manager.WithClear(ctx, func(context.Context, Runtime) error { called = true; return nil }); SanitizedError(err).Code != ErrorStateConflict {
		t.Fatalf("active clear gate error = %v", err)
	}
	if called {
		t.Fatal("active clear gate invoked administrative effect")
	}

	locked, err := manager.Store.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer locked.Close()
	if _, err := manager.Status(ctx); SanitizedError(err).Code != ErrorBusy {
		t.Fatalf("busy status error = %v", err)
	}
}

func TestSanitizedErrorClassifiesHostEffectRolledBackAndUpdateFailures(t *testing.T) {
	for _, tt := range []struct {
		name        string
		err         error
		code        ErrorCode
		wantMessage string
	}{
		{
			name:        "host effect timeout",
			err:         runtimeFailure("runtime restart", context.DeadlineExceeded),
			code:        ErrorHostEffectFailed,
			wantMessage: "runtime restart: timeout",
		},
		{
			name: "host effect timeout through errors.Join, matching the adapter path",
			// invokeAdapter and Restart build their cause with
			// errors.Join(err, exitError(result.ExitCode)); a bounded child
			// deadline reports err=context.DeadlineExceeded and a zero exit
			// code, so exitError(0) is nil and errors.Join drops it.
			err:         runtimeFailure("launch agent installation", errors.Join(context.DeadlineExceeded, exitError(0))),
			code:        ErrorHostEffectFailed,
			wantMessage: "launch agent installation: timeout",
		},
		{
			name: "host effect exit status through errors.Join, matching the adapter path",
			// A child that exits nonzero without a context error reports
			// err=nil and a nonzero exit code, so errors.Join drops the nil
			// err and keeps only the typed exit status.
			err:         runtimeFailure("runtime bootout", errors.Join(nil, exitError(17))),
			code:        ErrorHostEffectFailed,
			wantMessage: "runtime bootout: exit_status=17",
		},
		{
			name:        "host effect generic failure",
			err:         runtimeFailure("readiness", errors.New("bounded readiness exhausted")),
			code:        ErrorHostEffectFailed,
			wantMessage: "readiness: failed",
		},
		{
			name:        "rolled back after a runtime-operation cause",
			err:         rolledBackFailure(causeOperation(runtimeFailure("runtime restart", errors.New("synthetic")))),
			code:        ErrorRolledBack,
			wantMessage: "candidate failed at runtime restart; previous runtime restored",
		},
		{
			name:        "rolled back after a non-runtime cause falls back to a generic step name",
			err:         rolledBackFailure(causeOperation(errors.New("plain cause"))),
			code:        ErrorRolledBack,
			wantMessage: "candidate failed at deployment; previous runtime restored",
		},
		{
			name:        "update check failure",
			err:         fmt.Errorf("branch check failed: %w", ErrUpdateCheckFailed),
			code:        ErrorUpdateFailed,
			wantMessage: "update precondition or fast-forward check failed",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizedError(tt.err)
			if got == nil || got.Code != tt.code || got.Message != tt.wantMessage {
				t.Fatalf("SanitizedError(%v) = %#v, want code %s message %q", tt.err, got, tt.code, tt.wantMessage)
			}
			if strings.Contains(got.Message, "synthetic") || strings.Contains(got.Message, "plain cause") || strings.Contains(got.Message, "bounded readiness exhausted") {
				t.Fatalf("sanitized error leaked cause text: %q", got.Message)
			}
		})
	}
}

func TestSanitizedErrorNeverCarriesInternalCause(t *testing.T) {
	secret := "/Users/private/vault token=do-not-print"
	got := SanitizedError(errors.New(secret))
	if got.Code != ErrorRecoveryUnconfirmed || strings.Contains(got.Error(), secret) || strings.Contains(got.Error(), "vault") {
		t.Fatalf("sanitized error leaked cause: %q", got)
	}
	for _, err := range []error{ErrBusy, ErrArtifactMismatch, ErrStateConflict, ErrStateMalformed, &PathTopologyError{}, lifecycleError(ErrorRuntimeDrift)} {
		got := SanitizedError(err)
		if got == nil || got.Message != errorMessage(got.Code) {
			t.Fatalf("sanitized error = %#v for %v", got, err)
		}
	}
}

type fakeManagerRuntime struct {
	installedPresent   bool
	installedHash      string
	ready              bool
	unloaded           bool
	runtimeDrift       bool
	waitFailures       int
	confirmOverride    func() (bool, error)
	calls              []string
	serviceRestarts    []string
	serviceBootouts    []string
	observeCalls       int
	afterObserve       func()
	ynabLoaded         bool
	obsidianHTTPLoaded bool
	ynabHTTPLoaded     bool
}

func (f *fakeManagerRuntime) Observe(_ context.Context, m Manifest, controller string, artifacts RuntimeArtifacts) (Observed, error) {
	f.observeCalls++
	candidate, err := HashRegular(artifacts.Candidate)
	if err != nil {
		return Observed{}, err
	}
	authority, err := HashRegular(artifacts.Authority)
	if err != nil {
		return Observed{}, err
	}
	controllerHash, err := HashRegular(controller)
	if err != nil {
		return Observed{}, err
	}
	previous := ""
	if m.PreviousPresent {
		previous, err = HashRegular(artifacts.Previous)
		if err != nil {
			return Observed{}, err
		}
	}
	wrapper := m.WrapperSHA256
	if f.runtimeDrift {
		wrapper = strings.Repeat("0", 64)
	}
	observed := Observed{
		CandidateSHA256: candidate, AuthorityArtifactSHA256: authority,
		ControllerSHA256: controllerHash, PreviousSHA256: previous,
		InstalledPresent: f.installedPresent, InstalledSHA256: f.installedHash,
		PlistSHA256: m.PlistSHA256, WrapperSHA256: wrapper,
		MCPWrapperSHA256: m.MCPWrapperSHA256, EnvironmentSHA256: m.EnvironmentSHA256,
		RuntimeReady: f.ready, SupervisorUnloaded: f.unloaded,
	}
	if f.afterObserve != nil {
		f.afterObserve()
	}
	return observed, nil
}

func (f *fakeManagerRuntime) ServiceLoaded(_ context.Context, m Manifest) (bool, error) {
	switch m.LaunchAgentLabel {
	case "com.ericfeunekes.personal-mcp-gateway.ynab-tunnel":
		return f.ynabLoaded, nil
	case "com.ericfeunekes.personal-mcp-gateway.obsidian-http":
		return f.obsidianHTTPLoaded, nil
	case "com.ericfeunekes.personal-mcp-gateway.ynab-http":
		return f.ynabHTTPLoaded, nil
	default:
		return false, nil
	}
}

func (f *fakeManagerRuntime) ReadyOnce(context.Context, Manifest) bool {
	return f.ready
}

func (f *fakeManagerRuntime) InstallCandidate(_ context.Context, m Manifest, _ RuntimeArtifacts) error {
	f.calls = append(f.calls, "install")
	f.installedPresent, f.installedHash, f.ready, f.unloaded = true, m.CandidateSHA256, false, false
	return nil
}
func (f *fakeManagerRuntime) Restart(_ context.Context, manifest Manifest) error {
	f.calls = append(f.calls, "restart")
	f.serviceRestarts = append(f.serviceRestarts, manifest.LaunchAgentLabel)
	f.unloaded = false
	return nil
}
func (f *fakeManagerRuntime) WaitReady(context.Context, Manifest) error {
	f.calls = append(f.calls, "ready")
	if f.waitFailures > 0 {
		f.waitFailures--
		f.ready = false
		return errors.New("synthetic readiness details")
	}
	f.ready = true
	return nil
}
func (f *fakeManagerRuntime) RestorePrevious(_ context.Context, m Manifest, _ RuntimeArtifacts) error {
	f.calls = append(f.calls, "restore")
	f.installedPresent, f.installedHash, f.ready, f.unloaded = true, m.PreviousSHA256, false, false
	return nil
}
func (f *fakeManagerRuntime) Bootout(_ context.Context, manifest Manifest) error {
	f.calls = append(f.calls, "bootout")
	f.serviceBootouts = append(f.serviceBootouts, manifest.LaunchAgentLabel)
	f.unloaded, f.ready = true, false
	return nil
}
func (f *fakeManagerRuntime) ConfirmUnloaded(context.Context, Manifest) (bool, error) {
	f.calls = append(f.calls, "confirm_unloaded")
	if f.confirmOverride != nil {
		return f.confirmOverride()
	}
	return f.unloaded, nil
}
func (f *fakeManagerRuntime) RemoveTarget(context.Context, Manifest) error {
	f.calls = append(f.calls, "remove")
	f.installedPresent, f.installedHash = false, ""
	return nil
}
func (f *fakeManagerRuntime) InvokeInstallAdapter(context.Context, string, ...string) error {
	f.calls = append(f.calls, "install_adapter")
	return nil
}
func (f *fakeManagerRuntime) InvokeUninstallAdapter(context.Context, string, ...string) error {
	f.calls = append(f.calls, "uninstall_adapter")
	return nil
}

func newManagerFixture(t *testing.T, previous bool) (*Manager, *fakeManagerRuntime, PrepareRequest) {
	t.Helper()
	root := t.TempDir()
	stateRoot := filepath.Join(root, "state")
	store, err := NewStoreAt(stateRoot, 501)
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, contents string, mode os.FileMode) string {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(contents), mode); err != nil {
			t.Fatal(err)
		}
		return path
	}
	authority := write("controller", "controller", 0o700)
	target := filepath.Join(root, "gateway")
	runtime := &fakeManagerRuntime{unloaded: !previous}
	if previous {
		if err := os.WriteFile(target, []byte("previous"), 0o700); err != nil {
			t.Fatal(err)
		}
		runtime.installedPresent = true
		runtime.installedHash, err = HashRegular(target)
		if err != nil {
			t.Fatal(err)
		}
	}
	authoritySHA256, err := HashRegular(authority)
	if err != nil {
		t.Fatal(err)
	}
	manager := &Manager{Store: store, Runtime: runtime, ControllerPath: authority, ControllerSHA256: authoritySHA256}
	candidatePath := write("candidate", "candidate", 0o700)
	candidateSHA256, err := HashRegular(candidatePath)
	if err != nil {
		t.Fatal(err)
	}
	request := PrepareRequest{
		Commit: strings.Repeat("1", 40), CandidateSHA256: candidateSHA256, AuthoritySHA256: authoritySHA256, DependencySHA256: strings.Repeat("9", 64),
		CandidatePath: candidatePath, AuthorityPath: authority,
		TargetPath: target, EffectiveUID: 501, LaunchAgentLabel: "local.test.gateway",
		PlistPath: write("launch.plist", "plist", 0o600), WrapperPath: write("wrapper", "wrapper", 0o700),
		MCPWrapperPath: write("mcp-wrapper", "mcp wrapper", 0o700),
		StdoutPath:     filepath.Join(root, "stdout.log"), StderrPath: filepath.Join(root, "stderr.log"),
		EnvironmentPath: write("environment", "not-a-secret", 0o600), HealthURLFile: filepath.Join(root, "health-url"),
		ReadyTimeoutSeconds: 10, ReadyPollMilliseconds: 100,
	}
	return manager, runtime, request
}

func TestPrepareCapturesOnlyLoadedYNABServiceAndRestartsCapturedOrder(t *testing.T) {
	manager, runtime, request := newManagerFixture(t, true)
	root := filepath.Dir(request.TargetPath)
	write := func(name string) string {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(name), 0o700); err != nil {
			t.Fatal(err)
		}
		return path
	}
	candidate := ServiceCandidate{Server: "ynab", LaunchAgentLabel: "com.ericfeunekes.personal-mcp-gateway.ynab-tunnel",
		PlistPath: write("ynab.plist"), WrapperPath: write("run-ynab.sh"), MCPWrapperPath: write("run-ynab-mcp.sh"),
		StdoutPath: filepath.Join(root, "ynab.out"), StderrPath: filepath.Join(root, "ynab.err"),
		EnvironmentPath: write("ynab.env"), HealthURLFile: filepath.Join(root, "ynab.health")}
	request.ServiceCandidates = []ServiceCandidate{candidate}
	prepared, err := manager.Prepare(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Services) != 1 || prepared.Services[0].Server != "obsidian" {
		t.Fatalf("absent YNAB captured: %#v", prepared.Services)
	}

	manager, runtime, request = newManagerFixture(t, true)
	root = filepath.Dir(request.TargetPath)
	write = func(name string) string {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(name), 0o700); err != nil {
			t.Fatal(err)
		}
		return path
	}
	environment := write("ynab.env")
	if err := os.WriteFile(environment, []byte("GATEWAY_BIN="+request.TargetPath+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	request.ServiceCandidates = []ServiceCandidate{{Server: "ynab", LaunchAgentLabel: "com.ericfeunekes.personal-mcp-gateway.ynab-tunnel",
		PlistPath: write("ynab.plist"), WrapperPath: write("run-ynab.sh"), MCPWrapperPath: write("run-ynab-mcp.sh"),
		StdoutPath: filepath.Join(root, "ynab.out"), StderrPath: filepath.Join(root, "ynab.err"), EnvironmentPath: environment, HealthURLFile: filepath.Join(root, "ynab.health")}}
	runtime.ynabLoaded = true // loaded but not ready models a crashed child.
	prepared, err = manager.Prepare(context.Background(), request)
	if err != nil || len(prepared.Services) != 2 || prepared.Services[1].Server != "ynab" {
		t.Fatalf("loaded YNAB not captured: %#v err=%v", prepared, err)
	}
	if _, err := manager.Resume(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if want := []string{"local.test.gateway", "com.ericfeunekes.personal-mcp-gateway.ynab-tunnel"}; !reflect.DeepEqual(runtime.serviceRestarts, want) {
		t.Fatalf("restart order = %v, want %v", runtime.serviceRestarts, want)
	}
}

func TestPrepareCapturesOnlyLoadedHTTPServicesAndRestartsCapturedOrder(t *testing.T) {
	manager, runtime, request := newManagerFixture(t, true)
	root := filepath.Dir(request.TargetPath)
	write := func(name string) string {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(name), 0o700); err != nil {
			t.Fatal(err)
		}
		return path
	}
	obsidianHTTP := ServiceCandidate{Server: "obsidian-http", LaunchAgentLabel: "com.ericfeunekes.personal-mcp-gateway.obsidian-http",
		PlistPath: write("obsidian-http.plist"), WrapperPath: write("run-obsidian-http.sh"),
		StdoutPath: filepath.Join(root, "obsidian-http.out"), StderrPath: filepath.Join(root, "obsidian-http.err"),
		EnvironmentPath: write("obsidian-http.env"), HealthURLFile: filepath.Join(root, "obsidian-http.health")}
	ynabHTTP := ServiceCandidate{Server: "ynab-http", LaunchAgentLabel: "com.ericfeunekes.personal-mcp-gateway.ynab-http",
		PlistPath: write("ynab-http.plist"), WrapperPath: write("run-ynab-http.sh"),
		StdoutPath: filepath.Join(root, "ynab-http.out"), StderrPath: filepath.Join(root, "ynab-http.err"),
		EnvironmentPath: write("ynab-http.env"), HealthURLFile: filepath.Join(root, "ynab-http.health")}
	request.ServiceCandidates = []ServiceCandidate{obsidianHTTP, ynabHTTP}
	prepared, err := manager.Prepare(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Services) != 1 || prepared.Services[0].Server != "obsidian" {
		t.Fatalf("absent HTTP services captured: %#v", prepared.Services)
	}

	manager, runtime, request = newManagerFixture(t, true)
	root = filepath.Dir(request.TargetPath)
	write = func(name string) string {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(name), 0o700); err != nil {
			t.Fatal(err)
		}
		return path
	}
	obsidianHTTPEnv := write("obsidian-http.env")
	if err := os.WriteFile(obsidianHTTPEnv, []byte("GATEWAY_BIN="+request.TargetPath+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ynabHTTPEnv := write("ynab-http.env")
	if err := os.WriteFile(ynabHTTPEnv, []byte("GATEWAY_BIN="+request.TargetPath+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	request.ServiceCandidates = []ServiceCandidate{
		{Server: "obsidian-http", LaunchAgentLabel: "com.ericfeunekes.personal-mcp-gateway.obsidian-http",
			PlistPath: write("obsidian-http.plist"), WrapperPath: write("run-obsidian-http.sh"),
			StdoutPath: filepath.Join(root, "obsidian-http.out"), StderrPath: filepath.Join(root, "obsidian-http.err"),
			EnvironmentPath: obsidianHTTPEnv, HealthURLFile: filepath.Join(root, "obsidian-http.health")},
		{Server: "ynab-http", LaunchAgentLabel: "com.ericfeunekes.personal-mcp-gateway.ynab-http",
			PlistPath: write("ynab-http.plist"), WrapperPath: write("run-ynab-http.sh"),
			StdoutPath: filepath.Join(root, "ynab-http.out"), StderrPath: filepath.Join(root, "ynab-http.err"),
			EnvironmentPath: ynabHTTPEnv, HealthURLFile: filepath.Join(root, "ynab-http.health")},
	}
	runtime.obsidianHTTPLoaded = true // loaded but not ready models a crashed child.
	runtime.ynabHTTPLoaded = true
	prepared, err = manager.Prepare(context.Background(), request)
	if err != nil || len(prepared.Services) != 3 || prepared.Services[1].Server != "obsidian-http" || prepared.Services[2].Server != "ynab-http" {
		t.Fatalf("loaded HTTP services not captured in canonical order: %#v err=%v", prepared, err)
	}
	if prepared.Services[1].MCPWrapperPath != "" || prepared.Services[1].MCPWrapperSHA256 != "" ||
		prepared.Services[2].MCPWrapperPath != "" || prepared.Services[2].MCPWrapperSHA256 != "" {
		t.Fatalf("HTTP service descriptor recorded an MCP wrapper: %#v", prepared.Services)
	}
	if _, err := manager.Resume(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	want := []string{"local.test.gateway", "com.ericfeunekes.personal-mcp-gateway.obsidian-http", "com.ericfeunekes.personal-mcp-gateway.ynab-http"}
	if !reflect.DeepEqual(runtime.serviceRestarts, want) {
		t.Fatalf("restart order = %v, want %v", runtime.serviceRestarts, want)
	}
}

func TestPrepareCapturesAllLoadedAdditionalServicesInCanonicalOrder(t *testing.T) {
	manager, runtime, request := newManagerFixture(t, true)
	root := filepath.Dir(request.TargetPath)
	write := func(name, contents string) string {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
			t.Fatal(err)
		}
		return path
	}
	gatewayBinding := "GATEWAY_BIN=" + request.TargetPath + "\n"
	runtime.ynabLoaded, runtime.obsidianHTTPLoaded, runtime.ynabHTTPLoaded = true, true, true
	request.ServiceCandidates = []ServiceCandidate{
		{Server: "ynab", LaunchAgentLabel: "com.ericfeunekes.personal-mcp-gateway.ynab-tunnel",
			PlistPath: write("ynab.plist", "plist"), WrapperPath: write("run-ynab.sh", "wrapper"), MCPWrapperPath: write("run-ynab-mcp.sh", "mcp"),
			StdoutPath: filepath.Join(root, "ynab.out"), StderrPath: filepath.Join(root, "ynab.err"),
			EnvironmentPath: write("ynab.env", gatewayBinding), HealthURLFile: filepath.Join(root, "ynab.health")},
		{Server: "obsidian-http", LaunchAgentLabel: "com.ericfeunekes.personal-mcp-gateway.obsidian-http",
			PlistPath: write("obsidian-http.plist", "plist"), WrapperPath: write("run-obsidian-http.sh", "wrapper"),
			StdoutPath: filepath.Join(root, "obsidian-http.out"), StderrPath: filepath.Join(root, "obsidian-http.err"),
			EnvironmentPath: write("obsidian-http.env", gatewayBinding), HealthURLFile: filepath.Join(root, "obsidian-http.health")},
		{Server: "ynab-http", LaunchAgentLabel: "com.ericfeunekes.personal-mcp-gateway.ynab-http",
			PlistPath: write("ynab-http.plist", "plist"), WrapperPath: write("run-ynab-http.sh", "wrapper"),
			StdoutPath: filepath.Join(root, "ynab-http.out"), StderrPath: filepath.Join(root, "ynab-http.err"),
			EnvironmentPath: write("ynab-http.env", gatewayBinding), HealthURLFile: filepath.Join(root, "ynab-http.health")},
	}
	prepared, err := manager.Prepare(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	wantServers := []string{"obsidian", "ynab", "obsidian-http", "ynab-http"}
	if len(prepared.Services) != len(wantServers) {
		t.Fatalf("services = %#v, want servers %v", prepared.Services, wantServers)
	}
	for i, server := range wantServers {
		if prepared.Services[i].Server != server {
			t.Fatalf("services[%d].Server = %q, want %q (%#v)", i, prepared.Services[i].Server, server, prepared.Services)
		}
	}
	if _, err := manager.Resume(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	wantRestarts := []string{
		"local.test.gateway",
		"com.ericfeunekes.personal-mcp-gateway.ynab-tunnel",
		"com.ericfeunekes.personal-mcp-gateway.obsidian-http",
		"com.ericfeunekes.personal-mcp-gateway.ynab-http",
	}
	if !reflect.DeepEqual(runtime.serviceRestarts, wantRestarts) {
		t.Fatalf("restart order = %v, want %v", runtime.serviceRestarts, wantRestarts)
	}
}

func TestServiceGatewayBindingRejectsDifferentBinary(t *testing.T) {
	root := t.TempDir()
	environment := filepath.Join(root, ".env.ynab.local")
	if err := os.WriteFile(environment, []byte("GATEWAY_BIN=/private/other-gateway\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateServiceGatewayBinding(environment, filepath.Join(root, "Library", "LaunchAgents", "ynab.plist"), "/private/gateway"); err == nil {
		t.Fatal("different gateway binary was accepted")
	}
}

func TestFirstInstallRollbackBootsOutCapturedServicesInOrder(t *testing.T) {
	manager, runtime, request := newManagerFixture(t, false)
	root := filepath.Dir(request.TargetPath)
	write := func(name, contents string) string {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
			t.Fatal(err)
		}
		return path
	}
	runtime.ynabLoaded = true
	request.ServiceCandidates = []ServiceCandidate{{Server: "ynab", LaunchAgentLabel: "com.ericfeunekes.personal-mcp-gateway.ynab-tunnel",
		PlistPath: write("ynab.plist", "plist"), WrapperPath: write("run-ynab.sh", "wrapper"), MCPWrapperPath: write("run-ynab-mcp.sh", "mcp"),
		StdoutPath: filepath.Join(root, "ynab.out"), StderrPath: filepath.Join(root, "ynab.err"),
		EnvironmentPath: write("ynab.env", "GATEWAY_BIN="+request.TargetPath+"\n"), HealthURLFile: filepath.Join(root, "ynab.health")}}
	prepared, err := manager.Prepare(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := manager.Resume(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Rollback(context.Background(), pending.ID); err != nil {
		t.Fatal(err)
	}
	want := []string{"local.test.gateway", "com.ericfeunekes.personal-mcp-gateway.ynab-tunnel"}
	if !reflect.DeepEqual(runtime.serviceBootouts, want) {
		t.Fatalf("bootout order = %v, want %v", runtime.serviceBootouts, want)
	}
	if prepared == nil {
		t.Fatal("prepared release disappeared")
	}
}

func TestFirstInstallRollbackBootsOutCapturedHTTPServicesInOrder(t *testing.T) {
	manager, runtime, request := newManagerFixture(t, false)
	root := filepath.Dir(request.TargetPath)
	write := func(name, contents string) string {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
			t.Fatal(err)
		}
		return path
	}
	gatewayBinding := "GATEWAY_BIN=" + request.TargetPath + "\n"
	runtime.obsidianHTTPLoaded, runtime.ynabHTTPLoaded = true, true
	request.ServiceCandidates = []ServiceCandidate{
		{Server: "obsidian-http", LaunchAgentLabel: "com.ericfeunekes.personal-mcp-gateway.obsidian-http",
			PlistPath: write("obsidian-http.plist", "plist"), WrapperPath: write("run-obsidian-http.sh", "wrapper"),
			StdoutPath: filepath.Join(root, "obsidian-http.out"), StderrPath: filepath.Join(root, "obsidian-http.err"),
			EnvironmentPath: write("obsidian-http.env", gatewayBinding), HealthURLFile: filepath.Join(root, "obsidian-http.health")},
		{Server: "ynab-http", LaunchAgentLabel: "com.ericfeunekes.personal-mcp-gateway.ynab-http",
			PlistPath: write("ynab-http.plist", "plist"), WrapperPath: write("run-ynab-http.sh", "wrapper"),
			StdoutPath: filepath.Join(root, "ynab-http.out"), StderrPath: filepath.Join(root, "ynab-http.err"),
			EnvironmentPath: write("ynab-http.env", gatewayBinding), HealthURLFile: filepath.Join(root, "ynab-http.health")},
	}
	prepared, err := manager.Prepare(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := manager.Resume(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Rollback(context.Background(), pending.ID); err != nil {
		t.Fatal(err)
	}
	want := []string{"local.test.gateway", "com.ericfeunekes.personal-mcp-gateway.obsidian-http", "com.ericfeunekes.personal-mcp-gateway.ynab-http"}
	if !reflect.DeepEqual(runtime.serviceBootouts, want) {
		t.Fatalf("bootout order = %v, want %v", runtime.serviceBootouts, want)
	}
	if prepared == nil {
		t.Fatal("prepared release disappeared")
	}
}

// TestGatewayBinParsingMatchesShellLoaderGoldenTable runs the same raw
// GATEWAY_BIN values through the real scripts/internal/release-config.sh
// shell loader and through parseGatewayBinValue (the Go parser
// validateServiceGatewayBinding uses), and checks they agree. The two must
// agree exactly whenever the shell loader accepts a value that resolves to
// an absolute path: that is the case a release's active environment file is
// actually validated against. When the shell loader's result is not an
// absolute path (for example a single-quoted "$HOME/..." literal, which bash
// never expands), the Go parser is expected to reject it even though the
// shell loader itself accepted the (unusable) literal string — Go's job is
// to confirm the binding resolves to the target path, which a non-absolute
// value can never do.
func TestGatewayBinParsingMatchesShellLoaderGoldenTable(t *testing.T) {
	configScript, err := filepath.Abs(filepath.Join("..", "..", "scripts", "internal", "release-config.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(configScript); err != nil {
		t.Fatalf("release-config.sh not found at %s: %v", configScript, err)
	}
	home := t.TempDir()

	cases := []string{
		`/absolute/gateway`,
		`relative/gateway`,
		`$HOME`,
		`$HOME/bin/gw`,
		`${HOME}/bin/gw`,
		`"$HOME/bin/gw"`,
		`'$HOME/bin/gw'`,
		`'/absolute/gateway'`,
		`"/absolute/gateway"`,
		`$OTHER/bin/gw`,
		`"$OTHER/bin/gw"`,
		`'$OTHER/bin/gw'`,
		"`touch pwned`/gw",
		`"a\b"`,
		// Single-quoted values are bash literal text: backtick, backslash,
		// and a double quote are ordinary characters inside single quotes,
		// so the shell loader accepts all three unlike inside double quotes
		// or a bare value.
		`'/absolute/gate"way'`,
		`'/absolute/gate\way'`,
		"'/absolute/gate`way'",
		// A double-quoted value allows an embedded single quote (only
		// backtick, backslash, and a double quote are disallowed).
		`"/absolute/gate'way"`,
		// A bare (unquoted) value disallows embedded whitespace and "#",
		// which the shell's line regex still captures into the raw value.
		`/absolute/gate way`,
		`/absolute/gate#way`,
		// ${HOME}/ prefix expansion inside each quoting mode: double-quoted
		// expands like bare; single-quoted stays literal (never absolute).
		`"${HOME}/bin/gw"`,
		`'${HOME}/bin/gw'`,
	}

	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			dir := t.TempDir()
			envPath := filepath.Join(dir, "env")
			if err := os.WriteFile(envPath, []byte("GATEWAY_BIN="+raw+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			shellValue, shellOK := runShellGatewayBinLoader(t, configScript, home, envPath)
			shellAbs := shellOK && filepath.IsAbs(shellValue)

			goValue, goOK := parseGatewayBinValue(raw, home)

			if goOK != shellAbs {
				t.Fatalf("accept mismatch: shell ok=%v value=%q (abs=%v); go ok=%v value=%q",
					shellOK, shellValue, shellAbs, goOK, goValue)
			}
			if goOK && goValue != filepath.Clean(shellValue) {
				t.Fatalf("value mismatch: shell=%q go=%q", shellValue, goValue)
			}
		})
	}
}

// runShellGatewayBinLoader sources the real release-config.sh and runs
// load_release_config against envPath with HOME=home, exactly as the
// wrapper scripts do, returning the resulting GATEWAY_BIN value.
func runShellGatewayBinLoader(t *testing.T, configScript, home, envPath string) (value string, ok bool) {
	t.Helper()
	script := `
source "$1"
HOME="$2" load_release_config "$3" || exit 1
printf '%s' "$GATEWAY_BIN"
`
	cmd := exec.Command("bash", "-c", script, "bash", configScript, home, envPath)
	output, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return string(output), true
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
