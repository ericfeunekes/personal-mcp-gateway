package ynab

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"personal-mcp-gateway/internal/audit"
	localmcp "personal-mcp-gateway/internal/mcp"
)

func TestMCPWirePreservesIntegerMilliunits(t *testing.T) {
	var sent string
	tools := testTools(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		sent = string(b)
		_, _ = w.Write([]byte(`{"data":{"transaction":{"id":"saved"},"server_knowledge":1}}`))
	}))
	descriptors, err := tools.Descriptors()
	if err != nil {
		t.Fatal(err)
	}
	server, _, err := localmcp.NewNamedServer(sdk.Implementation{Name: "ynab", Version: "1"}, audit.Disabled(), "http", descriptors)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"create","arguments":{"items":[{"type":"transaction","plan_id":"p","data":{"account_id":"a","date":"2026-01-01","amount":9007199254740993}}]},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"precision-test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", "2026-07-28")
	req.Header.Set("Mcp-Method", "tools/call")
	req.Header.Set("Mcp-Name", "create")
	response := httptest.NewRecorder()
	localmcp.StreamableHTTPHandlerWithNativeDocuments(server, nil, "").ServeHTTP(response, req)
	if response.Code != 200 {
		t.Fatalf("response %d %s", response.Code, response.Body.String())
	}
	if !strings.Contains(sent, `"amount":9007199254740993`) {
		t.Fatalf("milliunits changed crossing MCP: %s", sent)
	}
}

func TestMCPWirePreservesReturnedIntegerMilliunits(t *testing.T) {
	tools := testTools(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"transactions":[{"id":"saved","date":"2026-01-10","amount":9007199254740993}],"server_knowledge":1}}`))
	}))
	descriptors, err := tools.Descriptors()
	if err != nil {
		t.Fatal(err)
	}
	server, _, err := localmcp.NewNamedServer(sdk.Implementation{Name: "ynab", Version: "1"}, audit.Disabled(), "http", descriptors)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list","arguments":{"items":[{"type":"transaction","plan_id":"p","since_date":"2026-01-01","until_date":"2026-01-31"}]},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"precision-test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", "2026-07-28")
	req.Header.Set("Mcp-Method", "tools/call")
	req.Header.Set("Mcp-Name", "list")
	response := httptest.NewRecorder()
	localmcp.StreamableHTTPHandlerWithNativeDocuments(server, nil, "").ServeHTTP(response, req)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"amount":9007199254740993`) {
		t.Fatalf("returned milliunits changed crossing MCP: %d %s", response.Code, response.Body.String())
	}
}
