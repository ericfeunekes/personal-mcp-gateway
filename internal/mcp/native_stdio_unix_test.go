//go:build unix

package mcp

import (
	"errors"
	"io"
	"os"
	"testing"
	"time"
)

func TestPollWriteCloserUsesOneAbsoluteResponseDeadline(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	bounded, err := newPollWriteCloser(write, 40*time.Millisecond)
	if err != nil {
		_ = write.Close()
		t.Fatal(err)
	}
	drained := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, read)
		close(drained)
	}()
	bounded.beginResponse()
	started := time.Now()
	var writeErr error
	for i := 0; i < 10; i++ {
		_, writeErr = bounded.Write(make([]byte, 1024))
		if writeErr != nil {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	bounded.endResponse()
	if !errors.Is(writeErr, os.ErrDeadlineExceeded) {
		t.Fatalf("trickle writes err=%v, want deadline exceeded", writeErr)
	}
	if elapsed := time.Since(started); elapsed > 150*time.Millisecond {
		t.Fatalf("absolute response deadline took %v", elapsed)
	}
	if err := bounded.Close(); err != nil {
		t.Fatal(err)
	}
	if err := write.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("pipe reader did not stop")
	}
}
