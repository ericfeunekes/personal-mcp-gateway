package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"personal-mcp-gateway/internal/audit"
	"personal-mcp-gateway/internal/config"
	"personal-mcp-gateway/internal/fsx"
	"personal-mcp-gateway/internal/testutil"
	"personal-mcp-gateway/internal/tools/obsidian"
)

func TestPhase1DescriptorsTeachCanonicalContinuation(t *testing.T) {
	application := newTestApp(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, stop := connectPipeTransport(t, ctx, application)
	defer stop()

	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != 2 {
		t.Fatalf("tool count = %d, want 2", len(listed.Tools))
	}
	byName := map[string]*sdk.Tool{}
	for _, tool := range listed.Tools {
		byName[tool.Name] = tool
	}
	if got := byName[obsidian.ToolResolve].Description; got != obsidian.ResolveDescription {
		t.Fatalf("resolve description = %q", got)
	}
	ls := byName[obsidian.ToolLS]
	if ls == nil || ls.Description != obsidian.LSDescription {
		t.Fatalf("ls description = %#v", ls)
	}
	if !strings.HasPrefix(ls.Description, "Continue a partial listing only by passing `coverage.next_cursor` as `cursor`") ||
		!strings.Contains(ls.Description, "Never omit `cursor` or change `limit` to continue") ||
		!strings.Contains(ls.Description, "restarts at the first entry and repeats results") ||
		!strings.Contains(ls.Description, "changing `limit` with the prior cursor returns `cursor_mismatch`") {
		t.Fatalf("ls description does not front-load continuation recovery = %q", ls.Description)
	}
	input := schemaObject(t, ls.InputSchema)
	limit := schemaProperty(t, input, "limit")
	if limit["default"] != float64(100) {
		t.Fatalf("limit schema = %#v", limit)
	}
	if description, _ := limit["description"].(string); !strings.Contains(description, "keep it identical") ||
		!strings.Contains(description, "restarts at the first entry") || !strings.Contains(description, "defaults to 100") ||
		!strings.Contains(description, "1 through 500") {
		t.Fatalf("limit schema description = %q", description)
	}
	cursor := schemaProperty(t, input, "cursor")
	if description, _ := cursor["description"].(string); !strings.Contains(description, "required to continue") ||
		!strings.Contains(description, "coverage.next_cursor unchanged") || !strings.Contains(description, "identical") ||
		!strings.Contains(description, "restarts at the first entry") {
		t.Fatalf("cursor schema description = %q", description)
	}
	output := schemaObject(t, ls.OutputSchema)
	if _, ok := output["properties"].(map[string]any)["coverage"]; !ok {
		t.Fatalf("ls output schema lacks coverage: %#v", output)
	}
	encodedOutput, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	for _, guidance := range []string{
		"use next_cursor instead of widening limit",
		"the next call must pass next_cursor as cursor with identical path, base, and limit",
		"never widen limit to continue",
	} {
		if !strings.Contains(string(encodedOutput), guidance) {
			t.Fatalf("ls output schema lacks %q guidance: %s", guidance, encodedOutput)
		}
	}
}

func TestPhase1SuccessGrammarKeepsEmptyAndZeroValues(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "zero.md"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "empty"), 0o700); err != nil {
		t.Fatal(err)
	}
	application := phase1App(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, stop := connectPipeTransport(t, ctx, application)
	defer stop()

	resolved := phase1Call(t, ctx, session, obsidian.ToolResolve, map[string]any{"path": "zero.md"})
	resolvedObject := structuredObject(t, resolved)
	if size, present := resolvedObject["size"]; !present || size != float64(0) {
		t.Fatalf("zero-byte resolve size = %#v, present=%t in %#v", size, present, resolvedObject)
	}

	empty := phase1Call(t, ctx, session, obsidian.ToolLS, map[string]any{"path": "empty"})
	emptyObject := structuredObject(t, empty)
	entries, present := emptyObject["entries"].([]any)
	if !present || len(entries) != 0 {
		t.Fatalf("empty directory entries = %#v, present=%t in %#v", emptyObject["entries"], present, emptyObject)
	}

	listed := phase1Call(t, ctx, session, obsidian.ToolLS, map[string]any{"path": ".", "limit": 100})
	listedObject := structuredObject(t, listed)
	listedEntries, ok := listedObject["entries"].([]any)
	if !ok {
		t.Fatalf("listing entries = %#v", listedObject["entries"])
	}
	foundZero := false
	for _, raw := range listedEntries {
		entry, _ := raw.(map[string]any)
		if entry["name"] == "zero.md" {
			foundZero = true
			if size, present := entry["size"]; !present || size != float64(0) {
				t.Fatalf("zero-byte entry size = %#v, present=%t in %#v", size, present, entry)
			}
		}
	}
	if !foundZero {
		t.Fatalf("zero-byte entry missing from %#v", listedEntries)
	}
}

func TestPhase1CanonicalPaginationAndCursorBinding(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"Alpha.md", "beta.md", "charlie.md", ".hidden.md"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	application := phase1App(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, stop := connectPipeTransport(t, ctx, application)
	defer stop()

	resolved := callTool[obsidian.ResolveOutput](t, ctx, session, obsidian.ToolResolve, map[string]any{"path": "alpha.md"})
	if !resolved.Exists || resolved.Path != "Alpha.md" {
		t.Fatalf("canonical resolve = %#v", resolved)
	}

	firstResult := phase1Call(t, ctx, session, obsidian.ToolLS, map[string]any{"path": ".", "limit": 1})
	var first obsidian.LSOutput
	unmarshalStructured(t, firstResult, &first)
	if !first.OK || !first.Truncated || first.Coverage.Continuation != "cursor" || first.Coverage.NextCursor == "" || !first.Coverage.ScopeComplete {
		t.Fatalf("first page = %#v", first)
	}
	if first.Coverage.FilesScanned != 4 || first.Coverage.BytesScanned != 0 {
		t.Fatalf("first coverage = %#v", first.Coverage)
	}
	assertSDKResultBudget(t, firstResult)

	secondResult := phase1Call(t, ctx, session, obsidian.ToolLS, map[string]any{
		"path": ".", "limit": 1, "cursor": first.Coverage.NextCursor,
	})
	var second obsidian.LSOutput
	unmarshalStructured(t, secondResult, &second)
	if !second.OK || len(second.Entries) != 1 || first.Entries[0].Name == second.Entries[0].Name {
		t.Fatalf("second page = %#v", second)
	}
	assertSDKResultBudget(t, secondResult)

	mismatch := phase1Call(t, ctx, session, obsidian.ToolLS, map[string]any{
		"path": ".", "limit": 2, "cursor": first.Coverage.NextCursor,
	})
	if !mismatch.IsError {
		t.Fatal("cursor mismatch returned success")
	}
	var mismatchOut obsidian.LSOutput
	unmarshalStructured(t, mismatch, &mismatchOut)
	if mismatchOut.Error == nil || mismatchOut.Error.Code != "cursor_mismatch" || mismatchOut.Coverage.Continuation != "restart" {
		t.Fatalf("cursor mismatch = %#v", mismatchOut)
	}
}

func TestPhase1CursorStalesAfterDirectoryMutationWithoutWriting(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.md", "b.md"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	application := phase1App(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, stop := connectPipeTransport(t, ctx, application)
	defer stop()

	firstResult := phase1Call(t, ctx, session, obsidian.ToolLS, map[string]any{"path": ".", "limit": 1})
	var first obsidian.LSOutput
	unmarshalStructured(t, firstResult, &first)
	if first.Coverage.NextCursor == "" {
		t.Fatalf("first page has no cursor: %#v", first)
	}
	if err := os.WriteFile(filepath.Join(root, "c.md"), []byte("c"), 0o600); err != nil {
		t.Fatal(err)
	}
	expectedVault := testutil.Snapshot(t, root)
	stale := phase1Call(t, ctx, session, obsidian.ToolLS, map[string]any{
		"path": ".", "limit": 1, "cursor": first.Coverage.NextCursor,
	})
	if !stale.IsError {
		t.Fatal("stale cursor returned success")
	}
	var out obsidian.LSOutput
	unmarshalStructured(t, stale, &out)
	if out.Error == nil || out.Error.Code != "cursor_stale" || out.Coverage.Continuation != "restart" || len(out.Entries) != 0 {
		t.Fatalf("stale output = %#v", out)
	}
	if after := testutil.Snapshot(t, root); !reflect.DeepEqual(expectedVault, after) {
		t.Fatalf("stale cursor call mutated vault:\nbefore=%#v\nafter=%#v", expectedVault, after)
	}
}

func TestPhase1CursorRejectionAndUnsignedRewriteAtSDKBoundary(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.md", "b.md", "c.md"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	application := phase1App(t, root)
	beforeVault := testutil.Snapshot(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, stop := connectPipeTransport(t, ctx, application)
	defer stop()

	firstResult := phase1Call(t, ctx, session, obsidian.ToolLS, map[string]any{"path": ".", "limit": 1})
	var first obsidian.LSOutput
	unmarshalStructured(t, firstResult, &first)
	issued, err := obsidian.DecodeCursor(first.Coverage.NextCursor)
	if err != nil {
		t.Fatal(err)
	}

	existingCursor, err := obsidian.EncodeCursor(issued.Tool, issued.Query, issued.Source, fsx.Position{NFC: "b.md", Stored: "b.md"})
	if err != nil {
		t.Fatal(err)
	}
	existing := phase1Call(t, ctx, session, obsidian.ToolLS, map[string]any{"path": ".", "limit": 1, "cursor": existingCursor})
	var existingOut obsidian.LSOutput
	unmarshalStructured(t, existing, &existingOut)
	if existing.IsError || !existingOut.OK || len(existingOut.Entries) != 1 || existingOut.Entries[0].Name != "c.md" ||
		existingOut.Coverage.FilesScanned != 3 || !existingOut.Coverage.ScopeComplete || existingOut.Coverage.Consistency != "stable" {
		t.Fatalf("existing-boundary rewrite = result %#v output %#v", existing, existingOut)
	}
	assertSDKResultBudget(t, existing)

	missingCursor, err := obsidian.EncodeCursor(issued.Tool, issued.Query, issued.Source, fsx.Position{NFC: "bb.md", Stored: "bb.md"})
	if err != nil {
		t.Fatal(err)
	}
	missing := phase1Call(t, ctx, session, obsidian.ToolLS, map[string]any{"path": ".", "limit": 1, "cursor": missingCursor})
	assertCursorToolError(t, missing, "cursor_invalid", 3)

	digest := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	validShape := fmt.Sprintf(`{"version":1,"tool":"ls","query":%q,"source":%q,"position":{"nfc":"a.md","stored":"a.md"}}`, digest, digest)
	malformed := map[string]string{
		"invalid alphabet": "%",
		"oversized":        strings.Repeat("A", obsidian.MaxCursorBytes+1),
		"unknown field":    encodeRawCursor(validShape[:len(validShape)-1] + `,"extra":true}`),
		"trailing value":   encodeRawCursor(validShape + `{}`),
		"unsupported":      encodeRawCursor(strings.Replace(validShape, `"version":1`, `"version":2`, 1)),
	}
	for name, cursor := range malformed {
		t.Run(name, func(t *testing.T) {
			result := phase1Call(t, ctx, session, obsidian.ToolLS, map[string]any{"path": ".", "limit": 1, "cursor": cursor})
			assertCursorToolError(t, result, "cursor_invalid", 0)
		})
	}
	if after := testutil.Snapshot(t, root); !reflect.DeepEqual(beforeVault, after) {
		t.Fatalf("cursor rejection/rewrite calls mutated vault:\nbefore=%#v\nafter=%#v", beforeVault, after)
	}
}

func TestPhase1LongNamesFitExactSDKResultBudget(t *testing.T) {
	root := t.TempDir()
	for index := 0; index < 500; index++ {
		name := fmt.Sprintf("%04d-%s.md", index, strings.Repeat("x", 220))
		if err := os.WriteFile(filepath.Join(root, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	application := phase1App(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, stop := connectPipeTransport(t, ctx, application)
	defer stop()

	result := phase1Call(t, ctx, session, obsidian.ToolLS, map[string]any{"path": ".", "limit": 500})
	if result.IsError {
		t.Fatalf("long-name page returned error: %#v", result.Content)
	}
	assertSDKResultBudget(t, result)
	var out obsidian.LSOutput
	unmarshalStructured(t, result, &out)
	if !out.Truncated || out.Coverage.StoppedBy != "response_limit" || out.Coverage.Continuation != "cursor" || out.Coverage.NextCursor == "" {
		t.Fatalf("long-name coverage = %#v", out.Coverage)
	}
	if len(out.Entries) == 0 || len(out.Entries) >= 500 {
		t.Fatalf("long-name entry count = %d", len(out.Entries))
	}

	continued := phase1Call(t, ctx, session, obsidian.ToolLS, map[string]any{
		"path": ".", "limit": 500, "cursor": out.Coverage.NextCursor,
	})
	if continued.IsError {
		t.Fatalf("continued long-name page returned error: %#v", continued.Content)
	}
	assertSDKResultBudget(t, continued)
}

func phase1App(t *testing.T, root string) *App {
	t.Helper()
	cfg, err := config.Validate(config.Config{Mode: config.ModeStdio, ObsidianRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	application, err := New(cfg, audit.Disabled())
	if err != nil {
		t.Fatal(err)
	}
	return application
}

func phase1Call(t *testing.T, ctx context.Context, session *sdk.ClientSession, name string, args map[string]any) *sdk.CallToolResult {
	t.Helper()
	result, err := session.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func assertSDKResultBudget(t *testing.T, result *sdk.CallToolResult) {
	t.Helper()
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > obsidian.MaxSDKResultBytes {
		t.Fatalf("SDK result bytes = %d, max %d", len(data), obsidian.MaxSDKResultBytes)
	}
	if len(result.Content) != 1 {
		t.Fatalf("content count = %d, want one bounded content item", len(result.Content))
	}
	if structured, err := json.Marshal(result.StructuredContent); err != nil || reflect.DeepEqual(data, structured) {
		t.Fatalf("result content appears to mirror structured output: result=%d structured=%d err=%v", len(data), len(structured), err)
	}
}

func assertCursorToolError(t *testing.T, result *sdk.CallToolResult, code string, filesScanned uint64) {
	t.Helper()
	if result == nil || !result.IsError {
		t.Fatalf("cursor result = %#v, want structured tool error", result)
	}
	var out obsidian.LSOutput
	unmarshalStructured(t, result, &out)
	if out.OK || out.Error == nil || out.Error.Code != code || len(out.Entries) != 0 ||
		out.Coverage.Continuation != "restart" || out.Coverage.FilesScanned != filesScanned {
		t.Fatalf("cursor error output = %#v", out)
	}
	assertSDKResultBudget(t, result)
}

func encodeRawCursor(raw string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func structuredObject(t *testing.T, result *sdk.CallToolResult) map[string]any {
	t.Helper()
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func schemaObject(t *testing.T, value any) map[string]any {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func schemaProperty(t *testing.T, schema map[string]any, name string) map[string]any {
	t.Helper()
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties = %#v", schema["properties"])
	}
	property, ok := properties[name].(map[string]any)
	if !ok {
		t.Fatalf("schema property %q = %#v", name, properties[name])
	}
	return property
}
