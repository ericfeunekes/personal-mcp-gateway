package resourceprobe

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"personal-mcp-gateway/internal/fsx"
)

func TestControllerAcknowledgesCausalGCAndAggregateSnapshots(t *testing.T) {
	commandRead, commandWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	ackRead, ackWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer commandWrite.Close()
	defer ackRead.Close()

	activity := &fsx.ActivityCounter{}
	controller := New(commandRead, ackWrite, activity)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runResult := make(chan error, 1)
	go func() { runResult <- controller.Run(ctx) }()
	reader := bufio.NewReader(ackRead)

	request := func(command string) string {
		t.Helper()
		if _, err := commandWrite.WriteString(command + "\n"); err != nil {
			t.Fatal(err)
		}
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		return line
	}
	if got := request("snapshot"); got != "snapshot 0 0\n" {
		t.Fatalf("initial snapshot = %q", got)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.md"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	vault, err := fsx.NewVaultWithActivity(root, activity)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vault.Resolve(context.Background(), "", "note.md"); err != nil {
		t.Fatal(err)
	}
	if got := request("snapshot"); got != "snapshot 1 0\n" {
		t.Fatalf("post-activity snapshot = %q", got)
	}
	if got := request("gc"); got != "gc ok\n" {
		t.Fatalf("GC acknowledgement = %q", got)
	}

	cancel()
	if err := <-runResult; err == nil {
		t.Fatal("controller returned nil after cancellation")
	}
}

func TestControllerRejectsUnknownCommand(t *testing.T) {
	for _, command := range []string{"unknown\n", strings.Repeat("x", maxCommandBytes+1) + "\n"} {
		t.Run(command[:1], func(t *testing.T) {
			commandRead, commandWrite, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			ackRead, ackWrite, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer commandWrite.Close()
			defer ackRead.Close()
			controller := New(commandRead, ackWrite, &fsx.ActivityCounter{})
			result := make(chan error, 1)
			go func() { result <- controller.Run(context.Background()) }()
			if _, err := commandWrite.WriteString(command); err != nil {
				t.Fatal(err)
			}
			if err := <-result; err == nil || err.Error() != "resource probe command was invalid" {
				t.Fatalf("Run error = %v", err)
			}
		})
	}
}

func TestPrivateDescriptorValidation(t *testing.T) {
	for _, value := range []string{"", " 3", "3 ", "2", "x"} {
		if _, err := parseFD(value); err == nil {
			t.Fatalf("parseFD(%q) succeeded", value)
		}
	}
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	defer write.Close()
	if err := requireAccessMode(int(read.Fd()), unix.O_RDONLY); err != nil {
		t.Fatalf("read descriptor rejected: %v", err)
	}
	if err := requireAccessMode(int(write.Fd()), unix.O_WRONLY); err != nil {
		t.Fatalf("write descriptor rejected: %v", err)
	}
	if err := requireAccessMode(int(read.Fd()), unix.O_WRONLY); err == nil {
		t.Fatal("read descriptor accepted as write-only")
	}
	if err := requirePipe(int(read.Fd())); err != nil {
		t.Fatalf("read pipe rejected: %v", err)
	}
	if err := requirePipe(int(write.Fd())); err != nil {
		t.Fatalf("write pipe rejected: %v", err)
	}
}

func TestFromEnvironmentAcceptsValidPipeEnds(t *testing.T) {
	commandRead, commandWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	ackRead, ackWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer commandRead.Close()
	defer commandWrite.Close()
	defer ackRead.Close()
	defer ackWrite.Close()
	commandFD, err := unix.Dup(int(commandRead.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	ackFD, err := unix.Dup(int(ackWrite.Fd()))
	if err != nil {
		unix.Close(commandFD)
		t.Fatal(err)
	}
	t.Setenv(Environment, fmt.Sprintf("%d,%d", commandFD, ackFD))
	controller, err := FromEnvironment()
	if err != nil || controller == nil {
		unix.Close(commandFD)
		unix.Close(ackFD)
		t.Fatalf("FromEnvironment = %#v, %v", controller, err)
	}
	if err := controller.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestFromEnvironmentRejectsRegularFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-pipe")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	command, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer command.Close()
	ack, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer ack.Close()
	t.Setenv(Environment, fmt.Sprintf("%d,%d", command.Fd(), ack.Fd()))
	if controller, err := FromEnvironment(); err == nil || controller != nil {
		t.Fatalf("regular files accepted: %#v, %v", controller, err)
	}
}

func TestFromEnvironmentRejectsSockets(t *testing.T) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fds[0])
	defer unix.Close(fds[1])
	if err := requirePipe(fds[0]); err == nil {
		t.Fatal("socket accepted as pipe")
	}
	t.Setenv(Environment, fmt.Sprintf("%d,%d", fds[0], fds[1]))
	if controller, err := FromEnvironment(); err == nil || controller != nil {
		t.Fatalf("sockets accepted: %#v, %v", controller, err)
	}
}

func TestFromEnvironmentIsAbsentByDefault(t *testing.T) {
	original, present := os.LookupEnv(Environment)
	if err := os.Unsetenv(Environment); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if present {
			_ = os.Setenv(Environment, original)
		} else {
			_ = os.Unsetenv(Environment)
		}
	})
	controller, err := FromEnvironment()
	if err != nil || controller != nil {
		t.Fatalf("FromEnvironment = %#v, %v", controller, err)
	}
}
