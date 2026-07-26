//go:build darwin

package fsx

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestMutationHelperCreatesStageAndCommitsExactPayload(t *testing.T) {
	dir := t.TempDir()
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

	protocol, err := newMutationProtocolParent(client, client, mutationOperationCreate, mutationFDControl|mutationFDRoot|mutationFDParent)
	if err != nil {
		t.Fatal(err)
	}
	const stage = ".pmcg-stage-0123456789abcdef0123456789abcdef"
	request := mutationControl{Version: mutationProtocolVersion, Operation: mutationOperationCreate, Phase: mutationPhaseRequest, FDRoles: mutationFDControl | mutationFDRoot | mutationFDParent, PayloadBytes: 5, TargetLeaf: "note.md", StageLeaf: stage}
	if err := protocol.SendControl(request); err != nil {
		t.Fatal(err)
	}
	if control, err := protocol.ReceiveControl(); err != nil || control.Phase != mutationPhaseStageCreated {
		t.Fatalf("stage_created = %#v, %v", control, err)
	}
	if err := protocol.SendControl(mutationControl{Version: mutationProtocolVersion, Operation: mutationOperationCreate, Phase: mutationPhaseStageAck}); err != nil {
		t.Fatal(err)
	}
	if control, err := protocol.ReceiveControl(); err != nil || control.Phase != mutationPhaseStageAnnounced || !validMutationRawStamp(control.StageIdentity) {
		t.Fatalf("stage_announced = %#v, %v", control, err)
	}
	if err := protocol.SendPayload([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if control, err := protocol.ReceiveControl(); err != nil || control.Phase != mutationPhasePayloadComplete || control.StageIdentity.Size != 5 {
		t.Fatalf("payload_complete = %#v, %v", control, err)
	}
	if err := protocol.SendControl(mutationControl{Version: mutationProtocolVersion, Operation: mutationOperationCreate, Phase: mutationPhaseCommit, CommitToken: "0123456789abcdef0123456789abcdef"}); err != nil {
		t.Fatal(err)
	}
	terminal, err := protocol.ReceiveControl()
	if err != nil {
		t.Fatal(err)
	}
	if terminal.Terminal != mutationTerminalCommitted {
		t.Fatalf("terminal = %#v", terminal)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "note.md"))
	if err != nil || string(got) != "hello" {
		t.Fatalf("target = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, stage)); !os.IsNotExist(err) {
		t.Fatalf("stage remains: %v", err)
	}
}

func TestMutationHelperFinalRevalidationAndAcceptedSyscallWindow(t *testing.T) {
	for _, test := range []struct {
		name     string
		before   bool
		terminal mutationTerminal
		want     string
	}{
		{name: "change before final revalidation refuses", before: true, terminal: mutationTerminalRefused, want: "writer"},
		{name: "change after final revalidation is accepted race", before: false, terminal: mutationTerminalCommitted, want: "final"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			targetPath := filepath.Join(dir, "note.md")
			if err := os.WriteFile(targetPath, []byte("original"), 0o600); err != nil {
				t.Fatal(err)
			}
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
			source, err := os.Open(targetPath)
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			expected, err := mutationRawStampFromFD(int(source.Fd()))
			if err != nil {
				t.Fatal(err)
			}
			previous := mutationHelperTestHooks
			hook := func() {
				if err := os.WriteFile(targetPath, []byte("writer"), 0o600); err != nil {
					t.Errorf("writer hook: %v", err)
				}
			}
			mutationHelperTestHooks = &mutationHelperHooks{}
			if test.before {
				mutationHelperTestHooks.beforeFinalRevalidation = hook
			} else {
				mutationHelperTestHooks.afterFinalRevalidation = hook
			}
			defer func() { mutationHelperTestHooks = previous }()

			fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
			if err != nil {
				t.Fatal(err)
			}
			client := os.NewFile(uintptr(fds[0]), "client")
			server := os.NewFile(uintptr(fds[1]), "server")
			defer client.Close()
			done := make(chan error, 1)
			go func() { done <- runMutationHelperFiles(server, root, parent, source) }()
			protocol, err := newMutationProtocolParent(client, client, mutationOperationReplace, mutationFDRequired|mutationFDSource)
			if err != nil {
				t.Fatal(err)
			}
			const stage = ".pmcg-stage-abcdef0123456789abcdef0123456789"
			request := mutationControl{Version: mutationProtocolVersion, Operation: mutationOperationReplace, Phase: mutationPhaseRequest, FDRoles: mutationFDRequired | mutationFDSource, PayloadBytes: 5, TargetLeaf: "note.md", StageLeaf: stage, ExpectedSource: expected}
			if err := protocol.SendControl(request); err != nil {
				t.Fatal(err)
			}
			if _, err := protocol.ReceiveControl(); err != nil {
				t.Fatal(err)
			}
			if err := protocol.SendControl(mutationControl{Version: mutationProtocolVersion, Operation: mutationOperationReplace, Phase: mutationPhaseStageAck}); err != nil {
				t.Fatal(err)
			}
			if _, err := protocol.ReceiveControl(); err != nil {
				t.Fatal(err)
			}
			if err := protocol.SendPayload([]byte("final")); err != nil {
				t.Fatal(err)
			}
			if _, err := protocol.ReceiveControl(); err != nil {
				t.Fatal(err)
			}
			if err := protocol.SendControl(mutationControl{Version: mutationProtocolVersion, Operation: mutationOperationReplace, Phase: mutationPhaseCommit, CommitToken: testToken}); err != nil {
				t.Fatal(err)
			}
			terminal, err := protocol.ReceiveControl()
			if err != nil {
				t.Fatal(err)
			}
			if terminal.Terminal != test.terminal {
				t.Fatalf("terminal = %+v", terminal)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(targetPath)
			if err != nil || string(got) != test.want {
				t.Fatalf("target = %q, %v, want %q", got, err, test.want)
			}
		})
	}
}

func TestRunMutationHelperModeRequiresExactPrivateArgument(t *testing.T) {
	if handled, _ := RunMutationHelperMode(nil); handled {
		t.Fatal("empty args handled")
	}
	if handled, _ := RunMutationHelperMode([]string{mutationHelperArgument, "extra"}); handled {
		t.Fatal("extra args handled")
	}
}
