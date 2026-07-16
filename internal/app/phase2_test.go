package app

import (
	"bytes"
	"context"
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
	application := newTestApp(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, stop := connectPipeTransport(t, ctx, application)
	defer stop()

	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(listed.Tools))
	byName := make(map[string]*sdk.Tool, len(listed.Tools))
	for _, tool := range listed.Tools {
		got = append(got, tool.Name)
		byName[tool.Name] = tool
		schema := schemaObject(t, tool.InputSchema)
		if additional, ok := schema["additionalProperties"].(bool); !ok || additional {
			t.Fatalf("%s input schema is not closed: %#v", tool.Name, schema)
		}
	}
	sort.Strings(got)
	want := []string{obsidian.ToolGrep, obsidian.ToolLS, obsidian.ToolRead, obsidian.ToolReadMany, obsidian.ToolResolve}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tools = %#v, want exact Phase 2 surface %#v", got, want)
	}

	readSelector := schemaProperty(t, schemaObject(t, byName[obsidian.ToolRead].InputSchema), "selector")
	assertClosedSelectorUnion(t, readSelector)
	readManyRequests := schemaProperty(t, schemaObject(t, byName[obsidian.ToolReadMany].InputSchema), "requests")
	requestSchema, ok := readManyRequests["items"].(map[string]any)
	if !ok || requestSchema["additionalProperties"] != false {
		t.Fatalf("read_many request schema is not closed: %#v", readManyRequests)
	}
	assertClosedSelectorUnion(t, schemaProperty(t, requestSchema, "selector"))

	if !strings.Contains(byName[obsidian.ToolRead].Description, "grep") ||
		!strings.Contains(byName[obsidian.ToolRead].Description, "read_many") ||
		!strings.Contains(byName[obsidian.ToolReadMany].Description, "Accumulate only the new items") ||
		!strings.Contains(byName[obsidian.ToolGrep].Description, "deterministic canonical-path order") {
		t.Fatalf("retrieval descriptions do not teach the intended journey")
	}

	invalid, err := session.CallTool(ctx, &sdk.CallToolParams{Name: obsidian.ToolRead, Arguments: map[string]any{
		"path": "README.md",
		"selector": map[string]any{
			"kind":    obsidian.SelectorFrontmatter,
			"heading": "not allowed for frontmatter",
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !invalid.IsError {
		t.Fatal("mixed selector fields passed SDK schema validation")
	}
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

func assertClosedSelectorUnion(t *testing.T, schema map[string]any) {
	t.Helper()
	variants, ok := schema["oneOf"].([]any)
	if !ok || len(variants) != 5 {
		t.Fatalf("selector oneOf = %#v", schema["oneOf"])
	}
	for index, raw := range variants {
		variant, ok := raw.(map[string]any)
		if !ok || variant["additionalProperties"] != false {
			t.Fatalf("selector variant %d is not closed: %#v", index, raw)
		}
	}
}
