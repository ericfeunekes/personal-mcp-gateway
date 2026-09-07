package ynab

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testTools(t *testing.T, h http.Handler) *Tools {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	tools := newTools(Options{Token: "secret", ExportRoot: t.TempDir()}, s.URL, &http.Client{})
	t.Cleanup(func() { _ = tools.Close() })
	return tools
}
func TestProviderRoutesAndRequiredDates(t *testing.T) {
	var gotPath, gotAuth string
	tools := testTools(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"transactions":[]}}`))
	}))
	_, out, _ := tools.list(context.Background(), nil, Input{Items: []Item{{Type: "transaction", PlanID: "p", AccountID: "a", SinceDate: "2026-01-01", UntilDate: "2026-01-31"}}})
	if out.Results[0].Status != "success" || gotPath != "/plans/p/accounts/a/transactions?since_date=2026-01-01&until_date=2026-01-31" || gotAuth != "Bearer secret" {
		t.Fatalf("out=%+v path=%q auth=%q", out, gotPath, gotAuth)
	}
	_, out, _ = tools.list(context.Background(), nil, Input{Items: []Item{{Type: "transaction", PlanID: "p"}}})
	if out.Results[0].Status != "error" || !strings.Contains(out.Results[0].Error, "since_date") {
		t.Fatalf("missing dates: %+v", out)
	}
}
func TestMutationStopsAfterProviderFailure(t *testing.T) {
	calls := 0
	tools := testTools(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(http.StatusBadRequest) }))
	_, out, _ := tools.create(context.Background(), nil, Input{Items: []Item{{Type: "payee", PlanID: "p", Data: json.RawMessage(`{"payee":{"name":"x"}}`)}, {Type: "payee", PlanID: "p", Data: json.RawMessage(`{"payee":{"name":"y"}}`)}}})
	if calls != 1 || out.Results[1].Status != "not_attempted" {
		t.Fatalf("calls=%d out=%+v", calls, out)
	}
}
func TestExportConfinementAndCSV(t *testing.T) {
	tools := testTools(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"transactions":[{"id":"x","account_id":"a","date":"2026-01-01","amount":1000,"payee_name":"=literal","subtransactions":[]}]}}`))
	}))
	_, out, _ := tools.create(context.Background(), nil, Input{Items: []Item{{Type: "export", PlanID: "p", Source: "transactions", Format: "csv", Filename: "x.csv", SinceDate: "2026-01-01", UntilDate: "2026-01-31"}}})
	if out.Results[0].Artifact == nil {
		t.Fatalf("out=%+v", out)
	}
	b, err := os.ReadFile(filepath.Join(tools.exportRoot, "x.csv"))
	if err != nil || !strings.Contains(string(b), "=literal") {
		t.Fatalf("export %q %v", b, err)
	}
	_, out, _ = tools.create(context.Background(), nil, Input{Items: []Item{{Type: "export", PlanID: "p", Source: "plan", Format: "json", Filename: "../bad.json"}}})
	if out.Results[0].Status != "error" {
		t.Fatalf("traversal %+v", out)
	}
}
