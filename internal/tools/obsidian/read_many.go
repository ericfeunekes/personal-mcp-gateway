package obsidian

import (
	"context"
	"encoding/base64"
	"errors"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"personal-mcp-gateway/internal/fsx"
	"personal-mcp-gateway/internal/limits"
)

const (
	readManyOutcomeFile    = "file"
	readManyOutcomeMissing = "missing"
	readManyOutcomeStatic  = "static"
)

// readManyObservation is intentionally compact: a maximum-sized cursor carries
// twenty of these records, never request or content values. The outer query
// digest binds the ordered requests.
type readManyObservation struct {
	Index       int    `json:"i"`
	Outcome     string `json:"o"`
	Code        string `json:"c,omitempty"`
	Fingerprint string `json:"f,omitempty"`
	InnerCursor string `json:"r,omitempty"`
	InnerMax    int    `json:"m,omitempty"`
}

type readManyCursorState struct {
	NextIndex    int                   `json:"n"`
	Observations []readManyObservation `json:"o"`
}

type readManyQuery struct {
	Requests []readManyQueryRequest `json:"requests"`
	MaxBytes int                    `json:"max_bytes"`
}

type readManyQueryRequest struct {
	Path     string       `json:"path"`
	Selector ReadSelector `json:"selector"`
	MaxBytes int          `json:"max_bytes"`
}

type preparedReadManyRequest struct {
	request      ReadRequest
	query        readManyQueryRequest
	effectiveMax int
	selectorErr  error
	maxBytesErr  error
}

type readManyCheckpoint struct {
	state readManyCursorState
	items []ReadManyItem
}

type readManyAttemptProvenance uint8

const (
	readManyAttemptItemLimit readManyAttemptProvenance = iota
	readManyAttemptAggregateLimit
	readManyAttemptResponseLimit
)

func (t *Tools) ReadMany(ctx context.Context, _ *sdk.CallToolRequest, input ReadManyInput) (*sdk.CallToolResult, ReadManyOutput, error) {
	toolCtx, cancel := context.WithTimeout(ctx, limits.ToolOperationTimeout)
	defer cancel()
	return t.readManyPage(toolCtx, input)
}

func (t *Tools) readManyPage(ctx context.Context, input ReadManyInput) (*sdk.CallToolResult, ReadManyOutput, error) {
	prepared, aggregateMax, queryHash, err := prepareReadMany(input)
	if err != nil {
		return readManyErrorResult(err, nil, CoverageWork{}, 0, len(input.Requests))
	}

	state := readManyCursorState{NextIndex: 0, Observations: []readManyObservation{}}
	if input.Cursor != "" {
		state, err = DecodeCursorState[readManyCursorState](t.vault, input.Cursor, ToolReadMany, queryHash)
		if err != nil {
			return readManyErrorResult(err, nil, CoverageWork{}, 0, len(prepared))
		}
		if !validReadManyCursorState(state, len(prepared)) {
			return readManyErrorResult(ErrCursorInvalid, nil, CoverageWork{}, 0, len(prepared))
		}
	}

	work := CoverageWork{}
	if err := t.revalidateReadManyObservations(ctx, prepared, state, &work); err != nil {
		return readManyErrorResult(err, nil, work, state.NextIndex, len(prepared)-state.NextIndex)
	}

	items := []ReadManyItem{}
	checkpoints := []readManyCheckpoint{{state: cloneReadManyState(state), items: []ReadManyItem{}}}
	aggregateUsed := 0

	for state.NextIndex < len(prepared) {
		if err := fitContextError(ctx); err != nil {
			return readManyErrorResult(err, items, work, state.NextIndex, len(prepared)-state.NextIndex)
		}
		remaining := aggregateMax - aggregateUsed
		if remaining <= 0 {
			return readManyPartialResult(t.vault, queryHash, checkpoints, work, CursorStopByteLimit, len(prepared), &fsx.Error{Code: fsx.CodeLimitExceeded})
		}

		index := state.NextIndex
		request := prepared[index]
		innerCursor, innerMax, hasInner := currentReadManyCursor(state)
		attemptProvenance := readManyAttemptItemLimit
		attemptMax := request.effectiveMax
		if remaining < attemptMax {
			attemptMax = remaining
			attemptProvenance = readManyAttemptAggregateLimit
		}
		if request.maxBytesErr != nil {
			attemptMax = request.request.MaxBytes
			attemptProvenance = readManyAttemptItemLimit
		}
		if hasInner {
			if attemptMax != innerMax {
				rebased, rebaseErr := t.rebaseCurrentReadManyCursor(ctx, request, state.Observations[index], innerCursor, innerMax, attemptMax)
				if rebaseErr != nil {
					return readManyErrorResult(rebaseErr, items, work, index, len(prepared)-index)
				}
				innerCursor = rebased
				innerMax = attemptMax
				work.SourceEntriesValidated++
				hasInner = false
			} else {
				attemptMax = innerMax
			}
		}

		readInput := ReadInput{
			Path: request.request.Path, Base: request.request.Base,
			Selector: request.request.Selector, MaxBytes: attemptMax, Cursor: innerCursor,
		}
		if request.selectorErr == nil {
			selector := request.query.Selector
			readInput.Selector = &selector
		}

		for {
			_, readOut, meta, callErr := t.readPageWithMeta(ctx, readInput)
			addReadManyWork(&work, readOut.Coverage)
			if hasInner {
				work.SourceEntriesValidated++
				hasInner = false
			}
			if callErr != nil {
				return readManyErrorResult(callErr, items, work, index, len(prepared)-index)
			}
			if !readOut.OK && readOut.Error != nil && readOut.Error.Code == ResponseTooLargeCode {
				return readManyPartialResult(t.vault, queryHash, checkpoints, work, CursorStopResponseLimit, len(prepared), ErrResponseTooLarge)
			}
			if !readOut.OK && readOut.Error != nil && readOut.Error.Code == string(fsx.CodeLimitExceeded) && request.maxBytesErr == nil {
				switch attemptProvenance {
				case readManyAttemptAggregateLimit:
					return readManyPartialResult(t.vault, queryHash, checkpoints, work, CursorStopByteLimit, len(prepared), &fsx.Error{Code: fsx.CodeLimitExceeded})
				case readManyAttemptResponseLimit:
					return readManyPartialResult(t.vault, queryHash, checkpoints, work, CursorStopResponseLimit, len(prepared), ErrResponseTooLarge)
				}
			}
			if !readOut.OK && !readManyItemLocal(readOut.Error) {
				return readManyErrorResult(toolErrorAsError(readOut.Error), items, work, index, len(prepared)-index)
			}

			observation, observationErr := makeReadManyObservation(index, readOut, meta, readInput.MaxBytes)
			if observationErr != nil {
				return readManyErrorResult(observationErr, items, work, index, len(prepared)-index)
			}
			candidateState := advanceReadManyState(state, observation, readOut)
			candidateItem := readManyItem(index, readOut)
			candidateItems := appendReadManyItem(items, candidateItem)
			candidateStop := readManyCandidateStop(readOut)
			if attemptProvenance == readManyAttemptResponseLimit && readOut.OK && readOut.Coverage.Continuation == CoverageContinuationCursor {
				candidateStop = CursorStopResponseLimit
			}
			candidateOut, fits, size, fitErr := buildReadManyCandidate(t.vault, queryHash, candidateState, candidateItems, work, len(prepared), candidateStop)
			if fitErr != nil {
				return readManyErrorResult(fitErr, items, work, index, len(prepared)-index)
			}
			if !fits {
				if len(items) > 0 {
					return readManyPartialResult(t.vault, queryHash, checkpoints, work, CursorStopResponseLimit, len(prepared), ErrResponseTooLarge)
				}
				if !readOut.OK || meta.SelectedBytes <= 1 {
					return readManyErrorResult(ErrResponseTooLarge, nil, work, index, len(prepared)-index)
				}
				lower := lowerReadManyAttempt(readInput.MaxBytes, meta.SelectedBytes, size-MaxSDKResultBytes)
				if lower < 1 || lower >= readInput.MaxBytes {
					return readManyErrorResult(ErrResponseTooLarge, nil, work, index, len(prepared)-index)
				}
				if readInput.Cursor != "" {
					rebased, rebaseErr := rebaseReadManyInnerCursor(t.vault, readInput.Cursor, request.query.Path, request.query.Selector, readInput.MaxBytes, lower)
					if rebaseErr != nil {
						return readManyErrorResult(rebaseErr, nil, work, index, len(prepared)-index)
					}
					readInput.Cursor = rebased
				}
				readInput.MaxBytes = lower
				attemptProvenance = readManyAttemptResponseLimit
				continue
			}

			state = candidateState
			items = candidateOut.Items
			aggregateUsed += meta.SelectedBytes
			checkpoints = append(checkpoints, readManyCheckpoint{state: cloneReadManyState(state), items: append([]ReadManyItem(nil), items...)})

			if readOut.OK && readOut.Coverage.Continuation == CoverageContinuationCursor {
				return successCallResult(), candidateOut, nil
			}
			if aggregateUsed >= aggregateMax && state.NextIndex < len(prepared) {
				return readManyPartialResult(t.vault, queryHash, checkpoints, work, CursorStopByteLimit, len(prepared), &fsx.Error{Code: fsx.CodeLimitExceeded})
			}
			break
		}
	}

	out := ReadManyOutput{
		OK: true, Items: items, NextRequestIndex: len(prepared), RemainingRequestCount: 0,
		Coverage: NewCompleteCoverage(work),
	}
	if fits, _, fitErr := readManyOutputFits(out); fitErr != nil {
		return readManyErrorResult(fitErr, nil, work, len(prepared), 0)
	} else if !fits {
		return readManyPartialResult(t.vault, queryHash, checkpoints, work, CursorStopResponseLimit, len(prepared), ErrResponseTooLarge)
	}
	return successCallResult(), out, nil
}

func prepareReadMany(input ReadManyInput) ([]preparedReadManyRequest, int, CursorQueryHash, error) {
	if len(input.Requests) < 1 || len(input.Requests) > MaxReadManyRequests {
		return nil, 0, CursorQueryHash{}, &fsx.Error{Code: fsx.CodeLimitExceeded}
	}
	aggregateMax := input.MaxBytes
	if aggregateMax == 0 {
		aggregateMax = DefaultReadManyBytes
	}
	if aggregateMax < 1 || aggregateMax > MaxReadManyBytes {
		return nil, 0, CursorQueryHash{}, &fsx.Error{Code: fsx.CodeLimitExceeded}
	}

	prepared := make([]preparedReadManyRequest, len(input.Requests))
	query := readManyQuery{Requests: make([]readManyQueryRequest, len(input.Requests)), MaxBytes: aggregateMax}
	for i, request := range input.Requests {
		normalizedPath, pathErr := fsx.NormalizePath(request.Base, request.Path)
		if pathErr != nil {
			// Invalid path shapes remain item-local outcomes. Preserve their
			// bounded raw identity in the query digest so continuation cannot
			// change the failing request into another request.
			normalizedPath = request.Base + "\x00" + request.Path
		}
		_, selector, selectorErr := normalizeReadSelector(request.Selector)
		if selectorErr != nil && request.Selector != nil {
			selector = *request.Selector
		}
		maxBytes, maxBytesErr := effectiveReadMaxBytes(request.MaxBytes)
		if maxBytesErr != nil {
			maxBytes = request.MaxBytes
		}
		queryRequest := readManyQueryRequest{
			Path: normalizedPath, Selector: selector, MaxBytes: maxBytes,
		}
		prepared[i] = preparedReadManyRequest{
			request: request, query: queryRequest, effectiveMax: maxBytes,
			selectorErr: selectorErr, maxBytesErr: maxBytesErr,
		}
		query.Requests[i] = queryRequest
	}
	queryHash, err := RetrievalQueryHash(ToolReadMany, query)
	return prepared, aggregateMax, queryHash, err
}

func validReadManyCursorState(state readManyCursorState, requestCount int) bool {
	if state.NextIndex < 0 || state.NextIndex >= requestCount || len(state.Observations) > MaxReadManyRequests || len(state.Observations) > requestCount {
		return false
	}
	if len(state.Observations) != state.NextIndex && len(state.Observations) != state.NextIndex+1 {
		return false
	}
	for i, observation := range state.Observations {
		if observation.Index != i || !validReadManyObservation(observation) {
			return false
		}
		if i < state.NextIndex && observation.InnerCursor != "" {
			return false
		}
	}
	if len(state.Observations) == state.NextIndex+1 {
		current := state.Observations[state.NextIndex]
		return current.Outcome == readManyOutcomeFile && current.Code == "" && current.InnerCursor != "" && current.InnerMax >= 1 && current.InnerMax <= MaxReadBytes
	}
	return true
}

func validReadManyObservation(observation readManyObservation) bool {
	switch observation.Outcome {
	case readManyOutcomeFile:
		decoded, err := base64.RawURLEncoding.DecodeString(observation.Fingerprint)
		if err != nil || len(decoded) != 32 || (observation.Code != "" && !readManyItemLocal(&ToolError{Code: observation.Code})) {
			return false
		}
		if observation.InnerCursor == "" {
			return observation.InnerMax == 0
		}
		return observation.Code == "" && len(observation.InnerCursor) <= MaxCursorBytes && observation.InnerMax >= 1 && observation.InnerMax <= MaxReadBytes
	case readManyOutcomeMissing:
		return observation.Code == string(fsx.CodeNotFound) && observation.Fingerprint == "" && observation.InnerCursor == "" && observation.InnerMax == 0
	case readManyOutcomeStatic:
		return observation.Code != "" && observation.Fingerprint == "" && observation.InnerCursor == "" && observation.InnerMax == 0
	default:
		return false
	}
}

func currentReadManyCursor(state readManyCursorState) (string, int, bool) {
	if len(state.Observations) != state.NextIndex+1 {
		return "", 0, false
	}
	current := state.Observations[state.NextIndex]
	return current.InnerCursor, current.InnerMax, true
}

func (t *Tools) revalidateReadManyObservations(ctx context.Context, requests []preparedReadManyRequest, state readManyCursorState, work *CoverageWork) error {
	for _, observation := range state.Observations {
		if observation.Index == state.NextIndex && observation.InnerCursor != "" {
			continue
		}
		validated, err := t.revalidateReadManyObservation(ctx, requests[observation.Index], observation)
		if validated {
			work.SourceEntriesValidated++
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (t *Tools) revalidateReadManyObservation(ctx context.Context, request preparedReadManyRequest, observation readManyObservation) (bool, error) {
	switch observation.Outcome {
	case readManyOutcomeFile:
		file, err := t.vault.OpenFile(ctx, request.request.Base, request.request.Path)
		if err != nil {
			if terminatingRetrievalError(err) {
				return true, err
			}
			return true, ErrCursorStale
		}
		defer file.Close()
		if fingerprintString(file.Fingerprint()) != observation.Fingerprint {
			return true, ErrCursorStale
		}
		return true, nil
	case readManyOutcomeMissing:
		file, err := t.vault.OpenFile(ctx, request.request.Base, request.request.Path)
		if file != nil {
			_ = file.Close()
			return true, ErrCursorStale
		}
		if fsx.IsCode(err, fsx.CodeNotFound) {
			return true, nil
		}
		if terminatingRetrievalError(err) {
			return true, err
		}
		return true, ErrCursorStale
	case readManyOutcomeStatic:
		return t.revalidateReadManyStatic(ctx, request, observation.Code)
	default:
		return false, ErrCursorInvalid
	}
}

func (t *Tools) revalidateReadManyStatic(ctx context.Context, request preparedReadManyRequest, expected string) (bool, error) {
	if request.selectorErr != nil {
		if retrievalErrorCode(request.selectorErr) == expected {
			return false, nil
		}
		return false, ErrCursorStale
	}
	if request.maxBytesErr != nil {
		if retrievalErrorCode(request.maxBytesErr) == expected {
			return false, nil
		}
		return false, ErrCursorStale
	}
	file, err := t.vault.OpenFile(ctx, request.request.Base, request.request.Path)
	if file != nil {
		_ = file.Close()
		return true, ErrCursorStale
	}
	if terminatingRetrievalError(err) {
		return true, err
	}
	if retrievalErrorCode(err) != expected {
		return true, ErrCursorStale
	}
	return true, nil
}

func (t *Tools) rebaseCurrentReadManyCursor(ctx context.Context, request preparedReadManyRequest, observation readManyObservation, cursor string, oldMax, newMax int) (string, error) {
	if oldMax < 1 || newMax < 1 || cursor == "" || observation.Fingerprint == "" {
		return "", ErrCursorInvalid
	}
	oldQuery, err := RetrievalQueryHash(ToolRead, normalizedReadQuery{
		Path: request.query.Path, Selector: request.query.Selector, MaxBytes: oldMax,
	})
	if err != nil {
		return "", err
	}
	innerState, err := DecodeCursorState[readCursorState](t.vault, cursor, ToolRead, oldQuery)
	if err != nil {
		return "", err
	}
	file, err := t.vault.OpenFile(ctx, request.request.Base, request.request.Path)
	if err != nil {
		if terminatingRetrievalError(err) {
			return "", err
		}
		return "", ErrCursorStale
	}
	defer file.Close()
	if fingerprintString(file.Fingerprint()) != observation.Fingerprint {
		return "", ErrCursorStale
	}
	newQuery, err := RetrievalQueryHash(ToolRead, normalizedReadQuery{
		Path: request.query.Path, Selector: request.query.Selector, MaxBytes: newMax,
	})
	if err != nil {
		return "", err
	}
	return EncodeCursorState(t.vault, ToolRead, newQuery, innerState)
}

func terminatingRetrievalError(err error) bool {
	return fsx.IsCode(err, fsx.CodeTimeout) || fsx.IsCode(err, fsx.CodeCanceled) || fsx.IsCode(err, fsx.CodeSourceChanged)
}

func makeReadManyObservation(index int, out ReadOutput, meta readPageMeta, innerMax int) (readManyObservation, error) {
	observation := readManyObservation{Index: index}
	if meta.FileBacked {
		if meta.Fingerprint == "" {
			return readManyObservation{}, ErrCursorInvalid
		}
		observation.Outcome = readManyOutcomeFile
		observation.Fingerprint = meta.Fingerprint
	} else if out.Error != nil && out.Error.Code == string(fsx.CodeNotFound) {
		observation.Outcome = readManyOutcomeMissing
		observation.Code = out.Error.Code
	} else {
		observation.Outcome = readManyOutcomeStatic
		if out.Error == nil || out.Error.Code == "" {
			return readManyObservation{}, ErrCursorInvalid
		}
		observation.Code = out.Error.Code
	}
	if out.Error != nil {
		observation.Code = out.Error.Code
	}
	if out.OK && out.Coverage.Continuation == CoverageContinuationCursor {
		if out.Coverage.NextCursor == "" {
			return readManyObservation{}, ErrCursorInvalid
		}
		observation.InnerCursor = out.Coverage.NextCursor
		observation.InnerMax = innerMax
	}
	return observation, nil
}

func advanceReadManyState(current readManyCursorState, observation readManyObservation, out ReadOutput) readManyCursorState {
	next := cloneReadManyState(current)
	if len(next.Observations) == observation.Index+1 {
		next.Observations[observation.Index] = observation
	} else {
		next.Observations = append(next.Observations, observation)
	}
	if out.OK && out.Coverage.Continuation == CoverageContinuationCursor {
		next.NextIndex = observation.Index
	} else {
		next.NextIndex = observation.Index + 1
	}
	return next
}

func cloneReadManyState(state readManyCursorState) readManyCursorState {
	state.Observations = append([]readManyObservation(nil), state.Observations...)
	return state
}

func readManyItem(index int, out ReadOutput) ReadManyItem {
	return ReadManyItem{Index: index, OK: out.OK, ReadResult: out.ReadResult, Error: out.Error}
}

func appendReadManyItem(items []ReadManyItem, item ReadManyItem) []ReadManyItem {
	out := make([]ReadManyItem, len(items)+1)
	copy(out, items)
	out[len(items)] = item
	return out
}

func readManyCandidateStop(out ReadOutput) CursorStop {
	if out.OK && out.Coverage.StoppedBy == string(CursorStopResponseLimit) {
		return CursorStopResponseLimit
	}
	return CursorStopByteLimit
}

func buildReadManyCandidate(sealer cursorSealer, query CursorQueryHash, state readManyCursorState, items []ReadManyItem, work CoverageWork, requestCount int, stopped CursorStop) (ReadManyOutput, bool, int, error) {
	if state.NextIndex >= requestCount {
		out := ReadManyOutput{
			OK: true, Items: append([]ReadManyItem{}, items...), NextRequestIndex: requestCount,
			RemainingRequestCount: 0, Coverage: NewCompleteCoverage(work),
		}
		fits, size, err := readManyOutputFits(out)
		return out, fits, size, err
	}
	cursor, err := EncodeCursorState(sealer, ToolReadMany, query, state)
	if err != nil {
		return ReadManyOutput{}, false, 0, err
	}
	coverage, err := NewCursorCoverageWithScope(work, stopped, cursor, false)
	if err != nil {
		return ReadManyOutput{}, false, 0, err
	}
	outItems := append([]ReadManyItem{}, items...)
	if len(outItems) > 0 {
		last := &outItems[len(outItems)-1]
		if last.OK && last.Coverage.Continuation == CoverageContinuationCursor {
			last.Coverage.NextCursor = cursor
		}
	}
	out := ReadManyOutput{
		OK: true, Items: outItems, NextRequestIndex: state.NextIndex,
		RemainingRequestCount: requestCount - state.NextIndex,
		Truncated:             true, Coverage: coverage,
	}
	fits, size, err := readManyOutputFits(out)
	return out, fits, size, err
}

func readManyOutputFits(out ReadManyOutput) (bool, int, error) {
	structured, _, err := StructuredOutputFits(out)
	if err != nil {
		return false, 0, err
	}
	complete, size, err := CompleteSDKResultFits(successCallResult(), out)
	return structured && complete, size, err
}

func readManyPartialResult(sealer cursorSealer, query CursorQueryHash, checkpoints []readManyCheckpoint, work CoverageWork, stopped CursorStop, requestCount int, terminalErr error) (*sdk.CallToolResult, ReadManyOutput, error) {
	for i := len(checkpoints) - 1; i >= 0; i-- {
		checkpoint := checkpoints[i]
		if len(checkpoint.items) == 0 {
			continue
		}
		out, fits, _, err := buildReadManyCandidate(sealer, query, checkpoint.state, checkpoint.items, work, requestCount, stopped)
		if err != nil {
			return readManyErrorResult(err, nil, work, checkpoint.state.NextIndex, 0)
		}
		if fits {
			return successCallResult(), out, nil
		}
	}
	next := 0
	if len(checkpoints) > 0 {
		next = checkpoints[0].state.NextIndex
	}
	return readManyErrorResult(terminalErr, nil, work, next, requestCount-next)
}

func lowerReadManyAttempt(current, selected, excess int) int {
	basis := min(current, selected)
	if basis <= 1 {
		return 0
	}
	reduction := max(excess, max(basis/4, 1))
	return max(basis-reduction, 1)
}

func rebaseReadManyInnerCursor(sealer cursorSealer, cursor, requestPath string, selector ReadSelector, oldMax, newMax int) (string, error) {
	if requestPath == "" || oldMax < 1 || newMax < 1 || newMax >= oldMax {
		return "", ErrCursorInvalid
	}
	oldQuery, err := RetrievalQueryHash(ToolRead, normalizedReadQuery{Path: requestPath, Selector: selector, MaxBytes: oldMax})
	if err != nil {
		return "", err
	}
	state, err := DecodeCursorState[readCursorState](sealer, cursor, ToolRead, oldQuery)
	if err != nil {
		return "", err
	}
	newQuery, err := RetrievalQueryHash(ToolRead, normalizedReadQuery{Path: requestPath, Selector: selector, MaxBytes: newMax})
	if err != nil {
		return "", err
	}
	return EncodeCursorState(sealer, ToolRead, newQuery, state)
}

func addReadManyWork(work *CoverageWork, coverage Coverage) {
	work.FilesScanned += coverage.FilesScanned
	work.BytesScanned += coverage.BytesScanned
	work.SourceEntriesValidated += coverage.SourceEntriesValidated
}

func readManyItemLocal(toolError *ToolError) bool {
	if toolError == nil {
		return false
	}
	switch toolError.Code {
	case string(fsx.CodePathDenied), string(fsx.CodeSymlinkDenied), string(fsx.CodeNotFound),
		UnsupportedFileCode, InvalidUTF8Code, InvalidSelectorCode, SelectorNotFoundCode,
		SelectorAmbiguousCode, string(fsx.CodeInputTooLarge), string(fsx.CodeLimitExceeded):
		return true
	default:
		return false
	}
}

func toolErrorAsError(toolError *ToolError) error {
	if toolError == nil {
		return ErrCursorInvalid
	}
	switch toolError.Code {
	case CursorInvalidCode:
		return ErrCursorInvalid
	case CursorMismatchCode:
		return ErrCursorMismatch
	case CursorStaleCode:
		return ErrCursorStale
	case ResponseTooLargeCode:
		return ErrResponseTooLarge
	case string(fsx.CodeTimeout):
		return &fsx.Error{Code: fsx.CodeTimeout}
	case string(fsx.CodeCanceled):
		return &fsx.Error{Code: fsx.CodeCanceled}
	case string(fsx.CodeSourceChanged):
		return &fsx.Error{Code: fsx.CodeSourceChanged}
	default:
		return errors.New(toolError.Code)
	}
}

func readManyErrorResult(err error, items []ReadManyItem, work CoverageWork, nextIndex, remaining int) (*sdk.CallToolResult, ReadManyOutput, error) {
	code := retrievalErrorCode(err)
	stop := restartStopFor(err)
	if errors.Is(err, ErrCursorStale) {
		stop = RestartStopSourceChange
	}
	coverage, coverageErr := NewRestartCoverage(work, stop)
	if coverageErr != nil {
		coverage, _ = NewRestartCoverage(work, RestartStopError)
	}
	return errorCallResult(), ReadManyOutput{
		OK: false, Items: append([]ReadManyItem{}, items...), NextRequestIndex: nextIndex,
		RemainingRequestCount: max(remaining, 0), Truncated: remaining > 0, Coverage: coverage,
		Error: &ToolError{Code: code, Message: sanitizedMessage(code)},
	}, nil
}
