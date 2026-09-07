package ynab

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"personal-mcp-gateway/internal/audit"
	localmcp "personal-mcp-gateway/internal/mcp"
)

// This boundary test uses real MCP dispatch and each real persistence sink.
// The provider fixture supplies private data deterministically without touching YNAB.
func TestMCPTelemetryPersistsCountersNotFinancialData(t *testing.T) {
	for _, sink := range []string{"jsonl", "sqlite"} {
		t.Run(sink, func(t *testing.T) {
			const token = "private-token-sentinel"
			const transaction = `{"id":"private-transaction-sentinel","date":"2025-01-02","amount":7654321987,"memo":"private-memo-sentinel","account_id":"private-account-sentinel","account_name":"private-account-name-sentinel","payee_name":"private-payee-name-sentinel","category_name":"private-category-name-sentinel"}`
			exportRoot := filepath.Join(t.TempDir(), "private-export-root-sentinel")
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer "+token {
					t.Error("provider did not receive configured credential")
				}
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.HasSuffix(r.URL.Path, "/private-failure-sentinel"):
					w.WriteHeader(http.StatusInternalServerError)
					fmt.Fprint(w, `{"error":{"id":"500","name":"private-error-name-sentinel","detail":"private-error-detail-sentinel"}}`)
				case r.URL.Path == "/plans/private-plan-sentinel":
					fmt.Fprintf(w, `{"data":{"plan":{"id":"private-plan-sentinel","name":"private-plan-name-sentinel","transactions":[%s]}}}`, transaction)
				default:
					fmt.Fprintf(w, `{"data":{"transaction":%s}}`, transaction)
				}
			}))
			defer provider.Close()
			tools := newTools(Options{Token: token, ExportRoot: exportRoot}, provider.URL, provider.Client())
			defer tools.Close()
			var jsonl bytes.Buffer
			var log *audit.Logger
			dbPath := filepath.Join(t.TempDir(), "events.sqlite")
			var err error
			if sink == "jsonl" {
				log = audit.NewJSONL(&jsonl, "privacy-boundary")
			} else {
				log, err = audit.NewSQLite(dbPath, "privacy-boundary")
				if err != nil {
					t.Fatal(err)
				}
			}
			t.Cleanup(func() { _ = log.Close() })
			descriptors, err := tools.Descriptors()
			if err != nil {
				t.Fatal(err)
			}
			server, _, err := localmcp.NewNamedServer(sdk.Implementation{Name: "ynab", Version: "test"}, log, "stdio", descriptors)
			if err != nil {
				t.Fatal(err)
			}
			ct, st := sdk.NewInMemoryTransports()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			ss, err := server.Connect(ctx, st, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer ss.Close()
			cs, err := sdk.NewClient(&sdk.Implementation{Name: "privacy-test", Version: "1"}, nil).Connect(ctx, ct, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer cs.Close()
			calls := []struct {
				name, args, visible string
				count, failures     float64
			}{
				{"get", `{"items":[{"type":"transaction","plan_id":"private-plan-sentinel","transaction_id":"private-transaction-sentinel"}]}`, "private-memo-sentinel", 1, 0},
				{"create", `{"items":[{"type":"transaction","plan_id":"private-plan-sentinel","data":{"account_id":"private-account-sentinel","date":"2025-01-02","amount":7654321987,"memo":"private-input-memo-sentinel","payee_name":"private-input-payee-sentinel"}},{"type":"export","plan_id":"private-plan-sentinel","source":"plan","format":"json","filename":"private-filename-sentinel.json"}]}`, "private-export-root-sentinel", 2, 0},
				{"get", `{"items":[{"type":"transaction","plan_id":"private-plan-sentinel","transaction_id":"private-failure-sentinel"}]}`, `"status":"error"`, 1, 1},
			}
			for _, call := range calls {
				result, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: call.name, Arguments: json.RawMessage(call.args)})
				if err != nil {
					t.Fatal(err)
				}
				body, err := json.Marshal(result.StructuredContent)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(body), call.visible) {
					t.Fatalf("call did not exercise intended sensitive result: %s", body)
				}
			}
			if err := cs.Close(); err != nil {
				t.Fatal(err)
			}
			if err := ss.Close(); err != nil {
				t.Fatal(err)
			}
			if log.Degraded() {
				t.Fatal("telemetry persistence degraded")
			}
			if err := log.Close(); err != nil {
				t.Fatal(err)
			}
			persisted := jsonl.String()
			if sink == "sqlite" {
				db, err := sql.Open("sqlite3", dbPath)
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				rows, err := db.Query("SELECT body_json FROM audit_events ORDER BY id")
				if err != nil {
					t.Fatal(err)
				}
				defer rows.Close()
				for rows.Next() {
					var body string
					if err := rows.Scan(&body); err != nil {
						t.Fatal(err)
					}
					persisted += body + "\n"
				}
				if err := rows.Err(); err != nil {
					t.Fatal(err)
				}
			}
			for _, private := range []string{"private-", "7654321987", exportRoot, "2025-01-02"} {
				if strings.Contains(persisted, private) {
					t.Fatalf("%s leaked financial data %q: %s", sink, private, persisted)
				}
			}
			index := 0
			for _, line := range strings.Split(strings.TrimSpace(persisted), "\n") {
				var record struct {
					Event   string `json:"event"`
					Tool    string `json:"tool"`
					Summary struct {
						Arguments map[string]any `json:"arguments"`
						Result    map[string]any `json:"result"`
					} `json:"summary"`
				}
				if err := json.Unmarshal([]byte(line), &record); err != nil {
					t.Fatal(err)
				}
				if record.Event != "tool.call" {
					continue
				}
				if index >= len(calls) {
					t.Fatal("unexpected additional tool call")
				}
				want := calls[index]
				if record.Tool != want.name || record.Summary.Arguments["request_count"] != want.count || record.Summary.Result["item_count"] != want.count || record.Summary.Result["item_error_count"] != want.failures {
					t.Fatalf("missing or incorrect operational counters: %s", line)
				}
				index++
			}
			if index != len(calls) {
				t.Fatalf("persisted %d calls, want %d", index, len(calls))
			}
		})
	}
}
