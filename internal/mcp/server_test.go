package mcp

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestLineLimitReadCloserRejectsOversizedMessage(t *testing.T) {
	reader := newLineLimitReadCloser(io.NopCloser(strings.NewReader("abcdx\n")), 4)
	buf := make([]byte, 2)
	var got strings.Builder

	for {
		n, err := reader.Read(buf)
		got.Write(buf[:n])
		if err == nil {
			continue
		}
		if !errors.Is(err, errMessageTooLarge) {
			t.Fatalf("Read() error = %v, want errMessageTooLarge", err)
		}
		if got.String() != "abcd" {
			t.Fatalf("bytes before limit error = %q, want %q", got.String(), "abcd")
		}
		return
	}
}

func TestLineLimitReadCloserResetsAfterNewline(t *testing.T) {
	reader := newLineLimitReadCloser(io.NopCloser(strings.NewReader("abcd\nabcd\n")), 5)
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if string(data) != "abcd\nabcd\n" {
		t.Fatalf("ReadAll() = %q", data)
	}
}
