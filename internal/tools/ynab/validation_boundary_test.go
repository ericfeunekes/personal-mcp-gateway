package ynab

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"personal-mcp-gateway/internal/audit"
	localmcp "personal-mcp-gateway/internal/mcp"
)

func TestMCPCrossFieldValidationPreventsAllProviderEffects(t *testing.T) {
	today := time.Now().UTC()
	date := func(offsetYears, offsetDays int) string {
		return today.AddDate(offsetYears, 0, offsetDays).Format("2006-01-02")
	}
	cases := []struct{ name, verb, kind, data, wantError string }{
		{"target frequency without target", ToolUpdate, "category", `{"goal_frequency":"monthly"}`, "requires goal_target"},
		{"target frequency null target", ToolUpdate, "category", `{"goal_frequency":"monthly","goal_target":null}`, "requires goal_target"},
		{"target frequency with target date", ToolUpdate, "category", `{"goal_frequency":"monthly","goal_target":1000,"goal_target_date":"2030-01-01"}`, "cannot combine"},
		{"ordinary future date", ToolCreate, "transaction", `{"account_id":"a","date":"` + date(0, 1) + `","amount":1}`, "future transactions"},
		{"scheduled today", ToolCreate, "scheduled_transaction", `{"account_id":"a","date":"` + date(0, 0) + `","amount":1}`, "scheduled date"},
		{"scheduled past", ToolCreate, "scheduled_transaction", `{"account_id":"a","date":"` + date(0, -1) + `","amount":1}`, "scheduled date"},
		{"scheduled beyond five years", ToolCreate, "scheduled_transaction", `{"account_id":"a","date":"` + date(5, 2) + `","amount":1}`, "scheduled date"},
		{"split category absent", ToolCreate, "transaction", `{"account_id":"a","date":"2020-01-01","amount":1,"subtransactions":[{"amount":1}]}`, "explicit null"},
		{"split category nonnull", ToolCreate, "transaction", `{"account_id":"a","date":"2020-01-01","amount":1,"category_id":"c","subtransactions":[{"amount":1}]}`, "explicit null"},
		{"split sum mismatch", ToolCreate, "transaction", `{"account_id":"a","date":"2020-01-01","amount":2,"category_id":null,"subtransactions":[{"amount":1}]}`, "must sum"},
		{"split positive overflow", ToolCreate, "transaction", `{"account_id":"a","date":"2020-01-01","amount":0,"category_id":null,"subtransactions":[{"amount":9223372036854775807},{"amount":1}]}`, "overflow"},
		{"split negative overflow", ToolCreate, "transaction", `{"account_id":"a","date":"2020-01-01","amount":0,"category_id":null,"subtransactions":[{"amount":-9223372036854775808},{"amount":-1}]}`, "overflow"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var calls atomic.Int32
			tools := testTools(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				_, _ = w.Write([]byte(`{"data":{"payee":{"id":"saved"}}}`))
			}))
			descriptors, err := tools.Descriptors()
			if err != nil {
				t.Fatal(err)
			}
			server, _, err := localmcp.NewNamedServer(sdk.Implementation{Name: "ynab", Version: "1"}, audit.Disabled(), "http", descriptors)
			if err != nil {
				t.Fatal(err)
			}
			valid := Item{Type: "payee", PlanID: "p", Data: json.RawMessage(`{"name":"must not be saved"}`)}
			invalid := Item{Type: c.kind, PlanID: "p", Data: json.RawMessage(c.data)}
			if c.verb == ToolUpdate {
				valid.PayeeID = "payee"
				invalid.CategoryID = "category"
			}
			arguments, err := json.Marshal(Input{Items: []Item{valid, invalid, valid}})
			if err != nil {
				t.Fatal(err)
			}
			body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + c.verb + `","arguments":` + string(arguments) + `,"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"validation-test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`
			req := httptest.NewRequest("POST", "/mcp", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			req.Header.Set("MCP-Protocol-Version", "2026-07-28")
			req.Header.Set("Mcp-Method", "tools/call")
			req.Header.Set("Mcp-Name", c.verb)
			response := httptest.NewRecorder()
			localmcp.StreamableHTTPHandlerWithNativeDocuments(server, nil, "").ServeHTTP(response, req)
			var wire struct {
				Result struct {
					IsError bool   `json:"isError"`
					Output  Output `json:"structuredContent"`
				} `json:"result"`
			}
			if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &wire) != nil {
				t.Fatalf("invalid MCP response: %d %s", response.Code, response.Body.String())
			}
			if calls.Load() != 0 || !wire.Result.IsError || len(wire.Result.Output.Results) != 3 {
				t.Fatalf("validation must reject entire batch before effects: calls=%d response=%s", calls.Load(), response.Body.String())
			}
			for i, result := range wire.Result.Output.Results {
				want := "not_attempted"
				if i == 1 {
					want = "error"
				}
				if result.Index != i || result.Status != want {
					t.Fatalf("incomplete indexed result: %+v", result)
				}
			}
			if !strings.Contains(wire.Result.Output.Results[1].Error, c.wantError) {
				t.Fatalf("wrong rejection reason: %+v", wire.Result.Output.Results[1])
			}
		})
	}
}
