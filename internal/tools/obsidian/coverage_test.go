package obsidian

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"personal-mcp-gateway/internal/fsx"
)

func TestCoverageConstructorsPreserveTruthfulStates(t *testing.T) {
	work := CoverageWork{FilesScanned: 17, BytesScanned: 0}
	complete := NewCompleteCoverage(work)
	if !complete.ResultComplete || !complete.ScopeComplete || complete.Consistency != CoverageConsistencyStable ||
		complete.StoppedBy != CoverageStopScope || complete.Continuation != CoverageContinuationComplete ||
		complete.NextCursor != "" || complete.FilesScanned != 17 {
		t.Fatalf("complete coverage = %#v", complete)
	}

	partial, err := NewCursorCoverage(work, CursorStopResponseLimit, "opaque")
	if err != nil {
		t.Fatal(err)
	}
	if partial.ResultComplete || !partial.ScopeComplete || partial.Consistency != CoverageConsistencyStable ||
		partial.StoppedBy != string(CursorStopResponseLimit) || partial.Continuation != CoverageContinuationCursor ||
		partial.NextCursor != "opaque" {
		t.Fatalf("cursor coverage = %#v", partial)
	}

	restart, err := NewRestartCoverage(work, RestartStopSourceChange)
	if err != nil {
		t.Fatal(err)
	}
	if restart.ResultComplete || restart.ScopeComplete || restart.Consistency != CoverageConsistencyBestEffort ||
		restart.StoppedBy != string(RestartStopSourceChange) || restart.Continuation != CoverageContinuationRestart ||
		restart.NextCursor != "" {
		t.Fatalf("restart coverage = %#v", restart)
	}
}

func TestCoverageConstructorsRejectImpossibleCombinations(t *testing.T) {
	if _, err := NewCursorCoverage(CoverageWork{}, CursorStopResultLimit, ""); !errors.Is(err, ErrInvalidCoverage) {
		t.Fatalf("empty cursor error = %v", err)
	}
	if _, err := NewCursorCoverage(CoverageWork{}, CursorStop("timeout"), "cursor"); !errors.Is(err, ErrInvalidCoverage) {
		t.Fatalf("invalid cursor stop error = %v", err)
	}
	if _, err := NewRestartCoverage(CoverageWork{}, RestartStop("result_limit")); !errors.Is(err, ErrInvalidCoverage) {
		t.Fatalf("invalid restart stop error = %v", err)
	}
}

func TestStructuredOutputFitsExactBoundaryAndRejectsOver(t *testing.T) {
	type payload struct {
		Value string `json:"value"`
	}
	emptySize, err := StructuredOutputBytes(payload{})
	if err != nil {
		t.Fatal(err)
	}
	exact := payload{Value: strings.Repeat("x", MaxStructuredResultBytes-emptySize)}
	fits, size, err := StructuredOutputFits(exact)
	if err != nil || !fits || size != MaxStructuredResultBytes {
		t.Fatalf("exact fits=%v size=%d err=%v", fits, size, err)
	}
	over := payload{Value: exact.Value + "x"}
	fits, size, err = StructuredOutputFits(over)
	if err != nil || fits || size != MaxStructuredResultBytes+1 {
		t.Fatalf("over fits=%v size=%d err=%v", fits, size, err)
	}
}

func TestFitLSOutputReturnsCompleteOrResultLimitedCoverage(t *testing.T) {
	candidates := fitCandidates(2, 8)
	cursorFor := func(position fsx.Position) (string, error) { return "cursor-" + position.Stored, nil }

	complete, err := FitLSOutput(context.Background(), LSFitRequest{Path: ".", Candidates: candidates, Work: CoverageWork{FilesScanned: 2}}, cursorFor)
	if err != nil {
		t.Fatal(err)
	}
	if len(complete.Entries) != 2 || complete.Truncated || complete.Coverage.Continuation != CoverageContinuationComplete {
		t.Fatalf("complete output = %#v", complete)
	}

	limited, err := FitLSOutput(context.Background(), LSFitRequest{Path: ".", Candidates: candidates, HasMore: true, Work: CoverageWork{FilesScanned: 10}}, cursorFor)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited.Entries) != 2 || !limited.Truncated || limited.Coverage.StoppedBy != string(CursorStopResultLimit) ||
		limited.Coverage.NextCursor == "" || limited.Coverage.FilesScanned != 10 {
		t.Fatalf("limited output = %#v", limited)
	}
}

func TestFitLSOutputReducesOnlyAtEntryBoundaryAndUsesLastEmittedCursor(t *testing.T) {
	candidates := fitCandidates(20, 4000)
	cursorFor := func(position fsx.Position) (string, error) { return "after-" + position.Stored, nil }
	out, err := FitLSOutput(context.Background(), LSFitRequest{Path: ".", Candidates: candidates, Work: CoverageWork{FilesScanned: 20}}, cursorFor)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Entries) == 0 || len(out.Entries) >= len(candidates) {
		t.Fatalf("entry count = %d, want a non-empty reduced prefix", len(out.Entries))
	}
	last := candidates[len(out.Entries)-1]
	if out.Coverage.StoppedBy != string(CursorStopResponseLimit) || out.Coverage.NextCursor != "after-"+last.Position.Stored {
		t.Fatalf("coverage = %#v, last=%#v", out.Coverage, last)
	}
	fits, size, err := StructuredOutputFits(out)
	if err != nil || !fits || size > MaxStructuredResultBytes {
		t.Fatalf("fitted output fits=%v size=%d err=%v", fits, size, err)
	}
}

func TestFitLSOutputChecksLaterPrefixesWhenCursorSizesDiffer(t *testing.T) {
	large := strings.Repeat("a", 24500)
	candidates := []LSFitCandidate{
		{Entry: LSEntry{Name: large, Path: large, Type: "file"}, Position: fsx.Position{NFC: "a", Stored: "a"}},
		{Entry: LSEntry{Name: "b", Path: "b", Type: "file"}, Position: fsx.Position{NFC: "b", Stored: "b"}},
	}
	out, err := FitLSOutput(context.Background(), LSFitRequest{Path: ".", Candidates: candidates, HasMore: true}, func(position fsx.Position) (string, error) {
		if position.Stored == "a" {
			return strings.Repeat("c", MaxCursorBytes), nil
		}
		return "c", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Entries) != 2 || out.Coverage.StoppedBy != string(CursorStopResultLimit) {
		t.Fatalf("output = %#v", out)
	}
}

func TestFitLSOutputReturnsResponseTooLargeWhenOneEntryAndContinuationCannotFit(t *testing.T) {
	candidates := fitCandidates(1, MaxStructuredResultBytes)
	_, err := FitLSOutput(context.Background(), LSFitRequest{Path: ".", Candidates: candidates, HasMore: true}, func(fsx.Position) (string, error) {
		return "cursor", nil
	})
	if !errors.Is(err, ErrResponseTooLarge) || ResponseFitErrorCode(err) != ResponseTooLargeCode {
		t.Fatalf("error = %v code=%q", err, ResponseFitErrorCode(err))
	}
}

func TestFitLSOutputHandlesEmptyCompleteResult(t *testing.T) {
	out, err := FitLSOutput(context.Background(), LSFitRequest{Path: ".", Work: CoverageWork{FilesScanned: 3}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Entries == nil || len(out.Entries) != 0 || out.Truncated || out.Coverage.FilesScanned != 3 {
		t.Fatalf("output = %#v", out)
	}
}

func TestFitLSOutputStopsAfterContextCancellation(t *testing.T) {
	candidates := fitCandidates(8, 32)
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	_, err := FitLSOutput(ctx, LSFitRequest{Path: ".", Candidates: candidates, HasMore: true}, func(fsx.Position) (string, error) {
		calls++
		if calls == 3 {
			cancel()
		}
		return "cursor", nil
	})
	if !fsx.IsCode(err, fsx.CodeCanceled) {
		t.Fatalf("error = %v, want canceled", err)
	}
	if calls != 3 {
		t.Fatalf("cursor calls = %d, want 3", calls)
	}
}

func TestFitLSOutputRejectsExpiredContextBeforeWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	_, err := FitLSOutput(ctx, LSFitRequest{Path: ".", Candidates: fitCandidates(2, 8), HasMore: true}, func(fsx.Position) (string, error) {
		calls++
		return "cursor", nil
	})
	if !fsx.IsCode(err, fsx.CodeCanceled) {
		t.Fatalf("error = %v, want canceled", err)
	}
	if calls != 0 {
		t.Fatalf("cursor calls = %d, want 0", calls)
	}
}

func TestFitLSOutputMapsExpiredDeadlineToTimeout(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	_, err := FitLSOutput(ctx, LSFitRequest{Path: ".", Candidates: fitCandidates(2, 8), HasMore: true}, func(fsx.Position) (string, error) {
		return "cursor", nil
	})
	if !fsx.IsCode(err, fsx.CodeTimeout) {
		t.Fatalf("error = %v, want timeout", err)
	}
}

func BenchmarkFitLSOutputMaximumCandidates(b *testing.B) {
	candidates := fitCandidates(fsx.MaxLimit, 64)
	request := LSFitRequest{Path: ".", Candidates: candidates, HasMore: true, Work: CoverageWork{FilesScanned: 1000}}
	cursorFor := func(position fsx.Position) (string, error) { return "cursor-" + position.Stored, nil }
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := FitLSOutput(context.Background(), request, cursorFor); err != nil {
			b.Fatal(err)
		}
	}
}

func fitCandidates(count, fieldBytes int) []LSFitCandidate {
	out := make([]LSFitCandidate, 0, count)
	for i := 0; i < count; i++ {
		name := strings.Repeat(string(rune('a'+i%26)), fieldBytes) + string(rune('A'+i%26))
		position := fsx.Position{NFC: name, Stored: name}
		out = append(out, LSFitCandidate{
			Entry:    LSEntry{Name: name, Path: name, Type: "file"},
			Position: position,
		})
	}
	return out
}
