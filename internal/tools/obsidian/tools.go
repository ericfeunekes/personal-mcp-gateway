package obsidian

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/jsonschema-go/jsonschema"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"personal-mcp-gateway/internal/fsx"
	"personal-mcp-gateway/internal/limits"
	localmcp "personal-mcp-gateway/internal/mcp"
)

const (
	ToolResolve = "resolve"
	ToolLS      = "ls"

	ResolveDescription = "Return the canonical stored vault path and metadata for one vault-relative path. Use the returned path in follow-on calls; missing paths return `exists:false`. This does not read file content."
	LSDescription      = "Continue a partial listing only by passing `coverage.next_cursor` as `cursor` with the identical `path`, `base`, and `limit`. Never omit `cursor` or change `limit` to continue: omitting `cursor` restarts at the first entry and repeats results, while changing `limit` with the prior cursor returns `cursor_mismatch`. List one directory level in deterministic canonical order; follow cursors until `coverage.continuation` is `complete`, and restart without a cursor only when it is `restart`. This lists metadata, not file content or recursive search."
)

type Tools struct {
	vault   *fsx.Vault
	openDir func(context.Context, string, string) (listDirectory, error)
}

func New(vault *fsx.Vault) *Tools {
	return &Tools{
		vault: vault,
		openDir: func(ctx context.Context, base, path string) (listDirectory, error) {
			return vault.OpenDir(ctx, base, path)
		},
	}
}

type listDirectory interface {
	Resolved() fsx.Resolved
	ListPage(context.Context, fsx.ListOptions) (fsx.ListPage, error)
	Close() error
}

func Descriptors(vault *fsx.Vault) ([]localmcp.ToolDescriptor, error) {
	tools := New(vault)
	resolve, err := localmcp.NewToolDescriptor(sdk.Tool{
		Name:        ToolResolve,
		Description: ResolveDescription,
		Annotations: readOnlyToolAnnotations(),
	}, tools.Resolve, summarizeResolveArgs, summarizeResolveResult)
	if err != nil {
		return nil, err
	}
	lsInputSchema, err := jsonschema.For[LSInput](nil)
	if err != nil {
		return nil, err
	}
	limitSchema := lsInputSchema.Properties["limit"]
	if limitSchema == nil {
		return nil, errors.New("ls input schema lacks limit")
	}
	limitSchema.Default = json.RawMessage("100")
	ls, err := localmcp.NewToolDescriptor(sdk.Tool{
		Name:        ToolLS,
		Description: LSDescription,
		Annotations: readOnlyToolAnnotations(),
		InputSchema: lsInputSchema,
	}, tools.LS, summarizeLSArgs, summarizeLSResult)
	if err != nil {
		return nil, err
	}
	return []localmcp.ToolDescriptor{resolve, ls}, nil
}

func readOnlyToolAnnotations() *sdk.ToolAnnotations {
	destructive := false
	openWorld := false
	return &sdk.ToolAnnotations{
		ReadOnlyHint:    true,
		DestructiveHint: &destructive,
		OpenWorldHint:   &openWorld,
	}
}

type ResolveInput struct {
	Path string `json:"path" jsonschema:"vault-relative path to resolve"`
	Base string `json:"base,omitempty" jsonschema:"optional vault-relative base path"`
}

type ResolveOutput struct {
	OK       bool       `json:"ok"`
	Path     string     `json:"path,omitempty"`
	Exists   bool       `json:"exists"`
	Type     string     `json:"type,omitempty"`
	Size     *int64     `json:"size,omitempty"`
	Modified string     `json:"modified,omitempty"`
	Error    *ToolError `json:"error,omitempty"`
}

type LSInput struct {
	Path   string `json:"path" jsonschema:"vault-relative directory path to list"`
	Base   string `json:"base,omitempty" jsonschema:"optional vault-relative base path"`
	Limit  int    `json:"limit,omitempty" jsonschema:"page size; choose it on the first call and keep it identical for every cursor continuation; increasing it without a cursor restarts at the first entry; defaults to 100 when omitted; valid values are 1 through 500"`
	Cursor string `json:"cursor,omitempty" jsonschema:"required to continue whenever the previous ls returned coverage.continuation cursor; pass coverage.next_cursor unchanged and repeat the identical path, base, and limit; omitting it restarts at the first entry"`
}

type LSOutput struct {
	OK        bool       `json:"ok"`
	Path      string     `json:"path,omitempty"`
	Entries   []LSEntry  `json:"entries"`
	Truncated bool       `json:"truncated"`
	Coverage  Coverage   `json:"coverage"`
	Error     *ToolError `json:"error,omitempty"`
}

type LSEntry struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Type     string `json:"type"`
	Size     int64  `json:"size"`
	Modified string `json:"modified,omitempty"`
}

type ToolError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (t *Tools) Resolve(ctx context.Context, _ *sdk.CallToolRequest, input ResolveInput) (*sdk.CallToolResult, ResolveOutput, error) {
	toolCtx, cancel := context.WithTimeout(ctx, limits.ToolOperationTimeout)
	defer cancel()

	resolved, err := t.vault.Resolve(toolCtx, input.Base, input.Path)
	if err != nil {
		return resolveError(err)
	}

	out := ResolveOutput{
		OK:     true,
		Path:   resolved.Rel,
		Exists: resolved.Exists,
	}
	if resolved.Exists {
		out.Type = string(resolved.Kind)
		size := resolved.Size
		out.Size = &size
		out.Modified = resolved.Modified.Format("2006-01-02T15:04:05Z07:00")
	}
	return successCallResult(), out, nil
}

func (t *Tools) LS(ctx context.Context, _ *sdk.CallToolRequest, input LSInput) (*sdk.CallToolResult, LSOutput, error) {
	toolCtx, cancel := context.WithTimeout(ctx, limits.ToolOperationTimeout)
	defer cancel()

	limit, err := effectiveLimit(input.Limit)
	if err != nil {
		return lsErrorWithWork(err, CoverageWork{}, RestartStopError)
	}
	directory, err := t.openDir(toolCtx, input.Base, input.Path)
	if err != nil {
		return lsErrorWithWork(err, CoverageWork{}, restartStopFor(err))
	}
	defer directory.Close()

	canonical := directory.Resolved().Rel
	query := LSQueryHash(canonical, limit)
	var decoded *DecodedCursor
	var after *fsx.Position
	if input.Cursor != "" {
		value, cursorErr := DecodeCursor(input.Cursor)
		if cursorErr != nil {
			return lsErrorWithWork(cursorErr, CoverageWork{}, RestartStopError)
		}
		if value.Tool != ToolLS || value.Query != query {
			return lsErrorWithWork(ErrCursorMismatch, CoverageWork{}, RestartStopError)
		}
		decoded = &value
		after = &value.Position
	}

	page, err := directory.ListPage(toolCtx, fsx.ListOptions{Limit: limit, After: after})
	work := CoverageWork{FilesScanned: page.FilesScanned, BytesScanned: page.BytesScanned}
	if err != nil {
		return lsErrorWithWork(err, work, restartStopFor(err))
	}
	if decoded != nil {
		if decoded.Source != page.Source {
			return lsErrorWithWork(ErrCursorStale, work, RestartStopSourceChange)
		}
		if !page.BoundaryFound {
			return lsErrorWithWork(ErrCursorInvalid, work, RestartStopError)
		}
	}

	entries := page.Entries
	hasMore := page.HasMore
	if len(entries) > limit {
		entries = entries[:limit]
		hasMore = true
	}
	candidates := make([]LSFitCandidate, 0, len(entries))
	for _, entry := range entries {
		candidates = append(candidates, LSFitCandidate{Entry: LSEntry{
			Name:     entry.Name,
			Path:     entry.Rel,
			Type:     string(entry.Kind),
			Size:     entry.Size,
			Modified: entry.Modified.Format("2006-01-02T15:04:05Z07:00"),
		}, Position: entry.Position})
	}
	out, err := FitLSOutput(toolCtx, LSFitRequest{
		Path:       canonical,
		Candidates: candidates,
		HasMore:    hasMore,
		Work:       work,
	}, func(position fsx.Position) (string, error) {
		return EncodeCursor(ToolLS, query, page.Source, position)
	})
	if err != nil {
		return lsErrorWithWork(err, work, RestartStopError)
	}
	return successCallResult(), out, nil
}

func effectiveLimit(value int) (int, error) {
	if value == 0 {
		return fsx.DefaultLimit, nil
	}
	if value < 0 || value > fsx.MaxLimit {
		return 0, &fsx.Error{Code: fsx.CodeLimitExceeded}
	}
	return value, nil
}

func resolveError(err error) (*sdk.CallToolResult, ResolveOutput, error) {
	code := errorCode(err)
	return errorCallResult(), ResolveOutput{
		OK: false,
		Error: &ToolError{
			Code:    code,
			Message: sanitizedMessage(code),
		},
	}, nil
}

func lsErrorWithWork(err error, work CoverageWork, stoppedBy RestartStop) (*sdk.CallToolResult, LSOutput, error) {
	code := errorCode(err)
	coverage, coverageErr := NewRestartCoverage(work, stoppedBy)
	if coverageErr != nil {
		coverage, _ = NewRestartCoverage(work, RestartStopError)
	}
	return errorCallResult(), LSOutput{
		OK:       false,
		Coverage: coverage,
		Error: &ToolError{
			Code:    code,
			Message: sanitizedMessage(code),
		},
	}, nil
}

func errorCode(err error) string {
	if code := CursorErrorCode(err); code != "" {
		return code
	}
	if code := ResponseFitErrorCode(err); code != "" {
		return code
	}
	var code fsx.Code
	switch {
	case fsx.IsCode(err, fsx.CodePathDenied):
		code = fsx.CodePathDenied
	case fsx.IsCode(err, fsx.CodeSymlinkDenied):
		code = fsx.CodeSymlinkDenied
	case fsx.IsCode(err, fsx.CodeNotFound):
		code = fsx.CodeNotFound
	case fsx.IsCode(err, fsx.CodeNotDirectory):
		code = fsx.CodeNotDirectory
	case fsx.IsCode(err, fsx.CodeLimitExceeded):
		code = fsx.CodeLimitExceeded
	case fsx.IsCode(err, fsx.CodeInputTooLarge):
		code = fsx.CodeInputTooLarge
	case fsx.IsCode(err, fsx.CodeTimeout):
		code = fsx.CodeTimeout
	case fsx.IsCode(err, fsx.CodeCanceled):
		code = fsx.CodeCanceled
	case fsx.IsCode(err, fsx.CodeSourceChanged):
		code = fsx.CodeSourceChanged
	default:
		code = fsx.CodePathDenied
	}
	return string(code)
}

func sanitizedMessage(code string) string {
	switch code {
	case string(fsx.CodePathDenied):
		return "path denied"
	case string(fsx.CodeSymlinkDenied):
		return "symlink traversal denied"
	case string(fsx.CodeNotFound):
		return "path not found"
	case string(fsx.CodeNotDirectory):
		return "path is not a directory"
	case string(fsx.CodeLimitExceeded):
		return "limit exceeds maximum"
	case string(fsx.CodeInputTooLarge):
		return "input too large"
	case string(fsx.CodeTimeout):
		return "operation timed out"
	case string(fsx.CodeCanceled):
		return "operation canceled"
	case string(fsx.CodeSourceChanged):
		return "directory changed during operation"
	case CursorInvalidCode:
		return CursorErrorMessage(ErrCursorInvalid)
	case CursorMismatchCode:
		return CursorErrorMessage(ErrCursorMismatch)
	case CursorStaleCode:
		return CursorErrorMessage(ErrCursorStale)
	case ResponseTooLargeCode:
		return "response exceeds maximum size"
	default:
		return "tool call failed"
	}
}

func successCallResult() *sdk.CallToolResult {
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "Obsidian metadata is available in structuredContent."}}}
}

func errorCallResult() *sdk.CallToolResult {
	return &sdk.CallToolResult{
		Content: []sdk.Content{&sdk.TextContent{Text: "Obsidian metadata request failed; inspect the structured error."}},
		IsError: true,
	}
}

func restartStopFor(err error) RestartStop {
	switch {
	case fsx.IsCode(err, fsx.CodeTimeout):
		return RestartStopTimeout
	case fsx.IsCode(err, fsx.CodeCanceled):
		return RestartStopCanceled
	case fsx.IsCode(err, fsx.CodeSourceChanged):
		return RestartStopSourceChange
	default:
		return RestartStopError
	}
}

func summarizeResolveArgs(builder *localmcp.SafeSummaryBuilder, raw json.RawMessage) error {
	return summarizeArgs(builder, raw, []string{"path", "base"}, false)
}

func summarizeLSArgs(builder *localmcp.SafeSummaryBuilder, raw json.RawMessage) error {
	return summarizeArgs(builder, raw, []string{"path", "base", "limit", "cursor"}, true)
}

func summarizeArgs(builder *localmcp.SafeSummaryBuilder, raw json.RawMessage, knownKeys []string, cursorAllowed bool) error {
	if len(raw) == 0 {
		return nil
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return builder.Enum(localmcp.SectionArguments, localmcp.EnumShape, localmcp.ValueInvalidJSON)
	}
	if err := builder.UnknownArgumentKeys(object, knownKeys...); err != nil {
		return err
	}
	for _, field := range []struct {
		name string
		slot localmcp.PathSlot
	}{{"path", localmcp.PathInput}, {"base", localmcp.PathBase}} {
		value, present := object[field.name]
		kind, text := summaryJSONValue(value)
		if err := builder.Path(field.slot, present, kind, text); err != nil {
			return err
		}
	}
	if cursorAllowed {
		if value, present := object["limit"]; present {
			if number, ok := value.(float64); ok && number >= 0 && number <= 1_000_000 && number == float64(uint64(number)) {
				if err := builder.Counter(localmcp.SectionArguments, localmcp.CounterLimit, uint64(number)); err != nil {
					return err
				}
			}
		}
		value, present := object["cursor"]
		cursor, _ := value.(string)
		if err := builder.Cursor(present, uint64(len([]byte(cursor)))); err != nil {
			return err
		}
	}
	return nil
}

func summarizeResolveResult(builder *localmcp.SafeSummaryBuilder, result *sdk.CallToolResult) error {
	if err := summarizeResultBase(builder, result); err != nil {
		return err
	}
	var out ResolveOutput
	if !decodeStructured(result, &out) {
		return nil
	}
	if err := builder.Bool(localmcp.SectionResult, localmcp.BoolOK, out.OK); err != nil {
		return err
	}
	if err := builder.Bool(localmcp.SectionResult, localmcp.BoolExists, out.Exists); err != nil {
		return err
	}
	if value, ok := summaryKindValue(out.Type); ok {
		if err := builder.Enum(localmcp.SectionResult, localmcp.EnumResultType, value); err != nil {
			return err
		}
	}
	return summarizeErrorCode(builder, out.Error)
}

func summarizeLSResult(builder *localmcp.SafeSummaryBuilder, result *sdk.CallToolResult) error {
	if err := summarizeResultBase(builder, result); err != nil {
		return err
	}
	var out LSOutput
	if !decodeStructured(result, &out) {
		return nil
	}
	for metric, value := range map[localmcp.BoolMetric]bool{
		localmcp.BoolOK:             out.OK,
		localmcp.BoolTruncated:      out.Truncated,
		localmcp.BoolResultComplete: out.Coverage.ResultComplete,
		localmcp.BoolScopeComplete:  out.Coverage.ScopeComplete,
	} {
		if err := builder.Bool(localmcp.SectionResult, metric, value); err != nil {
			return err
		}
	}
	for metric, value := range map[localmcp.CounterMetric]uint64{
		localmcp.CounterEntryCount:   uint64(len(out.Entries)),
		localmcp.CounterFilesScanned: out.Coverage.FilesScanned,
		localmcp.CounterBytesScanned: out.Coverage.BytesScanned,
	} {
		if err := builder.Counter(localmcp.SectionResult, metric, value); err != nil {
			return err
		}
	}
	for metric, value := range map[localmcp.EnumMetric]string{
		localmcp.EnumStoppedBy:    out.Coverage.StoppedBy,
		localmcp.EnumContinuation: out.Coverage.Continuation,
		localmcp.EnumConsistency:  out.Coverage.Consistency,
	} {
		if encoded, ok := summaryCoverageValue(value); ok {
			if err := builder.Enum(localmcp.SectionResult, metric, encoded); err != nil {
				return err
			}
		}
	}
	return summarizeErrorCode(builder, out.Error)
}

func summarizeResultBase(builder *localmcp.SafeSummaryBuilder, result *sdk.CallToolResult) error {
	if result == nil {
		return nil
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return builder.Counter(localmcp.SectionResult, localmcp.CounterResponseBytes, uint64(len(encoded)))
}

func decodeStructured(result *sdk.CallToolResult, out any) bool {
	if result == nil || result.StructuredContent == nil {
		return false
	}
	data, err := json.Marshal(result.StructuredContent)
	return err == nil && json.Unmarshal(data, out) == nil
}

func summarizeErrorCode(builder *localmcp.SafeSummaryBuilder, toolError *ToolError) error {
	if toolError == nil {
		return nil
	}
	value, ok := summaryErrorValue(toolError.Code)
	if !ok {
		return nil
	}
	return builder.Enum(localmcp.SectionResult, localmcp.EnumErrorCode, value)
}

func summaryJSONValue(value any) (localmcp.JSONKind, string) {
	switch typed := value.(type) {
	case nil:
		return localmcp.JSONNull, ""
	case string:
		return localmcp.JSONString, typed
	case bool:
		return localmcp.JSONBoolean, ""
	case float64:
		return localmcp.JSONNumber, ""
	case []any:
		return localmcp.JSONArray, ""
	case map[string]any:
		return localmcp.JSONObject, ""
	default:
		return localmcp.JSONNull, ""
	}
}

func summaryKindValue(value string) (localmcp.EnumValue, bool) {
	switch value {
	case string(fsx.KindFile):
		return localmcp.ValueFile, true
	case string(fsx.KindDir):
		return localmcp.ValueDirectory, true
	case string(fsx.KindSymlink):
		return localmcp.ValueSymlink, true
	case string(fsx.KindOther):
		return localmcp.ValueOther, true
	default:
		return 0, false
	}
}

func summaryCoverageValue(value string) (localmcp.EnumValue, bool) {
	values := map[string]localmcp.EnumValue{
		"scope":          localmcp.ValueScope,
		"result_limit":   localmcp.ValueResultLimit,
		"response_limit": localmcp.ValueResponseLimit,
		"timeout":        localmcp.ValueTimeout,
		"canceled":       localmcp.ValueCanceled,
		"source_change":  localmcp.ValueSourceChange,
		"error":          localmcp.ValueError,
		"complete":       localmcp.ValueComplete,
		"cursor":         localmcp.ValueCursor,
		"restart":        localmcp.ValueRestart,
		"stable":         localmcp.ValueStable,
		"best_effort":    localmcp.ValueBestEffort,
	}
	result, ok := values[value]
	return result, ok
}

func summaryErrorValue(value string) (localmcp.EnumValue, bool) {
	values := map[string]localmcp.EnumValue{
		"path_denied":        localmcp.ValuePathDenied,
		"symlink_denied":     localmcp.ValueSymlinkDenied,
		"not_found":          localmcp.ValueNotFound,
		"not_directory":      localmcp.ValueNotDirectory,
		"limit_exceeded":     localmcp.ValueLimitExceeded,
		"input_too_large":    localmcp.ValueInputTooLarge,
		"timeout":            localmcp.ValueTimeout,
		"canceled":           localmcp.ValueCanceled,
		"source_changed":     localmcp.ValueSourceChange,
		"cursor_invalid":     localmcp.ValueCursorInvalid,
		"cursor_mismatch":    localmcp.ValueCursorMismatch,
		"cursor_stale":       localmcp.ValueCursorStale,
		"response_too_large": localmcp.ValueResponseTooLarge,
	}
	result, ok := values[value]
	return result, ok
}
