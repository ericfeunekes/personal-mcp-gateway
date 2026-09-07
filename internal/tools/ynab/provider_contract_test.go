package ynab

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestProviderContractWriteMethodsAndWrappers(t *testing.T) {
	scheduledDate := time.Now().UTC().AddDate(0, 1, 0).Format("2006-01-02")
	cases := []struct{ name, verb, kind, id, month, body, method, path, wrapper string }{
		{"account create", ToolCreate, "account", "", "", `{"name":"Cash","type":"cash","balance":12345}`, "POST", "/plans/p/accounts", "account"},
		{"category create", ToolCreate, "category", "", "", `{"name":"Food","category_group_id":"g"}`, "POST", "/plans/p/categories", "category"},
		{"category update", ToolUpdate, "category", "c", "", `{"goal_target":null}`, "PATCH", "/plans/p/categories/c", "category"},
		{"group create", ToolCreate, "category_group", "", "", `{"name":"Home"}`, "POST", "/plans/p/category_groups", "category_group"},
		{"group rename", ToolUpdate, "category_group", "g", "", `{"name":"Home"}`, "PATCH", "/plans/p/category_groups/g", "category_group"},
		{"payee create", ToolCreate, "payee", "", "", `{"name":"Shop"}`, "POST", "/plans/p/payees", "payee"},
		{"payee rename", ToolUpdate, "payee", "p2", "", `{"name":"Shop"}`, "PATCH", "/plans/p/payees/p2", "payee"},
		{"scheduled create", ToolCreate, "scheduled_transaction", "", "", `{"account_id":"a","date":"` + scheduledDate + `","amount":-12345}`, "POST", "/plans/p/scheduled_transactions", "scheduled_transaction"},
		{"scheduled update", ToolUpdate, "scheduled_transaction", "s", "", `{"account_id":"a","date":"` + scheduledDate + `","memo":null}`, "PUT", "/plans/p/scheduled_transactions/s", "scheduled_transaction"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			calls := 0
			tools := testTools(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != c.method || r.URL.Path != c.path {
					t.Errorf("got %s %s want %s %s", r.Method, r.URL.Path, c.method, c.path)
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				var wrapped map[string]json.RawMessage
				if json.Unmarshal(body, &wrapped) != nil || len(wrapped) != 1 || len(wrapped[c.wrapper]) == 0 {
					t.Errorf("wrong provider wrapper %s", body)
				}
				var actual, expected any
				_ = json.Unmarshal(wrapped[c.wrapper], &actual)
				_ = json.Unmarshal([]byte(c.body), &expected)
				a, _ := json.Marshal(actual)
				e, _ := json.Marshal(expected)
				if string(a) != string(e) {
					t.Errorf("provider fields changed: %s != %s", a, e)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"data":{"` + c.wrapper + `":{"id":"saved"},"server_knowledge":1}}`))
			}))
			_, out, _ := tools.execute(context.Background(), c.verb, Input{Items: []Item{{Type: c.kind, PlanID: "p", ID: c.id, Month: c.month, Data: json.RawMessage(c.body)}}})
			if calls != 1 || len(out.Results) != 1 || out.Results[0].Status != "success" {
				t.Fatalf("calls %d output %+v", calls, out)
			}
		})
	}
}

func TestInvalidLaterMutationPreventsAllProviderEffects(t *testing.T) {
	calls := 0
	tools := testTools(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; _, _ = w.Write([]byte(`{"data":{}}`)) }))
	_, out, _ := tools.execute(context.Background(), ToolCreate, Input{Items: []Item{
		{Type: "payee", PlanID: "p", Data: json.RawMessage(`{"name":"Shop"}`)},
		{Type: "export", PlanID: "p", Source: "transactions", Format: "csv", Filename: "bad.csv"},
		{Type: "payee", PlanID: "p", Data: json.RawMessage(`{"name":"later"}`)},
	}})
	if calls != 0 {
		t.Fatalf("dispatched %d requests before rejecting later invalid item", calls)
	}
	if len(out.Results) != 3 || out.Results[0].Status != "not_attempted" || out.Results[1].Status != "error" || out.Results[2].Status != "not_attempted" {
		t.Fatalf("incomplete validation results %+v", out)
	}
}

func TestMonthBoundsBothRequiredInsideMonth(t *testing.T) {
	for _, dates := range [][2]string{{"2026-08-31", "2026-09-02"}, {"2026-09-02", "2026-10-01"}} {
		if validateDates(Item{Month: "2026-09-01", SinceDate: dates[0], UntilDate: dates[1]}) == nil {
			t.Fatalf("accepted cross-month %v", dates)
		}
	}
	if err := validateDates(Item{Month: "2026-09-01", SinceDate: "2026-09-02", UntilDate: "2026-09-07"}); err != nil {
		t.Fatal(err)
	}
}

func TestProviderErrorDoesNotEchoPrivateResponse(t *testing.T) {
	tools := testTools(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":{"detail":"private-payee-name"}}`))
	}))
	_, out, _ := tools.execute(context.Background(), ToolList, Input{Items: []Item{{Type: "account", PlanID: "p"}}})
	b, _ := json.Marshal(out)
	if strings.Contains(string(b), "private-payee-name") {
		t.Fatal("provider detail leaked")
	}
}
