package obsidian

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"path"
	"strings"
	"unicode/utf8"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"personal-mcp-gateway/internal/fsx"
	"personal-mcp-gateway/internal/limits"
)

const (
	readCursorModeContent = "content"
	readCursorModeOutline = "outline"
)

type normalizedReadQuery struct {
	Path     string       `json:"path"`
	Selector ReadSelector `json:"selector"`
	MaxBytes int          `json:"max_bytes"`
}

type readCursorState struct {
	Mode         string `json:"mode"`
	Fingerprint  string `json:"fingerprint"`
	UnitStart    int    `json:"unit_start"`
	UnitEnd      int    `json:"unit_end"`
	NextByte     int    `json:"next_byte"`
	NextLine     int    `json:"next_line"`
	TotalLines   int    `json:"total_lines"`
	NextOutline  int    `json:"next_outline"`
	OutlineCount int    `json:"outline_count"`
}

type contextFileReader struct {
	ctx   context.Context
	file  *fsx.File
	bytes uint64
}

// readPageMeta is the private orchestration evidence read_many needs without
// widening the public result. File-backed failures keep the stamp observed by
// the same confined open; SelectedBytes counts only source evidence actually
// returned on this page, not parser/file I/O.
type readPageMeta struct {
	FileBacked    bool
	Fingerprint   string
	SelectedBytes int
}

func (r *contextFileReader) Read(p []byte) (int, error) {
	n, err := r.file.Read(r.ctx, p)
	r.bytes += uint64(n)
	return n, err
}

func (t *Tools) Read(ctx context.Context, _ *sdk.CallToolRequest, input ReadInput) (*sdk.CallToolResult, ReadOutput, error) {
	toolCtx, cancel := context.WithTimeout(ctx, limits.ToolOperationTimeout)
	defer cancel()
	return t.readPage(toolCtx, input)
}

func (t *Tools) readPage(ctx context.Context, input ReadInput) (*sdk.CallToolResult, ReadOutput, error) {
	result, out, _, err := t.readPageWithMeta(ctx, input)
	return result, out, err
}

func (t *Tools) readPageWithMeta(ctx context.Context, input ReadInput) (*sdk.CallToolResult, ReadOutput, readPageMeta, error) {
	meta := readPageMeta{}
	result, out, err := t.readPageCore(ctx, input, &meta)
	return result, out, meta, err
}

// readPage is the single read engine used directly by read and by the ordered
// read_many orchestrator. It returns only domain results, never nested MCP
// serialization.
func (t *Tools) readPageCore(ctx context.Context, input ReadInput, meta *readPageMeta) (*sdk.CallToolResult, ReadOutput, error) {
	selector, publicSelector, err := normalizeReadSelector(input.Selector)
	if err != nil {
		return readErrorResult(err, CoverageWork{})
	}
	maxBytes, err := effectiveReadMaxBytes(input.MaxBytes)
	if err != nil {
		return readErrorResult(err, CoverageWork{})
	}

	requestPath, err := fsx.NormalizePath(input.Base, input.Path)
	if err != nil {
		return readErrorResult(err, CoverageWork{})
	}
	query := normalizedReadQuery{Path: requestPath, Selector: publicSelector, MaxBytes: maxBytes}
	queryHash, err := RetrievalQueryHash(ToolRead, query)
	if err != nil {
		return readErrorResult(err, CoverageWork{})
	}

	var continuedState *readCursorState
	if input.Cursor != "" {
		state, err := DecodeCursorState[readCursorState](t.vault, input.Cursor, ToolRead, queryHash)
		if err != nil {
			return readErrorResult(err, CoverageWork{})
		}
		continuedState = &state
	}

	file, err := t.vault.OpenFile(ctx, input.Base, input.Path)
	if err != nil {
		if continuedState != nil && !terminatingRetrievalError(err) {
			return readErrorResult(ErrCursorStale, CoverageWork{})
		}
		return readErrorResult(err, CoverageWork{})
	}
	defer file.Close()
	resolved := file.Resolved()
	work := CoverageWork{FilesScanned: 1}
	meta.FileBacked = true
	meta.Fingerprint = fingerprintString(file.Fingerprint())
	if !strings.EqualFold(path.Ext(resolved.Rel), ".md") {
		if continuedState != nil {
			return readErrorResult(ErrCursorStale, work)
		}
		return readErrorResult(errUnsupportedFile, work)
	}

	if continuedState != nil {
		state := *continuedState
		if state.Fingerprint != fingerprintString(file.Fingerprint()) {
			return readErrorResult(ErrCursorStale, work)
		}
		if state.Mode == readCursorModeOutline {
			return t.continueOutline(ctx, file, selector, publicSelector, maxBytes, queryHash, state, work, meta)
		}
		if state.Mode != readCursorModeContent || !validReadCursorState(state, resolved.Size) {
			return readErrorResult(ErrCursorInvalid, work)
		}
		return t.continueContent(ctx, file, publicSelector, maxBytes, queryHash, state, work, meta)
	}

	reader := &contextFileReader{ctx: ctx, file: file}
	source, err := LoadMarkdownSource(reader)
	work.BytesScanned = reader.bytes
	if err != nil {
		return readErrorResult(err, work)
	}
	if err := file.Revalidate(ctx); err != nil {
		return readErrorResult(err, work)
	}
	selection, err := source.Select(selector)
	if err != nil {
		return readErrorResult(err, work)
	}
	state := readCursorState{
		Fingerprint: fingerprintString(file.Fingerprint()),
		UnitStart:   selection.StartByte,
		UnitEnd:     selection.EndByte,
		NextByte:    selection.StartByte,
		NextLine:    selection.StartLine,
		TotalLines:  source.LineCount(),
	}
	if selection.Selector.Kind == SourceSelectorOutline {
		state.Mode = readCursorModeOutline
		state.OutlineCount = len(selection.Outline)
		return fitOutlineRead(ctx, t.vault, resolved, publicSelector, selection.Outline, source, 0, maxBytes, queryHash, state, work, meta)
	}
	state.Mode = readCursorModeContent
	return fitContentRead(ctx, t.vault, resolved, publicSelector, selection.Content, maxBytes, queryHash, state, work, meta)
}

func (t *Tools) continueContent(ctx context.Context, file *fsx.File, selector ReadSelector, maxBytes int, queryHash CursorQueryHash, state readCursorState, work CoverageWork, meta *readPageMeta) (*sdk.CallToolResult, ReadOutput, error) {
	remaining := state.UnitEnd - state.NextByte
	readLen := min(remaining, maxBytes)
	if readLen < 0 {
		return readErrorResult(ErrCursorInvalid, work)
	}
	if err := file.Seek(ctx, int64(state.NextByte)); err != nil {
		return readErrorResult(err, work)
	}
	content := make([]byte, readLen)
	reader := &contextFileReader{ctx: ctx, file: file}
	reader.file = file
	n, err := io.ReadFull(reader, content)
	work.BytesScanned = reader.bytes
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return readErrorResult(err, work)
	}
	content = content[:n]
	if n != readLen {
		return readErrorResult(ErrCursorStale, work)
	}
	if err := file.Revalidate(ctx); err != nil {
		return readErrorResult(err, work)
	}
	return fitContentRead(ctx, t.vault, file.Resolved(), selector, content, maxBytes, queryHash, state, work, meta)
}

func (t *Tools) continueOutline(ctx context.Context, file *fsx.File, selector SourceSelector, publicSelector ReadSelector, maxBytes int, queryHash CursorQueryHash, state readCursorState, work CoverageWork, meta *readPageMeta) (*sdk.CallToolResult, ReadOutput, error) {
	if state.NextOutline < 0 || state.OutlineCount < 0 || state.NextOutline > state.OutlineCount {
		return readErrorResult(ErrCursorInvalid, work)
	}
	reader := &contextFileReader{ctx: ctx, file: file}
	source, err := LoadMarkdownSource(reader)
	work.BytesScanned = reader.bytes
	if err != nil {
		return readErrorResult(err, work)
	}
	if err := file.Revalidate(ctx); err != nil {
		return readErrorResult(err, work)
	}
	selection, err := source.Select(selector)
	if err != nil {
		return readErrorResult(err, work)
	}
	if state.UnitStart != selection.StartByte || state.UnitEnd != selection.EndByte || state.TotalLines != source.LineCount() || state.OutlineCount != len(selection.Outline) {
		return readErrorResult(ErrCursorInvalid, work)
	}
	return fitOutlineRead(ctx, t.vault, file.Resolved(), publicSelector, selection.Outline, source, state.NextOutline, maxBytes, queryHash, state, work, meta)
}

func fitContentRead(ctx context.Context, sealer cursorSealer, resolved fsx.Resolved, selector ReadSelector, available []byte, maxBytes int, queryHash CursorQueryHash, state readCursorState, work CoverageWork, meta *readPageMeta) (*sdk.CallToolResult, ReadOutput, error) {
	if err := fitContextError(ctx); err != nil {
		return readErrorResult(err, work)
	}
	remaining := state.UnitEnd - state.NextByte
	if remaining < 0 || len(available) > remaining {
		return readErrorResult(ErrCursorInvalid, work)
	}
	allowed := min(len(available), maxBytes)
	allowed = utf8SafePrefixLength(available[:allowed])
	if allowed == 0 && remaining > 0 {
		return readErrorResult(&fsx.Error{Code: fsx.CodeLimitExceeded}, work)
	}

	build := func(count int) (ReadOutput, int, error) {
		if count < 0 || count > allowed || !utf8.Valid(available[:count]) {
			return ReadOutput{}, 0, ErrCursorInvalid
		}
		nextByte := state.NextByte + count
		nextLine := state.NextLine + bytes.Count(available[:count], []byte{'\n'})
		startLine := state.NextLine
		endLine := pageEndLine(startLine, available[:count])
		content := string(available[:count])
		totalLines := state.TotalLines
		result := ReadResult{
			Path:        resolved.Rel,
			Selector:    &selector,
			StartLine:   startLine,
			EndLine:     endLine,
			TotalLines:  &totalLines,
			Modified:    resolved.Modified.Format("2006-01-02T15:04:05.999999999Z07:00"),
			Fingerprint: state.Fingerprint,
			Content:     &content,
		}
		out := ReadOutput{OK: true, ReadResult: result}
		if nextByte >= state.UnitEnd {
			out.Coverage = NewCompleteCoverage(work)
		} else {
			next := state
			next.NextByte = nextByte
			next.NextLine = nextLine
			cursor, err := EncodeCursorState(sealer, ToolRead, queryHash, next)
			if err != nil {
				return ReadOutput{}, 0, err
			}
			stop := CursorStopByteLimit
			if count < allowed || state.NextByte+allowed >= state.UnitEnd {
				stop = CursorStopResponseLimit
			}
			out.Coverage, err = NewCursorCoverageWithScope(work, stop, cursor, true)
			if err != nil {
				return ReadOutput{}, 0, err
			}
			out.Truncated = true
		}
		out.ReadResult.Truncated = out.Truncated
		fitsStructured, _, err := StructuredOutputFits(out)
		if err != nil {
			return ReadOutput{}, 0, err
		}
		if !fitsStructured {
			return out, MaxSDKResultBytes + 1, nil
		}
		_, size, err := CompleteSDKResultFits(successCallResult(), out)
		return out, size, err
	}

	out, err := largestFittingContentPrefix(available[:allowed], build)
	if err != nil {
		return readErrorResult(err, work)
	}
	if out.Content != nil {
		meta.SelectedBytes = len([]byte(*out.Content))
	}
	return successCallResult(), out, nil
}

func largestFittingContentPrefix(content []byte, build func(int) (ReadOutput, int, error)) (ReadOutput, error) {
	boundaries := make([]int, 1, len(content)+1)
	for offset := 0; offset < len(content); {
		_, width := utf8.DecodeRune(content[offset:])
		if width <= 0 {
			return ReadOutput{}, ErrCursorInvalid
		}
		offset += width
		boundaries = append(boundaries, offset)
	}
	low, high := 0, len(boundaries)-1
	var best ReadOutput
	for low <= high {
		middle := low + (high-low)/2
		out, size, err := build(boundaries[middle])
		if err != nil {
			return ReadOutput{}, err
		}
		if size <= MaxSDKResultBytes {
			best = out
			low = middle + 1
		} else {
			high = middle - 1
		}
	}
	if high <= 0 && len(content) > 0 {
		return ReadOutput{}, ErrResponseTooLarge
	}
	return best, nil
}

func fitOutlineRead(ctx context.Context, sealer cursorSealer, resolved fsx.Resolved, selector ReadSelector, outline []OutlineEntry, source *MarkdownSource, start, maxBytes int, queryHash CursorQueryHash, state readCursorState, work CoverageWork, meta *readPageMeta) (*sdk.CallToolResult, ReadOutput, error) {
	if start < 0 || start > len(outline) {
		return readErrorResult(ErrCursorInvalid, work)
	}
	end := start
	selectedBytes := 0
	for end < len(outline) {
		line, ok := source.Line(outline[end].Line)
		if !ok {
			return readErrorResult(ErrCursorInvalid, work)
		}
		lineBytes := line.End - line.Start
		if selectedBytes+lineBytes > maxBytes {
			break
		}
		selectedBytes += lineBytes
		end++
	}
	if end == start && start < len(outline) {
		return readErrorResult(&fsx.Error{Code: fsx.CodeLimitExceeded}, work)
	}

	for candidateEnd := end; candidateEnd >= start; candidateEnd-- {
		page := append([]OutlineEntry(nil), outline[start:candidateEnd]...)
		totalLines := state.TotalLines
		startLine, endLine := 1, 0
		if len(page) > 0 {
			startLine, endLine = page[0].Line, page[len(page)-1].Line
		}
		out := ReadOutput{OK: true, ReadResult: ReadResult{
			Path: resolved.Rel, Selector: &selector, StartLine: startLine, EndLine: endLine,
			TotalLines: &totalLines, Modified: resolved.Modified.Format("2006-01-02T15:04:05.999999999Z07:00"),
			Fingerprint: state.Fingerprint, Outline: &page,
		}}
		if candidateEnd == len(outline) {
			out.Coverage = NewCompleteCoverage(work)
		} else {
			if candidateEnd == start {
				continue
			}
			next := state
			next.NextOutline = candidateEnd
			cursor, err := EncodeCursorState(sealer, ToolRead, queryHash, next)
			if err != nil {
				return readErrorResult(err, work)
			}
			stop := CursorStopByteLimit
			if candidateEnd < end || end == len(outline) {
				stop = CursorStopResponseLimit
			}
			out.Coverage, err = NewCursorCoverageWithScope(work, stop, cursor, true)
			if err != nil {
				return readErrorResult(err, work)
			}
			out.Truncated = true
			out.ReadResult.Truncated = true
		}
		fitsStructured, _, err := StructuredOutputFits(out)
		if err != nil {
			return readErrorResult(err, work)
		}
		fitsComplete, _, err := CompleteSDKResultFits(successCallResult(), out)
		if err != nil {
			return readErrorResult(err, work)
		}
		if fitsStructured && fitsComplete {
			meta.SelectedBytes = outlineSelectedBytes(source, outline[start:candidateEnd])
			return successCallResult(), out, nil
		}
	}
	return readErrorResult(ErrResponseTooLarge, work)
}

func outlineSelectedBytes(source *MarkdownSource, entries []OutlineEntry) int {
	total := 0
	for _, entry := range entries {
		if line, ok := source.Line(entry.Line); ok {
			total += line.End - line.Start
		}
	}
	return total
}

func normalizeReadSelector(input *ReadSelector) (SourceSelector, ReadSelector, error) {
	selector := SourceSelector{}
	if input != nil {
		selector = SourceSelector{
			Kind: SourceSelectorKind(input.Kind), StartLine: input.StartLine,
			Heading: input.Heading, Occurrence: input.Occurrence, BlockID: input.BlockID,
		}
	}
	normalized, err := selector.Normalize()
	if err != nil {
		return SourceSelector{}, ReadSelector{}, err
	}
	public := ReadSelector{
		Kind: string(normalized.Kind), StartLine: normalized.StartLine,
		Heading: normalized.Heading, Occurrence: normalized.Occurrence, BlockID: normalized.BlockID,
	}
	return normalized, public, nil
}

func effectiveReadMaxBytes(value int) (int, error) {
	if value == 0 {
		return DefaultReadBytes, nil
	}
	if value < 1 || value > MaxReadBytes {
		return 0, &fsx.Error{Code: fsx.CodeLimitExceeded}
	}
	return value, nil
}

func validReadCursorState(state readCursorState, fileSize int64) bool {
	return state.Fingerprint != "" && state.UnitStart >= 0 && state.UnitEnd >= state.UnitStart &&
		state.NextByte >= state.UnitStart && state.NextByte <= state.UnitEnd && int64(state.UnitEnd) <= fileSize &&
		state.NextLine >= 1 && state.TotalLines >= 0 && state.NextLine <= max(state.TotalLines, 1)
}

func utf8SafePrefixLength(value []byte) int {
	end := len(value)
	for end > 0 && !utf8.Valid(value[:end]) {
		end--
	}
	return end
}

func pageEndLine(start int, value []byte) int {
	if len(value) == 0 {
		return start - 1
	}
	end := start + bytes.Count(value, []byte{'\n'})
	if value[len(value)-1] == '\n' {
		end--
	}
	return end
}

func fingerprintString(value fsx.SourceFingerprint) string {
	return base64.RawURLEncoding.EncodeToString(value[:])
}

var errUnsupportedFile = errors.New(UnsupportedFileCode)

func readErrorResult(err error, work CoverageWork) (*sdk.CallToolResult, ReadOutput, error) {
	code := retrievalErrorCode(err)
	stop := restartStopFor(err)
	if errors.Is(err, ErrCursorStale) {
		stop = RestartStopSourceChange
	}
	coverage, coverageErr := NewRestartCoverage(work, stop)
	if coverageErr != nil {
		coverage, _ = NewRestartCoverage(work, RestartStopError)
	}
	return errorCallResult(), ReadOutput{
		OK: false, ReadResult: ReadResult{Coverage: coverage},
		Error: &ToolError{Code: code, Message: sanitizedMessage(code)},
	}, nil
}

func retrievalErrorCode(err error) string {
	switch {
	case errors.Is(err, errUnsupportedFile):
		return UnsupportedFileCode
	case errors.Is(err, ErrMarkdownInvalidUTF8):
		return InvalidUTF8Code
	case errors.Is(err, ErrMarkdownInputTooLarge):
		return string(fsx.CodeInputTooLarge)
	case errors.Is(err, ErrSourceSelectorInvalid):
		return InvalidSelectorCode
	case errors.Is(err, ErrSourceUnitNotFound):
		return SelectorNotFoundCode
	case errors.Is(err, ErrSourceUnitAmbiguous):
		return SelectorAmbiguousCode
	case fsx.IsCode(err, fsx.CodeNotFile), fsx.IsCode(err, fsx.CodeNotDirectory):
		return UnsupportedFileCode
	default:
		return errorCode(err)
	}
}
