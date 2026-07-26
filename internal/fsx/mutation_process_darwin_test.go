//go:build darwin

package fsx

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const (
	mutationParentProcessVaultEnv     = "PMG_MUTATION_PARENT_TEST_VAULT"
	mutationParentProcessCandidateEnv = "PMG_MUTATION_PARENT_TEST_CANDIDATE"
	mutationParentProcessReadyEnv     = "PMG_MUTATION_PARENT_TEST_READY"
	mutationParentProcessPhaseEnv     = "PMG_MUTATION_PARENT_TEST_PHASE"
	mutationParentProcessOperationEnv = "PMG_MUTATION_PARENT_TEST_OPERATION"
	mutationPreidentityHelperEnv      = "PMG_MUTATION_PREIDENTITY_HELPER"
	mutationPreidentityReadyEnv       = "PMG_MUTATION_PREIDENTITY_READY"
	mutationPreidentityReleaseEnv     = "PMG_MUTATION_PREIDENTITY_RELEASE"
	mutationWindowHelperEnv           = "PMG_MUTATION_WINDOW_HELPER"
	mutationWindowReadyEnv            = "PMG_MUTATION_WINDOW_READY"
	mutationWindowReleaseEnv          = "PMG_MUTATION_WINDOW_RELEASE"
	mutationPosteffectHelperEnv       = "PMG_MUTATION_POSTEFFECT_HELPER"
	mutationPosteffectReadyEnv        = "PMG_MUTATION_POSTEFFECT_READY"
	mutationCommitAcceptedHelperEnv   = "PMG_MUTATION_COMMIT_ACCEPTED_HELPER"
	mutationCommitAcceptedReadyEnv    = "PMG_MUTATION_COMMIT_ACCEPTED_READY"
	mutationFailureHelperEnv          = "PMG_MUTATION_FAILURE_HELPER"
	mutationFDProbeHelperEnv          = "PMG_MUTATION_FD_PROBE_HELPER"
	mutationFDProbeReadyEnv           = "PMG_MUTATION_FD_PROBE_READY"
	mutationFDProbeNumberEnv          = "PMG_MUTATION_FD_PROBE_NUMBER"
)

func init() {
	if len(os.Args) != 2 || os.Args[1] != mutationHelperArgument {
		return
	}
	switch {
	case os.Getenv(mutationPreidentityHelperEnv) == "1":
		mutationHelperTestHooks = &mutationHelperHooks{afterStageCreation: func() {
			ready := os.Getenv(mutationPreidentityReadyEnv)
			_ = os.WriteFile(ready, []byte(strconv.Itoa(os.Getpid())), 0o600)
			for {
				if release := os.Getenv(mutationPreidentityReleaseEnv); release != "" {
					if _, err := os.Stat(release); err == nil {
						return
					}
				}
				time.Sleep(10 * time.Millisecond)
			}
		}}
	case os.Getenv(mutationWindowHelperEnv) == "1":
		mutationHelperTestHooks = &mutationHelperHooks{afterFinalRevalidation: func() {
			_ = os.WriteFile(os.Getenv(mutationWindowReadyEnv), []byte("ready"), 0o600)
			for {
				if _, err := os.Stat(os.Getenv(mutationWindowReleaseEnv)); err == nil {
					return
				}
				time.Sleep(time.Millisecond)
			}
		}}
	case os.Getenv(mutationPosteffectHelperEnv) == "1":
		mutationHelperTestHooks = &mutationHelperHooks{afterEffectBeforeTerminal: func() {
			_ = os.WriteFile(os.Getenv(mutationPosteffectReadyEnv), []byte(strconv.Itoa(os.Getpid())), 0o600)
			for {
				time.Sleep(time.Hour)
			}
		}}
	case os.Getenv(mutationCommitAcceptedHelperEnv) == "1":
		mutationHelperTestHooks = &mutationHelperHooks{afterCommitAccepted: func() {
			_ = os.WriteFile(os.Getenv(mutationCommitAcceptedReadyEnv), []byte(strconv.Itoa(os.Getpid())), 0o600)
			for {
				time.Sleep(time.Hour)
			}
		}}
	case os.Getenv(mutationFailureHelperEnv) != "":
		switch os.Getenv(mutationFailureHelperEnv) {
		case "write":
			mutationHelperTestHooks = &mutationHelperHooks{writeStage: func(*os.File, []byte) error { return unix.EIO }}
		case "rename":
			mutationHelperTestHooks = &mutationHelperHooks{renameReplace: func(int, string, int, string) error { return unix.EIO }}
		case "cleanup-transient", "cleanup-persistent":
			mutationHelperTestHooks = &mutationHelperHooks{writeStage: func(*os.File, []byte) error { return unix.EIO }}
			calls := 0
			persistent := os.Getenv(mutationFailureHelperEnv) == "cleanup-persistent"
			mutationLauncherTestHooks = &mutationLauncherHooks{cleanupUnlink: func(parentFD int, leaf string) error {
				calls++
				if persistent || calls == 1 {
					return unix.EIO
				}
				return unix.Unlinkat(parentFD, leaf, 0)
			}}
		case "enotsup":
			mutationRenameExclusive = func(int, string, int, string) error { return unix.ENOTSUP }
		case "identity-mismatch":
			mutationHelperTestHooks = &mutationHelperHooks{executableIdentity: func() (mutationRawStamp, error) {
				stamp, err := mutationRunningExecutableIdentity()
				stamp.Inode++
				return stamp, err
			}}
		default:
			return
		}
	case os.Getenv(mutationFDProbeHelperEnv) == "1":
		mutationHelperTestHooks = &mutationHelperHooks{afterStageCreation: func() {
			fd, _ := strconv.Atoi(os.Getenv(mutationFDProbeNumberEnv))
			_, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
			result := "closed"
			if err == nil {
				result = "inherited"
			}
			_ = os.WriteFile(os.Getenv(mutationFDProbeReadyEnv), []byte(result), 0o600)
		}}
	default:
		return
	}
	os.Exit(RunMutationHelper())
}

func TestMutationKilledHelperBeforeIdentityAnnouncementCleansStage(t *testing.T) {
	root := t.TempDir()
	ready := filepath.Join(t.TempDir(), "ready")
	t.Setenv(mutationPreidentityHelperEnv, "1")
	t.Setenv(mutationPreidentityReadyEnv, ready)
	mutator := mutationTestMutator(t, root, os.Args[0])
	done := make(chan error, 1)
	go func() {
		_, err := mutator.CreateFile(context.Background(), ".", "note.md", []byte("payload"))
		done <- err
	}()
	pid := 0
	waitForMutationCondition(t, 5*time.Second, func() bool {
		payload, err := os.ReadFile(ready)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(string(payload))
		return err == nil && unix.Kill(pid, 0) == nil
	})
	if err := unix.Kill(pid, unix.SIGKILL); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil || IsCode(err, CodeUncertain) {
			t.Fatalf("pre-identity helper death err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("parent did not recover from pre-identity helper death")
	}
	assertMutationNoStageOrTarget(t, root, "note.md")
}

func TestMutationAcceptedExternalWriterWindowAtSubprocessBoundary(t *testing.T) {
	root := t.TempDir()
	targetPath := filepath.Join(root, "note.md")
	if err := os.WriteFile(targetPath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	mutator := mutationTestMutator(t, root, os.Args[0])
	target, err := mutator.StatMutationTarget(context.Background(), ".", "note.md")
	if err != nil {
		t.Fatal(err)
	}
	ready := filepath.Join(t.TempDir(), "ready")
	release := filepath.Join(t.TempDir(), "release")
	t.Setenv(mutationWindowHelperEnv, "1")
	t.Setenv(mutationWindowReadyEnv, ready)
	t.Setenv(mutationWindowReleaseEnv, release)
	done := make(chan error, 1)
	go func() {
		_, err := mutator.ReplaceFile(context.Background(), ".", "note.md", target.Fingerprint, []byte("final"))
		done <- err
	}()
	waitForMutationCondition(t, 5*time.Second, func() bool {
		_, err := os.Stat(ready)
		return err == nil
	})
	if err := os.WriteFile(targetPath, []byte("external-writer"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(release, []byte("continue"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("subprocess mutation did not leave accepted window")
	}
	assertMutationFileContent(t, targetPath, "final")
	assertMutationNoStages(t, root)
}

func TestMutationCreateHostileDestinationCollisionAtExclusiveRename(t *testing.T) {
	root := t.TempDir()
	targetPath := filepath.Join(root, "note.md")
	mutator := mutationTestMutator(t, root, os.Args[0])
	ready := filepath.Join(t.TempDir(), "ready")
	release := filepath.Join(t.TempDir(), "release")
	t.Setenv(mutationWindowHelperEnv, "1")
	t.Setenv(mutationWindowReadyEnv, ready)
	t.Setenv(mutationWindowReleaseEnv, release)
	done := make(chan error, 1)
	go func() {
		_, err := mutator.CreateFile(context.Background(), ".", "note.md", []byte("gateway"))
		done <- err
	}()
	waitForMutationCondition(t, 5*time.Second, func() bool {
		_, err := os.Stat(ready)
		return err == nil
	})
	if err := os.WriteFile(targetPath, []byte("external-writer"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(release, []byte("continue"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !IsCode(err, CodeDestinationExists) {
			t.Fatalf("hostile collision err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("create collision did not resolve")
	}
	assertMutationFileContent(t, targetPath, "external-writer")
	assertMutationNoStages(t, root)
}

func TestMutationKilledHelperAfterEffectReturnsUncertainWithoutPartialBytes(t *testing.T) {
	root := t.TempDir()
	ready := filepath.Join(t.TempDir(), "ready")
	t.Setenv(mutationPosteffectHelperEnv, "1")
	t.Setenv(mutationPosteffectReadyEnv, ready)
	mutator := mutationTestMutator(t, root, os.Args[0])
	done := make(chan error, 1)
	go func() {
		_, err := mutator.CreateFile(context.Background(), ".", "note.md", []byte("final"))
		done <- err
	}()
	pid := 0
	waitForMutationCondition(t, 5*time.Second, func() bool {
		payload, err := os.ReadFile(ready)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(string(payload))
		return err == nil && unix.Kill(pid, 0) == nil
	})
	if err := unix.Kill(pid, unix.SIGKILL); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !IsCode(err, CodeUncertain) {
			t.Fatalf("post-effect helper death err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("parent did not classify post-effect helper death")
	}
	assertMutationFileContent(t, filepath.Join(root, "note.md"), "final")
	assertMutationNoStages(t, root)
}

func TestMutationReplacementKilledAfterCommitAcceptancePreservesPriorBytes(t *testing.T) {
	root := t.TempDir()
	targetPath := filepath.Join(root, "note.md")
	if err := os.WriteFile(targetPath, []byte("prior"), 0o600); err != nil {
		t.Fatal(err)
	}
	mutator := mutationTestMutator(t, root, os.Args[0])
	target, err := mutator.StatMutationTarget(context.Background(), ".", "note.md")
	if err != nil {
		t.Fatal(err)
	}
	ready := filepath.Join(t.TempDir(), "ready")
	t.Setenv(mutationCommitAcceptedHelperEnv, "1")
	t.Setenv(mutationCommitAcceptedReadyEnv, ready)
	done := make(chan error, 1)
	go func() {
		_, err := mutator.ReplaceFile(context.Background(), ".", "note.md", target.Fingerprint, []byte("final"))
		done <- err
	}()
	pid := waitForMutationPID(t, ready)
	if err := unix.Kill(pid, unix.SIGKILL); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !IsCode(err, CodeUncertain) {
			t.Fatalf("accepted-token helper death err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("parent did not classify accepted-token helper death")
	}
	assertMutationFileContent(t, targetPath, "prior")
	assertMutationNoStages(t, root)
}

func TestMutationReplacementKilledAfterEffectHasExactFinalBytes(t *testing.T) {
	root := t.TempDir()
	targetPath := filepath.Join(root, "note.md")
	if err := os.WriteFile(targetPath, []byte("prior"), 0o600); err != nil {
		t.Fatal(err)
	}
	mutator := mutationTestMutator(t, root, os.Args[0])
	target, err := mutator.StatMutationTarget(context.Background(), ".", "note.md")
	if err != nil {
		t.Fatal(err)
	}
	ready := filepath.Join(t.TempDir(), "ready")
	t.Setenv(mutationPosteffectHelperEnv, "1")
	t.Setenv(mutationPosteffectReadyEnv, ready)
	done := make(chan error, 1)
	go func() {
		_, err := mutator.ReplaceFile(context.Background(), ".", "note.md", target.Fingerprint, []byte("final"))
		done <- err
	}()
	pid := waitForMutationPID(t, ready)
	if err := unix.Kill(pid, unix.SIGKILL); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !IsCode(err, CodeUncertain) {
			t.Fatalf("post-effect replacement death err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("parent did not classify post-effect replacement death")
	}
	assertMutationFileContent(t, targetPath, "final")
	assertMutationNoStages(t, root)
}

func TestMutationReplacementInjectedIOFailuresPreservePriorBytes(t *testing.T) {
	for _, phase := range []string{"write", "rename"} {
		t.Run(phase, func(t *testing.T) {
			root := t.TempDir()
			targetPath := filepath.Join(root, "note.md")
			if err := os.WriteFile(targetPath, []byte("prior"), 0o600); err != nil {
				t.Fatal(err)
			}
			mutator := mutationTestMutator(t, root, os.Args[0])
			target, err := mutator.StatMutationTarget(context.Background(), ".", "note.md")
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv(mutationFailureHelperEnv, phase)
			if _, err := mutator.ReplaceFile(context.Background(), ".", "note.md", target.Fingerprint, []byte("final")); err == nil || IsCode(err, CodeUncertain) {
				t.Fatalf("injected %s err = %v", phase, err)
			}
			assertMutationFileContent(t, targetPath, "prior")
			assertMutationNoStages(t, root)
		})
	}
}

func TestMutationCleanupFaultUsesSurvivingOwnerOrReturnsUncertain(t *testing.T) {
	for _, test := range []struct {
		name      string
		uncertain bool
	}{
		{name: "cleanup-transient"},
		{name: "cleanup-persistent", uncertain: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			mutator := mutationTestMutator(t, root, os.Args[0])
			t.Setenv(mutationFailureHelperEnv, test.name)
			previous := mutationLauncherTestHooks
			if test.uncertain {
				mutationLauncherTestHooks = &mutationLauncherHooks{cleanupUnlink: func(int, string) error { return unix.EIO }}
			}
			defer func() { mutationLauncherTestHooks = previous }()
			_, err := mutator.CreateFile(context.Background(), ".", "note.md", []byte("payload"))
			if err == nil || test.uncertain != IsCode(err, CodeUncertain) {
				t.Fatalf("cleanup lifecycle err = %v, uncertain=%v", err, test.uncertain)
			}
			if !test.uncertain {
				assertMutationNoStages(t, root)
			} else {
				stages, globErr := filepath.Glob(filepath.Join(root, ".pmcg-stage-*"))
				if globErr != nil || len(stages) != 1 {
					t.Fatalf("recoverable residue = %v, %v", stages, globErr)
				}
			}
			if _, statErr := os.Stat(filepath.Join(root, "note.md")); !os.IsNotExist(statErr) {
				t.Fatalf("public target changed: %v", statErr)
			}
		})
	}
}

func TestMutationCreateUnsupportedAndExecutableMismatchFailClosed(t *testing.T) {
	for _, test := range []struct {
		mode string
		code Code
	}{
		{mode: "enotsup", code: CodeUnsupported},
		{mode: "identity-mismatch", code: CodePathDenied},
	} {
		t.Run(test.mode, func(t *testing.T) {
			root := t.TempDir()
			mutator := mutationTestMutator(t, root, os.Args[0])
			t.Setenv(mutationFailureHelperEnv, test.mode)
			if _, err := mutator.CreateFile(context.Background(), ".", "note.md", []byte("payload")); !IsCode(err, test.code) {
				t.Fatalf("%s err = %v", test.mode, err)
			}
			assertMutationNoStageOrTarget(t, root, "note.md")
		})
	}
}

func TestMutationHelperDoesNotInheritUnlistedSentinelFD(t *testing.T) {
	sentinel, err := os.OpenFile("/dev/null", os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer sentinel.Close()
	probeFD := 200
	for ; probeFD < 256; probeFD++ {
		if _, err := unix.FcntlInt(uintptr(probeFD), unix.F_GETFD, 0); err != nil {
			break
		}
	}
	if probeFD == 256 {
		t.Fatal("no unused sentinel descriptor")
	}
	if err := unix.Dup2(int(sentinel.Fd()), probeFD); err != nil {
		t.Fatal(err)
	}
	defer unix.Close(probeFD)
	if _, err := unix.FcntlInt(uintptr(probeFD), unix.F_SETFD, 0); err != nil {
		t.Fatal(err)
	}
	ready := filepath.Join(t.TempDir(), "ready")
	t.Setenv(mutationFDProbeHelperEnv, "1")
	t.Setenv(mutationFDProbeReadyEnv, ready)
	t.Setenv(mutationFDProbeNumberEnv, strconv.Itoa(probeFD))
	root := t.TempDir()
	mutator := mutationTestMutator(t, root, os.Args[0])
	if _, err := mutator.CreateFile(context.Background(), ".", "note.md", []byte("payload")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(ready)
	if err != nil || string(got) != "closed" {
		t.Fatalf("sentinel result = %q, %v", got, err)
	}
}

func TestMutationActiveHelperCancellationCleansStage(t *testing.T) {
	candidate := buildMutationGatewayCandidate(t)
	root := t.TempDir()
	mutator := mutationTestMutator(t, root, candidate)
	ready := make(chan struct{})
	release := make(chan struct{})
	previous := mutationLauncherTestHooks
	mutationLauncherTestHooks = &mutationLauncherHooks{afterStageAnnounced: func(*exec.Cmd) {
		close(ready)
		<-release
	}}
	defer func() { mutationLauncherTestHooks = previous }()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := mutator.CreateFile(ctx, ".", "note.md", []byte("payload"))
		done <- err
	}()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("active helper did not announce stage")
	}
	cancel()
	close(release)
	select {
	case err := <-done:
		if !IsCode(err, CodeCanceled) {
			t.Fatalf("active cancellation err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("active cancellation did not quiesce")
	}
	assertMutationNoStageOrTarget(t, root, "note.md")
}

func TestMutationActiveReplacementCancellationAndTimeoutPreservePriorBytes(t *testing.T) {
	candidate := buildMutationGatewayCandidate(t)
	for _, mode := range []string{"cancel", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			targetPath := filepath.Join(root, "note.md")
			if err := os.WriteFile(targetPath, []byte("prior"), 0o600); err != nil {
				t.Fatal(err)
			}
			activity := &ActivityCounter{}
			vault, err := NewVaultWithActivity(root, activity)
			if err != nil {
				t.Fatal(err)
			}
			mutator := NewMutator(vault)
			mutator.helperExecutable = candidate
			target, err := mutator.StatMutationTarget(context.Background(), ".", "note.md")
			if err != nil {
				t.Fatal(err)
			}
			runtime.GC()
			beforeFDs := mutationOpenFDCount(t)
			ready := make(chan struct{})
			release := make(chan struct{})
			previous := mutationLauncherTestHooks
			mutationLauncherTestHooks = &mutationLauncherHooks{afterStageAnnounced: func(*exec.Cmd) {
				close(ready)
				<-release
			}}
			defer func() { mutationLauncherTestHooks = previous }()
			ctx := context.Background()
			cancel := func() {}
			if mode == "cancel" {
				ctx, cancel = context.WithCancel(ctx)
			} else {
				ctx, cancel = context.WithTimeout(ctx, 50*time.Millisecond)
			}
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := mutator.ReplaceFile(ctx, ".", "note.md", target.Fingerprint, []byte("final"))
				done <- err
			}()
			select {
			case <-ready:
			case <-time.After(5 * time.Second):
				t.Fatal("replacement helper did not announce stage")
			}
			if mode == "cancel" {
				cancel()
			} else {
				time.Sleep(75 * time.Millisecond)
			}
			close(release)
			select {
			case err := <-done:
				want := CodeCanceled
				if mode == "timeout" {
					want = CodeTimeout
				}
				if !IsCode(err, want) {
					t.Fatalf("%s err = %v", mode, err)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("%s did not quiesce", mode)
			}
			assertMutationFileContent(t, targetPath, "prior")
			assertMutationNoStages(t, root)
			runtime.GC()
			if afterFDs := mutationOpenFDCount(t); afterFDs != beforeFDs {
				t.Fatalf("%s descriptors grew from %d to %d", mode, beforeFDs, afterFDs)
			}
			if snapshot := activity.Snapshot(); snapshot.Active != 0 || snapshot.Total != 2 {
				t.Fatalf("%s activity = %+v", mode, snapshot)
			}
		})
	}
}

func TestMutationKilledHelperBeforeAnnouncementCleansStage(t *testing.T) {
	candidate := buildMutationGatewayCandidate(t)
	root := t.TempDir()
	mutator := mutationTestMutator(t, root, candidate)
	previous := mutationLauncherTestHooks
	mutationLauncherTestHooks = &mutationLauncherHooks{afterStageCreated: func(cmd *exec.Cmd) { _ = cmd.Process.Kill() }}
	defer func() { mutationLauncherTestHooks = previous }()
	if _, err := mutator.CreateFile(context.Background(), ".", "note.md", []byte("payload")); err == nil || IsCode(err, CodeUncertain) {
		t.Fatalf("pre-token helper death err = %v", err)
	}
	assertMutationNoStageOrTarget(t, root, "note.md")
}

func TestMutationCleanupPreservesReplacedAnnouncedStage(t *testing.T) {
	candidate := buildMutationGatewayCandidate(t)
	root := t.TempDir()
	mutator := mutationTestMutator(t, root, candidate)
	attacker := filepath.Join(root, "attacker")
	if err := os.WriteFile(attacker, []byte("attacker"), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := mutationLauncherTestHooks
	var replaceOnce sync.Once
	mutationLauncherTestHooks = &mutationLauncherHooks{
		afterStageAnnounced: func(cmd *exec.Cmd) { _ = cmd.Process.Kill() },
		beforeCleanupCheck: func(parentFD int, leaf string) {
			replaceOnce.Do(func() { _ = os.Rename(attacker, filepath.Join(root, leaf)) })
		},
	}
	defer func() { mutationLauncherTestHooks = previous }()
	if _, err := mutator.CreateFile(context.Background(), ".", "note.md", []byte("payload")); err == nil {
		t.Fatal("helper death unexpectedly succeeded")
	}
	stages, err := filepath.Glob(filepath.Join(root, ".pmcg-stage-*"))
	if err != nil || len(stages) != 1 {
		t.Fatalf("stages = %v, %v", stages, err)
	}
	if got, err := os.ReadFile(stages[0]); err != nil || string(got) != "attacker" {
		t.Fatalf("replacement stage = %q, %v", got, err)
	}
}

func TestMutationFifthHelperWaitIsContextBounded(t *testing.T) {
	for index := 0; index < cap(mutationHelperSlots); index++ {
		mutationHelperSlots <- struct{}{}
	}
	defer func() {
		for index := 0; index < cap(mutationHelperSlots); index++ {
			<-mutationHelperSlots
		}
	}()
	rootPath := t.TempDir()
	root, err := os.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	parent, err := os.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = runStagedMutation(ctx, stagedMutation{Executable: "/not-used", Operation: mutationOperationCreate, Root: root, Parent: parent, TargetLeaf: "note.md"})
	if err != context.DeadlineExceeded {
		t.Fatalf("fifth helper err = %v", err)
	}
	assertMutationNoStages(t, rootPath)
}

func TestMutationFourRealHelpersAndFifthCallerQuiesce(t *testing.T) {
	candidate := buildMutationGatewayCandidate(t)
	root := t.TempDir()
	activity := &ActivityCounter{}
	vault, err := NewVaultWithActivity(root, activity)
	if err != nil {
		t.Fatal(err)
	}
	mutator := NewMutator(vault)
	mutator.helperExecutable = candidate
	runtime.GC()
	beforeFDs := mutationOpenFDCount(t)
	ready := make(chan struct{}, 4)
	release := make(chan struct{})
	previous := mutationLauncherTestHooks
	mutationLauncherTestHooks = &mutationLauncherHooks{afterPayloadComplete: func(*exec.Cmd) {
		ready <- struct{}{}
		<-release
	}}
	defer func() { mutationLauncherTestHooks = previous }()
	errCh := make(chan error, 4)
	payload := bytes.Repeat([]byte{'x'}, mutationMaxPayloadBytes)
	for index := 0; index < 4; index++ {
		index := index
		go func() {
			_, err := mutator.CreateFile(context.Background(), ".", fmt.Sprintf("note-%d.md", index), payload)
			errCh <- err
		}()
	}
	for index := 0; index < 4; index++ {
		select {
		case <-ready:
		case <-time.After(5 * time.Second):
			t.Fatal("four helpers did not reach the announced checkpoint")
		}
	}
	stages, err := filepath.Glob(filepath.Join(root, ".pmcg-stage-*"))
	if err != nil || len(stages) != 4 {
		t.Fatalf("concurrent stages = %v, %v", stages, err)
	}
	for _, stage := range stages {
		info, err := os.Stat(stage)
		if err != nil || info.Size() != mutationMaxPayloadBytes {
			t.Fatalf("stage %q size = %v, %v", stage, info, err)
		}
	}
	fifthCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := mutator.CreateFile(fifthCtx, ".", "fifth.md", nil); !IsCode(err, CodeTimeout) {
		t.Fatalf("fifth call err = %v", err)
	}
	close(release)
	for index := 0; index < 4; index++ {
		if err := <-errCh; err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(filepath.Join(root, fmt.Sprintf("note-%d.md", index)))
		if err != nil || info.Size() != mutationMaxPayloadBytes {
			t.Fatalf("note-%d size = %v, %v", index, info, err)
		}
	}
	assertMutationNoStages(t, root)
	if snapshot := activity.Snapshot(); snapshot.Active != 0 || snapshot.Total != 5 {
		t.Fatalf("activity = %+v", snapshot)
	}
	runtime.GC()
	if afterFDs := mutationOpenFDCount(t); afterFDs != beforeFDs {
		t.Fatalf("concurrent descriptors grew from %d to %d", beforeFDs, afterFDs)
	}
}

func TestMutationKilledParentLeavesHelperToResolveOwnership(t *testing.T) {
	candidate := buildMutationGatewayCandidate(t)
	for _, phase := range []string{"pre-token", "post-token"} {
		t.Run(phase, func(t *testing.T) {
			root := t.TempDir()
			ready := filepath.Join(t.TempDir(), "ready")
			cmd := exec.Command(os.Args[0], "-test.run=^TestMutationParentOwnerSubprocess$")
			cmd.Env = append(os.Environ(),
				mutationParentProcessVaultEnv+"="+root,
				mutationParentProcessCandidateEnv+"="+candidate,
				mutationParentProcessReadyEnv+"="+ready,
				mutationParentProcessPhaseEnv+"="+phase,
			)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			waitForMutationCondition(t, 5*time.Second, func() bool {
				_, err := os.Stat(ready)
				return err == nil
			})
			helperPIDBytes, err := os.ReadFile(ready)
			if err != nil {
				t.Fatal(err)
			}
			helperPID, err := strconv.Atoi(string(helperPIDBytes))
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = cmd.Wait()
			waitForMutationCondition(t, 5*time.Second, func() bool {
				stages, _ := filepath.Glob(filepath.Join(root, ".pmcg-stage-*"))
				return len(stages) == 0
			})
			waitForMutationCondition(t, 5*time.Second, func() bool {
				return unix.Kill(helperPID, 0) != nil
			})
			target := filepath.Join(root, "note.md")
			if phase == "pre-token" {
				if _, err := os.Stat(target); !os.IsNotExist(err) {
					t.Fatalf("target exists after pre-token parent death: %v", err)
				}
			} else {
				assertMutationFileContent(t, target, "payload")
			}
		})
	}
}

func TestMutationReplacementKilledParentLeavesExactPriorOrFinalBytes(t *testing.T) {
	candidate := buildMutationGatewayCandidate(t)
	for _, phase := range []string{"pre-token", "post-token"} {
		t.Run(phase, func(t *testing.T) {
			root := t.TempDir()
			targetPath := filepath.Join(root, "note.md")
			if err := os.WriteFile(targetPath, []byte("prior"), 0o600); err != nil {
				t.Fatal(err)
			}
			ready := filepath.Join(t.TempDir(), "ready")
			cmd := exec.Command(os.Args[0], "-test.run=^TestMutationParentOwnerSubprocess$")
			cmd.Env = append(os.Environ(),
				mutationParentProcessVaultEnv+"="+root,
				mutationParentProcessCandidateEnv+"="+candidate,
				mutationParentProcessReadyEnv+"="+ready,
				mutationParentProcessPhaseEnv+"="+phase,
				mutationParentProcessOperationEnv+"=replace",
			)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			helperPID := waitForMutationPID(t, ready)
			if err := cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = cmd.Wait()
			waitForMutationCondition(t, 5*time.Second, func() bool {
				return unix.Kill(helperPID, 0) != nil
			})
			waitForMutationCondition(t, 5*time.Second, func() bool {
				stages, _ := filepath.Glob(filepath.Join(root, ".pmcg-stage-*"))
				return len(stages) == 0
			})
			got, err := os.ReadFile(targetPath)
			if err != nil || (string(got) != "prior" && string(got) != "final") {
				t.Fatalf("replacement after %s parent death = %q, %v", phase, got, err)
			}
		})
	}
}

func TestMutationKilledParentBeforeStageIdentityAnnouncementLeavesHelperToClean(t *testing.T) {
	root := t.TempDir()
	ready := filepath.Join(t.TempDir(), "ready")
	release := filepath.Join(t.TempDir(), "release")
	cmd := exec.Command(os.Args[0], "-test.run=^TestMutationPreidentityParentOwnerSubprocess$")
	cmd.Env = append(os.Environ(),
		mutationParentProcessVaultEnv+"="+root,
		mutationPreidentityHelperEnv+"=1",
		mutationPreidentityReadyEnv+"="+ready,
		mutationPreidentityReleaseEnv+"="+release,
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	helperPID := waitForMutationPID(t, ready)
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	if err := os.WriteFile(release, []byte("continue"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitForMutationCondition(t, 5*time.Second, func() bool {
		return unix.Kill(helperPID, 0) != nil
	})
	waitForMutationCondition(t, 5*time.Second, func() bool {
		stages, _ := filepath.Glob(filepath.Join(root, ".pmcg-stage-*"))
		return len(stages) == 0
	})
	if _, err := os.Stat(filepath.Join(root, "note.md")); !os.IsNotExist(err) {
		t.Fatalf("target exists after pre-identity parent death: %v", err)
	}
}

func TestMutationPreidentityParentOwnerSubprocess(t *testing.T) {
	root := os.Getenv(mutationParentProcessVaultEnv)
	if root == "" || os.Getenv(mutationPreidentityHelperEnv) != "1" {
		return
	}
	mutator := mutationTestMutator(t, root, os.Args[0])
	_, _ = mutator.CreateFile(context.Background(), ".", "note.md", []byte("payload"))
	os.Exit(3)
}

func TestMutationParentOwnerSubprocess(t *testing.T) {
	root := os.Getenv(mutationParentProcessVaultEnv)
	if root == "" {
		return
	}
	mutator := mutationTestMutator(t, root, os.Getenv(mutationParentProcessCandidateEnv))
	checkpoint := func(cmd *exec.Cmd) {
		if err := os.WriteFile(os.Getenv(mutationParentProcessReadyEnv), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600); err != nil {
			os.Exit(2)
		}
		select {}
	}
	mutationLauncherTestHooks = &mutationLauncherHooks{}
	if os.Getenv(mutationParentProcessPhaseEnv) == "post-token" {
		mutationLauncherTestHooks.afterCommitSent = checkpoint
	} else {
		mutationLauncherTestHooks.afterStageAnnounced = checkpoint
	}
	if os.Getenv(mutationParentProcessOperationEnv) == "replace" {
		target, err := mutator.StatMutationTarget(context.Background(), ".", "note.md")
		if err != nil {
			os.Exit(4)
		}
		_, _ = mutator.ReplaceFile(context.Background(), ".", "note.md", target.Fingerprint, []byte("final"))
	} else {
		_, _ = mutator.CreateFile(context.Background(), ".", "note.md", []byte("payload"))
	}
	os.Exit(3)
}

func buildMutationGatewayCandidate(t *testing.T) string {
	t.Helper()
	candidate := filepath.Join(t.TempDir(), "personal-mcp-gateway")
	cmd := exec.Command("go", "build", "-buildvcs=false", "-trimpath", "-o", candidate, "../../cmd/gateway")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build gateway candidate: %v\n%s", err, output)
	}
	return candidate
}

func mutationTestMutator(t *testing.T, root, candidate string) *Mutator {
	t.Helper()
	vault, err := NewVault(root)
	if err != nil {
		t.Fatal(err)
	}
	mutator := NewMutator(vault)
	mutator.helperExecutable = candidate
	return mutator
}

func assertMutationNoStageOrTarget(t *testing.T, root, target string) {
	t.Helper()
	assertMutationNoStages(t, root)
	if _, err := os.Stat(filepath.Join(root, target)); !os.IsNotExist(err) {
		t.Fatalf("target exists: %v", err)
	}
}

func assertMutationNoStages(t *testing.T, root string) {
	t.Helper()
	stages, err := filepath.Glob(filepath.Join(root, ".pmcg-stage-*"))
	if err != nil || len(stages) != 0 {
		t.Fatalf("stages = %v, %v", stages, err)
	}
}

func waitForMutationCondition(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for mutation process condition")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForMutationPID(t *testing.T, ready string) int {
	t.Helper()
	pid := 0
	waitForMutationCondition(t, 5*time.Second, func() bool {
		payload, err := os.ReadFile(ready)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(string(payload))
		return err == nil && unix.Kill(pid, 0) == nil
	})
	return pid
}
