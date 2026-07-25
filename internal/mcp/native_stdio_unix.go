//go:build unix

package mcp

import (
	"errors"
	"io"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// pollWriteCloser writes directly to a pollable fd. Native document output is
// potentially large, so a blocked stdout must not leave a payload retained.
type pollWriteCloser struct {
	fd       int
	timeout  time.Duration
	restore  bool
	deadline time.Time
}

func (w *pollWriteCloser) beginResponse() { w.deadline = time.Now().Add(w.timeout) }
func (w *pollWriteCloser) endResponse()   { w.deadline = time.Time{} }

func newPollWriteCloser(file *os.File, timeout time.Duration) (*pollWriteCloser, error) {
	if file == nil {
		return nil, errors.New("native document stdio requires stdout")
	}
	fd := int(file.Fd())
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil {
		return nil, err
	}
	if err := unix.SetNonblock(fd, true); err != nil {
		return nil, err
	}
	return &pollWriteCloser{fd: fd, timeout: timeout, restore: flags&unix.O_NONBLOCK == 0}, nil
}

func (w *pollWriteCloser) Write(data []byte) (int, error) {
	written := 0
	deadline := w.deadline
	if deadline.IsZero() {
		deadline = time.Now().Add(w.timeout)
	}
	for len(data) > 0 {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return written, os.ErrDeadlineExceeded
		}
		milliseconds := int(remaining.Milliseconds())
		if milliseconds < 1 {
			milliseconds = 1
		}
		fds := []unix.PollFd{{Fd: int32(w.fd), Events: unix.POLLOUT}}
		if _, err := unix.Poll(fds, milliseconds); err != nil {
			return written, err
		}
		if fds[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
			return written, io.ErrClosedPipe
		}
		n, err := unix.Write(w.fd, data)
		if n > 0 {
			written += n
			data = data[n:]
		}
		if err == unix.EAGAIN || err == unix.EWOULDBLOCK {
			continue
		}
		if err != nil {
			return written, err
		}
	}
	return written, nil
}

func (w *pollWriteCloser) Close() error {
	if w.restore {
		return unix.SetNonblock(w.fd, false)
	}
	return nil
}
