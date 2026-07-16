package obsidian

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"personal-mcp-gateway/internal/fsx"
)

func TestReadContentContinuationPreservesBytesWithoutReplay(t *testing.T) {
	root := t.TempDir()
	want := "alpha\r\nβeta\nlast line"
	writeReadFixture(t, root, "note.md", want)
	tools := newReadTools(t, root)
	input := ReadInput{Path: "note.md", MaxBytes: 7}

	var got strings.Builder
	lastEnd := 0
	for page := 0; page < 20; page++ {
		result, out, err := tools.Read(context.Background(), nil, input)
		if err != nil || !out.OK || result.IsError || out.Content == nil || out.Outline != nil {
			t.Fatalf("page %d result=%#v out=%#v err=%v", page, result, out, err)
		}
		if page > 0 && out.Coverage.BytesScanned > uint64(input.MaxBytes) {
			t.Fatalf("page %d reread prior content: bytes_scanned=%d max_bytes=%d", page, out.Coverage.BytesScanned, input.MaxBytes)
		}
		if out.StartLine < lastEnd && page > 0 {
			t.Fatalf("page %d line range regressed: prior end=%d current=%d-%d", page, lastEnd, out.StartLine, out.EndLine)
		}
		lastEnd = out.EndLine
		got.WriteString(*out.Content)
		if out.Coverage.Continuation == CoverageContinuationComplete {
			if out.Truncated || !out.Coverage.ResultComplete || !out.Coverage.ScopeComplete {
				t.Fatalf("final coverage = %#v", out.Coverage)
			}
			break
		}
		if out.Coverage.Continuation != CoverageContinuationCursor || out.Coverage.NextCursor == "" || !out.Truncated {
			t.Fatalf("partial coverage = %#v", out.Coverage)
		}
		input.Cursor = out.Coverage.NextCursor
	}
	if got.String() != want {
		t.Fatalf("accumulated content = %q, want %q", got.String(), want)
	}
}

func TestReadAllSelectorsHaveMultiPageEquivalence(t *testing.T) {
	root := t.TempDir()
	content := "---\ntitle: Cursor Proof\nowner: Eric\n---\n# Top\nalpha body\n## Child\nchild body\n# Next\nblock alpha\nblock beta ^proof\n\n"
	writeReadFixture(t, root, "note.md", content)
	tools := newReadTools(t, root)

	tests := []struct {
		name     string
		selector *ReadSelector
		pageMax  int
	}{
		{name: "content", selector: &ReadSelector{Kind: SelectorContent, StartLine: 2}, pageMax: 7},
		{name: "heading", selector: &ReadSelector{Kind: SelectorHeading, Heading: "Top"}, pageMax: 8},
		{name: "block", selector: &ReadSelector{Kind: SelectorBlock, BlockID: "proof"}, pageMax: 8},
		{name: "frontmatter", selector: &ReadSelector{Kind: SelectorFrontmatter}, pageMax: 8},
		{name: "outline", selector: &ReadSelector{Kind: SelectorOutline}, pageMax: 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, complete, err := tools.Read(context.Background(), nil, ReadInput{
				Path: "note.md", Selector: tt.selector, MaxBytes: MaxReadBytes,
			})
			if err != nil || !complete.OK || complete.Truncated {
				t.Fatalf("complete = %#v err=%v", complete, err)
			}

			input := ReadInput{Path: "note.md", Selector: tt.selector, MaxBytes: tt.pageMax}
			var accumulated strings.Builder
			var outline []OutlineEntry
			pages := 0
			for ; pages < 50; pages++ {
				_, page, err := tools.Read(context.Background(), nil, input)
				if err != nil || !page.OK {
					t.Fatalf("page %d = %#v err=%v", pages, page, err)
				}
				if page.Content != nil {
					accumulated.WriteString(*page.Content)
				}
				if page.Outline != nil {
					outline = append(outline, (*page.Outline)...)
				}
				if page.Coverage.Continuation == CoverageContinuationComplete {
					break
				}
				if page.Coverage.NextCursor == "" {
					t.Fatalf("page %d missing cursor: %#v", pages, page)
				}
				input.Cursor = page.Coverage.NextCursor
			}
			if pages < 1 {
				t.Fatalf("selector did not span multiple pages: %#v", complete)
			}
			if complete.Content != nil && accumulated.String() != *complete.Content {
				t.Fatalf("content accumulation = %q, want %q", accumulated.String(), *complete.Content)
			}
			if complete.Outline != nil && !reflect.DeepEqual(outline, *complete.Outline) {
				t.Fatalf("outline accumulation = %#v, want %#v", outline, *complete.Outline)
			}
		})
	}
}

func TestReadRejectsTamperedSelectorPositionsBeforeSourceWork(t *testing.T) {
	tests := []struct {
		name     string
		selector *ReadSelector
		maxBytes int
		edit     func(map[string]any)
	}{
		{name: "heading end cannot widen", selector: &ReadSelector{Kind: SelectorHeading, Heading: "Top"}, maxBytes: 6, edit: func(state map[string]any) { state["unit_end"] = state["unit_end"].(float64) + 1 }},
		{name: "content byte cannot replay", selector: &ReadSelector{Kind: SelectorContent}, maxBytes: 6, edit: func(state map[string]any) { state["next_byte"] = state["next_byte"].(float64) - 1 }},
		{name: "content line cannot relabel", selector: &ReadSelector{Kind: SelectorContent}, maxBytes: 6, edit: func(state map[string]any) { state["next_line"] = state["next_line"].(float64) + 1 }},
		{name: "outline index cannot skip", selector: &ReadSelector{Kind: SelectorOutline}, maxBytes: 10, edit: func(state map[string]any) { state["next_outline"] = state["next_outline"].(float64) + 1 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeReadFixture(t, root, "note.md", "# Top\nalpha body\n## Child\nchild body\n# Next\ntail\n")
			tools := newReadTools(t, root)
			input := ReadInput{Path: "note.md", Selector: tt.selector, MaxBytes: tt.maxBytes}
			_, first, err := tools.Read(context.Background(), nil, input)
			if err != nil || !first.OK || first.Coverage.NextCursor == "" {
				t.Fatalf("first = %#v err=%v", first, err)
			}
			input.Cursor = tamperCursorState(t, first.Coverage.NextCursor, tt.edit)
			if err := os.Remove(filepath.Join(root, "note.md")); err != nil {
				t.Fatal(err)
			}
			_, rejected, err := tools.Read(context.Background(), nil, input)
			if err != nil || rejected.OK || rejected.Error == nil || rejected.Error.Code != CursorInvalidCode || rejected.Coverage.FilesScanned != 0 {
				t.Fatalf("rejected = %#v err=%v", rejected, err)
			}
		})
	}
}

func TestReadHeadingAndOutlineUseNormalizedSelectors(t *testing.T) {
	root := t.TempDir()
	writeReadFixture(t, root, "note.md", "# Top\nbody\n## Child\nchild\n# Next\nend\n")
	tools := newReadTools(t, root)

	_, heading, err := tools.Read(context.Background(), nil, ReadInput{
		Path: "note.md", Selector: &ReadSelector{Kind: SelectorHeading, Heading: " Top "},
	})
	if err != nil || !heading.OK || heading.Content == nil || *heading.Content != "# Top\nbody\n## Child\nchild\n" {
		t.Fatalf("heading = %#v err=%v", heading, err)
	}
	if heading.Selector == nil || heading.Selector.Heading != "Top" || heading.Selector.Occurrence != 1 {
		t.Fatalf("normalized heading selector = %#v", heading.Selector)
	}

	_, outline, err := tools.Read(context.Background(), nil, ReadInput{
		Path: "note.md", Selector: &ReadSelector{Kind: SelectorOutline},
	})
	if err != nil || !outline.OK || outline.Content != nil || outline.Outline == nil || len(*outline.Outline) != 3 {
		t.Fatalf("outline = %#v err=%v", outline, err)
	}
}

func TestReadContinuationRejectsQueryAndSourceChanges(t *testing.T) {
	root := t.TempDir()
	filename := filepath.Join(root, "note.md")
	writeReadFixture(t, root, "note.md", strings.Repeat("abcdef", 20))
	tools := newReadTools(t, root)

	_, first, err := tools.Read(context.Background(), nil, ReadInput{Path: "note.md", MaxBytes: 8})
	if err != nil || first.Coverage.NextCursor == "" {
		t.Fatalf("first = %#v err=%v", first, err)
	}
	_, mismatch, _ := tools.Read(context.Background(), nil, ReadInput{
		Path: "note.md", MaxBytes: 9, Cursor: first.Coverage.NextCursor,
	})
	if mismatch.OK || mismatch.Error == nil || mismatch.Error.Code != CursorMismatchCode || mismatch.Coverage.Continuation != CoverageContinuationRestart {
		t.Fatalf("mismatch = %#v", mismatch)
	}

	if err := os.WriteFile(filename, []byte(strings.Repeat("z", 121)), 0o600); err != nil {
		t.Fatal(err)
	}
	_, stale, _ := tools.Read(context.Background(), nil, ReadInput{
		Path: "note.md", MaxBytes: 8, Cursor: first.Coverage.NextCursor,
	})
	if stale.OK || stale.Error == nil || stale.Error.Code != CursorStaleCode || stale.Coverage.NextCursor != "" {
		t.Fatalf("stale = %#v", stale)
	}
}

func TestReadContinuationBindsNormalizedRequestBeforeReopen(t *testing.T) {
	root := t.TempDir()
	writeReadFixture(t, root, "Note.md", strings.Repeat("abcdef", 20))
	tools := newReadTools(t, root)

	_, first, err := tools.Read(context.Background(), nil, ReadInput{Path: "note.md", MaxBytes: 8})
	if err != nil || first.Coverage.NextCursor == "" || first.Path != "Note.md" {
		t.Fatalf("first = %#v err=%v", first, err)
	}
	_, mismatch, err := tools.Read(context.Background(), nil, ReadInput{
		Path: "missing.md", MaxBytes: 8, Cursor: first.Coverage.NextCursor,
	})
	if err != nil || mismatch.Error == nil || mismatch.Error.Code != CursorMismatchCode {
		t.Fatalf("changed missing query = %#v err=%v", mismatch, err)
	}

	if err := os.Remove(filepath.Join(root, "Note.md")); err != nil {
		t.Fatal(err)
	}
	_, stale, err := tools.Read(context.Background(), nil, ReadInput{
		Path: "note.md", MaxBytes: 8, Cursor: first.Coverage.NextCursor,
	})
	if err != nil || stale.Error == nil || stale.Error.Code != CursorStaleCode || stale.Coverage.StoppedBy != string(RestartStopSourceChange) {
		t.Fatalf("deleted case-folded source = %#v err=%v", stale, err)
	}
}

func TestReadRejectsUnsupportedInvalidUTF8AndSourceComplexity(t *testing.T) {
	root := t.TempDir()
	writeReadFixture(t, root, "plain.txt", "text")
	if err := os.WriteFile(filepath.Join(root, "bad.md"), []byte{0xff}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "large.md"), []byte(strings.Repeat("x", MaxMarkdownSourceBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	tools := newReadTools(t, root)
	for _, test := range []struct {
		path string
		code string
	}{
		{path: "plain.txt", code: UnsupportedFileCode},
		{path: "bad.md", code: InvalidUTF8Code},
		{path: "large.md", code: string(fsx.CodeInputTooLarge)},
	} {
		result, out, err := tools.Read(context.Background(), nil, ReadInput{Path: test.path})
		if err != nil || !result.IsError || out.OK || out.Error == nil || out.Error.Code != test.code || out.Coverage.Continuation != CoverageContinuationRestart || out.Coverage.NextCursor != "" {
			t.Fatalf("%s result=%#v out=%#v err=%v", test.path, result, out, err)
		}
	}
}

func TestReadResponseFitMeasuresCompleteSDKEnvelope(t *testing.T) {
	root := t.TempDir()
	writeReadFixture(t, root, "note.md", strings.Repeat("<>&abcdef\n", 30_000))
	tools := newReadTools(t, root)
	result, out, err := tools.Read(context.Background(), nil, ReadInput{Path: "note.md", MaxBytes: MaxReadBytes})
	if err != nil || !out.OK || !out.Truncated || out.Coverage.StoppedBy != string(CursorStopResponseLimit) {
		t.Fatalf("out = %#v err=%v", out, err)
	}
	if out.Content == nil || len(*out.Content) == 0 {
		t.Fatal("response-limited read did not advance")
	}
	size, err := CompleteSDKResultBytes(result, out)
	if err != nil {
		t.Fatal(err)
	}
	if size > MaxSDKResultBytes {
		t.Fatalf("complete SDK result = %d bytes, max %d", size, MaxSDKResultBytes)
	}
}

func newReadTools(t *testing.T, root string) *Tools {
	t.Helper()
	vault, err := fsx.NewVault(root)
	if err != nil {
		t.Fatal(err)
	}
	return New(vault)
}

func writeReadFixture(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
