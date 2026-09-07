package ynab

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"personal-mcp-gateway/internal/audit"
	localmcp "personal-mcp-gateway/internal/mcp"
)

func TestSDKProviderResultsUseTypedCollections(t *testing.T) {
	tools := testTools(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/plans/p/accounts" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"accounts":[],"server_knowledge":5}}`))
	}))
	descriptors, err := tools.Descriptors()
	if err != nil {
		t.Fatal(err)
	}
	server, _, err := localmcp.NewNamedServer(sdk.Implementation{Name: "ynab", Version: "test"}, audit.Disabled(), "stdio", descriptors)
	if err != nil {
		t.Fatal(err)
	}
	ct, st := sdk.NewInMemoryTransports()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := sdk.NewClient(&sdk.Implementation{Name: "provider-boundary-test", Version: "1"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	result, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: "list", Arguments: map[string]any{"items": []any{map[string]any{"type": "account", "plan_id": "p"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("SDK rejected provider result: %+v", result)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Results []map[string]json.RawMessage `json:"results"`
	}
	if err = json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Results) != 1 || string(got.Results[0]["accounts"]) != "[]" || string(got.Results[0]["server_knowledge"]) != "5" {
		t.Fatalf("wrong model-visible result %s", encoded)
	}
	if _, found := got.Results[0]["data"]; found {
		t.Fatalf("opaque provider envelope exposed %s", encoded)
	}
}
