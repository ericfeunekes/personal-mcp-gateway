package resourceprobe

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/unix"

	"personal-mcp-gateway/internal/fsx"
)

// Environment is the sole opt-in marker for the private exact-candidate
// resource probe. Its value is "<command-read-fd>,<ack-write-fd>".
const Environment = "PERSONAL_MCP_GATEWAY_RESOURCE_PROBE_FDS"

const maxCommandBytes = 32

// Controller owns the child side of a private inherited-pipe protocol. It is
// never constructed during normal runtime and exposes no MCP, CLI, or network
// surface.
type Controller struct {
	command      *os.File
	ack          *os.File
	activity     *fsx.ActivityCounter
	grepActivity *fsx.SchedulerActivity
	runtimeGC    func()
	freeOSMemory func()
	readMemStats func(*runtime.MemStats)
	close        sync.Once
}

// FromEnvironment returns nil when the private marker is absent and fails
// closed when it is malformed or does not name valid inherited pipe ends.
func FromEnvironment() (*Controller, error) {
	raw, present := os.LookupEnv(Environment)
	if !present {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	if len(parts) != 2 {
		return nil, errors.New("invalid resource probe descriptors")
	}
	commandFD, err := parseFD(parts[0])
	if err != nil {
		return nil, err
	}
	ackFD, err := parseFD(parts[1])
	if err != nil || commandFD == ackFD {
		return nil, errors.New("invalid resource probe descriptors")
	}
	if err := requireAccessMode(commandFD, unix.O_RDONLY); err != nil {
		return nil, err
	}
	if err := requireAccessMode(ackFD, unix.O_WRONLY); err != nil {
		return nil, err
	}
	if err := requirePipe(commandFD); err != nil {
		return nil, err
	}
	if err := requirePipe(ackFD); err != nil {
		return nil, err
	}
	command := os.NewFile(uintptr(commandFD), "<resource-probe-command>")
	ack := os.NewFile(uintptr(ackFD), "<resource-probe-ack>")
	if command == nil || ack == nil {
		if command != nil {
			_ = command.Close()
		}
		if ack != nil {
			_ = ack.Close()
		}
		return nil, errors.New("invalid resource probe descriptors")
	}
	return New(command, ack, &fsx.ActivityCounter{}), nil
}

func parseFD(value string) (int, error) {
	if value == "" || strings.TrimSpace(value) != value {
		return 0, errors.New("invalid resource probe descriptors")
	}
	fd, err := strconv.Atoi(value)
	if err != nil || fd < 3 {
		return 0, errors.New("invalid resource probe descriptors")
	}
	return fd, nil
}

func requireAccessMode(fd, want int) error {
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil || flags&unix.O_ACCMODE != want {
		return errors.New("invalid resource probe descriptors")
	}
	return nil
}

func requirePipe(fd int) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFIFO {
		return errors.New("invalid resource probe descriptors")
	}
	return nil
}

// New is the deterministic package-test constructor. Production uses
// FromEnvironment so the controller cannot exist without inherited pipes.
func New(command, ack *os.File, activity *fsx.ActivityCounter) *Controller {
	return newController(command, ack, activity, &fsx.SchedulerActivity{}, runtime.GC, debug.FreeOSMemory, runtime.ReadMemStats)
}

func newController(command, ack *os.File, activity *fsx.ActivityCounter, grepActivity *fsx.SchedulerActivity, runtimeGC, freeOSMemory func(), readMemStats func(*runtime.MemStats)) *Controller {
	return &Controller{
		command:      command,
		ack:          ack,
		activity:     activity,
		grepActivity: grepActivity,
		runtimeGC:    runtimeGC,
		freeOSMemory: freeOSMemory,
		readMemStats: readMemStats,
	}
}

func (c *Controller) Activity() *fsx.ActivityCounter {
	if c == nil {
		return nil
	}
	return c.activity
}

// GrepActivity is a private resource-probe observer for active concurrent
// grep scans. It is absent from normal runtime and records aggregates only.
func (c *Controller) GrepActivity() *fsx.SchedulerActivity {
	if c == nil {
		return nil
	}
	return c.grepActivity
}

// Run serves exact gc and snapshot commands. The gc acknowledgement includes
// only aggregate runtime memory values read after blocking runtime.GC and
// debug.FreeOSMemory return, so one validated reply is causal evidence that
// both heap collection and best-effort page release completed.
func (c *Controller) Run(ctx context.Context) error {
	if c == nil || c.command == nil || c.ack == nil || c.activity == nil || c.grepActivity == nil || c.runtimeGC == nil || c.freeOSMemory == nil || c.readMemStats == nil {
		return errors.New("resource probe is not configured")
	}
	closed := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = c.command.Close()
		case <-closed:
		}
	}()
	defer close(closed)

	reader := bufio.NewReaderSize(c.command, maxCommandBytes)
	for {
		lineBytes, err := reader.ReadSlice('\n')
		if err != nil {
			if errors.Is(err, io.EOF) && ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, io.EOF) {
				return errors.New("resource probe command channel closed")
			}
			return errors.New("resource probe command was invalid")
		}
		line := string(lineBytes)
		switch line {
		case "gc\n":
			c.runtimeGC()
			c.freeOSMemory()
			var memory runtime.MemStats
			c.readMemStats(&memory)
			if _, err := fmt.Fprintf(c.ack, "gc %d %d %d %d %d\n",
				memory.HeapAlloc,
				memory.HeapInuse,
				memory.HeapObjects,
				memory.HeapReleased,
				memory.HeapSys,
			); err != nil {
				return errors.New("resource probe acknowledgement failed")
			}
		case "snapshot\n":
			snapshot := c.activity.Snapshot()
			grep := c.grepActivity.Snapshot()
			if _, err := fmt.Fprintf(c.ack, "snapshot %d %d %d %d %d\n", snapshot.Total, snapshot.Active, grep.Total, grep.Active, grep.InFlight); err != nil {
				return errors.New("resource probe acknowledgement failed")
			}
		default:
			return errors.New("resource probe command was invalid")
		}
	}
}

func (c *Controller) Close() error {
	if c == nil {
		return nil
	}
	var first error
	c.close.Do(func() {
		if c.command != nil {
			first = c.command.Close()
		}
		if c.ack != nil {
			if err := c.ack.Close(); first == nil {
				first = err
			}
		}
	})
	return first
}
