package releaseactivation

import (
	"context"
	"errors"
	"os"
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
		prepared.Version != 2 || prepared.Commit != request.Commit || prepared.CandidateSHA256 != request.CandidateSHA256 ||
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

	got, err := manager.Resume(ctx, prepared.ID)
	if SanitizedError(err).Code != ErrorRecoveryUnconfirmed || got == nil {
		t.Fatalf("resume = %#v, %v; want sanitized deployment failure", got, err)
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
	installedPresent bool
	installedHash    string
	ready            bool
	unloaded         bool
	runtimeDrift     bool
	waitFailures     int
	confirmOverride  func() (bool, error)
	calls            []string
	observeCalls     int
	afterObserve     func()
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

func (f *fakeManagerRuntime) InstallCandidate(_ context.Context, m Manifest, _ RuntimeArtifacts) error {
	f.calls = append(f.calls, "install")
	f.installedPresent, f.installedHash, f.ready, f.unloaded = true, m.CandidateSHA256, false, false
	return nil
}
func (f *fakeManagerRuntime) Restart(context.Context, Manifest) error {
	f.calls = append(f.calls, "restart")
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
func (f *fakeManagerRuntime) Bootout(context.Context, Manifest) error {
	f.calls = append(f.calls, "bootout")
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

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
