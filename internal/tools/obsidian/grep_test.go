package obsidian

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"personal-mcp-gateway/internal/fsx"
	"personal-mcp-gateway/internal/testutil"
)

func TestGrepLiteralCaseUnicodeOccurrencesAndContext(t *testing.T) {
	root := t.TempDir()
	writeGrepFile(t, root, "b.md", "TARGET elsewhere\n")
	writeGrepFile(t, root, "a.md", "before\nCafé target target\nafter\n")
	writeGrepFile(t, root, "ignore.txt", "target\n")
	writeGrepFile(t, root, ".hidden.md", "target\n")
	tools := grepTools(t, root)
	literal := false

	result, out, err := tools.Grep(context.Background(), nil, GrepInput{Pattern: "target", Regex: &literal})
	if err != nil || result == nil || result.IsError || !out.OK {
		t.Fatalf("result=%#v out=%#v err=%v", result, out, err)
	}
	if got, want := len(out.Matches), 2; got != want {
		t.Fatalf("matches = %d, want %d: %#v", got, want, out.Matches)
	}
	first := out.Matches[0]
	if first.Path != "a.md" || first.Line != 2 || first.Column != 6 || first.Occurrences != 2 || first.Text != "Café target target" {
		t.Fatalf("first match = %#v", first)
	}
	if !reflect.DeepEqual(first.Before, []GrepContextLine{{Line: 1, Text: "before"}}) ||
		!reflect.DeepEqual(first.After, []GrepContextLine{{Line: 3, Text: "after"}}) {
		t.Fatalf("context = before:%#v after:%#v", first.Before, first.After)
	}
	if out.Matches[1].Path != "b.md" || out.Matches[1].Column != 1 || out.Matches[1].Occurrences != 1 {
		t.Fatalf("second match = %#v", out.Matches[1])
	}
	if !out.Coverage.ResultComplete || !out.Coverage.ScopeComplete || out.Coverage.Continuation != CoverageContinuationComplete || out.Coverage.FilesScanned != 2 {
		t.Fatalf("coverage = %#v", out.Coverage)
	}
}

func TestGrepExplicitZeroContextAndRE2ZeroWidthSemantics(t *testing.T) {
	root := t.TempDir()
	writeGrepFile(t, root, "note.md", "abc\n")
	tools := grepTools(t, root)
	zero := 0
	regex := true

	_, out, err := tools.Grep(context.Background(), nil, GrepInput{
		Pattern:      `^|$`,
		Regex:        &regex,
		ContextLines: &zero,
	})
	if err != nil || !out.OK || len(out.Matches) != 1 {
		t.Fatalf("out=%#v err=%v", out, err)
	}
	match := out.Matches[0]
	if match.Column != 1 || match.Occurrences != 2 || len(match.Before) != 0 || len(match.After) != 0 {
		t.Fatalf("match = %#v", match)
	}
}

func TestGrepFitCapableHighCardinalityZeroWidthCountMatchesGoRegexp(t *testing.T) {
	root := t.TempDir()
	source := strings.Repeat("éz", 2*1024)
	writeGrepFile(t, root, "note.md", source+"\n")
	tools := grepTools(t, root)
	zero := 0
	regexMode := true
	pattern := `a*`

	_, out, err := tools.Grep(context.Background(), nil, GrepInput{
		Pattern:       pattern,
		Path:          "note.md",
		Regex:         &regexMode,
		CaseSensitive: true,
		ContextLines:  &zero,
	})
	wantOccurrences := len(regexp.MustCompile(pattern).FindAllStringIndex(source, -1))
	if err != nil || !out.OK || len(out.Matches) != 1 {
		t.Fatalf("out=%#v err=%v", out, err)
	}
	match := out.Matches[0]
	if match.Column != 1 || match.Occurrences != wantOccurrences || match.Text != source {
		t.Fatalf("match = %#v, want occurrences=%d", match, wantOccurrences)
	}
}

func TestGrepRejectsInvalidRegexUTF8UnsupportedAndOversizedLine(t *testing.T) {
	root := t.TempDir()
	writeGrepFile(t, root, "note.md", "valid\n")
	writeGrepFile(t, root, "note.txt", "valid\n")
	if err := os.WriteFile(filepath.Join(root, "invalid.md"), []byte{'x', 0xff, '\n'}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "large.md"), append(make([]byte, MaxGrepPhysicalLineBytes), '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	exactInvalid := append(make([]byte, MaxGrepPhysicalLineBytes-1), '\n')
	exactInvalid[len(exactInvalid)-2] = 0xff
	if err := os.WriteFile(filepath.Join(root, "exact-invalid.md"), exactInvalid, 0o600); err != nil {
		t.Fatal(err)
	}
	tools := grepTools(t, root)

	tests := []struct {
		name  string
		input GrepInput
		code  string
	}{
		{name: "regex", input: GrepInput{Pattern: "[", Path: "note.md"}, code: InvalidRegexCode},
		{name: "empty pattern", input: GrepInput{Pattern: "", Path: "note.md"}, code: InvalidRegexCode},
		{name: "oversize pattern", input: GrepInput{Pattern: strings.Repeat("x", MaxGrepPatternBytes+1), Path: "note.md"}, code: string(fsx.CodeInputTooLarge)},
		{name: "invalid UTF-8 pattern", input: GrepInput{Pattern: string([]byte{0xff}), Path: "note.md"}, code: InvalidUTF8Code},
		{name: "utf8", input: GrepInput{Pattern: "x", Path: "invalid.md"}, code: InvalidUTF8Code},
		{name: "unsupported", input: GrepInput{Pattern: "x", Path: "note.txt"}, code: UnsupportedFileCode},
		{name: "exact line invalid UTF-8", input: GrepInput{Pattern: "a*", Path: "exact-invalid.md"}, code: InvalidUTF8Code},
		{name: "line cap", input: GrepInput{Pattern: "a*", Path: "large.md"}, code: string(fsx.CodeInputTooLarge)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, out, err := tools.Grep(context.Background(), nil, tt.input)
			if err != nil || result == nil || !result.IsError || out.OK || out.Error == nil || out.Error.Code != tt.code {
				t.Fatalf("result=%#v out=%#v err=%v", result, out, err)
			}
			if out.Coverage.Continuation != CoverageContinuationRestart || out.Coverage.NextCursor != "" {
				t.Fatalf("coverage = %#v", out.Coverage)
			}
			if tt.name == "line cap" && out.Coverage.BytesScanned != MaxGrepPhysicalLineBytes+1 {
				t.Fatalf("line-cap bytes = %d, want one-byte sentinel", out.Coverage.BytesScanned)
			}
		})
	}
}

func TestGrepAcceptsExactlyMaximumPhysicalLine(t *testing.T) {
	root := t.TempDir()
	line := append([]byte(strings.Repeat("a", MaxGrepPhysicalLineBytes-1)), '\n')
	if err := os.WriteFile(filepath.Join(root, "note.md"), line, 0o600); err != nil {
		t.Fatal(err)
	}
	tools := grepTools(t, root)
	_, out, err := tools.Grep(context.Background(), nil, GrepInput{Pattern: "missing", Path: "note.md"})
	if err != nil || !out.OK || len(out.Matches) != 0 || out.Coverage.BytesScanned != MaxGrepPhysicalLineBytes {
		t.Fatalf("out=%#v err=%v", out, err)
	}
}

func TestGrepHighCardinalityOversizedEvidenceAvoidsPerMatchAllocation(t *testing.T) {
	tests := []struct {
		name         string
		physicalSize int
	}{
		{name: "mmap line", physicalSize: MaxGrepPhysicalLineBytes},
		{name: "heap line that cannot fit response", physicalSize: grepHeapLineBytes},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			line := append([]byte(strings.Repeat("z", tt.physicalSize-1)), '\n')
			if err := os.WriteFile(filepath.Join(root, "note.md"), line, 0o600); err != nil {
				t.Fatal(err)
			}
			tools := grepTools(t, root)
			zero := 0
			call := func(pattern string) GrepOutput {
				result, out, err := tools.Grep(context.Background(), nil, GrepInput{
					Pattern: pattern, Path: "note.md", CaseSensitive: true, ContextLines: &zero, Limit: 1,
				})
				if err != nil || result == nil || !result.IsError || out.OK || out.Error == nil || out.Error.Code != ResponseTooLargeCode ||
					out.Coverage.Continuation != CoverageContinuationRestart || out.Coverage.NextCursor != "" ||
					out.Coverage.BytesScanned != uint64(tt.physicalSize) {
					t.Fatalf("pattern=%q result=%#v out=%#v err=%v", pattern, result, out, err)
				}
				return out
			}
			call("^")
			call("a*")
			baseline := testing.AllocsPerRun(3, func() { call("^") })
			zeroWidth := testing.AllocsPerRun(3, func() { call("a*") })
			if zeroWidth > baseline+512 || zeroWidth >= 10_000 {
				t.Fatalf("allocations: a*=%0.0f baseline=%0.0f", zeroWidth, baseline)
			}
		})
	}
}

func TestGrepRejectsOversizedContextBeforeHighCardinalityCounting(t *testing.T) {
	tests := []struct {
		name    string
		content func() []byte
	}{
		{
			name: "before context",
			content: func() []byte {
				return append(append([]byte(strings.Repeat("z", MaxGrepPhysicalLineBytes-1)), '\n'), []byte(strings.Repeat("a", 32*1024)+"\n")...)
			},
		},
		{
			name: "after context",
			content: func() []byte {
				return append([]byte(strings.Repeat("a", 32*1024)+"\n"), append([]byte(strings.Repeat("z", MaxGrepPhysicalLineBytes-1)), '\n')...)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			content := tt.content()
			if err := os.WriteFile(filepath.Join(root, "note.md"), content, 0o600); err != nil {
				t.Fatal(err)
			}
			before := testutil.Snapshot(t, root)
			tools := grepTools(t, root)
			one := 1
			result, out, err := tools.Grep(context.Background(), nil, GrepInput{
				Pattern: "a", Path: "note.md", CaseSensitive: true, ContextLines: &one, Limit: 1,
			})
			if err != nil || result == nil || !result.IsError || out.Error == nil || out.Error.Code != ResponseTooLargeCode ||
				out.Coverage.Continuation != CoverageContinuationRestart || out.Coverage.NextCursor != "" {
				t.Fatalf("result=%#v out=%#v err=%v", result, out, err)
			}
			if after := testutil.Snapshot(t, root); !reflect.DeepEqual(before, after) {
				t.Fatalf("grep mutated vault:\nbefore=%#v\nafter=%#v", before, after)
			}
		})
	}
}

func TestGrepResultCursorContinuesWithoutReplayAndValidatesPrefix(t *testing.T) {
	root := t.TempDir()
	writeGrepFile(t, root, "a.md", "hit one\nhit two\n")
	writeGrepFile(t, root, "b.md", "hit three\n")
	tools := grepTools(t, root)
	zero := 0
	input := GrepInput{Pattern: "hit", Limit: 1, ContextLines: &zero}

	var got []string
	for page := 0; page < 5; page++ {
		_, out, err := tools.Grep(context.Background(), nil, input)
		if err != nil || !out.OK {
			t.Fatalf("page %d out=%#v err=%v", page, out, err)
		}
		for _, match := range out.Matches {
			got = append(got, match.Path+":"+match.Text)
		}
		if page > 0 && out.Coverage.SourceEntriesValidated == 0 {
			t.Fatalf("page %d did not validate cursor prefix: %#v", page, out.Coverage)
		}
		if out.Coverage.Continuation == CoverageContinuationComplete {
			break
		}
		if out.Coverage.Continuation != CoverageContinuationCursor || out.Coverage.NextCursor == "" {
			t.Fatalf("page %d coverage = %#v", page, out.Coverage)
		}
		input.Cursor = out.Coverage.NextCursor
	}
	want := []string{"a.md:hit one", "a.md:hit two", "b.md:hit three"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("matches = %#v, want %#v", got, want)
	}
}

func TestGrepRejectsTamperedFullAndPartialCursorStateBeforeSourceWork(t *testing.T) {
	digest := base64.RawURLEncoding.EncodeToString(make([]byte, 32))

	fullRoot := t.TempDir()
	writeGrepFile(t, fullRoot, "a.md", "none\n")
	writeGrepFile(t, fullRoot, "b.md", "none\n")
	fullTools := grepTools(t, fullRoot)
	fullInput := GrepInput{Pattern: "hit", MaxFiles: 1}
	_, full, err := fullTools.Grep(context.Background(), nil, fullInput)
	if err != nil || !full.OK || full.Coverage.NextCursor == "" {
		t.Fatalf("full cursor = %#v err=%v", full, err)
	}

	partialRoot := t.TempDir()
	writeGrepFile(t, partialRoot, "note.md", "a\nab\n")
	partialTools := grepTools(t, partialRoot)
	zero := 0
	partialInput := GrepInput{Pattern: "hit", MaxBytes: 4, ContextLines: &zero}
	_, partial, err := partialTools.Grep(context.Background(), nil, partialInput)
	if err != nil || !partial.OK || partial.Coverage.NextCursor == "" {
		t.Fatalf("partial cursor = %#v err=%v", partial, err)
	}

	tests := []struct {
		name  string
		tools *Tools
		input GrepInput
		token string
		edit  func(map[string]any)
	}{
		{name: "full boundary", tools: fullTools, input: fullInput, token: full.Coverage.NextCursor, edit: func(state map[string]any) {
			state["boundary"].(map[string]any)["stored"] = "b.md"
		}},
		{name: "full prefix", tools: fullTools, input: fullInput, token: full.Coverage.NextCursor, edit: func(state map[string]any) {
			state["prefix"] = digest
		}},
		{name: "partial boundary", tools: partialTools, input: partialInput, token: partial.Coverage.NextCursor, edit: func(state map[string]any) {
			state["boundary"].(map[string]any)["nfc"] = "other.md"
		}},
		{name: "partial prefix", tools: partialTools, input: partialInput, token: partial.Coverage.NextCursor, edit: func(state map[string]any) {
			state["prefix"] = digest
		}},
		{name: "partial fingerprint", tools: partialTools, input: partialInput, token: partial.Coverage.NextCursor, edit: func(state map[string]any) {
			state["partial"].(map[string]any)["fingerprint"] = digest
		}},
		{name: "partial resume offset", tools: partialTools, input: partialInput, token: partial.Coverage.NextCursor, edit: func(state map[string]any) {
			partial := state["partial"].(map[string]any)
			partial["resume_offset"] = partial["resume_offset"].(float64) + 1
		}},
		{name: "partial resume line", tools: partialTools, input: partialInput, token: partial.Coverage.NextCursor, edit: func(state map[string]any) {
			partial := state["partial"].(map[string]any)
			partial["resume_line"] = partial["resume_line"].(float64) + 1
		}},
		{name: "partial context offset", tools: partialTools, input: partialInput, token: partial.Coverage.NextCursor, edit: func(state map[string]any) {
			partial := state["partial"].(map[string]any)
			partial["context_offset"] = partial["context_offset"].(float64) + 1
		}},
		{name: "partial context line", tools: partialTools, input: partialInput, token: partial.Coverage.NextCursor, edit: func(state map[string]any) {
			partial := state["partial"].(map[string]any)
			partial["context_line"] = partial["context_line"].(float64) + 1
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changed := tt.input
			changed.Cursor = tamperCursorState(t, tt.token, tt.edit)
			_, rejected, err := tt.tools.Grep(context.Background(), nil, changed)
			if err != nil || rejected.OK || rejected.Error == nil || rejected.Error.Code != CursorInvalidCode ||
				rejected.Coverage.FilesScanned != 0 || rejected.Coverage.BytesScanned != 0 || rejected.Coverage.SourceEntriesValidated != 0 {
				t.Fatalf("rejected = %#v err=%v", rejected, err)
			}
		})
	}
}

func TestGrepResultCursorRereadsBoundedContextWithoutDuplicatingMatches(t *testing.T) {
	root := t.TempDir()
	writeGrepFile(t, root, "note.md", "hit one\nhit two\ntail\n")
	tools := grepTools(t, root)
	input := GrepInput{Pattern: "hit", Limit: 1}

	_, first, err := tools.Grep(context.Background(), nil, input)
	if err != nil || !first.OK || len(first.Matches) != 1 || first.Matches[0].Text != "hit one" ||
		!reflect.DeepEqual(first.Matches[0].After, []GrepContextLine{{Line: 2, Text: "hit two"}}) {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	input.Cursor = first.Coverage.NextCursor
	_, second, err := tools.Grep(context.Background(), nil, input)
	if err != nil || !second.OK || len(second.Matches) != 1 || second.Matches[0].Text != "hit two" ||
		!reflect.DeepEqual(second.Matches[0].Before, []GrepContextLine{{Line: 1, Text: "hit one"}}) {
		t.Fatalf("second=%#v err=%v", second, err)
	}
}

func TestGrepFileAndByteLimitsProduceAdvancingZeroMatchCursors(t *testing.T) {
	root := t.TempDir()
	writeGrepFile(t, root, "a.md", "none\n")
	writeGrepFile(t, root, "b.md", "none\n")
	tools := grepTools(t, root)
	input := GrepInput{Pattern: "hit", MaxFiles: 1}

	_, first, err := tools.Grep(context.Background(), nil, input)
	if err != nil || !first.OK || len(first.Matches) != 0 || first.Coverage.StoppedBy != string(CursorStopFileLimit) || first.Coverage.NextCursor == "" {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	input.Cursor = first.Coverage.NextCursor
	_, second, err := tools.Grep(context.Background(), nil, input)
	if err != nil || !second.OK || second.Coverage.Continuation != CoverageContinuationComplete || second.Coverage.SourceEntriesValidated == 0 {
		t.Fatalf("second=%#v err=%v", second, err)
	}

	root = t.TempDir()
	writeGrepFile(t, root, "note.md", "abc\nxyz\n")
	tools = grepTools(t, root)
	zero := 0
	bytesInput := GrepInput{Pattern: "hit", MaxBytes: 4, ContextLines: &zero}
	_, first, err = tools.Grep(context.Background(), nil, bytesInput)
	if err != nil || !first.OK || first.Coverage.StoppedBy != string(CursorStopByteLimit) || first.Coverage.BytesScanned != 4 {
		t.Fatalf("byte first=%#v err=%v", first, err)
	}
	bytesInput.Cursor = first.Coverage.NextCursor
	_, second, err = tools.Grep(context.Background(), nil, bytesInput)
	if err != nil || !second.OK || second.Coverage.Continuation != CoverageContinuationComplete {
		t.Fatalf("byte second=%#v tool_error=%#v err=%v", second, second.Error, err)
	}
}

func TestGrepTooSmallByteBudgetReturnsLimitExceededWithoutCursor(t *testing.T) {
	root := t.TempDir()
	writeGrepFile(t, root, "note.md", "abc\n")
	tools := grepTools(t, root)
	result, out, err := tools.Grep(context.Background(), nil, GrepInput{Pattern: "a", MaxBytes: 1})
	if err != nil || result == nil || !result.IsError || out.Error == nil || out.Error.Code != string(fsx.CodeLimitExceeded) || out.Coverage.NextCursor != "" {
		t.Fatalf("result=%#v out=%#v err=%v", result, out, err)
	}
}

func TestGrepByteBudgetKeepsPriorFullFileProgressWhenNextLineDoesNotFit(t *testing.T) {
	root := t.TempDir()
	writeGrepFile(t, root, "a.md", "a\n")
	writeGrepFile(t, root, "b.md", "abcdef\n")
	tools := grepTools(t, root)
	zero := 0
	input := GrepInput{Pattern: "hit", MaxBytes: 7, ContextLines: &zero}

	result, first, err := tools.Grep(context.Background(), nil, input)
	if err != nil || result == nil || result.IsError || !first.OK || first.Coverage.StoppedBy != string(CursorStopByteLimit) ||
		first.Coverage.NextCursor == "" || first.Coverage.BytesScanned != 7 {
		t.Fatalf("first result=%#v out=%#v err=%v", result, first, err)
	}
	input.Cursor = first.Coverage.NextCursor
	result, second, err := tools.Grep(context.Background(), nil, input)
	if err != nil || result == nil || result.IsError || !second.OK || second.Coverage.Continuation != CoverageContinuationComplete ||
		second.Coverage.SourceEntriesValidated == 0 {
		t.Fatalf("second result=%#v out=%#v err=%v", result, second, err)
	}
}

func TestGrepByteBudgetCannotCursorBeforePendingMatchContext(t *testing.T) {
	root := t.TempDir()
	writeGrepFile(t, root, "note.md", "hit\ncontext\n")
	tools := grepTools(t, root)
	result, out, err := tools.Grep(context.Background(), nil, GrepInput{Pattern: "hit", MaxBytes: 4})
	if err != nil || result == nil || !result.IsError || out.Error == nil || out.Error.Code != string(fsx.CodeLimitExceeded) || out.Coverage.NextCursor != "" {
		t.Fatalf("result=%#v out=%#v err=%v", result, out, err)
	}
}

func TestGrepCursorBecomesStaleWhenValidatedPrefixChanges(t *testing.T) {
	root := t.TempDir()
	writeGrepFile(t, root, "a.md", "none\n")
	writeGrepFile(t, root, "b.md", "none\n")
	tools := grepTools(t, root)
	input := GrepInput{Pattern: "hit", MaxFiles: 1}
	_, first, err := tools.Grep(context.Background(), nil, input)
	if err != nil || first.Coverage.NextCursor == "" {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	writeGrepFile(t, root, "0.md", "inserted\n")
	input.Cursor = first.Coverage.NextCursor
	result, out, err := tools.Grep(context.Background(), nil, input)
	if err != nil || result == nil || !result.IsError || out.Error == nil || out.Error.Code != CursorStaleCode || out.Coverage.StoppedBy != string(RestartStopSourceChange) {
		t.Fatalf("result=%#v out=%#v err=%v", result, out, err)
	}
}

func TestGrepCursorDetectsEveryPrefixMutationClass(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{
			name: "content change",
			mutate: func(t *testing.T, root string) {
				writeGrepFile(t, root, "a.md", "changed\n")
			},
		},
		{
			name: "deletion",
			mutate: func(t *testing.T, root string) {
				if err := os.Remove(filepath.Join(root, "a.md")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "rename",
			mutate: func(t *testing.T, root string) {
				if err := os.Rename(filepath.Join(root, "a.md"), filepath.Join(root, "aa.md")); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeGrepFile(t, root, "a.md", "none\n")
			writeGrepFile(t, root, "b.md", "none\n")
			tools := grepTools(t, root)
			input := GrepInput{Pattern: "hit", MaxFiles: 1}
			_, first, err := tools.Grep(context.Background(), nil, input)
			if err != nil || first.Coverage.NextCursor == "" {
				t.Fatalf("first=%#v err=%v", first, err)
			}
			test.mutate(t, root)
			input.Cursor = first.Coverage.NextCursor
			result, out, err := tools.Grep(context.Background(), nil, input)
			if err != nil || result == nil || !result.IsError || out.Error == nil || out.Error.Code != CursorStaleCode ||
				out.Coverage.StoppedBy != string(RestartStopSourceChange) || out.Coverage.NextCursor != "" {
				t.Fatalf("result=%#v out=%#v err=%v", result, out, err)
			}
		})
	}
}

func TestGrepPartialFileCursorSeeksAndRejectsChangedSource(t *testing.T) {
	root := t.TempDir()
	writeGrepFile(t, root, "note.md", "a\nab\n")
	tools := grepTools(t, root)
	zero := 0
	input := GrepInput{Pattern: "hit", MaxBytes: 4, ContextLines: &zero}
	_, first, err := tools.Grep(context.Background(), nil, input)
	if err != nil || !first.OK || first.Coverage.NextCursor == "" || first.Coverage.StoppedBy != string(CursorStopByteLimit) {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	input.Cursor = first.Coverage.NextCursor
	_, second, err := tools.Grep(context.Background(), nil, input)
	if err != nil || !second.OK || second.Coverage.Continuation != CoverageContinuationComplete || second.Coverage.BytesScanned != 3 {
		t.Fatalf("second=%#v err=%v", second, err)
	}

	input.Cursor = first.Coverage.NextCursor
	writeGrepFile(t, root, "note.md", "a\ncd\n")
	result, stale, err := tools.Grep(context.Background(), nil, input)
	if err != nil || result == nil || !result.IsError || stale.Error == nil || stale.Error.Code != CursorStaleCode {
		t.Fatalf("stale result=%#v out=%#v err=%v", result, stale, err)
	}
}

func TestGrepResumedScanRechecksFingerprintOnContentDescriptor(t *testing.T) {
	root := t.TempDir()
	writeGrepFile(t, root, "note.md", "old\n")
	tools := grepTools(t, root)

	var entry fsx.WalkFile
	if _, err := tools.vault.WalkFiles(context.Background(), "", ".", func(_ context.Context, candidate fsx.WalkFile) (fsx.WalkAction, error) {
		entry = candidate
		return fsx.WalkStop, nil
	}); err != nil {
		t.Fatal(err)
	}
	original, err := entry.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := original.Fingerprint()
	if err := original.Close(); err != nil {
		t.Fatal(err)
	}

	replacement := filepath.Join(root, "replacement.md")
	if err := os.WriteFile(replacement, []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, filepath.Join(root, "note.md")); err != nil {
		t.Fatal(err)
	}

	run := &grepRun{tools: tools, query: normalizedGrepQuery{MaxFiles: 1, MaxBytes: 1024}}
	partial := &grepPartialCursor{Fingerprint: encodeDigest(fingerprint[:]), ResumeLine: 1, ContextLine: 1}
	action, err := run.scanFile(context.Background(), entry, partial)
	if action != fsx.WalkStop || !errors.Is(err, ErrCursorStale) {
		t.Fatalf("action=%v err=%v, want stopped cursor_stale", action, err)
	}
}

func TestGrepContinuationBindsNormalizedScopeBeforeReopen(t *testing.T) {
	root := t.TempDir()
	writeGrepFile(t, root, "Note.md", "a\nab\n")
	tools := grepTools(t, root)
	zero := 0
	input := GrepInput{Pattern: "hit", Path: "note.md", MaxBytes: 4, ContextLines: &zero}
	_, first, err := tools.Grep(context.Background(), nil, input)
	if err != nil || first.Path != "Note.md" || first.Coverage.NextCursor == "" {
		t.Fatalf("first=%#v err=%v", first, err)
	}

	changed := input
	changed.Path = "missing.md"
	changed.Cursor = first.Coverage.NextCursor
	_, mismatch, err := tools.Grep(context.Background(), nil, changed)
	if err != nil || mismatch.Error == nil || mismatch.Error.Code != CursorMismatchCode {
		t.Fatalf("changed missing query=%#v err=%v", mismatch, err)
	}

	if err := os.Remove(filepath.Join(root, "Note.md")); err != nil {
		t.Fatal(err)
	}
	input.Cursor = first.Coverage.NextCursor
	_, stale, err := tools.Grep(context.Background(), nil, input)
	if err != nil || stale.Error == nil || stale.Error.Code != CursorStaleCode || stale.Coverage.StoppedBy != string(RestartStopSourceChange) {
		t.Fatalf("deleted case-folded scope=%#v err=%v", stale, err)
	}
}

func TestGrepCursorRejectsChangedQueryOrBudget(t *testing.T) {
	root := t.TempDir()
	writeGrepFile(t, root, "a.md", "hit\n")
	writeGrepFile(t, root, "b.md", "hit\n")
	tools := grepTools(t, root)
	input := GrepInput{Pattern: "hit", MaxFiles: 1}
	_, first, err := tools.Grep(context.Background(), nil, input)
	if err != nil || first.Coverage.NextCursor == "" {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	input.Cursor = first.Coverage.NextCursor
	input.MaxFiles = 2
	result, out, err := tools.Grep(context.Background(), nil, input)
	if err != nil || result == nil || !result.IsError || out.Error == nil || out.Error.Code != CursorMismatchCode {
		t.Fatalf("result=%#v out=%#v err=%v", result, out, err)
	}
}

func TestGrepChangeStrictlyAfterBoundaryDoesNotInvalidateCursor(t *testing.T) {
	root := t.TempDir()
	writeGrepFile(t, root, "a.md", "none\n")
	writeGrepFile(t, root, "b.md", "old\n")
	tools := grepTools(t, root)
	input := GrepInput{Pattern: "new", MaxFiles: 1}
	_, first, err := tools.Grep(context.Background(), nil, input)
	if err != nil || first.Coverage.NextCursor == "" {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	writeGrepFile(t, root, "b.md", "new\n")
	input.Cursor = first.Coverage.NextCursor
	_, second, err := tools.Grep(context.Background(), nil, input)
	if err != nil || !second.OK || len(second.Matches) != 1 || second.Matches[0].Path != "b.md" {
		t.Fatalf("second=%#v err=%v", second, err)
	}
}

func TestGrepIndivisibleMatchReturnsResponseTooLarge(t *testing.T) {
	root := t.TempDir()
	writeGrepFile(t, root, "note.md", "hit "+strings.Repeat("x", MaxStructuredResultBytes)+"\n")
	tools := grepTools(t, root)
	result, out, err := tools.Grep(context.Background(), nil, GrepInput{Pattern: "hit", Path: "note.md"})
	if err != nil || result == nil || !result.IsError || out.Error == nil || out.Error.Code != ResponseTooLargeCode || out.Coverage.NextCursor != "" {
		t.Fatalf("result=%#v out=%#v err=%v", result, out, err)
	}
}

func TestGrepResponseLimitReturnsLargestEmittedPrefixAndContinues(t *testing.T) {
	root := t.TempDir()
	line := "hit " + strings.Repeat("x", 34*1024)
	writeGrepFile(t, root, "note.md", line+"\n"+line+"\n")
	tools := grepTools(t, root)
	zero := 0
	input := GrepInput{Pattern: "hit", ContextLines: &zero}

	_, first, err := tools.Grep(context.Background(), nil, input)
	if err != nil || !first.OK || len(first.Matches) != 1 || first.Coverage.StoppedBy != string(CursorStopResponseLimit) || first.Coverage.NextCursor == "" {
		t.Fatalf("first matches=%d coverage=%#v error=%#v err=%v", len(first.Matches), first.Coverage, first.Error, err)
	}
	input.Cursor = first.Coverage.NextCursor
	_, second, err := tools.Grep(context.Background(), nil, input)
	if err != nil || !second.OK || len(second.Matches) != 1 || second.Matches[0].Line != 2 {
		t.Fatalf("second matches=%#v coverage=%#v error=%#v err=%v", second.Matches, second.Coverage, second.Error, err)
	}
}

func TestGrepResponseLimitPreservesPrefixBeforeOversizedEvidenceWithoutReplay(t *testing.T) {
	tests := []struct {
		name         string
		content      string
		contextLines int
	}{
		{
			name:         "current matching line",
			content:      "hit first\n" + "hit " + strings.Repeat("z", grepHeapLineBytes) + "\n",
			contextLines: 0,
		},
		{
			name:         "after context",
			content:      "hit first\nhit second\n" + strings.Repeat("z", grepHeapLineBytes) + "\n",
			contextLines: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeGrepFile(t, root, "note.md", tt.content)
			tools := grepTools(t, root)
			contextLines := tt.contextLines
			input := GrepInput{Pattern: "hit", Path: "note.md", ContextLines: &contextLines}

			result, first, err := tools.Grep(context.Background(), nil, input)
			if err != nil || result == nil || result.IsError || !first.OK || len(first.Matches) != 1 || first.Matches[0].Line != 1 ||
				first.Coverage.StoppedBy != string(CursorStopResponseLimit) || first.Coverage.Continuation != CoverageContinuationCursor ||
				first.Coverage.NextCursor == "" {
				t.Fatalf("first result=%#v out=%#v err=%v", result, first, err)
			}

			input.Cursor = first.Coverage.NextCursor
			result, resumed, err := tools.Grep(context.Background(), nil, input)
			if err != nil || result == nil || !result.IsError || resumed.OK || resumed.Error == nil || resumed.Error.Code != ResponseTooLargeCode ||
				len(resumed.Matches) != 0 || resumed.Coverage.Continuation != CoverageContinuationRestart || resumed.Coverage.NextCursor != "" {
				t.Fatalf("resumed result=%#v out=%#v err=%v", result, resumed, err)
			}
		})
	}
}

func TestGrepCancellationIsStructuredAndReadOnly(t *testing.T) {
	root := t.TempDir()
	writeGrepFile(t, root, "note.md", "hit\n")
	before := testutil.Snapshot(t, root)
	tools := grepTools(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, out, err := tools.Grep(ctx, nil, GrepInput{Pattern: "hit"})
	if err != nil || result == nil || !result.IsError || out.Error == nil || out.Error.Code != string(fsx.CodeCanceled) {
		t.Fatalf("result=%#v out=%#v err=%v", result, out, err)
	}
	if after := testutil.Snapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatalf("grep mutated vault:\nbefore=%#v\nafter=%#v", before, after)
	}
}

func grepTools(t *testing.T, root string) *Tools {
	t.Helper()
	vault, err := fsx.NewVault(root)
	if err != nil {
		t.Fatal(err)
	}
	return New(vault)
}

func writeGrepFile(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
