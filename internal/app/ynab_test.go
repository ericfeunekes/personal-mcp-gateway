package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"personal-mcp-gateway/internal/audit"
	"personal-mcp-gateway/internal/config"
)

func newYNABTestApp(t *testing.T) *App {
	t.Helper()
	cfg, err := config.Validate(config.Config{Mode: config.ModeStdio, Server: config.ServerYNAB, YNABToken: "fixture-private-token", YNABExportRoot: filepath.Join(t.TempDir(), "exports"), Telemetry: config.TelemetryOff})
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(cfg, audit.Disabled())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a
}

func TestYNABAppHasSeparateTypedToolSurfaceWithoutVault(t *testing.T) {
	a := newYNABTestApp(t)
	if a.vault != nil || a.documents != nil {
		t.Fatal("YNAB constructed Obsidian state")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, stop := connectPipeTransport(t, ctx, a)
	defer stop()
	result, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range result.Tools {
		names = append(names, tool.Name)
		b, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), `"items"`) || !strings.Contains(string(b), `"required"`) {
			t.Fatalf("tool %s lacks typed arrays", tool.Name)
		}
		if strings.Contains(string(b), "fixture-private-token") || strings.Contains(string(b), a.cfg.YNABExportRoot) {
			t.Fatal("private configuration in schema")
		}
	}
	sort.Strings(names)
	if !reflect.DeepEqual(names, []string{"create", "delete", "get", "import_transactions", "list", "update"}) {
		t.Fatalf("tools %v", names)
	}
	resultCall, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "list", Arguments: map[string]any{"items": []any{map[string]any{"type": "transaction", "plan_id": "fixture"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if !resultCall.IsError {
		t.Fatal("transaction list without dates accepted")
	}
}

func TestYNABReadinessIsLocalAndIndependent(t *testing.T) {
	a := newYNABTestApp(t)
	for _, route := range []string{"/healthz", "/readyz"} {
		r := httptest.NewRecorder()
		a.HTTPHandler().ServeHTTP(r, httptest.NewRequest(http.MethodGet, route, nil))
		if r.Code != http.StatusOK {
			t.Fatalf("%s status %d", route, r.Code)
		}
	}
}

func TestYNABHTTPServesSameIdentityAndTools(t *testing.T) {
	a := newYNABTestApp(t)
	server := httptest.NewServer(a.HTTPHandler())
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := sdk.NewClient(&sdk.Implementation{Name: "ynab-http-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &sdk.StreamableClientTransport{Endpoint: server.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	info := session.InitializeResult().ServerInfo
	if info == nil || info.Name != "ynab" {
		t.Fatalf("wrong HTTP identity: %#v", info)
	}
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != 6 {
		t.Fatalf("tools: %d", len(listed.Tools))
	}
}
