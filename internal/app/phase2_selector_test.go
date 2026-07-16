package app

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"personal-mcp-gateway/internal/testutil"
	"personal-mcp-gateway/internal/tools/obsidian"
)

func TestPhase2SDKBlockSelectionKeepsGoldmarkSemanticsOnDenseSource(t *testing.T) {
	root := t.TempDir()
	markdown := "target ^same\n~ continuation\n\n" + strings.Repeat("\n", 4_094)
	if err := os.WriteFile(filepath.Join(root, "dense.md"), []byte(markdown), 0o600); err != nil {
		t.Fatal(err)
	}
	before := testutil.Snapshot(t, root)
	application := phase1App(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, stop := connectPipeTransport(t, ctx, application)
	defer stop()

	result, err := session.CallTool(ctx, &sdk.CallToolParams{Name: obsidian.ToolRead, Arguments: map[string]any{
		"path": "dense.md", "selector": map[string]any{"kind": obsidian.SelectorBlock, "block_id": "same"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	assertSDKResultBudget(t, result)
	var out obsidian.ReadOutput
	unmarshalStructured(t, result, &out)
	if !result.IsError || out.OK || out.Error == nil || out.Error.Code != obsidian.SelectorNotFoundCode || out.Content != nil ||
		out.Coverage.Continuation != "restart" || out.Coverage.NextCursor != "" || out.Coverage.ScopeComplete {
		t.Fatalf("dense block result=%#v out=%#v", result, out)
	}
	if after := testutil.Snapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatalf("dense block SDK call mutated vault:\nbefore=%#v\nafter=%#v", before, after)
	}
}
