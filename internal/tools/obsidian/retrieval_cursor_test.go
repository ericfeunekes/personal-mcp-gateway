package obsidian

import (
	"errors"
	"strings"
	"testing"
)

type retrievalCursorTestState struct {
	Next int    `json:"next"`
	Hash string `json:"hash"`
}

func TestRetrievalCursorUsesSharedStrictEnvelope(t *testing.T) {
	vault := newCursorTestVault(t, t.TempDir())
	query, err := RetrievalQueryHash(ToolRead, normalizedReadQuery{
		Path: "note.md", Selector: ReadSelector{Kind: SelectorContent, StartLine: 1}, MaxBytes: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := retrievalCursorTestState{Next: 7, Hash: strings.Repeat("a", 43)}
	encoded, err := EncodeCursorState(vault, ToolRead, query, want)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > MaxCursorBytes || strings.Contains(encoded, "=") {
		t.Fatalf("cursor is not bounded raw base64url: len=%d", len(encoded))
	}
	got, err := DecodeCursorState[retrievalCursorTestState](vault, encoded, ToolRead, query)
	if err != nil || got != want {
		t.Fatalf("decoded = %#v, %v", got, err)
	}
	if _, err := DecodeCursorState[retrievalCursorTestState](vault, encoded, ToolGrep, query); !errors.Is(err, ErrCursorMismatch) {
		t.Fatalf("tool mismatch = %v", err)
	}
	other, err := RetrievalQueryHash(ToolRead, normalizedReadQuery{
		Path: "note.md", Selector: ReadSelector{Kind: SelectorContent, StartLine: 1}, MaxBytes: 9,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeCursorState[retrievalCursorTestState](vault, encoded, ToolRead, other); !errors.Is(err, ErrCursorMismatch) {
		t.Fatalf("query mismatch = %v", err)
	}
}

func TestRetrievalCursorRejectsUnknownStateAndOversize(t *testing.T) {
	vault := newCursorTestVault(t, t.TempDir())
	query, err := RetrievalQueryHash(ToolRead, map[string]any{"path": "note.md"})
	if err != nil {
		t.Fatal(err)
	}
	unknown, err := EncodeCursorState(vault, ToolRead, query, map[string]any{"next": 1, "hash": "x", "extra": true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeCursorState[retrievalCursorTestState](vault, unknown, ToolRead, query); !errors.Is(err, ErrCursorInvalid) {
		t.Fatalf("unknown state error = %v", err)
	}
	if _, err := EncodeCursorState(vault, ToolRead, query, retrievalCursorTestState{Next: 1, Hash: strings.Repeat("x", MaxCursorBytes)}); !errors.Is(err, ErrCursorInvalid) {
		t.Fatalf("oversize state error = %v", err)
	}
}

func TestRetrievalCoverageAllowsIncompleteStreamingScope(t *testing.T) {
	work := CoverageWork{FilesScanned: 2, BytesScanned: 9, SourceEntriesValidated: 3}
	coverage, err := NewCursorCoverageWithScope(work, CursorStopByteLimit, "cursor", false)
	if err != nil {
		t.Fatal(err)
	}
	if coverage.ResultComplete || coverage.ScopeComplete || coverage.StoppedBy != "byte_limit" ||
		coverage.Continuation != CoverageContinuationCursor || coverage.NextCursor != "cursor" ||
		coverage.SourceEntriesValidated != 3 {
		t.Fatalf("coverage = %#v", coverage)
	}
}
