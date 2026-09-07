package ynab

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestIgnoredSplitFieldsAreNotSuccessful(t *testing.T) {
	calls := 0
	tools := testTools(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"data":{"transaction":{"id":"t1","amount":100,"date":"2026-01-01","category_id":null,"subtransactions":[{"id":"s1","amount":100,"category_id":"c1"}]}}}`))
	}))
	_, out, _ := tools.execute(context.Background(), ToolUpdate, Input{Items: []Item{
		{Type: "transaction", PlanID: "p", TransactionID: "t1", Data: json.RawMessage(`{"amount":200}`)},
		{Type: "payee", PlanID: "p", PayeeID: "payee", Data: json.RawMessage(`{"name":"later"}`)},
	}})
	if calls != 1 || out.Results[0].Status != "uncertain" || out.Results[1].Status != "not_attempted" {
		t.Fatalf("ignored update reported applied %+v calls=%d", out, calls)
	}
}

func TestAppliedTransactionFieldsUseExistingResponseOnly(t *testing.T) {
	calls := 0
	tools := testTools(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"data":{"transaction":{"id":"t1","amount":200,"date":"2026-01-01","category_id":"c1"}}}`))
	}))
	_, out, _ := tools.execute(context.Background(), ToolUpdate, Input{Items: []Item{{Type: "transaction", PlanID: "p", TransactionID: "t1", Data: json.RawMessage(`{"amount":200,"category_id":"c1"}`)}}})
	if calls != 1 || out.Results[0].Status != "success" {
		t.Fatalf("applied update not confirmed %+v calls=%d", out, calls)
	}
}

func TestSplitComparisonIgnoresProviderAddedIDs(t *testing.T) {
	if !requestedJSONMatches([]byte(`[{"amount":2,"memo":null},{"amount":1}]`), []byte(`[{"id":"one","amount":1},{"id":"two","amount":2,"memo":null}]`)) {
		t.Fatal("provider identities/order changed requested values")
	}
}
