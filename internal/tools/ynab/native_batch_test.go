package ynab

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func TestNativeTransactionBatchUsesOneProviderRequest(t *testing.T) {
	calls := 0
	tools := testTools(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/plans/p/transactions" {
			t.Fatalf("%s %s", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var got struct {
			Transactions []json.RawMessage `json:"transactions"`
		}
		if json.Unmarshal(body, &got) != nil || len(got.Transactions) != 2 {
			t.Fatalf("body %s", body)
		}
		_, _ = w.Write([]byte(`{"data":{"transactions":[{"id":"a","import_id":"one"},{"id":"b","import_id":"two"}]}}`))
	}))
	_, out, _ := tools.execute(context.Background(), ToolCreate, Input{Items: []Item{{Type: "transaction", PlanID: "p", Data: json.RawMessage(`{"account_id":"a","date":"2026-01-01","amount":1,"import_id":"one"}`)}, {Type: "transaction", PlanID: "p", Data: json.RawMessage(`{"account_id":"a","date":"2026-01-02","amount":2,"import_id":"two"}`)}}})
	if calls != 1 || len(out.Results) != 2 || out.Results[0].Status != "success" || out.Results[0].IDs[0] != "a" || out.Results[1].IDs[0] != "b" {
		t.Fatalf("calls=%d out=%+v", calls, out)
	}
}

func TestNativeBatchStopsLaterMixedMutation(t *testing.T) {
	calls := 0
	tools := testTools(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(http.StatusBadRequest) }))
	_, out, _ := tools.execute(context.Background(), ToolCreate, Input{Items: []Item{{Type: "transaction", PlanID: "p", Data: json.RawMessage(`{"account_id":"a","date":"2026-01-01","amount":1,"import_id":"one"}`)}, {Type: "transaction", PlanID: "p", Data: json.RawMessage(`{"account_id":"a","date":"2026-01-02","amount":2,"import_id":"two"}`)}, {Type: "payee", PlanID: "p", Data: json.RawMessage(`{"name":"later"}`)}}})
	if calls != 1 || out.Results[0].Status != "error" || out.Results[1].Status != "error" || out.Results[2].Status != "not_attempted" {
		t.Fatalf("calls=%d out=%+v", calls, out)
	}
}

func TestNativeBatchDuplicateImportIsNotIndexMatched(t *testing.T) {
	calls := 0
	tools := testTools(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body struct {
			Transactions []json.RawMessage `json:"transactions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body.Transactions) != 2 {
			t.Error("native batch was not used")
		}
		_, _ = w.Write([]byte(`{"data":{"duplicate_import_ids":["imp"],"transaction_ids":["created"],"transactions":[{"id":"created","import_id":"new"}],"server_knowledge":1}}`))
	}))
	_, out, _ := tools.execute(context.Background(), ToolCreate, Input{Items: []Item{{Type: "transaction", PlanID: "p", Data: json.RawMessage(`{"account_id":"a","date":"2026-01-01","amount":1,"import_id":"imp"}`)}, {Type: "transaction", PlanID: "p", Data: json.RawMessage(`{"account_id":"a","date":"2026-01-02","amount":2,"import_id":"new"}`)}}})
	if calls != 1 || len(out.Results[0].DuplicateImportIDs) != 1 || len(out.Results[1].IDs) != 1 || out.Results[1].IDs[0] != "created" || len(out.Results[0].IDs) != 0 {
		t.Fatalf("out=%+v", out)
	}
}

func TestNativeUpdateCarriesOuterIDsAndCorrelatesReturnedIdentities(t *testing.T) {
	calls := 0
	tools := testTools(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "PATCH" || r.URL.Path != "/plans/p/transactions" {
			t.Errorf("wrong update endpoint %s %s", r.Method, r.URL.Path)
		}
		var body struct {
			Transactions []struct {
				ID   string  `json:"id"`
				Memo *string `json:"memo"`
			} `json:"transactions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body.Transactions) != 2 || body.Transactions[0].ID != "t1" || body.Transactions[1].ID != "t2" {
			t.Errorf("lost update IDs %+v", body)
		}
		_, _ = w.Write([]byte(`{"data":{"transaction_ids":["t2","t1"],"transactions":[{"id":"t2"},{"id":"t1"}]}}`))
	}))
	_, out, _ := tools.execute(context.Background(), ToolUpdate, Input{Items: []Item{
		{Type: "transaction", PlanID: "p", TransactionID: "t1", Data: json.RawMessage(`{"memo":null}`)},
		{Type: "transaction", PlanID: "p", TransactionID: "t2", Data: json.RawMessage(`{"memo":"updated"}`)},
	}})
	if calls != 1 || out.Results[0].IDs[0] != "t1" || out.Results[1].IDs[0] != "t2" {
		t.Fatalf("bad mapping %+v", out)
	}
}

func TestNativeUpdateMissingReturnedIDIsUncertain(t *testing.T) {
	tools := testTools(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"transaction_ids":["t1"]}}`))
	}))
	_, out, _ := tools.execute(context.Background(), ToolUpdate, Input{Items: []Item{
		{Type: "transaction", PlanID: "p", TransactionID: "t1", Data: json.RawMessage(`{"memo":null}`)},
		{Type: "transaction", PlanID: "p", TransactionID: "t2", Data: json.RawMessage(`{"memo":null}`)},
	}})
	if out.Results[0].Status != "success" || out.Results[1].Status != "uncertain" || len(out.Results[1].IDs) != 0 {
		t.Fatalf("unproven success %+v", out)
	}
}
