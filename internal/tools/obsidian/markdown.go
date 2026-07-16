package obsidian

import (
	"errors"
	"io"
	"sort"
	"sync"
	"unicode/utf8"
)

var (
	ErrMarkdownInputTooLarge = errors.New("markdown source exceeds the fixed complexity limit")
	ErrMarkdownInvalidUTF8   = errors.New("markdown source is not valid UTF-8")
)

// PhysicalLine is a byte range in the original Markdown source. Start is
// inclusive. ContentEnd excludes a trailing LF or CRLF terminator, and End is
// exclusive and includes that terminator when present.
type PhysicalLine struct {
	Start      int
	ContentEnd int
	End        int
}

// MarkdownSource is a preflighted, immutable Markdown source. Callers must not
// mutate the byte slice passed to NewMarkdownSource while this value is in use.
// Structural parsing is lazy so content and frontmatter reads never construct
// a Markdown AST.
type MarkdownSource struct {
	source []byte
	lines  []PhysicalLine

	structureOnce sync.Once
	structure     markdownStructure
}

// LoadMarkdownSource reads at most the accepted byte ceiling plus one sentinel
// byte, then performs line-complexity and UTF-8 validation before any Markdown
// parser can run.
func LoadMarkdownSource(r io.Reader) (*MarkdownSource, error) {
	source, err := io.ReadAll(io.LimitReader(r, MaxMarkdownSourceBytes+1))
	if err != nil {
		return nil, err
	}
	return NewMarkdownSource(source)
}

// LoadMarkdownSourceSized uses the descriptor-observed regular-file size to
// reject an already oversized source without allocating or reading it and to
// avoid io.ReadAll's geometric buffer growth for accepted near-limit files.
// One sentinel byte still detects growth after the size observation; the
// caller's descriptor revalidation owns the final source-change decision.
func LoadMarkdownSourceSized(r io.Reader, observedSize int64) (*MarkdownSource, error) {
	if observedSize < 0 || observedSize > MaxMarkdownSourceBytes {
		return nil, ErrMarkdownInputTooLarge
	}
	source := make([]byte, int(observedSize)+1)
	n, err := io.ReadFull(r, source)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, err
	}
	return NewMarkdownSource(source[:n])
}

// NewMarkdownSource preflights an already loaded source. Limit errors take
// precedence over UTF-8 errors so over-cap input is rejected before parsing or
// text interpretation.
func NewMarkdownSource(source []byte) (*MarkdownSource, error) {
	if len(source) > MaxMarkdownSourceBytes {
		return nil, ErrMarkdownInputTooLarge
	}

	lines, ok := indexPhysicalLines(source, MaxMarkdownSourceLines)
	if !ok {
		return nil, ErrMarkdownInputTooLarge
	}
	if !utf8.Valid(source) {
		return nil, ErrMarkdownInvalidUTF8
	}

	return &MarkdownSource{source: source, lines: lines}, nil
}

// Bytes returns the original, source-preserving bytes. The returned slice is
// immutable by contract.
func (s *MarkdownSource) Bytes() []byte {
	return s.source
}

// LineCount returns the number of physical source lines. A non-empty final line
// without a terminator counts as one; an empty source has zero lines.
func (s *MarkdownSource) LineCount() int {
	return len(s.lines)
}

// Line returns the one-based physical line range.
func (s *MarkdownSource) Line(number int) (PhysicalLine, bool) {
	if number < 1 || number > len(s.lines) {
		return PhysicalLine{}, false
	}
	return s.lines[number-1], true
}

// PhysicalLines returns a copy of the source's line-offset table.
func (s *MarkdownSource) PhysicalLines() []PhysicalLine {
	return append([]PhysicalLine(nil), s.lines...)
}

func indexPhysicalLines(source []byte, maximum int) ([]PhysicalLine, bool) {
	lines := make([]PhysicalLine, 0, min(len(source)/32+1, maximum))
	start := 0
	for i, b := range source {
		if b != '\n' {
			continue
		}
		contentEnd := i
		if contentEnd > start && source[contentEnd-1] == '\r' {
			contentEnd--
		}
		lines = append(lines, PhysicalLine{Start: start, ContentEnd: contentEnd, End: i + 1})
		if len(lines) > maximum {
			return nil, false
		}
		start = i + 1
	}
	if start < len(source) {
		lines = append(lines, PhysicalLine{Start: start, ContentEnd: len(source), End: len(source)})
		if len(lines) > maximum {
			return nil, false
		}
	}
	return lines, true
}

func (s *MarkdownSource) lineIndexAt(offset int) int {
	if len(s.lines) == 0 {
		return -1
	}
	if offset < 0 {
		return 0
	}
	if offset >= len(s.source) {
		return len(s.lines) - 1
	}
	i := sort.Search(len(s.lines), func(i int) bool {
		return s.lines[i].End > offset
	})
	if i == len(s.lines) {
		return len(s.lines) - 1
	}
	return i
}

func (s *MarkdownSource) selectionLines(start, end int) (int, int) {
	if len(s.lines) == 0 {
		return 1, 0
	}
	startLine := s.lineIndexAt(start) + 1
	if end <= start {
		return startLine, startLine
	}
	return startLine, s.lineIndexAt(end-1) + 1
}
