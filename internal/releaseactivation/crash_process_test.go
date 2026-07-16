package releaseactivation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const crashHelperEnvironment = "PERSONAL_MCP_GATEWAY_RELEASE_CRASH_HELPER"

// TestReleaseActivationCrashHelperProcess is the only subprocess entry point.
// Every fixture path is explicit and synthetic; production store discovery and
// production-only fault flags are never involved.
func TestReleaseActivationCrashHelperProcess(t *testing.T) {
	if os.Getenv(crashHelperEnvironment) != "1" {
		return
	}
	args, err := crashHelperArgs(os.Args)
	if err == nil {
		err = runCrashHelper(args)
	}
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}

func TestManagerProcessLockReleasesOnSIGKILL(t *testing.T) {
	fixture := newCrashFixture(t, false)
	helper := startCrashHelper(t, fixture.configPath, "hold", "hold")

	store, err := NewStoreAt(fixture.StateRoot, fixture.EffectiveUID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Acquire(); !errors.Is(err, ErrBusy) {
		t.Fatalf("contending Acquire error = %v, want ErrBusy", err)
	}
	if manifest, err := store.Inspect(); err != nil || manifest != nil {
		t.Fatalf("contender changed clear state: %#v, %v", manifest, err)
	}

	helper.killAndWait(t)
	locked, err := store.Acquire()
	if err != nil {
		t.Fatalf("Acquire after SIGKILL: %v", err)
	}
	if err := locked.Close(); err != nil {
		t.Fatalf("close recovered lock: %v", err)
	}
}

func TestManagerCrashMatrixPublishAndManifestRewrites(t *testing.T) {
	t.Run("publish active.next to active", func(t *testing.T) {
		points := []struct {
			checkpoint    string
			activeVisible bool
		}{
			{"before_manifest_temp_write", false},
			{"after_manifest_temp_write", false},
			{"before_manifest_temp_sync", false},
			{"after_manifest_temp_sync", false},
			{"before_manifest_publish", false},
			{"after_manifest_publish", false},
			{"before_manifest_dir_sync", false},
			{"after_manifest_dir_sync", false},
			{"before_active_publish", false},
			{"after_active_publish", true},
			{"before_active_parent_sync", true},
			{"after_active_parent_sync", true},
		}
		for _, point := range points {
			t.Run(point.checkpoint, func(t *testing.T) {
				fixture := newCrashFixture(t, false)
				helper := startCrashHelper(t, fixture.configPath, "prepare", point.checkpoint)
				helper.killAndWait(t)

				manifest := fixture.inspect(t)
				if !point.activeVisible {
					if manifest != nil {
						t.Fatalf("pre-active-publish crash exposed manifest %#v", manifest)
					}
					if fixture.targetPresent(t) {
						t.Fatal("pre-active-publish crash mutated target")
					}
					manifest = fixture.prepare(t)
				} else {
					requireState(t, manifest, StatePrepared)
				}
				pending := fixture.resume(t, manifest.ID)
				requireState(t, pending, StatePending)
				fixture.rollbackToClear(t, pending.ID)
			})
		}
	})

	t.Run("deployment_ready rewrite to pending", func(t *testing.T) {
		for _, point := range manifestRewriteCrashPoints() {
			t.Run(point.checkpoint, func(t *testing.T) {
				fixture := newCrashFixture(t, false)
				prepared := fixture.prepare(t)
				helper := startCrashHelper(t, fixture.configPath, "resume", point.checkpoint)
				helper.killAndWait(t)

				manifest := fixture.inspect(t)
				if !point.newVisible {
					requireState(t, manifest, StatePrepared)
					manifest = fixture.resume(t, prepared.ID)
				}
				requireState(t, manifest, StatePending)
				if manifest.ID != prepared.ID {
					t.Fatalf("pending ID = %s, want %s", manifest.ID, prepared.ID)
				}
				fixture.rollbackToClear(t, manifest.ID)
			})
		}
	})

	t.Run("pending rewrite to accepting", func(t *testing.T) {
		for _, point := range manifestRewriteCrashPoints() {
			t.Run(point.checkpoint, func(t *testing.T) {
				fixture := newCrashFixture(t, true)
				pending := fixture.pending(t)
				helper := startCrashHelper(t, fixture.configPath, "accept", point.checkpoint)
				helper.killAndWait(t)

				manifest := fixture.inspect(t)
				if !point.newVisible {
					requireState(t, manifest, StatePending)
				} else {
					requireState(t, manifest, StateAccepting)
					fixture.requireConflict(t, "rollback", pending.ID)
				}
				fixture.acceptToClear(t, pending.ID)
			})
		}
	})

	t.Run("pending rewrite to rolling_back", func(t *testing.T) {
		for _, point := range manifestRewriteCrashPoints() {
			t.Run(point.checkpoint, func(t *testing.T) {
				fixture := newCrashFixture(t, true)
				pending := fixture.pending(t)
				helper := startCrashHelper(t, fixture.configPath, "rollback", point.checkpoint)
				helper.killAndWait(t)

				manifest := fixture.inspect(t)
				if !point.newVisible {
					requireState(t, manifest, StatePending)
				} else {
					requireState(t, manifest, StateRollingBack)
					fixture.requireConflict(t, "accept", pending.ID)
				}
				fixture.rollbackToClear(t, pending.ID)
			})
		}
	})
}

type manifestCrashPoint struct {
	checkpoint string
	newVisible bool
}

func manifestRewriteCrashPoints() []manifestCrashPoint {
	return []manifestCrashPoint{
		{"before_manifest_temp_write", false},
		{"after_manifest_temp_write", false},
		{"before_manifest_temp_sync", false},
		{"after_manifest_temp_sync", false},
		{"before_manifest_publish", false},
		{"after_manifest_publish", true},
		{"before_manifest_dir_sync", true},
		{"after_manifest_dir_sync", true},
	}
}

func TestManagerCrashMatrixDeploymentEffects(t *testing.T) {
	tests := []struct {
		name       string
		operation  string
		checkpoint string
		assert     func(*testing.T, *crashFixture, string, *Manifest)
	}{
		{
			name: "candidate install and target sync", operation: "install", checkpoint: "install",
			assert: func(t *testing.T, fixture *crashFixture, phase string, manifest *Manifest) {
				if phase == "before" {
					if fixture.targetPresent(t) {
						t.Fatal("candidate existed before install boundary")
					}
					return
				}
				fixture.requireTargetHash(t, manifest.CandidateSHA256)
			},
		},
		{
			name: "candidate restart returns", operation: "restart", checkpoint: "restart",
			assert: func(t *testing.T, fixture *crashFixture, phase string, manifest *Manifest) {
				fixture.requireTargetHash(t, manifest.CandidateSHA256)
				want := 0
				if phase == "after" {
					want = 1
				}
				if got := fixture.effectCount(t, "restart"); got != want {
					t.Fatalf("restart count = %d, want %d", got, want)
				}
			},
		},
		{
			name: "WaitReady receipt before deployment_ready", operation: "wait_ready", checkpoint: "wait_ready",
			assert: func(t *testing.T, fixture *crashFixture, phase string, _ *Manifest) {
				wantReady, wantCount := false, 0
				if phase == "after" {
					wantReady, wantCount = true, 1
				}
				if got := fixture.markerPresent(t, "ready"); got != wantReady {
					t.Fatalf("ready marker = %v, want %v", got, wantReady)
				}
				if got := fixture.effectCount(t, "wait_ready"); got != wantCount {
					t.Fatalf("WaitReady count = %d, want %d", got, wantCount)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, phase := range []string{"before", "after"} {
				t.Run(phase, func(t *testing.T) {
					fixture := newCrashFixture(t, false)
					prepared := fixture.prepare(t)
					helper := startCrashHelper(t, fixture.configPath, "resume", phase+"_"+test.checkpoint)
					helper.killAndWait(t)

					manifest := fixture.inspect(t)
					requireState(t, manifest, StatePrepared)
					if manifest.ID != prepared.ID {
						t.Fatalf("crash changed release ID: got %s want %s", manifest.ID, prepared.ID)
					}
					test.assert(t, fixture, phase, manifest)
					pending := fixture.resume(t, prepared.ID)
					requireState(t, pending, StatePending)
					fixture.rollbackToClear(t, prepared.ID)
				})
			}
		})
	}
}

func TestManagerCrashMatrixRollbackEffects(t *testing.T) {
	t.Run("first-install bootout and unloaded readback", func(t *testing.T) {
		for _, effect := range []string{"bootout", "confirm_unloaded"} {
			for _, phase := range []string{"before", "after"} {
				t.Run(effect+"_"+phase, func(t *testing.T) {
					fixture := newCrashFixture(t, false)
					pending := fixture.pending(t)
					helper := startCrashHelper(t, fixture.configPath, "rollback", phase+"_"+effect)
					helper.killAndWait(t)

					requireState(t, fixture.inspect(t), StateRollingBack)
					wantUnloaded := effect == "confirm_unloaded" || phase == "after"
					if got := fixture.markerPresent(t, "unloaded"); got != wantUnloaded {
						t.Fatalf("unloaded supervisor = %v, want %v", got, wantUnloaded)
					}
					wantConfirmCount := 0
					if effect == "confirm_unloaded" && phase == "after" {
						wantConfirmCount = 1
					}
					if got := fixture.effectCount(t, "confirm_unloaded"); got != wantConfirmCount {
						t.Fatalf("unloaded readback count = %d, want %d", got, wantConfirmCount)
					}
					if !fixture.targetPresent(t) {
						t.Fatal("target removed before unloaded readback completed")
					}
					fixture.rollbackToClear(t, pending.ID)
					if fixture.targetPresent(t) {
						t.Fatal("first-install rollback retained candidate target")
					}
				})
			}
		}
	})

	t.Run("first-install target removal and parent sync", func(t *testing.T) {
		for _, phase := range []string{"before", "after"} {
			t.Run(phase, func(t *testing.T) {
				fixture := newCrashFixture(t, false)
				pending := fixture.pending(t)
				helper := startCrashHelper(t, fixture.configPath, "rollback", phase+"_remove")
				helper.killAndWait(t)

				requireState(t, fixture.inspect(t), StateRollingBack)
				wantPresent := phase == "before"
				if got := fixture.targetPresent(t); got != wantPresent {
					t.Fatalf("target present = %v, want %v", got, wantPresent)
				}
				if !fixture.markerPresent(t, "unloaded") {
					t.Fatal("target-removal boundary reached before unloaded readback")
				}
				fixture.rollbackToClear(t, pending.ID)
			})
		}
	})

	t.Run("previous target restore and parent sync", func(t *testing.T) {
		for _, phase := range []string{"before", "after"} {
			t.Run(phase, func(t *testing.T) {
				fixture := newCrashFixture(t, true)
				pending := fixture.pending(t)
				helper := startCrashHelper(t, fixture.configPath, "rollback", phase+"_restore")
				helper.killAndWait(t)

				requireState(t, fixture.inspect(t), StateRollingBack)
				wantHash := pending.CandidateSHA256
				if phase == "after" {
					wantHash = pending.PreviousSHA256
				}
				fixture.requireTargetHash(t, wantHash)
				fixture.rollbackToClear(t, pending.ID)
				fixture.requireTargetHash(t, pending.PreviousSHA256)
			})
		}
	})

	t.Run("previous restart and readiness receipt", func(t *testing.T) {
		for _, effect := range []string{"restart", "wait_ready"} {
			for _, phase := range []string{"before", "after"} {
				t.Run(effect+"_"+phase, func(t *testing.T) {
					fixture := newCrashFixture(t, true)
					pending := fixture.pending(t)
					baselineRestart := fixture.effectCount(t, "restart")
					baselineReady := fixture.effectCount(t, "wait_ready")
					helper := startCrashHelper(t, fixture.configPath, "rollback", phase+"_"+effect)
					helper.killAndWait(t)

					requireState(t, fixture.inspect(t), StateRollingBack)
					fixture.requireTargetHash(t, pending.PreviousSHA256)
					wantRestartCount := baselineRestart
					if effect == "wait_ready" || phase == "after" {
						wantRestartCount++
					}
					if got := fixture.effectCount(t, "restart"); got != wantRestartCount {
						t.Fatalf("rollback restart count = %d, want %d", got, wantRestartCount)
					}
					wantReadyCount := baselineReady
					if effect == "wait_ready" && phase == "after" {
						wantReadyCount++
					}
					if got := fixture.effectCount(t, "wait_ready"); got != wantReadyCount {
						t.Fatalf("rollback WaitReady count = %d, want %d", got, wantReadyCount)
					}
					fixture.rollbackToClear(t, pending.ID)
					if got := fixture.effectCount(t, "restart"); got <= wantRestartCount {
						t.Fatalf("crash-lost rollback receipt did not repeat restart: count=%d crash=%d", got, wantRestartCount)
					}
					if got := fixture.effectCount(t, "wait_ready"); got <= wantReadyCount {
						t.Fatalf("crash-lost rollback receipt did not repeat WaitReady: count=%d crash=%d", got, wantReadyCount)
					}
				})
			}
		}
	})
}

func TestManagerCrashMatrixClearAndCleanup(t *testing.T) {
	t.Run("active to cleanup clear commit", func(t *testing.T) {
		points := []struct {
			checkpoint     string
			clearVisible   bool
			cleanupPresent bool
		}{
			{"before_clear_publish", false, false},
			{"after_clear_publish", true, true},
			{"before_clear_parent_sync", true, true},
			{"after_clear_parent_sync", true, true},
			{"before_clear_cleanup", true, true},
			{"after_clear_cleanup", true, false},
		}
		for _, point := range points {
			t.Run(point.checkpoint, func(t *testing.T) {
				fixture := newCrashFixture(t, true)
				pending := fixture.pending(t)
				helper := startCrashHelper(t, fixture.configPath, "accept", point.checkpoint)
				helper.killAndWait(t)

				if !point.clearVisible {
					requireState(t, fixture.inspect(t), StateAccepting)
					fixture.acceptToClear(t, pending.ID)
				} else {
					if got := fixture.inspect(t); got != nil {
						t.Fatalf("post-clear crash retained active transaction %#v", got)
					}
					if got := fixture.cleanupPresent(t, pending.ID); got != point.cleanupPresent {
						t.Fatalf("cleanup orphan present = %v, want %v", got, point.cleanupPresent)
					}
				}
				fixture.requireTargetHash(t, pending.CandidateSHA256)
			})
		}
	})

	t.Run("cleanup orphan deletion", func(t *testing.T) {
		for _, phase := range []string{"before", "after"} {
			t.Run(phase, func(t *testing.T) {
				fixture := newCrashFixture(t, true)
				pending := fixture.pending(t)
				clearer := startCrashHelper(t, fixture.configPath, "accept", "before_clear_cleanup")
				clearer.killAndWait(t)
				if !fixture.cleanupPresent(t, pending.ID) {
					t.Fatal("setup did not leave cleanup orphan")
				}

				cleaner := startCrashHelper(t, fixture.configPath, "with_clear", phase+"_orphan_cleanup")
				cleaner.killAndWait(t)
				wantPresent := phase == "before"
				if got := fixture.cleanupPresent(t, pending.ID); got != wantPresent {
					t.Fatalf("cleanup orphan present = %v, want %v", got, wantPresent)
				}
				if phase == "before" {
					if err := fixture.manager(false).WithClear(context.Background(), func(context.Context, Runtime) error { return nil }); err != nil {
						t.Fatalf("resume cleanup: %v", err)
					}
				}
				if fixture.cleanupPresent(t, pending.ID) {
					t.Fatal("cleanup orphan remained after reconciliation")
				}
				fixture.requireTargetHash(t, pending.CandidateSHA256)
			})
		}
	})
}

func TestManagerTerminalDirectionProcessRaces(t *testing.T) {
	tests := []struct {
		name      string
		winner    string
		loser     string
		wantState State
	}{
		{name: "accept wins", winner: "accept", loser: "rollback", wantState: StateAccepting},
		{name: "rollback wins", winner: "rollback", loser: "accept", wantState: StateRollingBack},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCrashFixture(t, true)
			pending := fixture.pending(t)
			winner := startCrashHelper(t, fixture.configPath, test.winner, "after_manifest_publish")

			manager := fixture.manager(false)
			var err error
			if test.loser == "accept" {
				_, err = manager.Accept(context.Background(), pending.ID)
			} else {
				_, err = manager.Rollback(context.Background(), pending.ID)
			}
			if got := SanitizedError(err); got == nil || got.Code != ErrorBusy {
				t.Fatalf("contender error = %v, want busy", err)
			}

			winner.killAndWait(t)
			requireState(t, fixture.inspect(t), test.wantState)
			fixture.requireConflict(t, test.loser, pending.ID)
			if test.winner == "accept" {
				fixture.acceptToClear(t, pending.ID)
				fixture.requireTargetHash(t, pending.CandidateSHA256)
			} else {
				fixture.rollbackToClear(t, pending.ID)
				fixture.requireTargetHash(t, pending.PreviousSHA256)
			}
		})
	}
}

type crashFixture struct {
	StateRoot        string         `json:"state_root"`
	EffectsRoot      string         `json:"effects_root"`
	ControllerSource string         `json:"controller_source"`
	EffectiveUID     int            `json:"effective_uid"`
	Request          PrepareRequest `json:"request"`
	configPath       string
}

func newCrashFixture(t *testing.T, previous bool) *crashFixture {
	t.Helper()
	root := t.TempDir()
	write := func(name, contents string, mode os.FileMode) string {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(contents), mode); err != nil {
			t.Fatal(err)
		}
		return path
	}
	effects := filepath.Join(root, "effects")
	if err := os.Mkdir(effects, 0o700); err != nil {
		t.Fatal(err)
	}
	controller := write("controller", "synthetic controller", 0o700)
	target := filepath.Join(root, "gateway")
	healthURLFile := filepath.Join(root, "health-url")
	if previous {
		if err := os.WriteFile(target, []byte("synthetic previous gateway"), 0o700); err != nil {
			t.Fatal(err)
		}
		writeTestMarker(t, filepath.Join(effects, "ready"))
		if err := os.WriteFile(healthURLFile, []byte("http://127.0.0.1:1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	} else {
		writeTestMarker(t, filepath.Join(effects, "unloaded"))
	}
	fixture := &crashFixture{
		StateRoot:        filepath.Join(root, "state"),
		EffectsRoot:      effects,
		ControllerSource: controller,
		EffectiveUID:     os.Geteuid(),
		Request: PrepareRequest{
			Commit: strings.Repeat("1", 40), DependencySHA256: strings.Repeat("9", 64),
			CandidatePath: write("candidate", "synthetic candidate gateway", 0o700),
			AuthorityPath: controller, TargetPath: target, EffectiveUID: os.Geteuid(),
			LaunchAgentLabel: "local.test.release-crash", PlistPath: write("launch.plist", "plist", 0o600),
			WrapperPath: write("wrapper", "wrapper", 0o700), MCPWrapperPath: write("mcp-wrapper", "mcp wrapper", 0o700),
			StdoutPath: filepath.Join(root, "stdout.log"), StderrPath: filepath.Join(root, "stderr.log"),
			EnvironmentPath: write("environment", "synthetic configuration", 0o600),
			HealthURLFile:   healthURLFile, ReadyTimeoutSeconds: 10, ReadyPollMilliseconds: 100,
		},
	}
	candidateSHA256, err := HashRegular(fixture.Request.CandidatePath)
	if err != nil {
		t.Fatal(err)
	}
	fixture.Request.CandidateSHA256 = candidateSHA256
	authoritySHA256, err := HashRegular(fixture.Request.AuthorityPath)
	if err != nil {
		t.Fatal(err)
	}
	fixture.Request.AuthoritySHA256 = authoritySHA256
	fixture.configPath = filepath.Join(root, "fixture.json")
	data, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (f *crashFixture) manager(preparing bool) *Manager {
	store, err := NewStoreAt(f.StateRoot, f.EffectiveUID)
	if err != nil {
		panic(err)
	}
	controller := store.ActiveAuthorityPath()
	if preparing {
		controller = f.ControllerSource
	}
	return &Manager{Store: store, Runtime: &crashRuntime{fixture: f}, ControllerPath: controller, ControllerSHA256: f.Request.AuthoritySHA256}
}

func (f *crashFixture) prepare(t *testing.T) *Manifest {
	t.Helper()
	manifest, err := f.manager(true).Prepare(context.Background(), f.Request)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	requireState(t, manifest, StatePrepared)
	return manifest
}

func (f *crashFixture) resume(t *testing.T, id ReleaseID) *Manifest {
	t.Helper()
	manifest, err := f.manager(false).Resume(context.Background(), id)
	if err != nil {
		t.Fatalf("resume %s: %v", id, err)
	}
	return manifest
}

func (f *crashFixture) pending(t *testing.T) *Manifest {
	t.Helper()
	prepared := f.prepare(t)
	pending := f.resume(t, prepared.ID)
	requireState(t, pending, StatePending)
	return pending
}

func (f *crashFixture) inspect(t *testing.T) *Manifest {
	t.Helper()
	store, err := NewStoreAt(f.StateRoot, f.EffectiveUID)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := store.Inspect()
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	return manifest
}

func (f *crashFixture) acceptToClear(t *testing.T, id ReleaseID) {
	t.Helper()
	manifest, err := f.manager(false).Accept(context.Background(), id)
	if err != nil || manifest != nil {
		t.Fatalf("accept = %#v, %v; want clear", manifest, err)
	}
	if got := f.inspect(t); got != nil {
		t.Fatalf("accept retained active state %#v", got)
	}
}

func (f *crashFixture) rollbackToClear(t *testing.T, id ReleaseID) {
	t.Helper()
	manifest, err := f.manager(false).Rollback(context.Background(), id)
	if err != nil || manifest != nil {
		t.Fatalf("rollback = %#v, %v; want clear", manifest, err)
	}
	if got := f.inspect(t); got != nil {
		t.Fatalf("rollback retained active state %#v", got)
	}
}

func (f *crashFixture) requireConflict(t *testing.T, operation string, id ReleaseID) {
	t.Helper()
	manager := f.manager(false)
	var err error
	if operation == "accept" {
		_, err = manager.Accept(context.Background(), id)
	} else {
		_, err = manager.Rollback(context.Background(), id)
	}
	if got := SanitizedError(err); got == nil || got.Code != ErrorStateConflict {
		t.Fatalf("opposite %s error = %v, want state_conflict", operation, err)
	}
}

func (f *crashFixture) targetPresent(t *testing.T) bool {
	t.Helper()
	_, err := os.Lstat(f.Request.TargetPath)
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	return true
}

func (f *crashFixture) requireTargetHash(t *testing.T, want string) {
	t.Helper()
	got, err := HashRegular(f.Request.TargetPath)
	if err != nil {
		t.Fatalf("hash target: %v", err)
	}
	if got != want {
		t.Fatalf("target hash = %s, want %s", got, want)
	}
}

func (f *crashFixture) markerPresent(t *testing.T, name string) bool {
	t.Helper()
	_, err := os.Lstat(filepath.Join(f.EffectsRoot, name))
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	return true
}

func (f *crashFixture) effectCount(t *testing.T, name string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.EffectsRoot, name+".count"))
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	count, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("parse %s count: %v", name, err)
	}
	return count
}

func (f *crashFixture) cleanupPresent(t *testing.T, id ReleaseID) bool {
	t.Helper()
	_, err := os.Lstat(filepath.Join(f.StateRoot, "cleanup."+string(id)))
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	return true
}

func requireState(t *testing.T, manifest *Manifest, want State) {
	t.Helper()
	if manifest == nil || manifest.State != want {
		t.Fatalf("manifest = %#v, want state %s", manifest, want)
	}
}

func writeTestMarker(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

type crashRuntime struct {
	fixture *crashFixture
	barrier *crashBarrier
}

func (r *crashRuntime) Observe(ctx context.Context, manifest Manifest, controller string, artifacts RuntimeArtifacts) (Observed, error) {
	return r.osRuntime().Observe(ctx, manifest, controller, artifacts)
}

func (r *crashRuntime) InstallCandidate(ctx context.Context, manifest Manifest, artifacts RuntimeArtifacts) error {
	if err := r.hit("before_install"); err != nil {
		return err
	}
	// Cover the production copy, hash check, rename, parent sync, and installed
	// readback. Only launchctl and readiness are deterministic test doubles.
	if err := r.osRuntime().InstallCandidate(ctx, manifest, artifacts); err != nil {
		return err
	}
	if err := r.record("install"); err != nil {
		return err
	}
	return r.hit("after_install")
}

func (r *crashRuntime) Restart(ctx context.Context, manifest Manifest) error {
	if err := r.hit("before_restart"); err != nil {
		return err
	}
	if err := r.osRuntime().Restart(ctx, manifest); err != nil {
		return err
	}
	if err := r.record("restart"); err != nil {
		return err
	}
	return r.hit("after_restart")
}

func (r *crashRuntime) WaitReady(ctx context.Context, manifest Manifest) error {
	if err := r.hit("before_wait_ready"); err != nil {
		return err
	}
	if err := r.osRuntime().WaitReady(ctx, manifest); err != nil {
		return err
	}
	if err := r.writeMarker("ready"); err != nil {
		return err
	}
	if err := r.record("wait_ready"); err != nil {
		return err
	}
	return r.hit("after_wait_ready")
}

func (r *crashRuntime) RestorePrevious(ctx context.Context, manifest Manifest, artifacts RuntimeArtifacts) error {
	if err := r.hit("before_restore"); err != nil {
		return err
	}
	if err := r.osRuntime().RestorePrevious(ctx, manifest, artifacts); err != nil {
		return err
	}
	if err := r.removeMarker("ready"); err != nil {
		return err
	}
	if err := r.record("restore"); err != nil {
		return err
	}
	return r.hit("after_restore")
}

func (r *crashRuntime) Bootout(ctx context.Context, manifest Manifest) error {
	if err := r.hit("before_bootout"); err != nil {
		return err
	}
	if err := r.osRuntime().Bootout(ctx, manifest); err != nil {
		return err
	}
	if err := r.record("bootout"); err != nil {
		return err
	}
	return r.hit("after_bootout")
}

func (r *crashRuntime) ConfirmUnloaded(ctx context.Context, manifest Manifest) (bool, error) {
	if err := r.hit("before_confirm_unloaded"); err != nil {
		return false, err
	}
	unloaded, err := r.osRuntime().ConfirmUnloaded(ctx, manifest)
	if err != nil {
		return false, err
	}
	if err := r.record("confirm_unloaded"); err != nil {
		return false, err
	}
	if err := r.hit("after_confirm_unloaded"); err != nil {
		return false, err
	}
	return unloaded, nil
}

func (r *crashRuntime) RemoveTarget(ctx context.Context, manifest Manifest) error {
	if err := r.hit("before_remove"); err != nil {
		return err
	}
	if err := r.osRuntime().RemoveTarget(ctx, manifest); err != nil {
		return err
	}
	if err := r.record("remove"); err != nil {
		return err
	}
	return r.hit("after_remove")
}

func (*crashRuntime) InvokeInstallAdapter(context.Context, string, ...string) error   { return nil }
func (*crashRuntime) InvokeUninstallAdapter(context.Context, string, ...string) error { return nil }

func (r *crashRuntime) osRuntime() *OSRuntime {
	return &OSRuntime{
		Runner:     crashLaunchctl{runtime: r},
		HTTPClient: crashHTTPClient{},
		Sleep:      contextSleep,
	}
}

type crashLaunchctl struct{ runtime *crashRuntime }

func (r crashLaunchctl) Run(ctx context.Context, name string, args ...string) (CommandResult, error) {
	if err := ctx.Err(); err != nil {
		return CommandResult{}, err
	}
	if name != "launchctl" || r.runtime == nil || r.runtime.fixture == nil {
		return CommandResult{}, fmt.Errorf("unexpected crash runtime command %q", name)
	}
	request := r.runtime.fixture.Request
	service := "gui/" + strconv.Itoa(request.EffectiveUID) + "/" + request.LaunchAgentLabel
	domain := "gui/" + strconv.Itoa(request.EffectiveUID)
	switch {
	case len(args) == 2 && args[0] == "print" && args[1] == service:
		if r.runtime.marker("unloaded") {
			return CommandResult{ExitCode: launchctlNotFound}, nil
		}
		return CommandResult{Stdout: []byte("program = " + r.runtime.fixture.Request.WrapperPath + "\n")}, nil
	case len(args) == 3 && args[0] == "kickstart" && args[1] == "-k" && args[2] == service:
		if err := r.runtime.removeMarker("unloaded"); err != nil {
			return CommandResult{}, err
		}
		if err := r.runtime.removeMarker("ready"); err != nil {
			return CommandResult{}, err
		}
		if err := r.runtime.writeSynced(r.runtime.fixture.Request.HealthURLFile, "http://127.0.0.1:1\n"); err != nil {
			return CommandResult{}, err
		}
		return CommandResult{}, nil
	case len(args) == 3 && args[0] == "bootout" && args[1] == domain && args[2] == request.PlistPath:
		if err := r.runtime.removeMarker("ready"); err != nil {
			return CommandResult{}, err
		}
		if err := r.runtime.writeMarker("unloaded"); err != nil {
			return CommandResult{}, err
		}
		if err := os.Remove(r.runtime.fixture.Request.HealthURLFile); err != nil && !errors.Is(err, os.ErrNotExist) {
			return CommandResult{}, err
		}
		return CommandResult{}, nil
	default:
		return CommandResult{}, fmt.Errorf("unexpected launchctl arguments %q", args)
	}
}

type crashHTTPClient struct{}

func (crashHTTPClient) Do(request *http.Request) (*http.Response, error) {
	if request == nil || request.URL.Host != "127.0.0.1:1" ||
		(request.URL.Path != "/healthz" && request.URL.Path != "/readyz") {
		return nil, errors.New("unexpected crash readiness request")
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("ok")),
	}, nil
}

func (r *crashRuntime) marker(name string) bool {
	_, err := os.Lstat(filepath.Join(r.fixture.EffectsRoot, name))
	return err == nil
}

func (r *crashRuntime) writeMarker(name string) error {
	return r.writeSynced(filepath.Join(r.fixture.EffectsRoot, name), "1\n")
}

func (r *crashRuntime) writeSynced(path, contents string) error {
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	err = file.Sync()
	closeErr := file.Close()
	return errors.Join(err, closeErr)
}

func (r *crashRuntime) removeMarker(name string) error {
	err := os.Remove(filepath.Join(r.fixture.EffectsRoot, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (r *crashRuntime) record(name string) error {
	path := filepath.Join(r.fixture.EffectsRoot, name+".count")
	count := 0
	data, err := os.ReadFile(path)
	if err == nil {
		count, err = strconv.Atoi(strings.TrimSpace(string(data)))
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmp := path + ".next"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(count+1)+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return syncDir(r.fixture.EffectsRoot)
}

func (r *crashRuntime) hit(name string) error {
	if r.barrier == nil {
		return nil
	}
	return r.barrier.hit(name)
}

type crashBarrier struct {
	want    string
	ready   *os.File
	hold    *os.File
	hitOnce bool
}

func newCrashBarrier(want string) (*crashBarrier, error) {
	ready := os.NewFile(uintptr(3), "crash-ready")
	hold := os.NewFile(uintptr(4), "crash-hold")
	if ready == nil || hold == nil {
		return nil, errors.New("missing inherited crash synchronization pipes")
	}
	return &crashBarrier{want: want, ready: ready, hold: hold}, nil
}

func (b *crashBarrier) hit(name string) error {
	if b == nil || b.hitOnce || name != b.want {
		return nil
	}
	b.hitOnce = true
	if _, err := b.ready.Write([]byte{1}); err != nil {
		return fmt.Errorf("signal crash boundary %s: %w", name, err)
	}
	if err := b.ready.Close(); err != nil {
		return fmt.Errorf("close crash boundary signal: %w", err)
	}
	_, err := io.Copy(io.Discard, b.hold)
	if err != nil {
		return fmt.Errorf("hold crash boundary %s: %w", name, err)
	}
	// Every parent-side row terminates the helper with SIGKILL. If the inherited
	// hold pipe closes early, stay parked instead of allowing a post-commit hook
	// warning to be ignored and the helper to exit successfully before Wait.
	select {}
}

func runCrashHelper(args []string) error {
	if len(args) != 3 {
		return fmt.Errorf("helper requires fixture, action, and checkpoint; got %d arguments", len(args))
	}
	data, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}
	var fixture crashFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		return err
	}
	barrier, err := newCrashBarrier(args[2])
	if err != nil {
		return err
	}
	store, err := NewStoreAtWithHook(fixture.StateRoot, fixture.EffectiveUID, func(point StoreHookPoint) error {
		return barrier.hit(string(point))
	})
	if err != nil {
		return err
	}
	runtime := &crashRuntime{fixture: &fixture, barrier: barrier}
	controller := store.ActiveAuthorityPath()
	if args[1] == "prepare" {
		controller = fixture.ControllerSource
	}
	manager := &Manager{Store: store, Runtime: runtime, ControllerPath: controller, ControllerSHA256: fixture.Request.AuthoritySHA256}

	if args[1] == "hold" {
		locked, err := store.Acquire()
		if err != nil {
			return err
		}
		defer locked.Close()
		return barrier.hit("hold")
	}

	var operationErr error
	switch args[1] {
	case "prepare":
		_, operationErr = manager.Prepare(context.Background(), fixture.Request)
	case "resume":
		manifest, err := store.Inspect()
		if err != nil || manifest == nil {
			return fmt.Errorf("resume helper inspect: manifest=%v error=%w", manifest, err)
		}
		_, operationErr = manager.Resume(context.Background(), manifest.ID)
	case "accept":
		manifest, err := store.Inspect()
		if err != nil || manifest == nil {
			return fmt.Errorf("accept helper inspect: manifest=%v error=%w", manifest, err)
		}
		_, operationErr = manager.Accept(context.Background(), manifest.ID)
	case "rollback":
		manifest, err := store.Inspect()
		if err != nil || manifest == nil {
			return fmt.Errorf("rollback helper inspect: manifest=%v error=%w", manifest, err)
		}
		_, operationErr = manager.Rollback(context.Background(), manifest.ID)
	case "with_clear":
		operationErr = manager.WithClear(context.Background(), func(context.Context, Runtime) error { return nil })
	default:
		return fmt.Errorf("unknown helper action %q", args[1])
	}
	if operationErr != nil {
		return operationErr
	}
	if !barrier.hitOnce {
		return fmt.Errorf("operation %s completed without reaching checkpoint %s", args[1], args[2])
	}
	return nil
}

type crashHelperProcess struct {
	cmd    *exec.Cmd
	hold   *os.File
	stderr *bytes.Buffer
	waited bool
}

func startCrashHelper(t *testing.T, args ...string) *crashHelperProcess {
	t.Helper()
	readyRead, readyWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	holdRead, holdWrite, err := os.Pipe()
	if err != nil {
		_ = readyRead.Close()
		_ = readyWrite.Close()
		t.Fatal(err)
	}
	commandArgs := []string{"-test.run=^TestReleaseActivationCrashHelperProcess$", "--"}
	commandArgs = append(commandArgs, args...)
	cmd := exec.Command(os.Args[0], commandArgs...)
	cmd.Env = append(os.Environ(), crashHelperEnvironment+"=1")
	cmd.ExtraFiles = []*os.File{readyWrite, holdRead}
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		_ = readyRead.Close()
		_ = readyWrite.Close()
		_ = holdRead.Close()
		_ = holdWrite.Close()
		t.Fatal(err)
	}
	_ = readyWrite.Close()
	_ = holdRead.Close()

	helper := &crashHelperProcess{cmd: cmd, hold: holdWrite, stderr: stderr}
	t.Cleanup(func() {
		if helper.waited {
			return
		}
		_ = helper.cmd.Process.Kill()
		_ = helper.hold.Close()
		_ = helper.cmd.Wait()
		helper.waited = true
	})
	var ready [1]byte
	_, readErr := io.ReadFull(readyRead, ready[:])
	_ = readyRead.Close()
	if readErr != nil {
		_ = holdWrite.Close()
		waitErr := cmd.Wait()
		helper.waited = true
		t.Fatalf("crash helper failed before checkpoint: read=%v wait=%v stderr=%q", readErr, waitErr, stderr.String())
	}
	return helper
}

func (h *crashHelperProcess) killAndWait(t *testing.T) {
	t.Helper()
	if h.waited {
		t.Fatal("crash helper already waited")
	}
	if err := h.cmd.Process.Kill(); err != nil {
		t.Fatalf("SIGKILL crash helper: %v", err)
	}
	_ = h.hold.Close()
	err := h.cmd.Wait()
	h.waited = true
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("crash helper Wait = %v, want signal exit; stderr=%q", err, h.stderr.String())
	}
}

func crashHelperArgs(args []string) ([]string, error) {
	for index, arg := range args {
		if arg == "--" {
			if index+1 == len(args) {
				return nil, errors.New("missing helper arguments")
			}
			return args[index+1:], nil
		}
	}
	return nil, errors.New("missing helper argument separator")
}
