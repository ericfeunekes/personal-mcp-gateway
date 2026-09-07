package ynab

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"personal-mcp-gateway/internal/audit"
	localmcp "personal-mcp-gateway/internal/mcp"
)

func TestYNABResponseBudgetOnEmittedHTTPAndStdioFrames(t *testing.T) {
	providerBody := func(n int) json.RawMessage {
		return json.RawMessage(`{"data":{"transactions":[{"id":"transaction-wire-fixture","date":"2025-01-02","memo":"` + strings.Repeat("x", n) + `"}],"server_knowledge":918273}}`)
	}
	// Locate the exact adapter cutoff, then test both adjacent fixtures through
	// SDK framing. The independent assertions below protect the public wire cap.
	low, high := 0, 192<<10
	for low+1 < high {
		mid := (low + high) / 2
		result, _, err := finishOutput(Output{Results: []Result{projectProviderResult(0, providerBody(mid))}}, false)
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError {
			high = mid
		} else {
			low = mid
		}
	}
	for _, transport := range []string{"http", "stdio"} {
		t.Run(transport, func(t *testing.T) {
			for _, size := range []int{low, high} {
				t.Run(fmt.Sprint(size), func(t *testing.T) {
					tools := testTools(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.URL.Path != "/plans/p/transactions" || r.URL.Query().Get("since_date") != "2025-01-01" {
							t.Errorf("unexpected provider request: %s", r.URL)
						}
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write(providerBody(size))
					}))
					descriptors, err := tools.Descriptors()
					if err != nil {
						t.Fatal(err)
					}
					server, _, err := localmcp.NewNamedServer(sdk.Implementation{Name: "ynab", Version: "test"}, audit.Disabled(), transport, descriptors)
					if err != nil {
						t.Fatal(err)
					}
					request := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list","arguments":{"items":[{"type":"transaction","plan_id":"p","since_date":"2025-01-01","until_date":"2025-01-31"}]},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"response-wire-test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`
					var frame []byte
					if transport == "http" {
						req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(request))
						req.Header.Set("Content-Type", "application/json")
						req.Header.Set("Accept", "application/json, text/event-stream")
						req.Header.Set("MCP-Protocol-Version", "2026-07-28")
						req.Header.Set("Mcp-Method", "tools/call")
						req.Header.Set("Mcp-Name", "list")
						response := httptest.NewRecorder()
						localmcp.StreamableHTTPHandler(server).ServeHTTP(response, req)
						if response.Code != http.StatusOK {
							t.Fatalf("HTTP status %d: %s", response.Code, response.Body.String())
						}
						frame = response.Body.Bytes()
					} else {
						sr, cw := io.Pipe()
						cr, sw := io.Pipe()
						ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
						defer cancel()
						defer sr.Close()
						defer cw.Close()
						defer cr.Close()
						defer sw.Close()
						done := make(chan error, 1)
						go func() { done <- server.Run(ctx, &sdk.IOTransport{Reader: sr, Writer: sw}) }()
						if _, err := fmt.Fprintln(cw, request); err != nil {
							t.Fatal(err)
						}
						frame, err = bufio.NewReader(cr).ReadBytes('\n')
						if err != nil {
							t.Fatal(err)
						}
						_ = cw.Close()
						_ = cr.Close()
						select {
						case err := <-done:
							if err != nil {
								t.Fatal(err)
							}
						case <-ctx.Done():
							t.Fatal("stdio server did not stop")
						}
					}
					if len(frame) > 192<<10 {
						t.Fatalf("emitted frame exceeds public 192 KiB limit: %d", len(frame))
					}
					var wire struct {
						JSONRPC string          `json:"jsonrpc"`
						ID      int             `json:"id"`
						Error   json.RawMessage `json:"error"`
						Result  struct {
							IsError    bool `json:"isError"`
							Structured struct {
								Results []map[string]json.RawMessage `json:"results"`
							} `json:"structuredContent"`
						} `json:"result"`
					}
					if err := json.Unmarshal(frame, &wire); err != nil {
						t.Fatalf("invalid emitted JSON-RPC frame: %v", err)
					}
					if wire.JSONRPC != "2.0" || wire.ID != 1 || len(wire.Error) != 0 || len(wire.Result.Structured.Results) != 1 {
						t.Fatalf("unexpected response envelope: %.500s", frame)
					}
					row := wire.Result.Structured.Results[0]
					if size == low {
						if wire.Result.IsError || len(frame) < 180<<10 || string(row["server_knowledge"]) != "918273" || !strings.Contains(string(row["transactions"]), strings.Repeat("x", size)) {
							t.Fatalf("near-limit data did not survive actual transport: %d bytes", len(frame))
						}
					} else {
						if !wire.Result.IsError || string(row["status"]) != `"error"` || len(frame) > 4096 {
							t.Fatalf("oversized result not compact error: %.500s", frame)
						}
						for _, forbidden := range []string{"transactions", "server_knowledge", "transaction-wire-fixture", "918273", "cursor"} {
							if strings.Contains(string(frame), forbidden) {
								t.Fatalf("partial data or checkpoint leaked in size error: %s", forbidden)
							}
						}
					}
					t.Logf("emitted complete %s JSON-RPC frame: %d bytes, size error: %v", transport, len(frame), wire.Result.IsError)
				})
			}
		})
	}
}
