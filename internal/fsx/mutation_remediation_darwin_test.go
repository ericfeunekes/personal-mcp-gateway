//go:build darwin

package fsx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestMutationOperationsStayOnOneRootGeneration(t *testing.T) {
	candidate := buildMutationGatewayCandidate(t)
	for _, test := range []struct {
		name string
		run  func(*testing.T, *Mutator, string)
	}{
		{name: "create", run: func(t *testing.T, mutator *Mutator, retained string) {
			if _, err := mutator.CreateFile(context.Background(), ".", "note.md", []byte("created")); err != nil {
				t.Fatal(err)
			}
			assertMutationFileContent(t, filepath.Join(retained, "note.md"), "created")
		}},
		{name: "replace", run: func(t *testing.T, mutator *Mutator, retained string) {
			target, err := mutator.StatMutationTarget(context.Background(), ".", "note.md")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := mutator.ReplaceFile(context.Background(), ".", "note.md", target.Fingerprint, []byte("replaced")); err != nil {
				t.Fatal(err)
			}
			assertMutationFileContent(t, filepath.Join(retained, "note.md"), "replaced")
		}},
		{name: "move", run: func(t *testing.T, mutator *Mutator, retained string) {
			target, err := mutator.StatMutationTarget(context.Background(), ".", "note.md")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := mutator.Move(context.Background(), ".", "note.md", "moved.md", target.Fingerprint); err != nil {
				t.Fatal(err)
			}
			assertMutationFileContent(t, filepath.Join(retained, "moved.md"), "original")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			container := t.TempDir()
			root := filepath.Join(container, "vault")
			retained := filepath.Join(container, "retained")
			if err := os.Mkdir(root, 0o700); err != nil {
				t.Fatal(err)
			}
			if test.name != "create" {
				if err := os.WriteFile(filepath.Join(root, "note.md"), []byte("original"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			mutator := mutationTestMutator(t, root, candidate)
			mutator.vault.testHooks = &vaultTestHooks{afterMutationRoot: func() {
				if err := os.Rename(root, retained); err != nil {
					t.Fatalf("rename root: %v", err)
				}
				if err := os.Mkdir(root, 0o700); err != nil {
					t.Fatalf("replace root: %v", err)
				}
				if err := os.WriteFile(filepath.Join(root, "note.md"), []byte("replacement-generation"), 0o600); err != nil {
					t.Fatalf("seed replacement generation: %v", err)
				}
			}}
			test.run(t, mutator, retained)
			assertMutationFileContent(t, filepath.Join(root, "note.md"), "replacement-generation")
			assertMutationNoStages(t, root)
			assertMutationNoStages(t, retained)
		})
	}
}

func TestMutationPostEffectStampFailureIsUncertain(t *testing.T) {
	previous := mutationHelperTestHooks
	mutationHelperTestHooks = &mutationHelperHooks{afterEffectStamp: func(int) (mutationRawStamp, error) {
		return mutationRawStamp{}, unix.EIO
	}}
	defer func() { mutationHelperTestHooks = previous }()

	dir := t.TempDir()
	terminal, helperErr := driveInProcessCreateMutation(t, dir, []byte("final"))
	if helperErr != nil {
		t.Fatal(helperErr)
	}
	if terminal.Terminal != mutationTerminalUncertain || terminal.TerminalCode != mutationTerminalCodeUncertain {
		t.Fatalf("terminal = %+v", terminal)
	}
	assertMutationFileContent(t, filepath.Join(dir, "note.md"), "final")
	assertMutationNoStages(t, dir)
}

func TestMutationRenameExclusiveUnsupportedRefusesWithoutEffect(t *testing.T) {
	previous := mutationRenameExclusive
	mutationRenameExclusive = func(int, string, int, string) error { return unix.ENOTSUP }
	defer func() { mutationRenameExclusive = previous }()

	dir := t.TempDir()
	terminal, helperErr := driveInProcessCreateMutation(t, dir, []byte("final"))
	if helperErr != nil {
		t.Fatal(helperErr)
	}
	if terminal.Terminal != mutationTerminalError || terminal.TerminalCode != mutationTerminalCodeUnsupported {
		t.Fatalf("terminal = %+v", terminal)
	}
	assertMutationNoStageOrTarget(t, dir, "note.md")
}

func TestMutationCleanupFailureMapsToUncertain(t *testing.T) {
	if err := mapStagedMutationError(errMutationCleanup); !IsCode(err, CodeUncertain) {
		t.Fatalf("mapped err = %v", err)
	}
	dir := t.TempDir()
	stage := testStageLeaf
	if err := os.WriteFile(filepath.Join(dir, stage), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	parent, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	stamp, err := mutationRawStampFromPath(parent, stage)
	if err != nil {
		t.Fatal(err)
	}
	previous := mutationLauncherTestHooks
	mutationLauncherTestHooks = &mutationLauncherHooks{cleanupUnlink: func(int, string) error { return unix.EIO }}
	defer func() { mutationLauncherTestHooks = previous }()
	if mutationParentCleanupStage(int(parent.Fd()), stage, stamp) {
		t.Fatal("persistent cleanup fault reported success")
	}
	if _, err := os.Stat(filepath.Join(dir, stage)); err != nil {
		t.Fatalf("stage not preserved for recovery: %v", err)
	}
	calls := 0
	mutationLauncherTestHooks = &mutationLauncherHooks{cleanupUnlink: func(parentFD int, leaf string) error {
		calls++
		if calls == 1 {
			return unix.EIO
		}
		return unix.Unlinkat(parentFD, leaf, 0)
	}}
	if mutationParentCleanupStage(int(parent.Fd()), stage, stamp) {
		t.Fatal("first owner hid transient cleanup failure")
	}
	if !mutationParentCleanupStage(int(parent.Fd()), stage, stamp) {
		t.Fatal("surviving owner did not recover stage")
	}
	if _, err := os.Stat(filepath.Join(dir, stage)); !os.IsNotExist(err) {
		t.Fatalf("stage remains after recovery: %v", err)
	}
}

func TestMutationHelperSlotWaitHonorsActiveCancellation(t *testing.T) {
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
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, err = runStagedMutation(ctx, stagedMutation{Executable: "/not-used", Operation: mutationOperationCreate, Root: root, Parent: parent, TargetLeaf: "note.md"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled wait err = %v", err)
	}
	assertMutationNoStages(t, rootPath)
}

func TestMutationHardDeadlineCapsLongCallerDeadline(t *testing.T) {
	candidate := buildMutationGatewayCandidate(t)
	root := t.TempDir()
	mutator := mutationTestMutator(t, root, candidate)
	previous := mutationLauncherTestHooks
	mutationLauncherTestHooks = &mutationLauncherHooks{afterStageAnnounced: func(*exec.Cmd) {
		time.Sleep(2100 * time.Millisecond)
	}}
	defer func() { mutationLauncherTestHooks = previous }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	started := time.Now()
	_, err := mutator.CreateFile(ctx, ".", "note.md", []byte("payload"))
	if !IsCode(err, CodeTimeout) {
		t.Fatalf("err = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("hard deadline took %v", elapsed)
	}
	assertMutationNoStageOrTarget(t, root, "note.md")
}

func TestMutationRepeatedCandidateOperationsDoNotLeakFDs(t *testing.T) {
	candidate := buildMutationGatewayCandidate(t)
	root := t.TempDir()
	mutator := mutationTestMutator(t, root, candidate)
	runtime.GC()
	before := mutationOpenFDCount(t)
	for index := 0; index < 8; index++ {
		name := fmt.Sprintf("note-%d.md", index)
		if _, err := mutator.CreateFile(context.Background(), ".", name, []byte("payload")); err != nil {
			t.Fatal(err)
		}
	}
	runtime.GC()
	after := mutationOpenFDCount(t)
	if after != before {
		t.Fatalf("open descriptors grew from %d to %d", before, after)
	}
}

func TestMutationReplacementVisibilityIsAlwaysPriorOrFinal(t *testing.T) {
	candidate := buildMutationGatewayCandidate(t)
	root := t.TempDir()
	path := filepath.Join(root, "note.md")
	prior := bytes.Repeat([]byte{'a'}, 256<<10)
	final := bytes.Repeat([]byte{'b'}, 256<<10)
	if err := os.WriteFile(path, prior, 0o600); err != nil {
		t.Fatal(err)
	}
	mutator := mutationTestMutator(t, root, candidate)
	target, err := mutator.StatMutationTarget(context.Background(), ".", "note.md")
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	readerDone := make(chan struct{})
	readerErr := make(chan error, 1)
	go func() {
		defer close(readerDone)
		for {
			select {
			case <-stop:
				return
			default:
			}
			got, err := os.ReadFile(path)
			if err != nil || (!bytes.Equal(got, prior) && !bytes.Equal(got, final)) {
				observedErr := err
				if observedErr == nil {
					observedErr = fmt.Errorf("content size %d matched neither complete version", len(got))
				}
				select {
				case readerErr <- observedErr:
				default:
				}
				return
			}
		}
	}()
	for index := 0; index < 12; index++ {
		payload := final
		if index%2 == 1 {
			payload = prior
		}
		result, err := mutator.ReplaceFile(context.Background(), ".", "note.md", target.Fingerprint, payload)
		if err != nil {
			close(stop)
			<-readerDone
			t.Fatal(err)
		}
		target = MutationTarget{Resolved: result.Resolved, Fingerprint: result.Fingerprint}
	}
	close(stop)
	<-readerDone
	select {
	case err := <-readerErr:
		t.Fatal(err)
	default:
	}
	assertMutationNoStages(t, root)
}

func driveInProcessCreateMutation(t *testing.T, dir string, payload []byte) (mutationControl, error) {
	t.Helper()
	root, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	parent, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	client := os.NewFile(uintptr(fds[0]), "client")
	server := os.NewFile(uintptr(fds[1]), "server")
	defer client.Close()
	done := make(chan error, 1)
	go func() { done <- runMutationHelperFiles(server, root, parent, nil) }()
	protocol, err := newMutationProtocolParent(client, client, mutationOperationCreate, mutationFDRequired)
	if err != nil {
		t.Fatal(err)
	}
	request := mutationControl{Version: mutationProtocolVersion, Operation: mutationOperationCreate, Phase: mutationPhaseRequest, FDRoles: mutationFDRequired, PayloadBytes: uint64(len(payload)), TargetLeaf: "note.md", StageLeaf: testStageLeaf}
	requireProtocolSend(t, protocol, request)
	requireProtocolReceive(t, protocol, mutationPhaseStageCreated)
	requireProtocolSend(t, protocol, controlFor(mutationOperationCreate, mutationPhaseStageAck))
	requireProtocolReceive(t, protocol, mutationPhaseStageAnnounced)
	if err := protocol.SendPayload(payload); err != nil {
		t.Fatal(err)
	}
	requireProtocolReceive(t, protocol, mutationPhasePayloadComplete)
	requireProtocolSend(t, protocol, mutationControl{Version: mutationProtocolVersion, Operation: mutationOperationCreate, Phase: mutationPhaseCommit, CommitToken: testToken})
	terminal, receiveErr := protocol.ReceiveControl()
	if receiveErr != nil {
		t.Fatal(receiveErr)
	}
	return terminal, <-done
}

func mutationRawStampFromPath(parent *os.File, leaf string) (mutationRawStamp, error) {
	var stat unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), leaf, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return mutationRawStamp{}, err
	}
	return mutationRawStampFromStat(&stat), nil
}

func mutationOpenFDCount(t *testing.T) int {
	t.Helper()
	dir, err := os.Open("/dev/fd")
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	names, err := dir.Readdirnames(-1)
	if err != nil {
		t.Fatal(err)
	}
	return len(names)
}

func assertMutationFileContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != want {
		t.Fatalf("%s = %q, %v; want %q", path, got, err, want)
	}
}
