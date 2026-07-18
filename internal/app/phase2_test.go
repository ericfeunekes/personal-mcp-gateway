package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"personal-mcp-gateway/internal/audit"
	"personal-mcp-gateway/internal/config"
	"personal-mcp-gateway/internal/tools/obsidian"
)

func TestPhase2ExactSurfaceSchemasAreClosedAndAgentOriented(t *testing.T) {
	t.Run("pipe", func(t *testing.T) {
		application := newTestApp(t)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		session, stop := connectPipeTransport(t, ctx, application)
		defer stop()

		assertPhase2ToolDescriptors(t, listPhase2Tools(t, ctx, session))
		assertPhase2SDKSchemaEnforcement(t, ctx, session)
	})

	t.Run("streamable HTTP", func(t *testing.T) {
		application := newTestApp(t)
		server := httptest.NewServer(application.HTTPHandler())
		defer server.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		client := sdk.NewClient(&sdk.Implementation{Name: "phase2-schema-test", Version: "v0.0.1"}, nil)
		session, err := client.Connect(ctx, &sdk.StreamableClientTransport{Endpoint: server.URL + "/mcp"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer session.Close()

		assertPhase2ToolDescriptors(t, listPhase2Tools(t, ctx, session))
		assertPhase2SDKSchemaEnforcement(t, ctx, session)
	})
}

func TestPhase2SDKJourneyGrepReadAndContinuedReadMany(t *testing.T) {
	root := t.TempDir()
	alpha := "---\ntags: [training]\n---\n# Workout\nneedle warmup\nabcdefgh\n"
	beta := "# Health\nneedle recovery\n"
	for name, body := range map[string]string{"alpha.md": alpha, "beta.md": beta} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	application := phase1App(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, stop := connectPipeTransport(t, ctx, application)
	defer stop()

	grepResult := phase1Call(t, ctx, session, obsidian.ToolGrep, map[string]any{
		"pattern": "needle", "path": ".", "regex": false, "context_lines": 0, "limit": 10,
	})
	if grepResult.IsError {
		t.Fatalf("grep returned error: %#v", grepResult.Content)
	}
	assertSDKResultBudget(t, grepResult)
	var grepOut obsidian.GrepOutput
	unmarshalStructured(t, grepResult, &grepOut)
	if !grepOut.OK || len(grepOut.Matches) != 2 || grepOut.Coverage.Continuation != "complete" {
		t.Fatalf("grep output = %#v", grepOut)
	}

	readResult := phase1Call(t, ctx, session, obsidian.ToolRead, map[string]any{
		"path": "alpha.md", "selector": map[string]any{"kind": obsidian.SelectorHeading, "heading": "Workout"},
	})
	if readResult.IsError {
		t.Fatalf("read returned error: %#v", readResult.Content)
	}
	assertSDKResultBudget(t, readResult)
	var readOut obsidian.ReadOutput
	unmarshalStructured(t, readResult, &readOut)
	if !readOut.OK || readOut.Content == nil || !strings.Contains(*readOut.Content, "needle warmup") {
		t.Fatalf("heading read output = %#v", readOut)
	}

	arguments := map[string]any{
		"requests": []any{
			map[string]any{"path": "alpha.md", "selector": map[string]any{"kind": obsidian.SelectorContent, "start_line": 1}, "max_bytes": 7},
			map[string]any{"path": "beta.md"},
		},
		"max_bytes": 16,
	}
	var alphaAccumulated strings.Builder
	var betaAccumulated strings.Builder
	for page := 0; page < 32; page++ {
		result := phase1Call(t, ctx, session, obsidian.ToolReadMany, arguments)
		if result.IsError {
			t.Fatalf("read_many page %d returned error: %#v", page, result.Content)
		}
		assertSDKResultBudget(t, result)
		var out obsidian.ReadManyOutput
		unmarshalStructured(t, result, &out)
		if !out.OK {
			t.Fatalf("read_many page %d = %#v", page, out)
		}
		for _, item := range out.Items {
			if !item.OK || item.Content == nil {
				t.Fatalf("read_many page %d item = %#v", page, item)
			}
			switch item.Index {
			case 0:
				alphaAccumulated.WriteString(*item.Content)
			case 1:
				betaAccumulated.WriteString(*item.Content)
			default:
				t.Fatalf("unexpected read_many index %d", item.Index)
			}
		}
		if out.Coverage.Continuation == "complete" {
			break
		}
		if out.Coverage.Continuation != "cursor" || out.Coverage.NextCursor == "" {
			t.Fatalf("read_many page %d coverage = %#v", page, out.Coverage)
		}
		arguments["cursor"] = out.Coverage.NextCursor
	}
	if alphaAccumulated.String() != alpha || betaAccumulated.String() != beta {
		t.Fatalf("accumulated alpha=%q beta=%q", alphaAccumulated.String(), betaAccumulated.String())
	}
}

func TestPhase2TelemetryKeepsRetrievalEvidencePrivate(t *testing.T) {
	const (
		runID           = "phase2-privacy-run"
		privatePath     = "private-workout-source.md"
		privatePattern  = "sensitive-health-pattern"
		privateContents = "sensitive-health-pattern private medical content\n"
	)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, privatePath), []byte(privateContents), 0o600); err != nil {
		t.Fatal(err)
	}
	var telemetry bytes.Buffer
	log := audit.NewJSONL(&telemetry, runID)
	cfg, err := config.Validate(config.Config{Mode: config.ModeStdio, ObsidianRoot: root, Telemetry: config.TelemetryStderr})
	if err != nil {
		t.Fatal(err)
	}
	application, err := New(cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, stop := connectPipeTransport(t, ctx, application)
	defer stop()

	readResult := phase1Call(t, ctx, session, obsidian.ToolRead, map[string]any{"path": privatePath, "max_bytes": 4})
	var readOut obsidian.ReadOutput
	unmarshalStructured(t, readResult, &readOut)
	if !readOut.OK || readOut.Coverage.NextCursor == "" {
		t.Fatalf("privacy read setup = %#v", readOut)
	}
	_ = phase1Call(t, ctx, session, obsidian.ToolReadMany, map[string]any{
		"requests": []any{map[string]any{"path": privatePath, "selector": map[string]any{"kind": obsidian.SelectorContent, "start_line": 1}}},
	})
	_ = phase1Call(t, ctx, session, obsidian.ToolGrep, map[string]any{
		"pattern": privatePattern, "path": ".", "regex": false, "context_lines": 0,
	})

	records := toolCallRecords(auditRecords(t, telemetry.String()))
	for _, name := range []string{obsidian.ToolRead, obsidian.ToolReadMany, obsidian.ToolGrep} {
		if findToolRecord(records, name, "ok", "") == nil {
			t.Fatalf("missing %s telemetry record: %s", name, telemetry.String())
		}
	}
	grepRecord := findToolRecord(records, obsidian.ToolGrep, "ok", "")
	grepArgs := summarySection(t, grepRecord, "arguments")
	if intFromRecord(grepArgs, "pattern_bytes") != len(privatePattern) || grepArgs["regex"] != false {
		t.Fatalf("grep safe summary = %#v", grepArgs)
	}
	readManyRecord := findToolRecord(records, obsidian.ToolReadMany, "ok", "")
	readManyArgs := summarySection(t, readManyRecord, "arguments")
	if intFromRecord(readManyArgs, "request_count") != 1 {
		t.Fatalf("read_many safe summary = %#v", readManyArgs)
	}

	encoded := telemetry.String()
	for _, forbidden := range []string{
		privatePath,
		privatePattern,
		privateContents,
		readOut.Coverage.NextCursor,
		audit.HashString(runID, privatePattern),
	} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("retrieval telemetry retained prohibited evidence %q: %s", forbidden, encoded)
		}
	}
}

func listPhase2Tools(t *testing.T, ctx context.Context, session *sdk.ClientSession) map[string]*sdk.Tool {
	t.Helper()
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(listed.Tools))
	byName := make(map[string]*sdk.Tool, len(listed.Tools))
	for _, tool := range listed.Tools {
		if _, duplicate := byName[tool.Name]; duplicate {
			t.Fatalf("duplicate tool descriptor %q", tool.Name)
		}
		got = append(got, tool.Name)
		byName[tool.Name] = tool
	}
	sort.Strings(got)
	want := []string{obsidian.ToolGrep, obsidian.ToolLS, obsidian.ToolRead, obsidian.ToolReadMany, obsidian.ToolResolve}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tools = %#v, want exact Phase 2 surface %#v", got, want)
	}
	return byName
}

func assertPhase2ToolDescriptors(t *testing.T, tools map[string]*sdk.Tool) {
	t.Helper()
	descriptions := map[string]string{
		obsidian.ToolResolve:  obsidian.ResolveDescription,
		obsidian.ToolLS:       obsidian.LSDescription,
		obsidian.ToolRead:     obsidian.ReadDescription,
		obsidian.ToolReadMany: obsidian.ReadManyDescription,
		obsidian.ToolGrep:     obsidian.GrepDescription,
	}
	for name, wantDescription := range descriptions {
		tool := tools[name]
		if tool == nil {
			t.Fatalf("missing descriptor %q", name)
		}
		if tool.Description != wantDescription {
			t.Fatalf("%s description = %q, want %q", name, tool.Description, wantDescription)
		}
		assertExactReadOnlyAnnotations(t, tool)
	}

	resolve := schemaObject(t, tools[obsidian.ToolResolve].InputSchema)
	assertExactObjectGrammar(t, resolve, []string{"base", "path"}, []string{"path"})
	assertPropertyType(t, resolve, "path", "string")
	assertPropertyType(t, resolve, "base", "string")

	ls := schemaObject(t, tools[obsidian.ToolLS].InputSchema)
	assertExactObjectGrammar(t, ls, []string{"base", "cursor", "limit", "path"}, []string{"path"})
	for _, name := range []string{"path", "base", "cursor"} {
		assertPropertyType(t, ls, name, "string")
	}
	assertIntegerContract(t, schemaProperty(t, ls, "limit"), 1, 500, 100)

	read := schemaObject(t, tools[obsidian.ToolRead].InputSchema)
	assertExactObjectGrammar(t, read, []string{"base", "cursor", "max_bytes", "path", "selector"}, []string{"path"})
	for _, name := range []string{"path", "base", "cursor"} {
		assertPropertyType(t, read, name, "string")
	}
	assertIntegerContract(t, schemaProperty(t, read, "max_bytes"), 1, obsidian.MaxReadBytes, obsidian.DefaultReadBytes)
	assertSelectorContract(t, schemaProperty(t, read, "selector"))

	readMany := schemaObject(t, tools[obsidian.ToolReadMany].InputSchema)
	assertExactObjectGrammar(t, readMany, []string{"cursor", "max_bytes", "requests"}, []string{"requests"})
	assertPropertyType(t, readMany, "cursor", "string")
	assertIntegerContract(t, schemaProperty(t, readMany, "max_bytes"), 1, obsidian.MaxReadManyBytes, obsidian.DefaultReadManyBytes)
	requests := schemaProperty(t, readMany, "requests")
	assertSchemaValue(t, requests, "type", "array")
	assertSchemaValue(t, requests, "minItems", 1)
	assertSchemaValue(t, requests, "maxItems", obsidian.MaxReadManyRequests)
	request, ok := requests["items"].(map[string]any)
	if !ok {
		t.Fatalf("read_many request items = %#v", requests["items"])
	}
	assertExactObjectGrammar(t, request, []string{"base", "max_bytes", "path", "selector"}, []string{"path"})
	for _, name := range []string{"path", "base"} {
		assertPropertyType(t, request, name, "string")
	}
	assertIntegerContract(t, schemaProperty(t, request, "max_bytes"), 1, obsidian.MaxReadBytes, obsidian.DefaultReadBytes)
	assertSelectorContract(t, schemaProperty(t, request, "selector"))

	grep := schemaObject(t, tools[obsidian.ToolGrep].InputSchema)
	assertExactObjectGrammar(t, grep, []string{
		"base", "case_sensitive", "context_lines", "cursor", "limit", "max_bytes", "max_files", "path", "pattern", "regex",
	}, []string{"pattern"})
	for _, name := range []string{"pattern", "path", "base", "cursor"} {
		assertPropertyType(t, grep, name, "string")
	}
	pattern := schemaProperty(t, grep, "pattern")
	assertSchemaValue(t, pattern, "minLength", 1)
	assertSchemaValue(t, pattern, "maxLength", obsidian.MaxGrepPatternBytes)
	assertSchemaValue(t, schemaProperty(t, grep, "path"), "default", ".")
	assertBooleanContract(t, schemaProperty(t, grep, "regex"), true)
	assertBooleanContract(t, schemaProperty(t, grep, "case_sensitive"), false)
	assertIntegerContract(t, schemaProperty(t, grep, "context_lines"), 0, obsidian.MaxGrepContextLines, obsidian.DefaultGrepContextLines)
	assertIntegerContract(t, schemaProperty(t, grep, "limit"), 1, obsidian.MaxGrepLimit, obsidian.DefaultGrepLimit)
	assertIntegerContract(t, schemaProperty(t, grep, "max_files"), 1, obsidian.MaxGrepMaxFiles, obsidian.DefaultGrepMaxFiles)
	assertIntegerContract(t, schemaProperty(t, grep, "max_bytes"), 1, obsidian.MaxGrepMaxBytes, obsidian.DefaultGrepMaxBytes)
}

func assertExactReadOnlyAnnotations(t *testing.T, tool *sdk.Tool) {
	t.Helper()
	annotations := tool.Annotations
	if annotations == nil || !annotations.ReadOnlyHint || annotations.DestructiveHint == nil || *annotations.DestructiveHint ||
		annotations.OpenWorldHint == nil || *annotations.OpenWorldHint || annotations.IdempotentHint || annotations.Title != "" {
		t.Fatalf("%s annotations = %#v, want exact read-only/non-destructive/closed-world hints", tool.Name, annotations)
	}
}

func assertExactObjectGrammar(t *testing.T, schema map[string]any, wantProperties, wantRequired []string) {
	t.Helper()
	assertSchemaValue(t, schema, "type", "object")
	assertSchemaValue(t, schema, "additionalProperties", false)
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties = %#v", schema["properties"])
	}
	gotProperties := make([]string, 0, len(properties))
	for name := range properties {
		gotProperties = append(gotProperties, name)
	}
	sort.Strings(gotProperties)
	wantProperties = append([]string(nil), wantProperties...)
	sort.Strings(wantProperties)
	if !reflect.DeepEqual(gotProperties, wantProperties) {
		t.Fatalf("schema properties = %#v, want %#v", gotProperties, wantProperties)
	}
	gotRequired := schemaStringSlice(t, schema, "required")
	sort.Strings(gotRequired)
	wantRequired = append([]string(nil), wantRequired...)
	sort.Strings(wantRequired)
	if !reflect.DeepEqual(gotRequired, wantRequired) {
		t.Fatalf("schema required = %#v, want %#v", gotRequired, wantRequired)
	}
}

func schemaStringSlice(t *testing.T, schema map[string]any, name string) []string {
	t.Helper()
	raw, ok := schema[name].([]any)
	if !ok {
		t.Fatalf("schema %s = %#v", name, schema[name])
	}
	values := make([]string, len(raw))
	for index, value := range raw {
		text, ok := value.(string)
		if !ok {
			t.Fatalf("schema %s[%d] = %#v", name, index, value)
		}
		values[index] = text
	}
	return values
}

func assertPropertyType(t *testing.T, schema map[string]any, name, want string) {
	t.Helper()
	assertSchemaValue(t, schemaProperty(t, schema, name), "type", want)
}

func assertIntegerContract(t *testing.T, schema map[string]any, minimum, maximum, defaultValue any) {
	t.Helper()
	assertSchemaValue(t, schema, "type", "integer")
	assertSchemaValue(t, schema, "minimum", minimum)
	assertSchemaValue(t, schema, "maximum", maximum)
	assertSchemaValue(t, schema, "default", defaultValue)
}

func assertBooleanContract(t *testing.T, schema map[string]any, defaultValue bool) {
	t.Helper()
	assertSchemaValue(t, schema, "type", "boolean")
	assertSchemaValue(t, schema, "default", defaultValue)
}

func assertSchemaValue(t *testing.T, schema map[string]any, name string, want any) {
	t.Helper()
	got := schema[name]
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var normalized any
	if err := json.Unmarshal(data, &normalized); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, normalized) {
		t.Fatalf("schema %s = %#v, want %#v in %#v", name, got, normalized, schema)
	}
}

func assertSelectorContract(t *testing.T, schema map[string]any) {
	t.Helper()
	assertSchemaValue(t, schema, "default", map[string]any{"kind": obsidian.SelectorContent, "start_line": 1})
	variants, ok := schema["oneOf"].([]any)
	if !ok || len(variants) != 5 {
		t.Fatalf("selector oneOf = %#v", schema["oneOf"])
	}
	byKind := make(map[string]map[string]any, len(variants))
	for index, raw := range variants {
		variant, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("selector variant %d = %#v", index, raw)
		}
		kindSchema := schemaProperty(t, variant, "kind")
		kind, ok := kindSchema["const"].(string)
		if !ok || kind == "" {
			t.Fatalf("selector variant %d kind.const = %#v", index, kindSchema["const"])
		}
		if _, duplicate := byKind[kind]; duplicate {
			t.Fatalf("duplicate selector kind.const %q", kind)
		}
		byKind[kind] = variant
	}
	selectorVariant := func(kind string, properties, required []string) map[string]any {
		t.Helper()
		variant, ok := byKind[kind]
		if !ok {
			t.Fatalf("selector variant %q missing; kinds = %#v", kind, byKind)
		}
		assertExactObjectGrammar(t, variant, properties, required)
		kindSchema := schemaProperty(t, variant, "kind")
		assertSchemaValue(t, kindSchema, "type", "string")
		assertSchemaValue(t, kindSchema, "const", kind)
		return variant
	}
	content := selectorVariant(obsidian.SelectorContent, []string{"kind", "start_line"}, []string{"kind"})
	assertIntegerContract(t, schemaProperty(t, content, "start_line"), 1, obsidian.MaxMarkdownSourceLines, 1)
	heading := selectorVariant(obsidian.SelectorHeading, []string{"heading", "kind", "occurrence"}, []string{"heading", "kind"})
	headingText := schemaProperty(t, heading, "heading")
	assertSchemaValue(t, headingText, "type", "string")
	assertSchemaValue(t, headingText, "minLength", 1)
	assertIntegerContract(t, schemaProperty(t, heading, "occurrence"), 1, obsidian.MaxMarkdownSourceLines, 1)
	block := selectorVariant(obsidian.SelectorBlock, []string{"block_id", "kind"}, []string{"block_id", "kind"})
	blockID := schemaProperty(t, block, "block_id")
	assertSchemaValue(t, blockID, "type", "string")
	assertSchemaValue(t, blockID, "minLength", 1)
	selectorVariant(obsidian.SelectorFrontmatter, []string{"kind"}, []string{"kind"})
	selectorVariant(obsidian.SelectorOutline, []string{"kind"}, []string{"kind"})
}

func assertPhase2SDKSchemaEnforcement(t *testing.T, ctx context.Context, session *sdk.ClientSession) {
	t.Helper()
	for name, arguments := range map[string]map[string]any{
		"ls zero limit": {"path": ".", "limit": 0},
		"ls over limit": {"path": ".", "limit": 501},
		"mixed selector fields": {
			"path": "README.md",
			"selector": map[string]any{
				"kind":    obsidian.SelectorFrontmatter,
				"heading": "not allowed for frontmatter",
			},
		},
	} {
		toolName := obsidian.ToolLS
		if name == "mixed selector fields" {
			toolName = obsidian.ToolRead
		}
		result, err := session.CallTool(ctx, &sdk.CallToolParams{Name: toolName, Arguments: arguments})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !result.IsError {
			t.Fatalf("%s passed SDK schema validation", name)
		}
	}

	pattern := strings.Repeat("é", obsidian.MaxGrepPatternBytes/2+1)
	if len([]rune(pattern)) > obsidian.MaxGrepPatternBytes || len([]byte(pattern)) <= obsidian.MaxGrepPatternBytes {
		t.Fatalf("invalid multibyte pattern fixture: runes=%d bytes=%d", len([]rune(pattern)), len([]byte(pattern)))
	}
	result, err := session.CallTool(ctx, &sdk.CallToolParams{Name: obsidian.ToolGrep, Arguments: map[string]any{
		"pattern": pattern,
		"path":    ".",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("multibyte grep pattern above 4096 bytes passed domain enforcement")
	}
	var out obsidian.GrepOutput
	unmarshalStructured(t, result, &out)
	if out.OK || out.Error == nil || out.Error.Code != "input_too_large" {
		t.Fatalf("multibyte grep pattern error = %#v", out)
	}
}
