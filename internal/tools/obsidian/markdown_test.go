package obsidian

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestMarkdownSourcePreflightBoundaries(t *testing.T) {
	t.Run("exact byte ceiling", func(t *testing.T) {
		source, err := NewMarkdownSource(bytes.Repeat([]byte{'a'}, MaxMarkdownSourceBytes))
		if err != nil {
			t.Fatalf("NewMarkdownSource: %v", err)
		}
		if got := source.LineCount(); got != 1 {
			t.Fatalf("LineCount = %d, want 1", got)
		}
	})

	t.Run("byte ceiling plus one", func(t *testing.T) {
		_, err := NewMarkdownSource(bytes.Repeat([]byte{'a'}, MaxMarkdownSourceBytes+1))
		if !errors.Is(err, ErrMarkdownInputTooLarge) {
			t.Fatalf("error = %v, want ErrMarkdownInputTooLarge", err)
		}
	})

	t.Run("exact line ceiling", func(t *testing.T) {
		source, err := NewMarkdownSource(bytes.Repeat([]byte{'\n'}, MaxMarkdownSourceLines))
		if err != nil {
			t.Fatalf("NewMarkdownSource: %v", err)
		}
		if got := source.LineCount(); got != MaxMarkdownSourceLines {
			t.Fatalf("LineCount = %d, want %d", got, MaxMarkdownSourceLines)
		}
	})

	t.Run("line ceiling plus one", func(t *testing.T) {
		_, err := NewMarkdownSource(bytes.Repeat([]byte{'\n'}, MaxMarkdownSourceLines+1))
		if !errors.Is(err, ErrMarkdownInputTooLarge) {
			t.Fatalf("error = %v, want ErrMarkdownInputTooLarge", err)
		}
	})

	t.Run("invalid UTF-8", func(t *testing.T) {
		_, err := NewMarkdownSource([]byte{'o', 'k', '\n', 0xff})
		if !errors.Is(err, ErrMarkdownInvalidUTF8) {
			t.Fatalf("error = %v, want ErrMarkdownInvalidUTF8", err)
		}
	})

	t.Run("load reads a sentinel", func(t *testing.T) {
		_, err := LoadMarkdownSource(strings.NewReader(strings.Repeat("a", MaxMarkdownSourceBytes+1)))
		if !errors.Is(err, ErrMarkdownInputTooLarge) {
			t.Fatalf("error = %v, want ErrMarkdownInputTooLarge", err)
		}
	})
}

func TestMarkdownSourcePhysicalLineTablePreservesTerminators(t *testing.T) {
	source, err := NewMarkdownSource([]byte("one\r\ntwo\nthree"))
	if err != nil {
		t.Fatalf("NewMarkdownSource: %v", err)
	}

	want := []PhysicalLine{
		{Start: 0, ContentEnd: 3, End: 5},
		{Start: 5, ContentEnd: 8, End: 9},
		{Start: 9, ContentEnd: 14, End: 14},
	}
	got := source.PhysicalLines()
	if len(got) != len(want) {
		t.Fatalf("lines = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %#v, want %#v", i+1, got[i], want[i])
		}
	}

	selection, err := source.Select(SourceSelector{Kind: SourceSelectorContent, StartLine: 2})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if got, want := string(selection.Content), "two\nthree"; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
	if selection.StartLine != 2 || selection.EndLine != 3 {
		t.Fatalf("line range = %d..%d, want 2..3", selection.StartLine, selection.EndLine)
	}
}

func TestEmptyMarkdownContentIsAValidDefaultSelection(t *testing.T) {
	source, err := NewMarkdownSource(nil)
	if err != nil {
		t.Fatalf("NewMarkdownSource: %v", err)
	}
	selection, err := source.Select(SourceSelector{})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if selection.Content == nil || len(selection.Content) != 0 || selection.StartLine != 1 || selection.EndLine != 0 {
		t.Fatalf("selection = %#v", selection)
	}
}
