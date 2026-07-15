package obsidian

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"personal-mcp-gateway/internal/fsx"
)

const MaxStructuredResultBytes = MaxSDKResultBytes - SDKResultReserve

const ResponseTooLargeCode = "response_too_large"

var ErrResponseTooLarge = errors.New(ResponseTooLargeCode)

type LSFitCandidate struct {
	Entry    LSEntry
	Position fsx.Position
}

type LSFitRequest struct {
	Path       string
	Candidates []LSFitCandidate
	HasMore    bool
	Work       CoverageWork
}

type CursorForPosition func(fsx.Position) (string, error)

// StructuredOutputBytes returns the exact encoded structured-output size used
// by the domain's pre-SDK budget.
func StructuredOutputBytes(value any) (int, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return 0, fmt.Errorf("encode structured output: %w", err)
	}
	return len(data), nil
}

func StructuredOutputFits(value any) (bool, int, error) {
	size, err := StructuredOutputBytes(value)
	if err != nil {
		return false, 0, err
	}
	return size <= MaxStructuredResultBytes, size, nil
}

// FitLSOutput deterministically returns the largest leading candidate page
// that fits the domain budget. Any partial page carries a cursor at the last
// entry actually emitted. It never emits a lossy page that cannot advance.
func FitLSOutput(ctx context.Context, request LSFitRequest, cursorFor CursorForPosition) (LSOutput, error) {
	if err := fitContextError(ctx); err != nil {
		return LSOutput{}, err
	}
	if len(request.Candidates) == 0 {
		if request.HasMore {
			return LSOutput{}, ErrResponseTooLarge
		}
		out := LSOutput{
			OK:       true,
			Path:     request.Path,
			Entries:  []LSEntry{},
			Coverage: NewCompleteCoverage(request.Work),
		}
		out.Truncated = !out.Coverage.ResultComplete
		fits, _, err := StructuredOutputFits(out)
		if err != nil {
			return LSOutput{}, err
		}
		if !fits {
			return LSOutput{}, ErrResponseTooLarge
		}
		return out, nil
	}

	// Marshal every entry once for cumulative payload accounting. Each
	// candidate is then measured with a bounded one-entry result shell and its
	// candidate-specific cursor. This keeps fitting linear in total entry bytes
	// while still checking every prefix: cursor size is not monotonic.
	entrySizes := make([]int, len(request.Candidates))
	for i := range request.Candidates {
		if err := fitContextError(ctx); err != nil {
			return LSOutput{}, err
		}
		encoded, err := json.Marshal(request.Candidates[i].Entry)
		if err != nil {
			return LSOutput{}, fmt.Errorf("encode ls entry: %w", err)
		}
		entrySizes[i] = len(encoded)
	}

	bestCount := 0
	var bestCoverage Coverage
	prefixBytesBefore := 0
	for i := range request.Candidates {
		if err := fitContextError(ctx); err != nil {
			return LSOutput{}, err
		}
		count := i + 1
		needsCursor := count < len(request.Candidates) || request.HasMore
		out := LSOutput{
			OK:      true,
			Path:    request.Path,
			Entries: []LSEntry{request.Candidates[i].Entry},
		}
		if needsCursor {
			if cursorFor == nil {
				return LSOutput{}, ErrResponseTooLarge
			}
			cursor, err := cursorFor(request.Candidates[i].Position)
			if err != nil {
				return LSOutput{}, err
			}
			if err := fitContextError(ctx); err != nil {
				return LSOutput{}, err
			}
			stoppedBy := CursorStopResponseLimit
			if count == len(request.Candidates) && request.HasMore {
				stoppedBy = CursorStopResultLimit
			}
			out.Coverage, err = NewCursorCoverage(request.Work, stoppedBy, cursor)
			if err != nil {
				return LSOutput{}, err
			}
		} else {
			out.Coverage = NewCompleteCoverage(request.Work)
		}
		out.Truncated = !out.Coverage.ResultComplete

		oneEntrySize, err := StructuredOutputBytes(out)
		if err != nil {
			return LSOutput{}, err
		}
		// The one-entry shell already contains entry i. Replacing that array
		// with the whole prefix adds prior encoded entries and one comma each.
		candidateSize := oneEntrySize + prefixBytesBefore + i
		if candidateSize > MaxStructuredResultBytes {
			// Cursor length can vary with the boundary spelling, so a later
			// prefix is not assumed to be larger solely because this one did
			// not fit. Check every bounded candidate prefix.
			prefixBytesBefore += entrySizes[i]
			continue
		}
		bestCount = count
		bestCoverage = out.Coverage
		prefixBytesBefore += entrySizes[i]
	}
	if bestCount == 0 {
		return LSOutput{}, ErrResponseTooLarge
	}
	if err := fitContextError(ctx); err != nil {
		return LSOutput{}, err
	}
	out := LSOutput{
		OK:        true,
		Path:      request.Path,
		Entries:   entriesPrefix(request.Candidates, bestCount),
		Truncated: !bestCoverage.ResultComplete,
		Coverage:  bestCoverage,
	}
	// Keep a final exact whole-structure check at the public boundary. It
	// protects the byte cap if the LSOutput representation changes later.
	fits, _, err := StructuredOutputFits(out)
	if err != nil {
		return LSOutput{}, err
	}
	if !fits {
		return LSOutput{}, ErrResponseTooLarge
	}
	return out, nil
}

func ResponseFitErrorCode(err error) string {
	if errors.Is(err, ErrResponseTooLarge) {
		return ResponseTooLargeCode
	}
	return ""
}

func entriesPrefix(candidates []LSFitCandidate, count int) []LSEntry {
	entries := make([]LSEntry, count)
	for i := 0; i < count; i++ {
		entries[i] = candidates[i].Entry
	}
	return entries
}

func fitContextError(ctx context.Context) error {
	if ctx == nil {
		return &fsx.Error{Code: fsx.CodeCanceled}
	}
	switch err := ctx.Err(); {
	case errors.Is(err, context.DeadlineExceeded):
		return &fsx.Error{Code: fsx.CodeTimeout}
	case err != nil:
		return &fsx.Error{Code: fsx.CodeCanceled}
	default:
		return nil
	}
}
