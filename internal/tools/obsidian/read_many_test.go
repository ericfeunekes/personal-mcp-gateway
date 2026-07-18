package obsidian

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"personal-mcp-gateway/internal/fsx"
)

func TestReadManyReturnsOrderedItemsAndIsolatesItemErrors(t *testing.T) {
	root := t.TempDir()
	writeReadFixture(t, root, "first.md", "first")
	writeReadFixture(t, root, "plain.txt", "plain")
	if err := os.WriteFile(filepath.Join(root, "bad.md"), []byte{0xff}, 0o600); err != nil {
		t.Fatal(err)
	}
	writeReadFixture(t, root, "dense.md", strings.Repeat("x\n", MaxMarkdownSourceLines+1))
	tools := newReadTools(t, root)

	requests := []ReadRequest{
		{Path: "first.md"},
		{Path: "missing.md"},
		{Path: "plain.txt"},
		{Path: "bad.md"},
		{Path: "dense.md"},
		{Path: "first.md", Selector: &ReadSelector{Kind: SelectorHeading, Heading: "  "}},
		{Path: "first.md", MaxBytes: MaxReadBytes + 1},
	}
	result, out, err := tools.ReadMany(context.Background(), nil, ReadManyInput{Requests: requests})
	if err != nil || result.IsError || !out.OK || out.Coverage.Continuation != CoverageContinuationComplete {
		t.Fatalf("result=%#v out=%#v err=%v", result, out, err)
	}
	if len(out.Items) != len(requests) || out.NextRequestIndex != len(requests) || out.RemainingRequestCount != 0 {
		t.Fatalf("batch shape = %#v", out)
	}
	wantCodes := []string{"", string(fsx.CodeNotFound), UnsupportedFileCode, InvalidUTF8Code, string(fsx.CodeInputTooLarge), InvalidSelectorCode, string(fsx.CodeLimitExceeded)}
	for i, item := range out.Items {
		if item.Index != i {
			t.Fatalf("item %d index = %d", i, item.Index)
		}
		if wantCodes[i] == "" {
			if !item.OK || item.Content == nil || *item.Content != "first" || item.Error != nil {
				t.Fatalf("success item = %#v", item)
			}
			continue
		}
		if item.OK || item.Error == nil || item.Error.Code != wantCodes[i] || item.Coverage.Continuation != CoverageContinuationRestart {
			t.Fatalf("item %d = %#v, want code %q", i, item, wantCodes[i])
		}
	}
}

func TestReadManySplitItemUsesOnlyAuthoritativeBatchCursorWithoutReplay(t *testing.T) {
	root := t.TempDir()
	writeReadFixture(t, root, "long.md", "abcdefgh")
	writeReadFixture(t, root, "last.md", "last")
	tools := newReadTools(t, root)
	input := ReadManyInput{Requests: []ReadRequest{{Path: "long.md", MaxBytes: 3}, {Path: "last.md"}}}

	var accumulated strings.Builder
	seenLast := false
	for page := 0; page < 10; page++ {
		result, out, err := tools.ReadMany(context.Background(), nil, input)
		if err != nil || result.IsError || !out.OK || len(out.Items) == 0 {
			t.Fatalf("page %d result=%#v out=%#v err=%v", page, result, out, err)
		}
		for _, item := range out.Items {
			switch item.Index {
			case 0:
				if item.Content == nil {
					t.Fatalf("page %d content item = %#v", page, item)
				}
				accumulated.WriteString(*item.Content)
			case 1:
				if seenLast || item.Content == nil || *item.Content != "last" {
					t.Fatalf("page %d last item = %#v", page, item)
				}
				seenLast = true
			default:
				t.Fatalf("unexpected index %d", item.Index)
			}
		}
		if out.Coverage.Continuation == CoverageContinuationComplete {
			break
		}
		if out.Coverage.NextCursor == "" || out.NextRequestIndex >= len(input.Requests) {
			t.Fatalf("partial page = %#v", out)
		}
		last := out.Items[len(out.Items)-1]
		if last.Index == out.NextRequestIndex && last.Coverage.Continuation == CoverageContinuationCursor && last.Coverage.NextCursor != out.Coverage.NextCursor {
			t.Fatalf("nested cursor differs from authoritative cursor")
		}
		state, err := DecodeCursorState[readManyCursorState](tools.vault, out.Coverage.NextCursor, ToolReadMany, mustReadManyQueryHash(t, input))
		if err != nil {
			t.Fatalf("decode batch cursor: %v", err)
		}
		if len(state.Observations) == state.NextIndex+1 {
			inner := state.Observations[state.NextIndex].InnerCursor
			encoded, marshalErr := json.Marshal(out)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if inner == "" || strings.Contains(string(encoded), inner) {
				t.Fatalf("inner read cursor escaped in public output")
			}
		}
		input.Cursor = out.Coverage.NextCursor
	}
	if got, want := accumulated.String(), "abcdefgh"; got != want {
		t.Fatalf("accumulated index 0 = %q, want %q", got, want)
	}
	if !seenLast {
		t.Fatal("final request was never returned")
	}
}

func TestReadManyRebasesSmallRemainderCursorOnNextPage(t *testing.T) {
	root := t.TempDir()
	writeReadFixture(t, root, "first.md", "123456789")
	writeReadFixture(t, root, "second.md", strings.Repeat("x", 30))
	tools := newReadTools(t, root)
	input := ReadManyInput{
		Requests: []ReadRequest{{Path: "first.md", MaxBytes: 9}, {Path: "second.md", MaxBytes: 100}},
		MaxBytes: 10,
	}

	_, first, err := tools.ReadMany(context.Background(), nil, input)
	if err != nil || !first.OK || first.NextRequestIndex != 1 || len(first.Items) != 2 || first.Items[1].Content == nil || len(*first.Items[1].Content) != 1 {
		t.Fatalf("first page = %#v err=%v", first, err)
	}
	input.Cursor = first.Coverage.NextCursor
	_, second, err := tools.ReadMany(context.Background(), nil, input)
	if err != nil || !second.OK || len(second.Items) != 1 || second.Items[0].Index != 1 || second.Items[0].Content == nil {
		t.Fatalf("second page = %#v err=%v", second, err)
	}
	if got := len(*second.Items[0].Content); got != 10 {
		t.Fatalf("rebased page content bytes = %d, want 10", got)
	}
	if second.Coverage.SourceEntriesValidated != 2 {
		t.Fatalf("source_entries_validated = %d, want 2 (prior item plus rebased current item)", second.Coverage.SourceEntriesValidated)
	}
}

func TestReadManyContinuationRejectsQueryAndPriorObservationChanges(t *testing.T) {
	t.Run("query mismatch", func(t *testing.T) {
		root := t.TempDir()
		writeReadFixture(t, root, "note.md", "abcdef")
		tools := newReadTools(t, root)
		input := ReadManyInput{Requests: []ReadRequest{{Path: "note.md", MaxBytes: 1}}}
		_, first, _ := tools.ReadMany(context.Background(), nil, input)
		changed := input
		changed.Requests = []ReadRequest{{Path: "note.md", MaxBytes: 2}}
		changed.Cursor = first.Coverage.NextCursor
		_, out, _ := tools.ReadMany(context.Background(), nil, changed)
		assertReadManyTopError(t, out, CursorMismatchCode)
	})

	for _, test := range []struct {
		name      string
		firstPath string
		setup     func(*testing.T, string)
		mutate    func(*testing.T, string)
		firstCode string
	}{
		{
			name: "file source", firstPath: "first.md",
			setup:  func(t *testing.T, root string) { writeReadFixture(t, root, "first.md", "first") },
			mutate: func(t *testing.T, root string) { writeReadFixture(t, root, "first.md", "changed content") },
		},
		{
			name: "missing outcome", firstPath: "missing.md", firstCode: string(fsx.CodeNotFound),
			setup:  func(*testing.T, string) {},
			mutate: func(t *testing.T, root string) { writeReadFixture(t, root, "missing.md", "now present") },
		},
		{
			name: "file-backed error", firstPath: "bad.md", firstCode: InvalidUTF8Code,
			setup: func(t *testing.T, root string) {
				if err := os.WriteFile(filepath.Join(root, "bad.md"), []byte{0xff}, 0o600); err != nil {
					t.Fatal(err)
				}
			},
			mutate: func(t *testing.T, root string) {
				if err := os.WriteFile(filepath.Join(root, "bad.md"), []byte{0xfe, 0xff}, 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "denial outcome", firstPath: "link.md", firstCode: string(fsx.CodeSymlinkDenied),
			setup: func(t *testing.T, root string) {
				writeReadFixture(t, root, "target.md", "target")
				if err := os.Symlink(filepath.Join(root, "target.md"), filepath.Join(root, "link.md")); err != nil {
					t.Fatal(err)
				}
			},
			mutate: func(t *testing.T, root string) {
				if err := os.Remove(filepath.Join(root, "link.md")); err != nil {
					t.Fatal(err)
				}
				writeReadFixture(t, root, "link.md", "now regular")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			test.setup(t, root)
			writeReadFixture(t, root, "current.md", "abcdefgh")
			tools := newReadTools(t, root)
			input := ReadManyInput{Requests: []ReadRequest{{Path: test.firstPath}, {Path: "current.md", MaxBytes: 1}}}
			_, first, err := tools.ReadMany(context.Background(), nil, input)
			if err != nil || !first.OK || first.NextRequestIndex != 1 || first.Coverage.NextCursor == "" {
				t.Fatalf("first page = %#v err=%v", first, err)
			}
			if test.firstCode != "" && (first.Items[0].Error == nil || first.Items[0].Error.Code != test.firstCode) {
				t.Fatalf("first observation item = %#v", first.Items[0])
			}
			test.mutate(t, root)
			input.Cursor = first.Coverage.NextCursor
			_, resumed, _ := tools.ReadMany(context.Background(), nil, input)
			assertReadManyTopError(t, resumed, CursorStaleCode)
			if resumed.Coverage.StoppedBy != string(RestartStopSourceChange) || resumed.Coverage.NextCursor != "" || len(resumed.Items) != 0 {
				t.Fatalf("stale coverage = %#v items=%#v", resumed.Coverage, resumed.Items)
			}
		})
	}
}

func TestReadManyWorstShapeCursorHasTwentyBoundedObservations(t *testing.T) {
	root := t.TempDir()
	requests := make([]ReadRequest, MaxReadManyRequests)
	for i := 0; i < MaxReadManyRequests-1; i++ {
		name := fmt.Sprintf("empty-%02d.md", i)
		writeReadFixture(t, root, name, "")
		requests[i] = ReadRequest{Path: name}
	}
	writeReadFixture(t, root, "current.md", "abcdefgh")
	requests[len(requests)-1] = ReadRequest{Path: "current.md", MaxBytes: 1}
	tools := newReadTools(t, root)
	input := ReadManyInput{Requests: requests}

	result, out, err := tools.ReadMany(context.Background(), nil, input)
	if err != nil || result.IsError || !out.OK || len(out.Items) != MaxReadManyRequests || out.Coverage.NextCursor == "" {
		t.Fatalf("result=%#v out=%#v err=%v", result, out, err)
	}
	if len(out.Coverage.NextCursor) > MaxCursorBytes {
		t.Fatalf("cursor bytes = %d, max %d", len(out.Coverage.NextCursor), MaxCursorBytes)
	}
	state, err := DecodeCursorState[readManyCursorState](tools.vault, out.Coverage.NextCursor, ToolReadMany, mustReadManyQueryHash(t, input))
	if err != nil || len(state.Observations) != MaxReadManyRequests || state.NextIndex != MaxReadManyRequests-1 {
		t.Fatalf("state=%#v err=%v", state, err)
	}
	input.Cursor = out.Coverage.NextCursor
	_, resumed, err := tools.ReadMany(context.Background(), nil, input)
	if err != nil || !resumed.OK || resumed.Coverage.SourceEntriesValidated != MaxReadManyRequests {
		t.Fatalf("resumed = %#v err=%v", resumed, err)
	}
}

func TestReadManyMaximumObservationVectorDetectsMutationAtEveryPriorIndex(t *testing.T) {
	for changedIndex := 0; changedIndex < MaxReadManyRequests-1; changedIndex++ {
		t.Run(fmt.Sprintf("index-%02d", changedIndex), func(t *testing.T) {
			root := t.TempDir()
			requests := make([]ReadRequest, MaxReadManyRequests)
			for i := 0; i < MaxReadManyRequests-1; i++ {
				name := fmt.Sprintf("prior-%02d.md", i)
				writeReadFixture(t, root, name, "")
				requests[i] = ReadRequest{Path: name}
			}
			writeReadFixture(t, root, "current.md", "abcdefgh")
			requests[len(requests)-1] = ReadRequest{Path: "current.md", MaxBytes: 1}
			tools := newReadTools(t, root)
			input := ReadManyInput{Requests: requests}
			_, first, err := tools.ReadMany(context.Background(), nil, input)
			if err != nil || !first.OK || first.NextRequestIndex != MaxReadManyRequests-1 || first.Coverage.NextCursor == "" {
				t.Fatalf("first = %#v err=%v", first, err)
			}

			writeReadFixture(t, root, fmt.Sprintf("prior-%02d.md", changedIndex), "changed")
			input.Cursor = first.Coverage.NextCursor
			_, stale, err := tools.ReadMany(context.Background(), nil, input)
			if err != nil || stale.OK || stale.Error == nil || stale.Error.Code != CursorStaleCode ||
				stale.Coverage.Continuation != CoverageContinuationRestart || stale.Coverage.NextCursor != "" || len(stale.Items) != 0 {
				t.Fatalf("stale = %#v err=%v", stale, err)
			}
			if got, want := stale.Coverage.SourceEntriesValidated, uint64(changedIndex+1); got != want {
				t.Fatalf("source_entries_validated = %d, want %d", got, want)
			}
		})
	}
}

func TestReadManyRejectsTamperedOuterVectorAndNestedCursorBeforeSourceWork(t *testing.T) {
	root := t.TempDir()
	requests := make([]ReadRequest, MaxReadManyRequests)
	for i := 0; i < MaxReadManyRequests-1; i++ {
		name := fmt.Sprintf("prior-%02d.md", i)
		writeReadFixture(t, root, name, "")
		requests[i] = ReadRequest{Path: name}
	}
	writeReadFixture(t, root, "current.md", "abcdefgh")
	requests[len(requests)-1] = ReadRequest{Path: "current.md", MaxBytes: 1}
	tools := newReadTools(t, root)
	input := ReadManyInput{Requests: requests}
	_, first, err := tools.ReadMany(context.Background(), nil, input)
	if err != nil || !first.OK || first.Coverage.NextCursor == "" {
		t.Fatalf("first = %#v err=%v", first, err)
	}

	tests := []struct {
		name string
		edit func(map[string]any)
	}{
		{name: "outer index", edit: func(state map[string]any) { state["n"] = float64(MaxReadManyRequests - 2) }},
		{name: "observation vector", edit: func(state map[string]any) {
			observations := state["o"].([]any)
			observations[0].(map[string]any)["f"] = base64.RawURLEncoding.EncodeToString(make([]byte, 32))
		}},
		{name: "nested current read state", edit: func(state map[string]any) {
			observations := state["o"].([]any)
			current := observations[len(observations)-1].(map[string]any)
			inner := current["r"].(string)
			current["r"] = tamperCursorState(t, inner, func(innerState map[string]any) {
				innerState["next_byte"] = innerState["next_byte"].(float64) + 1
			})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changed := input
			changed.Cursor = tamperCursorState(t, first.Coverage.NextCursor, tt.edit)
			_, rejected, err := tools.ReadMany(context.Background(), nil, changed)
			if err != nil || rejected.OK || rejected.Error == nil || rejected.Error.Code != CursorInvalidCode ||
				rejected.Coverage.SourceEntriesValidated != 0 || rejected.Coverage.FilesScanned != 0 || len(rejected.Items) != 0 {
				t.Fatalf("rejected = %#v err=%v", rejected, err)
			}
		})
	}
}

func TestReadManyAdaptiveFitStaysWithinCompleteSDKEnvelope(t *testing.T) {
	root := t.TempDir()
	writeReadFixture(t, root, "large.md", strings.Repeat("<>&abcdef\n", 30_000))
	tools := newReadTools(t, root)
	input := ReadManyInput{Requests: []ReadRequest{{Path: "large.md", MaxBytes: MaxReadBytes}}, MaxBytes: MaxReadManyBytes}
	result, out, err := tools.ReadMany(context.Background(), nil, input)
	if err != nil || result.IsError || !out.OK || !out.Truncated || out.Coverage.NextCursor == "" {
		t.Fatalf("result=%#v out=%#v err=%v", result, out, err)
	}
	if out.Items[0].Coverage.NextCursor != out.Coverage.NextCursor || out.Coverage.StoppedBy != string(CursorStopResponseLimit) {
		t.Fatalf("coverage = %#v item=%#v", out.Coverage, out.Items[0].Coverage)
	}
	size, err := CompleteSDKResultBytes(result, out)
	if err != nil {
		t.Fatal(err)
	}
	if size > MaxSDKResultBytes {
		t.Fatalf("complete SDK result = %d bytes, max %d", size, MaxSDKResultBytes)
	}
}

func TestReadManyAggregateResidualReturnsCheckpointAcrossUnicodeSelectorsWithoutReplay(t *testing.T) {
	tests := []struct {
		name        string
		markdown    string
		selector    *ReadSelector
		wantContent string
		wantOutline []OutlineEntry
	}{
		{name: "content", markdown: "βeta", selector: &ReadSelector{Kind: SelectorContent}, wantContent: "βeta"},
		{name: "setext heading", markdown: "βeta\n====\nbody\n# Next\n", selector: &ReadSelector{Kind: SelectorHeading, Heading: "βeta"}, wantContent: "βeta\n====\nbody\n"},
		{name: "block", markdown: "βeta ^proof\n", selector: &ReadSelector{Kind: SelectorBlock, BlockID: "proof"}, wantContent: "βeta ^proof\n"},
		{
			name: "outline", markdown: "# βeta\n## Child\n", selector: &ReadSelector{Kind: SelectorOutline},
			wantOutline: []OutlineEntry{{Line: 1, Level: 1, Text: "βeta"}, {Line: 2, Level: 2, Text: "Child"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeReadFixture(t, root, "first.md", "123456789")
			writeReadFixture(t, root, "selected.md", tt.markdown)
			tools := newReadTools(t, root)
			input := ReadManyInput{
				Requests: []ReadRequest{
					{Path: "first.md", MaxBytes: 9},
					{Path: "selected.md", Selector: tt.selector, MaxBytes: 64},
				},
				MaxBytes: 10,
			}

			var content strings.Builder
			var outline []OutlineEntry
			completed := false
			for page := 0; page < 10; page++ {
				result, out, err := tools.ReadMany(context.Background(), nil, input)
				if err != nil || result.IsError || !out.OK {
					t.Fatalf("page %d result=%#v out=%#v err=%v", page, result, out, err)
				}
				if size, sizeErr := CompleteSDKResultBytes(result, out); sizeErr != nil || size > MaxSDKResultBytes {
					t.Fatalf("page %d SDK size=%d err=%v", page, size, sizeErr)
				}

				if page == 0 {
					if len(out.Items) != 1 || out.Items[0].Index != 0 || out.Items[0].Content == nil || *out.Items[0].Content != "123456789" ||
						out.NextRequestIndex != 1 || out.RemainingRequestCount != 1 || out.Coverage.StoppedBy != string(CursorStopByteLimit) || out.Coverage.NextCursor == "" {
						t.Fatalf("aggregate fallback = %#v", out)
					}
					state, decodeErr := DecodeCursorState[readManyCursorState](tools.vault, out.Coverage.NextCursor, ToolReadMany, mustReadManyQueryHash(t, input))
					if decodeErr != nil || state.NextIndex != 1 || len(state.Observations) != 1 {
						t.Fatalf("checkpoint state=%#v err=%v", state, decodeErr)
					}
				} else {
					for _, item := range out.Items {
						if item.Index != 1 || !item.OK {
							t.Fatalf("page %d replayed or failed item: %#v", page, item)
						}
						if item.Content != nil {
							content.WriteString(*item.Content)
						}
						if item.Outline != nil {
							outline = append(outline, (*item.Outline)...)
						}
					}
				}

				if out.Coverage.Continuation == CoverageContinuationComplete {
					completed = true
					break
				}
				if out.Coverage.NextCursor == "" {
					t.Fatalf("page %d missing cursor: %#v", page, out)
				}
				input.Cursor = out.Coverage.NextCursor
			}
			if !completed {
				t.Fatal("selector did not complete")
			}
			if got := content.String(); got != tt.wantContent {
				t.Fatalf("content = %q, want %q", got, tt.wantContent)
			}
			if !reflect.DeepEqual(outline, tt.wantOutline) {
				t.Fatalf("outline = %#v, want %#v", outline, tt.wantOutline)
			}
		})
	}
}

func TestReadManyResponseLimitUsesProgressAndTerminalErrorWithoutCheckpoint(t *testing.T) {
	root := t.TempDir()
	writeReadFixture(t, root, "first.md", "first")
	tools := newReadTools(t, root)
	selector := &ReadSelector{Kind: SelectorOutline}

	// Find the largest indivisible Unicode outline entry that still fits a
	// direct read response. The read_many wrapper is larger, so this fixture
	// forces its response adaptation to a byte cap that cannot advance.
	best := 0
	for low, high := 1, MaxSDKResultBytes; low <= high; {
		middle := low + (high-low)/2
		writeReadFixture(t, root, "outline.md", "# "+strings.Repeat("β", middle)+"\n")
		_, out, err := tools.Read(context.Background(), nil, ReadInput{Path: "outline.md", Selector: selector, MaxBytes: MaxReadBytes})
		if err != nil {
			t.Fatalf("direct read at %d runes: %v", middle, err)
		}
		if out.OK {
			best = middle
			low = middle + 1
			continue
		}
		if out.Error == nil || out.Error.Code != ResponseTooLargeCode {
			t.Fatalf("direct read at %d runes = %#v", middle, out)
		}
		high = middle - 1
	}
	if best == 0 || best == MaxSDKResultBytes {
		t.Fatalf("could not locate direct response boundary: best=%d", best)
	}

	writeReadFixture(t, root, "outline.md", "# "+strings.Repeat("β", best)+"\n")
	_, adapted, err := tools.ReadMany(context.Background(), nil, ReadManyInput{
		Requests: []ReadRequest{{Path: "outline.md", Selector: selector, MaxBytes: MaxReadBytes}},
		MaxBytes: MaxReadManyBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertReadManyTopError(t, adapted, ResponseTooLargeCode)
	if len(adapted.Items) != 0 || adapted.NextRequestIndex != 0 {
		t.Fatalf("response-adapted terminal result = %#v", adapted)
	}

	writeReadFixture(t, root, "outline.md", "# "+strings.Repeat("β", best+1)+"\n")
	input := ReadManyInput{
		Requests: []ReadRequest{
			{Path: "first.md", MaxBytes: 5},
			{Path: "outline.md", Selector: selector, MaxBytes: MaxReadBytes},
		},
		MaxBytes: MaxReadManyBytes,
	}
	result, first, err := tools.ReadMany(context.Background(), nil, input)
	if err != nil || result.IsError || !first.OK || len(first.Items) != 1 || first.Items[0].Index != 0 ||
		first.NextRequestIndex != 1 || first.Coverage.StoppedBy != string(CursorStopResponseLimit) || first.Coverage.NextCursor == "" {
		t.Fatalf("response progress fallback result=%#v out=%#v err=%v", result, first, err)
	}
	if size, sizeErr := CompleteSDKResultBytes(result, first); sizeErr != nil || size > MaxSDKResultBytes {
		t.Fatalf("response fallback SDK size=%d err=%v", size, sizeErr)
	}

	input.Cursor = first.Coverage.NextCursor
	_, resumed, err := tools.ReadMany(context.Background(), nil, input)
	if err != nil {
		t.Fatal(err)
	}
	assertReadManyTopError(t, resumed, ResponseTooLargeCode)
	if len(resumed.Items) != 0 || resumed.NextRequestIndex != 1 {
		t.Fatalf("resumed terminal result = %#v", resumed)
	}
}

func TestReadManyDistinguishesAggregateFromPerItemNonAdvancingLimit(t *testing.T) {
	root := t.TempDir()
	writeReadFixture(t, root, "unicode.md", "βeta")
	tools := newReadTools(t, root)

	_, aggregate, _ := tools.ReadMany(context.Background(), nil, ReadManyInput{
		Requests: []ReadRequest{{Path: "unicode.md", MaxBytes: 10}}, MaxBytes: 1,
	})
	assertReadManyTopError(t, aggregate, string(fsx.CodeLimitExceeded))

	_, item, err := tools.ReadMany(context.Background(), nil, ReadManyInput{
		Requests: []ReadRequest{{Path: "unicode.md", MaxBytes: 1}}, MaxBytes: 10,
	})
	if err != nil || !item.OK || len(item.Items) != 1 || item.Items[0].OK || item.Items[0].Error == nil || item.Items[0].Error.Code != string(fsx.CodeLimitExceeded) || item.Coverage.Continuation != CoverageContinuationComplete {
		t.Fatalf("item-limited batch = %#v err=%v", item, err)
	}

	writeReadFixture(t, root, "nested.md", "aβ")
	nestedInput := ReadManyInput{
		Requests: []ReadRequest{{Path: "nested.md", MaxBytes: 10}}, MaxBytes: 1,
	}
	_, first, err := tools.ReadMany(context.Background(), nil, nestedInput)
	if err != nil || !first.OK || len(first.Items) != 1 || first.Items[0].Content == nil || *first.Items[0].Content != "a" || first.Coverage.NextCursor == "" {
		t.Fatalf("nested first page = %#v err=%v", first, err)
	}
	nestedInput.Cursor = first.Coverage.NextCursor
	_, resumed, err := tools.ReadMany(context.Background(), nil, nestedInput)
	if err != nil {
		t.Fatal(err)
	}
	assertReadManyTopError(t, resumed, string(fsx.CodeLimitExceeded))
	if len(resumed.Items) != 0 || resumed.NextRequestIndex != 0 {
		t.Fatalf("nested no-checkpoint result = %#v", resumed)
	}
}

func TestReadManyRejectsTopLevelShapeAndTermination(t *testing.T) {
	root := t.TempDir()
	writeReadFixture(t, root, "note.md", "note")
	tools := newReadTools(t, root)
	for name, input := range map[string]ReadManyInput{
		"empty":         {},
		"too many":      {Requests: make([]ReadRequest, MaxReadManyRequests+1)},
		"bad aggregate": {Requests: []ReadRequest{{Path: "note.md"}}, MaxBytes: MaxReadManyBytes + 1},
	} {
		t.Run(name, func(t *testing.T) {
			_, out, _ := tools.ReadMany(context.Background(), nil, input)
			assertReadManyTopError(t, out, string(fsx.CodeLimitExceeded))
		})
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, canceledOut, _ := tools.ReadMany(canceled, nil, ReadManyInput{Requests: []ReadRequest{{Path: "note.md"}}})
	assertReadManyTopError(t, canceledOut, string(fsx.CodeCanceled))

	expired, cancelExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelExpired()
	_, timeoutOut, _ := tools.ReadMany(expired, nil, ReadManyInput{Requests: []ReadRequest{{Path: "note.md"}}})
	assertReadManyTopError(t, timeoutOut, string(fsx.CodeTimeout))
}

func mustReadManyQueryHash(t *testing.T, input ReadManyInput) CursorQueryHash {
	t.Helper()
	_, _, query, err := prepareReadMany(input)
	if err != nil {
		t.Fatalf("prepareReadMany: %v", err)
	}
	return query
}

func assertReadManyTopError(t *testing.T, out ReadManyOutput, code string) {
	t.Helper()
	if out.OK || out.Error == nil || out.Error.Code != code || out.Coverage.Continuation != CoverageContinuationRestart || out.Coverage.NextCursor != "" {
		t.Fatalf("out = %#v, want top-level %q", out, code)
	}
}

func TestReadManyCursorStateValidationRejectsMalformedObservations(t *testing.T) {
	fingerprint := fingerprintString(fsx.SourceFingerprint{1})
	valid := readManyCursorState{NextIndex: 1, Observations: []readManyObservation{{Index: 0, Outcome: readManyOutcomeFile, Fingerprint: fingerprint}}}
	if !validReadManyCursorState(valid, 2) {
		t.Fatal("valid cursor state rejected")
	}
	for name, mutate := range map[string]func(*readManyCursorState){
		"gap":             func(s *readManyCursorState) { s.Observations[0].Index = 1 },
		"bad fingerprint": func(s *readManyCursorState) { s.Observations[0].Fingerprint = "bad" },
		"completed inner": func(s *readManyCursorState) { s.Observations[0].InnerCursor = "cursor"; s.Observations[0].InnerMax = 1 },
		"too many":        func(s *readManyCursorState) { s.Observations = make([]readManyObservation, MaxReadManyRequests+1) },
	} {
		t.Run(name, func(t *testing.T) {
			state := cloneReadManyState(valid)
			mutate(&state)
			if validReadManyCursorState(state, 2) {
				t.Fatalf("malformed state accepted: %#v", state)
			}
		})
	}
}

func TestReadManyErrorConversionCoversTopLevelClasses(t *testing.T) {
	for code, want := range map[string]error{
		CursorInvalidCode:             ErrCursorInvalid,
		CursorMismatchCode:            ErrCursorMismatch,
		CursorStaleCode:               ErrCursorStale,
		ResponseTooLargeCode:          ErrResponseTooLarge,
		string(fsx.CodeTimeout):       &fsx.Error{Code: fsx.CodeTimeout},
		string(fsx.CodeCanceled):      &fsx.Error{Code: fsx.CodeCanceled},
		string(fsx.CodeSourceChanged): &fsx.Error{Code: fsx.CodeSourceChanged},
	} {
		got := toolErrorAsError(&ToolError{Code: code})
		if CursorErrorCode(want) != "" {
			if !errors.Is(got, want) {
				t.Fatalf("code %q error = %v, want %v", code, got, want)
			}
			continue
		}
		if retrievalErrorCode(got) != retrievalErrorCode(want) {
			t.Fatalf("code %q error = %v, want class %v", code, got, want)
		}
	}
}
