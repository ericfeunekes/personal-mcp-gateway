package obsidian

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/sys/unix"

	"personal-mcp-gateway/internal/fsx"
	"personal-mcp-gateway/internal/limits"
)

var (
	errInvalidRegex = errors.New(InvalidRegexCode)
	errInvalidUTF8  = errors.New(InvalidUTF8Code)
	errUnsupported  = errors.New(UnsupportedFileCode)
	errLineTooLarge = errors.New("grep line too large")
	errLineBudget   = errors.New("grep line exceeds remaining byte budget")
)

var grepPrefixDomain = []byte("personal-mcp-gateway/obsidian/grep-prefix/v1\x00")

// Literal negative scans can validate and reject a complete ordinary file in
// one allocation. Keeping this bounded at 256 KiB lets the worker pool avoid
// per-line framing for the common vault-note size while retaining a small,
// request-bounded working set.
const grepFastFileBytes = 256 * 1024

type normalizedGrepQuery struct {
	Pattern       string `json:"pattern"`
	Path          string `json:"path"`
	Regex         bool   `json:"regex"`
	CaseSensitive bool   `json:"case_sensitive"`
	ContextLines  int    `json:"context_lines"`
	Limit         int    `json:"limit"`
	MaxFiles      int    `json:"max_files"`
	MaxBytes      int64  `json:"max_bytes"`
}

type grepCursorPosition struct {
	NFC    string `json:"nfc"`
	Stored string `json:"stored"`
}

type grepPartialCursor struct {
	Fingerprint   string `json:"fingerprint"`
	ResumeOffset  int64  `json:"resume_offset"`
	ResumeLine    int    `json:"resume_line"`
	ContextOffset int64  `json:"context_offset"`
	ContextLine   int    `json:"context_line"`
}

type grepCursorState struct {
	Boundary grepCursorPosition `json:"boundary"`
	Prefix   string             `json:"prefix"`
	Partial  *grepPartialCursor `json:"partial,omitempty"`
}

type grepLine struct {
	number           int
	start            int64
	end              int64
	text             string
	large            []byte
	mapped           *mappedGrepLine
	textTruncated    bool
	textStartColumn  int
	textEndColumn    int
	contextText      string
	contextEndColumn int
	lineBytes        int64
	matchKnown       bool
	matched          bool
	matchColumn      int
	occurrences      int
}

type grepCheckpoint struct {
	position fsx.Position
	prefix   [sha256.Size]byte
	partial  grepPartialCursor
}

type pendingGrepMatch struct {
	match            GrepMatch
	occurrencesReady bool
	remaining        int
	emission         grepCheckpoint
	retry            grepCheckpoint
}

type grepStop struct {
	reason CursorStop
	state  grepCursorState
}

type grepRun struct {
	tools          *Tools
	query          normalizedGrepQuery
	canonical      string
	queryHash      CursorQueryHash
	re             *regexp.Regexp
	work           CoverageWork
	matches        []GrepMatch
	emissionStates []grepCursorState
	prefix         [sha256.Size]byte
	lastFull       *fsx.Position
	stop           *grepStop
	resume         *grepCursorState
	boundarySeen   bool
	pageBytes      int64
}

// Grep streams Markdown lines in canonical walk order. It does not build an
// index or materialize a whole-vault file list.
func (t *Tools) Grep(ctx context.Context, _ *sdk.CallToolRequest, input GrepInput) (*sdk.CallToolResult, GrepOutput, error) {
	return t.grep(ctx, input, nil)
}

func (t *Tools) grep(ctx context.Context, input GrepInput, hooks *grepConcurrentHooks) (*sdk.CallToolResult, GrepOutput, error) {
	toolCtx, cancel := context.WithTimeout(ctx, limits.ToolOperationTimeout)
	defer cancel()

	query, re, err := normalizeGrep(input)
	if err != nil {
		return grepErrorOutput("", nil, CoverageWork{}, err)
	}
	queryHash, err := RetrievalQueryHash(ToolGrep, query)
	if err != nil {
		return grepErrorOutput("", nil, CoverageWork{}, err)
	}
	run := &grepRun{
		tools:     t,
		query:     query,
		queryHash: queryHash,
		re:        re,
		prefix:    sha256.Sum256(grepPrefixDomain),
	}
	if input.Cursor != "" {
		state, cursorErr := DecodeCursorState[grepCursorState](t.vault, input.Cursor, ToolGrep, queryHash)
		if cursorErr != nil {
			return grepErrorOutput("", nil, run.work, cursorErr)
		}
		if cursorErr := validateGrepCursorState(state); cursorErr != nil {
			return grepErrorOutput("", nil, run.work, cursorErr)
		}
		run.resume = &state
	}

	scope := input.Path
	if scope == "" {
		scope = "."
	}
	resolved, err := t.vault.Resolve(toolCtx, input.Base, scope)
	if err != nil {
		if run.resume != nil && !terminatingRetrievalError(err) {
			return grepErrorOutput("", nil, run.work, ErrCursorStale)
		}
		return grepErrorOutput("", nil, run.work, err)
	}
	canonical := resolved.Rel
	if !resolved.Exists {
		if run.resume != nil {
			return grepErrorOutput(canonical, nil, run.work, ErrCursorStale)
		}
		return grepErrorOutput(canonical, nil, run.work, &fsx.Error{Code: fsx.CodeNotFound})
	}
	if resolved.Kind == fsx.KindSymlink {
		if run.resume != nil {
			return grepErrorOutput(canonical, nil, run.work, ErrCursorStale)
		}
		return grepErrorOutput(canonical, nil, run.work, &fsx.Error{Code: fsx.CodeSymlinkDenied})
	}
	if resolved.Kind == fsx.KindFile && !isMarkdownPath(resolved.Rel) {
		if run.resume != nil {
			return grepErrorOutput(canonical, nil, run.work, ErrCursorStale)
		}
		return grepErrorOutput(canonical, nil, run.work, errUnsupported)
	}
	if resolved.Kind != fsx.KindFile && resolved.Kind != fsx.KindDir {
		if run.resume != nil {
			return grepErrorOutput(canonical, nil, run.work, ErrCursorStale)
		}
		return grepErrorOutput(canonical, nil, run.work, errUnsupported)
	}
	run.canonical = canonical

	walkErr := run.walkConcurrentWithHooks(toolCtx, hooks)
	if walkErr != nil {
		if run.resume != nil && !terminatingRetrievalError(walkErr) && !fsx.IsCode(walkErr, fsx.CodeSourceChanged) &&
			!errors.Is(walkErr, ErrCursorStale) && !errors.Is(walkErr, ErrResponseTooLarge) {
			walkErr = ErrCursorStale
		}
		return grepErrorOutput(canonical, run.matches, run.work, walkErr)
	}
	if run.resume != nil && !run.boundarySeen {
		return grepErrorOutput(canonical, run.matches, run.work, ErrCursorStale)
	}
	if run.stop != nil {
		return run.cursorOutput(canonical, run.stop.reason, run.stop.state)
	}
	out := GrepOutput{
		OK:       true,
		Path:     canonical,
		Matches:  nonNilMatches(run.matches),
		Coverage: NewCompleteCoverage(run.work),
	}
	if err := requireGrepOutputFits(out); err != nil {
		return grepErrorOutput(canonical, nil, run.work, err)
	}
	return successCallResult(), out, nil
}

func normalizeGrep(input GrepInput) (normalizedGrepQuery, *regexp.Regexp, error) {
	if input.Pattern == "" {
		return normalizedGrepQuery{}, nil, errInvalidRegex
	}
	if len([]byte(input.Pattern)) > MaxGrepPatternBytes {
		return normalizedGrepQuery{}, nil, &fsx.Error{Code: fsx.CodeInputTooLarge}
	}
	if !utf8.ValidString(input.Pattern) {
		return normalizedGrepQuery{}, nil, errInvalidUTF8
	}
	regexMode := true
	if input.Regex != nil {
		regexMode = *input.Regex
	}
	contextLines := DefaultGrepContextLines
	if input.ContextLines != nil {
		contextLines = *input.ContextLines
	}
	limit := input.Limit
	if limit == 0 {
		limit = DefaultGrepLimit
	}
	maxFiles := input.MaxFiles
	if maxFiles == 0 {
		maxFiles = DefaultGrepMaxFiles
	}
	maxBytes := input.MaxBytes
	if maxBytes == 0 {
		maxBytes = DefaultGrepMaxBytes
	}
	if contextLines < 0 || contextLines > MaxGrepContextLines || limit < 1 || limit > MaxGrepLimit ||
		maxFiles < 1 || maxFiles > MaxGrepMaxFiles || maxBytes < 1 || maxBytes > MaxGrepMaxBytes {
		return normalizedGrepQuery{}, nil, &fsx.Error{Code: fsx.CodeLimitExceeded}
	}

	pattern := input.Pattern
	if !regexMode {
		pattern = regexp.QuoteMeta(pattern)
	}
	if !input.CaseSensitive {
		pattern = "(?i:" + pattern + ")"
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return normalizedGrepQuery{}, nil, errInvalidRegex
	}
	scope := input.Path
	if scope == "" {
		scope = "."
	}
	normalizedScope, err := fsx.NormalizePath(input.Base, scope)
	if err != nil {
		return normalizedGrepQuery{}, nil, err
	}
	query := normalizedGrepQuery{
		Pattern:       input.Pattern,
		Path:          normalizedScope,
		Regex:         regexMode,
		CaseSensitive: input.CaseSensitive,
		ContextLines:  contextLines,
		Limit:         limit,
		MaxFiles:      maxFiles,
		MaxBytes:      maxBytes,
	}
	return query, re, nil
}

func (g *grepRun) newGrepLineReader(ctx context.Context, file *fsx.File, size, offset int64, line int, remaining int64) (*grepLineReader, bool, error) {
	if offset != 0 || g.query.Regex || !g.query.CaseSensitive || size > remaining || size > grepFastFileBytes {
		return newGrepLineReader(file, size, offset, line, remaining, g.oversizedLiteralRegexp()), false, nil
	}
	data := make([]byte, int(size))
	read := 0
	for read < len(data) {
		n, err := file.Read(ctx, data[read:])
		read += n
		if err != nil && !(errors.Is(err, io.EOF) && read == len(data)) {
			return nil, false, err
		}
		if n == 0 && read < len(data) {
			return nil, false, &fsx.Error{Code: fsx.CodeSourceChanged}
		}
	}
	if !bytes.Contains(data, []byte(g.query.Pattern)) {
		if !utf8.Valid(data) {
			g.pageBytes += size
			g.work.BytesScanned = uint64(g.pageBytes)
			return nil, false, errInvalidUTF8
		}
		if err := fitContextError(ctx); err != nil {
			return nil, false, err
		}
		return nil, true, nil
	}
	return newBufferedGrepLineReader(file, size, line, remaining, data), false, nil
}

func (g *grepRun) oversizedLiteralRegexp() *regexp.Regexp {
	if g.query.Regex {
		return nil
	}
	return g.re
}

func (g *grepRun) checkpointAdvances(checkpoint grepCheckpoint) bool {
	if g.resume == nil {
		return g.lastFull != nil || checkpoint.partial.ResumeOffset > 0
	}
	order := compareGrepPosition(checkpoint.position, cursorPositionToFSX(g.resume.Boundary))
	if order > 0 {
		return true
	}
	if order < 0 || g.resume.Partial == nil {
		return false
	}
	return checkpoint.partial.ResumeOffset > g.resume.Partial.ResumeOffset
}

func (g *grepRun) fullBoundaryAdvances(position fsx.Position) bool {
	if g.resume == nil {
		return true
	}
	order := compareGrepPosition(position, cursorPositionToFSX(g.resume.Boundary))
	if order != 0 {
		return order > 0
	}
	// Replacing a partial-file cursor with a full-file boundary advances past
	// the remainder of that file even though the canonical path is identical.
	return g.resume.Partial != nil
}

func (g *grepRun) emit(ctx context.Context, candidate pendingGrepMatch) (bool, error) {
	if err := fitContextError(ctx); err != nil {
		return false, err
	}
	prospective := append(append([]GrepMatch(nil), g.matches...), candidate.match)
	state := candidate.emission.state()
	stopReason := CursorStopResponseLimit
	if len(prospective) >= g.query.Limit {
		stopReason = CursorStopResultLimit
	}
	cursor, err := EncodeCursorState(g.tools.vault, ToolGrep, g.queryHash, state)
	if err != nil {
		return false, err
	}
	coverage, err := NewCursorCoverageWithScope(g.work, stopReason, cursor, false)
	if err != nil {
		return false, err
	}
	out := GrepOutput{OK: true, Path: g.canonical, Matches: prospective, Truncated: true, Coverage: coverage}
	if err := requireGrepOutputFits(out); err != nil {
		return g.stopForUnfitGrepCandidate()
	}
	if err := fitContextError(ctx); err != nil {
		return false, err
	}
	if !candidate.occurrencesReady {
		candidate.match.Occurrences = len(g.re.FindAllStringIndex(candidate.match.Text, -1))
	}
	if err := fitContextError(ctx); err != nil {
		return false, err
	}
	prospective[len(prospective)-1] = candidate.match
	out.Matches = prospective
	if err := requireGrepOutputFits(out); err != nil {
		return g.stopForUnfitGrepCandidate()
	}
	g.matches = prospective
	g.emissionStates = append(g.emissionStates, state)
	if len(g.matches) >= g.query.Limit {
		g.stop = &grepStop{reason: CursorStopResultLimit, state: state}
		return true, nil
	}
	return false, nil
}

func (g *grepRun) stopForUnfitGrepCandidate() (bool, error) {
	if len(g.matches) == 0 {
		return false, ErrResponseTooLarge
	}
	g.stop = &grepStop{reason: CursorStopResponseLimit, state: g.emissionStates[len(g.emissionStates)-1]}
	return true, nil
}

func (g *grepRun) stopAction(err error) (fsx.WalkAction, error) {
	if err != nil {
		return fsx.WalkStop, err
	}
	return fsx.WalkStop, nil
}

func (g *grepRun) partialCheckpoint(position fsx.Position, fingerprint fsx.SourceFingerprint, resumeOffset int64, resumeLine int, contextOffset int64, contextLine int) grepCheckpoint {
	return grepCheckpoint{
		position: position,
		prefix:   g.prefix,
		partial: grepPartialCursor{
			Fingerprint:   encodeDigest(fingerprint[:]),
			ResumeOffset:  resumeOffset,
			ResumeLine:    resumeLine,
			ContextOffset: contextOffset,
			ContextLine:   contextLine,
		},
	}
}

func (c grepCheckpoint) state() grepCursorState {
	partial := c.partial
	return grepCursorState{
		Boundary: grepCursorPosition{NFC: c.position.NFC, Stored: c.position.Stored},
		Prefix:   encodeDigest(c.prefix[:]),
		Partial:  &partial,
	}
}

func (g *grepRun) fullBoundaryState(position fsx.Position) grepCursorState {
	return grepCursorState{
		Boundary: grepCursorPosition{NFC: position.NFC, Stored: position.Stored},
		Prefix:   encodeDigest(g.prefix[:]),
	}
}

func (g *grepRun) cursorOutput(path string, reason CursorStop, state grepCursorState) (*sdk.CallToolResult, GrepOutput, error) {
	cursor, err := EncodeCursorState(g.tools.vault, ToolGrep, g.queryHash, state)
	if err != nil {
		return grepErrorOutput(path, g.matches, g.work, err)
	}
	coverage, err := NewCursorCoverageWithScope(g.work, reason, cursor, false)
	if err != nil {
		return grepErrorOutput(path, g.matches, g.work, err)
	}
	out := GrepOutput{OK: true, Path: path, Matches: nonNilMatches(g.matches), Truncated: true, Coverage: coverage}
	if err := requireGrepOutputFits(out); err != nil {
		return grepErrorOutput(path, nil, g.work, err)
	}
	return successCallResult(), out, nil
}

type grepLineReader struct {
	file             *fsx.File
	size             int64
	offset           int64
	line             int
	remaining        int64
	pending          []byte
	pendingPos       int
	newBytes         int64
	mapped           []*mappedGrepLine
	oversizedLiteral *regexp.Regexp
}

const (
	grepHeapLineBytes     = 64 * 1024
	grepInitialLineBytes  = 256
	grepEvidenceTextBytes = 8 * 1024
)

type mappedGrepLine struct {
	data     []byte
	released bool
}

func (m *mappedGrepLine) release() {
	if m == nil || m.released {
		return
	}
	m.released = true
	_ = unix.Munmap(m.data)
}

type grepLineBuffer struct {
	heap   []byte
	mapped *mappedGrepLine
	length int
}

func newGrepLineBuffer() grepLineBuffer {
	return grepLineBuffer{}
}

func (b *grepLineBuffer) bytes() []byte {
	if b.mapped != nil {
		return b.mapped.data[:b.length]
	}
	return b.heap
}

func (r *grepLineReader) appendLine(buffer *grepLineBuffer, fragment []byte) error {
	if buffer.mapped == nil && len(buffer.heap)+len(fragment) <= grepHeapLineBytes {
		if buffer.heap == nil {
			buffer.heap = make([]byte, 0, max(grepInitialLineBytes, len(fragment)))
		}
		buffer.heap = append(buffer.heap, fragment...)
		buffer.length = len(buffer.heap)
		return nil
	}
	if buffer.mapped == nil {
		mapped, err := unix.Mmap(-1, 0, MaxGrepPhysicalLineBytes+1, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_ANON|unix.MAP_PRIVATE)
		if err != nil {
			return err
		}
		buffer.mapped = &mappedGrepLine{data: mapped}
		r.mapped = append(r.mapped, buffer.mapped)
		copy(mapped, buffer.heap)
		buffer.length = len(buffer.heap)
		buffer.heap = nil
	}
	if buffer.length+len(fragment) > len(buffer.mapped.data) {
		return errLineTooLarge
	}
	copy(buffer.mapped.data[buffer.length:], fragment)
	buffer.length += len(fragment)
	return nil
}

func (r *grepLineReader) close() {
	for _, mapped := range r.mapped {
		mapped.release()
	}
}

func newGrepLineReader(file *fsx.File, size, offset int64, line int, remaining int64, oversizedLiteral *regexp.Regexp) *grepLineReader {
	return &grepLineReader{file: file, size: size, offset: offset, line: line, remaining: remaining, oversizedLiteral: oversizedLiteral}
}

func newBufferedGrepLineReader(file *fsx.File, size int64, line int, remaining int64, data []byte) *grepLineReader {
	return &grepLineReader{
		file:      file,
		size:      size,
		line:      line,
		remaining: remaining - int64(len(data)),
		pending:   data,
		newBytes:  int64(len(data)),
	}
}

func (r *grepLineReader) next(ctx context.Context) (grepLine, error) {
	start, lineNumber := r.offset, r.line
	line := newGrepLineBuffer()
	for {
		if r.pendingPos < len(r.pending) {
			fragment := r.pending[r.pendingPos:]
			if newline := bytes.IndexByte(fragment, '\n'); newline >= 0 {
				newline++
				if line.length == 0 {
					r.pendingPos += newline
					r.offset += int64(newline)
					if newline > MaxGrepPhysicalLineBytes {
						return grepLine{}, errLineTooLarge
					}
					text := fragment[:newline-1]
					if len(text) > 0 && text[len(text)-1] == '\r' {
						text = text[:len(text)-1]
					}
					r.line++
					return grepLine{number: lineNumber, start: start, end: r.offset, text: string(text)}, nil
				}
				if r.oversizedLiteral != nil && line.length+newline > MaxGrepPhysicalLineBytes+1 {
					take := MaxGrepPhysicalLineBytes + 1 - line.length
					if err := r.appendLine(&line, fragment[:take]); err != nil {
						return grepLine{}, err
					}
					r.pendingPos += take
					r.offset += int64(take)
					return r.finishOversizedLiteral(ctx, start, lineNumber, &line, false)
				}
				if err := r.appendLine(&line, fragment[:newline]); err != nil {
					return grepLine{}, err
				}
				r.pendingPos += newline
				r.offset += int64(newline)
				if line.length > MaxGrepPhysicalLineBytes {
					if r.oversizedLiteral != nil {
						return r.finishOversizedLiteral(ctx, start, lineNumber, &line, true)
					}
					return grepLine{}, errLineTooLarge
				}
				text := line.bytes()[:line.length-1]
				if len(text) > 0 && text[len(text)-1] == '\r' {
					text = text[:len(text)-1]
				}
				r.line++
				if line.mapped != nil {
					return grepLine{number: lineNumber, start: start, end: r.offset, large: text, mapped: line.mapped}, nil
				}
				return grepLine{number: lineNumber, start: start, end: r.offset, text: string(text)}, nil
			}
			if r.oversizedLiteral != nil && line.length+len(fragment) > MaxGrepPhysicalLineBytes+1 {
				take := MaxGrepPhysicalLineBytes + 1 - line.length
				if err := r.appendLine(&line, fragment[:take]); err != nil {
					return grepLine{}, err
				}
				r.pendingPos += take
				r.offset += int64(take)
				return r.finishOversizedLiteral(ctx, start, lineNumber, &line, false)
			}
			if err := r.appendLine(&line, fragment); err != nil {
				return grepLine{}, err
			}
			r.offset += int64(len(fragment))
			r.pendingPos = len(r.pending)
			if line.length > MaxGrepPhysicalLineBytes {
				if r.oversizedLiteral != nil {
					return r.finishOversizedLiteral(ctx, start, lineNumber, &line, false)
				}
				return grepLine{}, errLineTooLarge
			}
		}
		if r.offset >= r.size {
			if line.length == 0 {
				return grepLine{}, io.EOF
			}
			r.line++
			if line.mapped != nil {
				return grepLine{number: lineNumber, start: start, end: r.offset, large: line.bytes(), mapped: line.mapped}, nil
			}
			return grepLine{number: lineNumber, start: start, end: r.offset, text: string(line.heap)}, nil
		}
		if r.remaining <= 0 {
			return grepLine{}, errLineBudget
		}
		// Never read more than the one-byte sentinel needed to prove that this
		// physical line exceeds the fixed cap.
		lineAllowance := int64(MaxGrepPhysicalLineBytes + 1 - line.length)
		readSize := min(int64(32*1024), r.remaining, r.size-r.offset, lineAllowance)
		buffer := make([]byte, int(readSize))
		n, err := r.file.Read(ctx, buffer)
		if n > 0 {
			r.remaining -= int64(n)
			r.newBytes += int64(n)
			r.pending = buffer[:n]
			r.pendingPos = 0
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return grepLine{}, err
		}
		if n == 0 {
			if errors.Is(err, io.EOF) && r.offset >= r.size {
				continue
			}
			return grepLine{}, &fsx.Error{Code: fsx.CodeSourceChanged}
		}
	}
}

func (r *grepLineReader) takeNewBytes() int64 {
	value := r.newBytes
	r.newBytes = 0
	return value
}

func validateGrepCursorState(state grepCursorState) error {
	if state.Boundary.NFC == "" || state.Boundary.Stored == "" || len(state.Boundary.NFC) > 4096 || len(state.Boundary.Stored) > 4096 ||
		!utf8.ValidString(state.Boundary.NFC) || !utf8.ValidString(state.Boundary.Stored) {
		return ErrCursorInvalid
	}
	if _, err := decodeDigest(state.Prefix); err != nil {
		return ErrCursorInvalid
	}
	if state.Partial == nil {
		return nil
	}
	if _, err := decodeDigest(state.Partial.Fingerprint); err != nil {
		return ErrCursorInvalid
	}
	if state.Partial.ResumeOffset < 0 || state.Partial.ContextOffset < 0 || state.Partial.ContextOffset > state.Partial.ResumeOffset ||
		state.Partial.ResumeLine < 1 || state.Partial.ContextLine < 1 || state.Partial.ContextLine > state.Partial.ResumeLine {
		return ErrCursorInvalid
	}
	return nil
}

func extendGrepPrefix(previous [sha256.Size]byte, position fsx.Position, fingerprint fsx.SourceFingerprint) [sha256.Size]byte {
	h := sha256.New()
	_, _ = h.Write(grepPrefixDomain)
	_, _ = h.Write(previous[:])
	writeGrepFrame(h, []byte(position.NFC))
	writeGrepFrame(h, []byte(position.Stored))
	writeGrepFrame(h, fingerprint[:])
	var out [sha256.Size]byte
	copy(out[:], h.Sum(nil))
	return out
}

func writeGrepFrame(w io.Writer, value []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = w.Write(size[:])
	_, _ = w.Write(value)
}

func appendGrepRing(ring []grepLine, line grepLine, capacity int) []grepLine {
	if capacity == 0 {
		line.mapped.release()
		return ring[:0]
	}
	if len(ring) == capacity {
		ring[0].mapped.release()
		copy(ring, ring[1:])
		ring = ring[:capacity-1]
	}
	return append(ring, line)
}

func grepRingHasLargeLine(ring []grepLine) bool {
	for _, line := range ring {
		if line.large != nil {
			return true
		}
	}
	return false
}

func grepResumeContext(before []grepLine, current grepLine, contextLines int) (int64, int) {
	if contextLines == 0 {
		return current.end, current.number + 1
	}
	combined := append(append([]grepLine(nil), before...), current)
	if len(combined) > contextLines {
		combined = combined[len(combined)-contextLines:]
	}
	return combined[0].start, combined[0].number
}

func requireGrepOutputFits(out GrepOutput) error {
	structured, _, err := StructuredOutputFits(out)
	if err != nil {
		return err
	}
	complete, _, err := CompleteSDKResultFits(successCallResult(), out)
	if err != nil {
		return err
	}
	if !structured || !complete {
		return ErrResponseTooLarge
	}
	return nil
}

func grepErrorOutput(path string, matches []GrepMatch, work CoverageWork, err error) (*sdk.CallToolResult, GrepOutput, error) {
	code := grepErrorCode(err)
	coverage, _ := NewRestartCoverage(work, restartStopFor(err))
	if errors.Is(err, ErrCursorStale) {
		coverage, _ = NewRestartCoverage(work, RestartStopSourceChange)
	}
	return errorCallResult(), GrepOutput{
		OK:        false,
		Path:      path,
		Matches:   nonNilMatches(matches),
		Truncated: true,
		Coverage:  coverage,
		Error:     &ToolError{Code: code, Message: grepSanitizedMessage(code)},
	}, nil
}

func grepErrorCode(err error) string {
	switch {
	case errors.Is(err, errInvalidRegex):
		return InvalidRegexCode
	case errors.Is(err, errInvalidUTF8):
		return InvalidUTF8Code
	case errors.Is(err, errUnsupported), fsx.IsCode(err, fsx.CodeNotFile):
		return UnsupportedFileCode
	default:
		return errorCode(err)
	}
}

func grepSanitizedMessage(code string) string {
	switch code {
	case InvalidRegexCode:
		return "regular expression is invalid"
	case InvalidUTF8Code:
		return "source is not valid UTF-8"
	case UnsupportedFileCode:
		return "source is not a supported Markdown file"
	default:
		return sanitizedMessage(code)
	}
}

func grepContextError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return &fsx.Error{Code: fsx.CodeTimeout}
	}
	return &fsx.Error{Code: fsx.CodeCanceled}
}

func compareGrepPosition(left, right fsx.Position) int {
	if left.NFC < right.NFC {
		return -1
	}
	if left.NFC > right.NFC {
		return 1
	}
	return strings.Compare(left.Stored, right.Stored)
}

func cursorPositionToFSX(position grepCursorPosition) fsx.Position {
	return fsx.Position{NFC: position.NFC, Stored: position.Stored}
}

func isMarkdownPath(value string) bool {
	return strings.EqualFold(path.Ext(value), ".md")
}

func equalDigest(left, right []byte) bool {
	return len(left) == len(right) && string(left) == string(right)
}

func nonNilMatches(matches []GrepMatch) []GrepMatch {
	if matches == nil {
		return []GrepMatch{}
	}
	return matches
}
