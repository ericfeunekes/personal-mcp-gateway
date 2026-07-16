package obsidian

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestSourceSelectorNormalizeIsClosedAndAppliesDefaults(t *testing.T) {
	tests := []struct {
		name    string
		input   SourceSelector
		want    SourceSelector
		wantErr bool
	}{
		{name: "default", input: SourceSelector{}, want: SourceSelector{Kind: SourceSelectorContent, StartLine: 1}},
		{name: "content", input: SourceSelector{Kind: SourceSelectorContent, StartLine: 3}, want: SourceSelector{Kind: SourceSelectorContent, StartLine: 3}},
		{name: "heading", input: SourceSelector{Kind: SourceSelectorHeading, Heading: "  Cafe\u0301  "}, want: SourceSelector{Kind: SourceSelectorHeading, Heading: "Café", Occurrence: 1}},
		{name: "heading occurrence", input: SourceSelector{Kind: SourceSelectorHeading, Heading: "A", Occurrence: 2}, want: SourceSelector{Kind: SourceSelectorHeading, Heading: "A", Occurrence: 2}},
		{name: "block", input: SourceSelector{Kind: SourceSelectorBlock, BlockID: "daily-1"}, want: SourceSelector{Kind: SourceSelectorBlock, BlockID: "daily-1"}},
		{name: "frontmatter", input: SourceSelector{Kind: SourceSelectorFrontmatter}, want: SourceSelector{Kind: SourceSelectorFrontmatter}},
		{name: "outline", input: SourceSelector{Kind: SourceSelectorOutline}, want: SourceSelector{Kind: SourceSelectorOutline}},
		{name: "unknown kind", input: SourceSelector{Kind: "unknown"}, wantErr: true},
		{name: "mixed content", input: SourceSelector{Kind: SourceSelectorContent, Heading: "A"}, wantErr: true},
		{name: "mixed heading", input: SourceSelector{Kind: SourceSelectorHeading, Heading: "A", StartLine: 1}, wantErr: true},
		{name: "mixed block", input: SourceSelector{Kind: SourceSelectorBlock, BlockID: "id", Occurrence: 1}, wantErr: true},
		{name: "mixed outline", input: SourceSelector{Kind: SourceSelectorOutline, BlockID: "id"}, wantErr: true},
		{name: "negative start", input: SourceSelector{Kind: SourceSelectorContent, StartLine: -1}, wantErr: true},
		{name: "negative occurrence", input: SourceSelector{Kind: SourceSelectorHeading, Heading: "A", Occurrence: -1}, wantErr: true},
		{name: "empty heading", input: SourceSelector{Kind: SourceSelectorHeading, Heading: "  "}, wantErr: true},
		{name: "empty block", input: SourceSelector{Kind: SourceSelectorBlock}, wantErr: true},
		{name: "block includes caret", input: SourceSelector{Kind: SourceSelectorBlock, BlockID: "^id"}, wantErr: true},
		{name: "block includes space", input: SourceSelector{Kind: SourceSelectorBlock, BlockID: "bad id"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.input.Normalize()
			if tt.wantErr {
				if !errors.Is(err, ErrSourceSelectorInvalid) {
					t.Fatalf("error = %v, want ErrSourceSelectorInvalid", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Normalize: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("normalized = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestHeadingSelectionPreservesOriginalRangesAndOccurrence(t *testing.T) {
	const markdown = "# Cafe\u0301 ###\r\n" +
		"intro\r\n" +
		"## Child\r\n" +
		"child\r\n" +
		"> ## Quoted\r\n" +
		"> body\r\n" +
		"# Café\r\n" +
		"second\r\n" +
		"\r\n" +
		"Setext\r\n" +
		"======\r\n" +
		"after\r\n"
	source := mustMarkdownSource(t, markdown)

	first, err := source.Select(SourceSelector{Kind: SourceSelectorHeading, Heading: "Café"})
	if err != nil {
		t.Fatalf("first heading: %v", err)
	}
	wantFirst := "# Cafe\u0301 ###\r\nintro\r\n## Child\r\nchild\r\n> ## Quoted\r\n> body\r\n"
	if got := string(first.Content); got != wantFirst {
		t.Fatalf("first content = %q, want %q", got, wantFirst)
	}
	if first.StartLine != 1 || first.EndLine != 6 {
		t.Fatalf("first line range = %d..%d, want 1..6", first.StartLine, first.EndLine)
	}

	second, err := source.Select(SourceSelector{Kind: SourceSelectorHeading, Heading: "Café", Occurrence: 2})
	if err != nil {
		t.Fatalf("second heading: %v", err)
	}
	if got, want := string(second.Content), "# Café\r\nsecond\r\n\r\n"; got != want {
		t.Fatalf("second content = %q, want %q", got, want)
	}

	setext, err := source.Select(SourceSelector{Kind: SourceSelectorHeading, Heading: "Setext"})
	if err != nil {
		t.Fatalf("Setext heading: %v", err)
	}
	if got, want := string(setext.Content), "Setext\r\n======\r\nafter\r\n"; got != want {
		t.Fatalf("Setext content = %q, want %q", got, want)
	}
}

func TestNestedHeadingAndSetextRangesKeepQuoteAndListMarkers(t *testing.T) {
	const markdown = "> - ## Nested ###\n" +
		">   body\n" +
		">\n" +
		"> Quoted setext\n" +
		"> -------------\n" +
		"> tail\n"
	source := mustMarkdownSource(t, markdown)

	nested, err := source.Select(SourceSelector{Kind: SourceSelectorHeading, Heading: "Nested"})
	if err != nil {
		t.Fatalf("nested heading: %v", err)
	}
	if got, want := string(nested.Content), "> - ## Nested ###\n>   body\n>\n"; got != want {
		t.Fatalf("nested content = %q, want %q", got, want)
	}

	setext, err := source.Select(SourceSelector{Kind: SourceSelectorHeading, Heading: "Quoted setext"})
	if err != nil {
		t.Fatalf("quoted setext: %v", err)
	}
	if got, want := string(setext.Content), "> Quoted setext\n> -------------\n> tail\n"; got != want {
		t.Fatalf("quoted setext content = %q, want %q", got, want)
	}
}

func TestHeadingAndBlockLookingTextInsideCodeIsExcluded(t *testing.T) {
	const markdown = "```md\n# Fake\nfake ^code\n```\n\n" +
		"    # Indented fake\n" +
		"    fake ^indented\n\n" +
		"# Real\nreal\n"
	source := mustMarkdownSource(t, markdown)

	for _, heading := range []string{"Fake", "Indented fake"} {
		if _, err := source.Select(SourceSelector{Kind: SourceSelectorHeading, Heading: heading}); !errors.Is(err, ErrSourceUnitNotFound) {
			t.Fatalf("heading %q error = %v, want not found", heading, err)
		}
	}
	for _, id := range []string{"code", "indented"} {
		if _, err := source.Select(SourceSelector{Kind: SourceSelectorBlock, BlockID: id}); !errors.Is(err, ErrSourceUnitNotFound) {
			t.Fatalf("block %q error = %v, want not found", id, err)
		}
	}
}

func TestBlockSelectionUsesSmallestOriginalLeafBlock(t *testing.T) {
	const markdown = "plain first\r\ncontinued ^plain\r\n\r\n" +
		"> - nested first\r\n" +
		">   continued ^nested\r\n\r\n" +
		"joined^not-token\r\n"
	source := mustMarkdownSource(t, markdown)

	plain, err := source.Select(SourceSelector{Kind: SourceSelectorBlock, BlockID: "plain"})
	if err != nil {
		t.Fatalf("plain block: %v", err)
	}
	if got, want := string(plain.Content), "plain first\r\ncontinued ^plain\r\n"; got != want {
		t.Fatalf("plain content = %q, want %q", got, want)
	}

	nested, err := source.Select(SourceSelector{Kind: SourceSelectorBlock, BlockID: "nested"})
	if err != nil {
		t.Fatalf("nested block: %v", err)
	}
	if got, want := string(nested.Content), "> - nested first\r\n>   continued ^nested\r\n"; got != want {
		t.Fatalf("nested content = %q, want %q", got, want)
	}

	if _, err := source.Select(SourceSelector{Kind: SourceSelectorBlock, BlockID: "not-token"}); !errors.Is(err, ErrSourceUnitNotFound) {
		t.Fatalf("joined marker error = %v, want not found", err)
	}
}

func TestDuplicateBlockIDIsAmbiguous(t *testing.T) {
	source := mustMarkdownSource(t, "first ^same\n\nsecond ^same\n")
	_, err := source.Select(SourceSelector{Kind: SourceSelectorBlock, BlockID: "same"})
	if !errors.Is(err, ErrSourceUnitAmbiguous) {
		t.Fatalf("error = %v, want ErrSourceUnitAmbiguous", err)
	}
}

func TestFrontmatterSelectionRequiresLeadingClosedDelimiters(t *testing.T) {
	source := mustMarkdownSource(t, "---\r\ntitle: Test ^yaml\r\n...\r\n# Body\r\n")
	selection, err := source.Select(SourceSelector{Kind: SourceSelectorFrontmatter})
	if err != nil {
		t.Fatalf("frontmatter: %v", err)
	}
	if got, want := string(selection.Content), "---\r\ntitle: Test ^yaml\r\n...\r\n"; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
	if _, err := source.Select(SourceSelector{Kind: SourceSelectorBlock, BlockID: "yaml"}); !errors.Is(err, ErrSourceUnitNotFound) {
		t.Fatalf("frontmatter block marker error = %v, want not found", err)
	}

	for name, markdown := range map[string]string{
		"not leading":    "body\n---\ntitle: Test\n---\n",
		"not terminated": "---\ntitle: Test\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := mustMarkdownSource(t, markdown).Select(SourceSelector{Kind: SourceSelectorFrontmatter})
			if !errors.Is(err, ErrSourceUnitNotFound) {
				t.Fatalf("error = %v, want ErrSourceUnitNotFound", err)
			}
		})
	}
}

func TestOutlineIsInSourceOrderAndExcludesCode(t *testing.T) {
	const markdown = "---\nfrontmatter: value\n---\n# One\ntext\n> ## Two ###\n```\n# Fake\n```\nSetext\n---\n"
	source := mustMarkdownSource(t, markdown)
	selection, err := source.Select(SourceSelector{Kind: SourceSelectorOutline})
	if err != nil {
		t.Fatalf("outline: %v", err)
	}
	want := []OutlineEntry{
		{Line: 4, Level: 1, Text: "One"},
		{Line: 6, Level: 2, Text: "Two"},
		{Line: 10, Level: 2, Text: "Setext"},
	}
	if !reflect.DeepEqual(selection.Outline, want) {
		t.Fatalf("outline = %#v, want %#v", selection.Outline, want)
	}
}

func TestEmptyOutlineSucceeds(t *testing.T) {
	selection, err := mustMarkdownSource(t, "body\n").Select(SourceSelector{Kind: SourceSelectorOutline})
	if err != nil {
		t.Fatalf("outline: %v", err)
	}
	if selection.Outline == nil || len(selection.Outline) != 0 {
		t.Fatalf("outline = %#v, want non-nil empty slice", selection.Outline)
	}
}

func TestStructuralParsingAtAcceptedComplexityCeilings(t *testing.T) {
	t.Run("eight MiB", func(t *testing.T) {
		prefix := []byte("# Boundary\n")
		raw := make([]byte, MaxMarkdownSourceBytes)
		copy(raw, prefix)
		for i := len(prefix); i < len(raw); i++ {
			raw[i] = 'a'
		}
		source, err := NewMarkdownSource(raw)
		if err != nil {
			t.Fatalf("NewMarkdownSource: %v", err)
		}
		selection, err := source.Select(SourceSelector{Kind: SourceSelectorOutline})
		if err != nil {
			t.Fatalf("outline: %v", err)
		}
		want := []OutlineEntry{{Line: 1, Level: 1, Text: "Boundary"}}
		if !reflect.DeepEqual(selection.Outline, want) {
			t.Fatalf("outline = %#v, want %#v", selection.Outline, want)
		}
	})

	t.Run("fifty thousand high-density lines", func(t *testing.T) {
		raw := bytes.Repeat([]byte("- x\n"), MaxMarkdownSourceLines)
		source, err := NewMarkdownSource(raw)
		if err != nil {
			t.Fatalf("NewMarkdownSource: %v", err)
		}
		selection, err := source.Select(SourceSelector{Kind: SourceSelectorOutline})
		if err != nil {
			t.Fatalf("outline: %v", err)
		}
		if selection.Outline == nil || len(selection.Outline) != 0 {
			t.Fatalf("outline = %#v, want non-nil empty slice", selection.Outline)
		}
	})
}

func TestLargeSingleLineBlockAvoidsIrrelevantDenseTailWithoutChangingBoundary(t *testing.T) {
	raw := []byte("dense ^dense\n" + strings.Repeat("- x\n", MaxMarkdownSourceLines-1))
	source, err := NewMarkdownSource(raw)
	if err != nil {
		t.Fatalf("NewMarkdownSource: %v", err)
	}
	selection, err := source.Select(SourceSelector{Kind: SourceSelectorBlock, BlockID: "dense"})
	if err != nil {
		t.Fatalf("block: %v", err)
	}
	if got, want := string(selection.Content), "dense ^dense\n"; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestLargeSingleLineBlockFastPathFallsBackForSetextAndDuplicates(t *testing.T) {
	padding := strings.Repeat("\n", structuralFastPathMinLines)
	for name, markdown := range map[string]string{
		"setext":                "title ^same\n---\n" + padding,
		"ordered non-interrupt": "title ^same\n2. continuation\n" + padding,
		"duplicate":             "first ^same\n\nsecond ^same\n" + padding,
	} {
		t.Run(name, func(t *testing.T) {
			source := mustMarkdownSource(t, markdown)
			_, err := source.Select(SourceSelector{Kind: SourceSelectorBlock, BlockID: "same"})
			if name != "duplicate" && !errors.Is(err, ErrSourceUnitNotFound) {
				t.Fatalf("%s error = %v, want not found", name, err)
			}
			if name == "duplicate" && !errors.Is(err, ErrSourceUnitAmbiguous) {
				t.Fatalf("duplicate error = %v, want ambiguous", err)
			}
		})
	}
}

func TestStructuralParserSupportsConcurrentSources(t *testing.T) {
	const workers = 16
	var group sync.WaitGroup
	errorsByWorker := make(chan error, workers)
	for i := 0; i < workers; i++ {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			source, err := NewMarkdownSource([]byte(fmt.Sprintf("# Heading %d\nbody ^block-%d\n", i, i)))
			if err != nil {
				errorsByWorker <- err
				return
			}
			outline, err := source.Select(SourceSelector{Kind: SourceSelectorOutline})
			if err != nil {
				errorsByWorker <- err
				return
			}
			if len(outline.Outline) != 1 || outline.Outline[0].Text != fmt.Sprintf("Heading %d", i) {
				errorsByWorker <- fmt.Errorf("worker %d outline = %#v", i, outline.Outline)
				return
			}
			if _, err := source.Select(SourceSelector{Kind: SourceSelectorBlock, BlockID: fmt.Sprintf("block-%d", i)}); err != nil {
				errorsByWorker <- err
			}
		}(i)
	}
	group.Wait()
	close(errorsByWorker)
	for err := range errorsByWorker {
		t.Error(err)
	}
}

func mustMarkdownSource(t *testing.T, source string) *MarkdownSource {
	t.Helper()
	markdown, err := NewMarkdownSource([]byte(source))
	if err != nil {
		t.Fatalf("NewMarkdownSource: %v", err)
	}
	return markdown
}
