package ynab

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
)

// TestCoverageAllProviderOperations is the pinned 44-operation inventory from
// docs/requirements/ynab-tools.md.  It deliberately exercises execute through
// HTTP rather than route alone: schemas, local validation, request wrapping,
// query encoding, response projection, and output schemas all meet here.
func TestCoverageAllProviderOperations(t *testing.T) {
	const plan = "plan-1"
	date := "2026-09-01"
	scheduledDate := time.Now().UTC().AddDate(0, 1, 0).Format("2006-01-02")
	lastKnowledge := int64(7)
	cases := []coverageCase{
		{"list plans", ToolList, Item{Type: "plan", IncludeAccounts: true}, "GET", "/plans", "include_accounts=true", "", "plans", false},
		{"list accounts", ToolList, Item{Type: "account", PlanID: plan, LastKnowledge: &lastKnowledge}, "GET", "/plans/plan-1/accounts", "last_knowledge_of_server=7", "", "accounts", false},
		{"list categories", ToolList, Item{Type: "category", PlanID: plan}, "GET", "/plans/plan-1/categories", "", "", "category_groups", false},
		{"list payees", ToolList, Item{Type: "payee", PlanID: plan}, "GET", "/plans/plan-1/payees", "", "", "payees", false},
		{"list payee locations", ToolList, Item{Type: "payee_location", PlanID: plan}, "GET", "/plans/plan-1/payee_locations", "", "", "payee_locations", false},
		{"list payee locations by payee", ToolList, Item{Type: "payee_location", PlanID: plan, PayeeID: "payee-1"}, "GET", "/plans/plan-1/payees/payee-1/payee_locations", "", "", "payee_locations", false},
		{"list months", ToolList, Item{Type: "month", PlanID: plan}, "GET", "/plans/plan-1/months", "", "", "months", false},
		{"list money movements", ToolList, Item{Type: "money_movement", PlanID: plan}, "GET", "/plans/plan-1/money_movements", "", "", "money_movements", false},
		{"list month money movements", ToolList, Item{Type: "money_movement", PlanID: plan, Month: date}, "GET", "/plans/plan-1/months/2026-09-01/money_movements", "", "", "money_movements", false},
		{"list money movement groups", ToolList, Item{Type: "money_movement_group", PlanID: plan}, "GET", "/plans/plan-1/money_movement_groups", "", "", "money_movement_groups", false},
		{"list month money movement groups", ToolList, Item{Type: "money_movement_group", PlanID: plan, Month: date}, "GET", "/plans/plan-1/months/2026-09-01/money_movement_groups", "", "", "money_movement_groups", false},
		{"list transactions", ToolList, transactionList(plan), "GET", "/plans/plan-1/transactions", "since_date=2026-09-01&until_date=2026-09-30", "", "transactions", false},
		{"list account transactions", ToolList, transactionList(plan, "AccountID", "account-1"), "GET", "/plans/plan-1/accounts/account-1/transactions", "since_date=2026-09-01&until_date=2026-09-30", "", "transactions", false},
		{"list category transactions", ToolList, transactionList(plan, "CategoryID", "category-1"), "GET", "/plans/plan-1/categories/category-1/transactions", "since_date=2026-09-01&until_date=2026-09-30", "", "transactions", false},
		{"list payee transactions", ToolList, transactionList(plan, "PayeeID", "payee-1"), "GET", "/plans/plan-1/payees/payee-1/transactions", "since_date=2026-09-01&until_date=2026-09-30", "", "transactions", false},
		{"list month transactions", ToolList, Item{Type: "transaction", PlanID: plan, Month: date, SinceDate: "2026-09-01", UntilDate: "2026-09-30"}, "GET", "/plans/plan-1/months/2026-09-01/transactions", "since_date=2026-09-01&until_date=2026-09-30", "", "transactions", false},
		{"list scheduled transactions", ToolList, Item{Type: "scheduled_transaction", PlanID: plan}, "GET", "/plans/plan-1/scheduled_transactions", "", "", "scheduled_transactions", false},

		{"get user", ToolGet, Item{Type: "user"}, "GET", "/user", "", "", "user", false},
		{"get plan metadata", ToolGet, Item{Type: "plan", PlanID: plan}, "GET", "/plans/plan-1", "", "", "plan", false},
		{"get settings", ToolGet, Item{Type: "settings", PlanID: plan}, "GET", "/plans/plan-1/settings", "", "", "settings", false},
		{"get account", ToolGet, Item{Type: "account", PlanID: plan, AccountID: "account-1"}, "GET", "/plans/plan-1/accounts/account-1", "", "", "account", false},
		{"get category", ToolGet, Item{Type: "category", PlanID: plan, CategoryID: "category-1"}, "GET", "/plans/plan-1/categories/category-1", "", "", "category", false},
		{"get month category", ToolGet, Item{Type: "category", PlanID: plan, CategoryID: "category-1", Month: date}, "GET", "/plans/plan-1/months/2026-09-01/categories/category-1", "", "", "category", false},
		{"get payee", ToolGet, Item{Type: "payee", PlanID: plan, PayeeID: "payee-1"}, "GET", "/plans/plan-1/payees/payee-1", "", "", "payee", false},
		{"get payee location", ToolGet, Item{Type: "payee_location", PlanID: plan, PayeeLocationID: "location-1"}, "GET", "/plans/plan-1/payee_locations/location-1", "", "", "payee_location", false},
		{"get month", ToolGet, Item{Type: "month", PlanID: plan, Month: date}, "GET", "/plans/plan-1/months/2026-09-01", "", "", "month", false},
		{"get transaction", ToolGet, Item{Type: "transaction", PlanID: plan, TransactionID: "transaction-1"}, "GET", "/plans/plan-1/transactions/transaction-1", "", "", "transaction", false},
		{"get scheduled transaction", ToolGet, Item{Type: "scheduled_transaction", PlanID: plan, ScheduledTransactionID: "scheduled-1"}, "GET", "/plans/plan-1/scheduled_transactions/scheduled-1", "", "", "scheduled_transaction", false},

		{"create account", ToolCreate, Item{Type: "account", PlanID: plan, Data: raw(`{"name":"Cash","type":"cash","balance":1000}`)}, "POST", "/plans/plan-1/accounts", "", "account", "account", false},
		{"create category", ToolCreate, Item{Type: "category", PlanID: plan, Data: raw(`{"name":"Food","category_group_id":"group-1"}`)}, "POST", "/plans/plan-1/categories", "", "category", "category", false},
		{"create category group", ToolCreate, Item{Type: "category_group", PlanID: plan, Data: raw(`{"name":"Home"}`)}, "POST", "/plans/plan-1/category_groups", "", "category_group", "category_group", false},
		{"create payee", ToolCreate, Item{Type: "payee", PlanID: plan, Data: raw(`{"name":"Store"}`)}, "POST", "/plans/plan-1/payees", "", "payee", "payee", false},
		{"create transactions native batch", ToolCreate, Item{Type: "transaction", PlanID: plan, Data: raw(`{"account_id":"account-1","date":"2026-09-01","amount":-1000,"import_id":"import-1"}`)}, "POST", "/plans/plan-1/transactions", "", "transactions", "transaction", true},
		{"create scheduled transaction", ToolCreate, Item{Type: "scheduled_transaction", PlanID: plan, Data: raw(`{"account_id":"account-1","date":"` + scheduledDate + `","amount":-1000,"frequency":"monthly"}`)}, "POST", "/plans/plan-1/scheduled_transactions", "", "scheduled_transaction", "scheduled_transaction", false},

		{"update category", ToolUpdate, Item{Type: "category", PlanID: plan, CategoryID: "category-1", Data: raw(`{"name":"Food"}`)}, "PATCH", "/plans/plan-1/categories/category-1", "", "category", "category", false},
		{"update month category", ToolUpdate, Item{Type: "category", PlanID: plan, CategoryID: "category-1", Month: date, Data: raw(`{"budgeted":1000}`)}, "PATCH", "/plans/plan-1/months/2026-09-01/categories/category-1", "", "category", "category", false},
		{"update category group", ToolUpdate, Item{Type: "category_group", PlanID: plan, CategoryGroupID: "group-1", Data: raw(`{"name":"Home"}`)}, "PATCH", "/plans/plan-1/category_groups/group-1", "", "category_group", "category_group", false},
		{"update payee", ToolUpdate, Item{Type: "payee", PlanID: plan, PayeeID: "payee-1", Data: raw(`{"name":"Store"}`)}, "PATCH", "/plans/plan-1/payees/payee-1", "", "payee", "payee", false},
		{"update transaction", ToolUpdate, Item{Type: "transaction", PlanID: plan, TransactionID: "transaction-1", Data: raw(`{"memo":"updated"}`)}, "PUT", "/plans/plan-1/transactions/transaction-1", "", "transaction", "transaction", false},
		{"update transactions native batch", ToolUpdate, Item{Type: "transaction", PlanID: plan, Data: raw(`{"id":"transaction-1","memo":"updated"}`)}, "PATCH", "/plans/plan-1/transactions", "", "transactions", "transaction", true},
		{"update scheduled transaction", ToolUpdate, Item{Type: "scheduled_transaction", PlanID: plan, ScheduledTransactionID: "scheduled-1", Data: raw(`{"account_id":"account-1","date":"` + scheduledDate + `","memo":"updated"}`)}, "PUT", "/plans/plan-1/scheduled_transactions/scheduled-1", "", "scheduled_transaction", "scheduled_transaction", false},

		{"delete transaction", ToolDelete, Item{Type: "transaction", PlanID: plan, TransactionID: "transaction-1"}, "DELETE", "/plans/plan-1/transactions/transaction-1", "", "", "", false},
		{"delete scheduled transaction", ToolDelete, Item{Type: "scheduled_transaction", PlanID: plan, ScheduledTransactionID: "scheduled-1"}, "DELETE", "/plans/plan-1/scheduled_transactions/scheduled-1", "", "", "", false},
		{"import linked transactions", ToolImport, Item{Type: "bank_import", PlanID: plan}, "POST", "/plans/plan-1/transactions/import", "", "", "", false},
	}
	if len(cases) != 44 {
		t.Fatalf("coverage inventory has %d operations, want 44", len(cases))
	}
	assertPinnedOperationInventory(t, cases)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCoverageCase(t, c) })
	}

	// Export is a client-side create operation over the already-covered full-plan
	// GET route; it is intentionally not a 45th provider operation.
	t.Run("create full plan export", func(t *testing.T) {
		var gotPath string
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			_, _ = io.WriteString(w, `{"data":{"plan":{"id":"plan-1","name":"Plan","accounts":[]}}}`)
		}))
		defer s.Close()
		tools := newTools(Options{Token: "synthetic-token", ExportRoot: t.TempDir()}, s.URL, &http.Client{})
		item := Item{Type: "export", PlanID: plan, Source: "plan", Format: "json", Filename: "plan.json"}
		validateSchemas(t, ToolCreate, Input{Items: []Item{item}})
		_, out, err := tools.execute(context.Background(), ToolCreate, Input{Items: []Item{item}})
		if err != nil || gotPath != "/plans/plan-1" || out.Results[0].Artifact == nil {
			t.Fatalf("err=%v path=%q result=%+v", err, gotPath, out.Results)
		}
		if _, err := os.Stat(filepath.Join(tools.exportRoot, "plan.json")); err != nil {
			t.Fatal(err)
		}
		validateOutput(t, ToolCreate, out)
	})
}

func assertPinnedOperationInventory(t *testing.T, cases []coverageCase) {
	t.Helper()
	data, err := os.ReadFile("testdata/openapi-v1.86.0.json")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err = json.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	for path, methods := range spec.Paths {
		for method := range methods {
			switch method {
			case "get", "post", "put", "patch", "delete":
				want[strings.ToUpper(method)+" "+path] = true
			}
		}
	}
	replacer := strings.NewReplacer("plan-1", "{plan_id}", "account-1", "{account_id}", "category-1", "{category_id}", "group-1", "{category_group_id}", "payee-1", "{payee_id}", "location-1", "{payee_location_id}", "scheduled-1", "{scheduled_transaction_id}", "transaction-1", "{transaction_id}", "2026-09-01", "{month}")
	seen := map[string]bool{}
	for _, c := range cases {
		key := c.method + " " + replacer.Replace(c.path)
		if !want[key] || seen[key] {
			t.Fatalf("duplicate or non-provider operation %s", key)
		}
		seen[key] = true
	}
	for key := range want {
		if !seen[key] {
			t.Errorf("missing provider operation %s", key)
		}
	}
}

type coverageCase struct {
	name, verb                                 string
	item                                       Item
	method, path, query, wrapper, responseKind string
	native                                     bool
}

func runCoverageCase(t *testing.T, c coverageCase) {
	t.Helper()
	items := []Item{c.item}
	if c.native {
		second := c.item
		if c.verb == ToolCreate {
			second.Data = raw(`{"account_id":"account-1","date":"2026-09-02","amount":-2000,"import_id":"import-2"}`)
		} else {
			second.Data = raw(`{"id":"transaction-2","memo":"updated again"}`)
		}
		items = append(items, second)
	}
	in := Input{Items: items}
	validateSchemas(t, c.verb, in)
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != c.method || r.URL.Path != c.path || r.URL.RawQuery != c.query {
			t.Errorf("got %s %s?%s, want %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery, c.method, c.path, c.query)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer synthetic-token" {
			t.Errorf("authorization=%q", got)
		}
		body, _ := io.ReadAll(r.Body)
		if c.wrapper == "" && len(body) != 0 {
			t.Errorf("unexpected request body %s", body)
		}
		if c.wrapper != "" {
			var wrapped map[string]json.RawMessage
			if json.Unmarshal(body, &wrapped) != nil || len(wrapped) != 1 || len(wrapped[c.wrapper]) == 0 {
				t.Errorf("body %s does not have only %q wrapper", body, c.wrapper)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, coverageResponse(c))
	}))
	defer s.Close()
	tools := newTools(Options{Token: "synthetic-token", ExportRoot: t.TempDir()}, s.URL, &http.Client{})
	_, out, err := tools.execute(context.Background(), c.verb, in)
	if err != nil || calls != 1 {
		t.Fatalf("err=%v calls=%d results=%+v", err, calls, out.Results)
	}
	if len(out.Results) != len(items) {
		t.Fatalf("results=%+v", out.Results)
	}
	for _, result := range out.Results {
		if result.Status != "success" {
			t.Fatalf("result=%+v", result)
		}
	}
	validateOutput(t, c.verb, out)
	if c.verb == ToolList {
		encoded, _ := json.Marshal(out.Results[0])
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(encoded, &fields)
		if string(fields[c.responseKind]) != "[]" {
			t.Fatalf("missing provider collection %s in %s", c.responseKind, encoded)
		}
	}
}

func transactionList(plan string, field ...string) Item {
	x := Item{Type: "transaction", PlanID: plan, SinceDate: "2026-09-01", UntilDate: "2026-09-30"}
	if len(field) == 2 {
		reflect.ValueOf(&x).Elem().FieldByName(field[0]).SetString(field[1])
	}
	return x
}

func raw(value string) json.RawMessage { return json.RawMessage(value) }

func validateSchemas(t *testing.T, verb string, in Input) {
	t.Helper()
	schema, err := inputSchemaForVerb(verb)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var value any
	if err = json.Unmarshal(b, &value); err != nil {
		t.Fatal(err)
	}
	if err = resolved.Validate(value); err != nil {
		t.Fatalf("input schema rejected %s: %v", b, err)
	}
}

func validateOutput(t *testing.T, verb string, out Output) {
	t.Helper()
	schema, err := outputSchemaForVerb(verb)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var value any
	if err = json.Unmarshal(b, &value); err != nil {
		t.Fatal(err)
	}
	if err = resolved.Validate(value); err != nil {
		t.Fatalf("output schema rejected %s: %v", b, err)
	}
}

func coverageResponse(c coverageCase) string {
	if c.verb == ToolImport {
		return `{"data":{"transaction_ids":["transaction-1"]}}`
	}
	if c.native {
		if c.verb == ToolCreate {
			return `{"data":{"transactions":[{"id":"transaction-1","import_id":"import-1"},{"id":"transaction-2","import_id":"import-2"}]}}`
		}
		return `{"data":{"transactions":[{"id":"transaction-1"},{"id":"transaction-2"}]}}`
	}
	if c.responseKind == "" {
		if c.verb == ToolDelete {
			return `{"data":{"` + c.item.Type + `":` + string(coverageRecord(c.item.Type)) + `}}`
		}
		return `{"data":{}}`
	}
	if c.verb == ToolList {
		return `{"data":{"` + c.responseKind + `":[],"server_knowledge":7}}`
	}
	return `{"data":{"` + c.responseKind + `":` + string(coverageRecord(c.responseKind)) + `,"server_knowledge":7}}`
}

// coverageRecord is generated from the pinned provider-schema artifact, not
// production projection code. It supplies every required singleton field while
// lists intentionally use empty arrays.
func coverageRecord(kind string) json.RawMessage {
	s := providerSchema("output", kind)
	b, _ := json.Marshal(coverageValue(s))
	return b
}

func coverageValue(s *jsonschema.Schema) any {
	if len(s.Enum) > 0 {
		return s.Enum[0]
	}
	for _, typ := range append([]string{s.Type}, s.Types...) {
		if typ == "null" {
			return nil
		}
	}
	typ := s.Type
	if typ == "" && len(s.Types) > 0 {
		typ = s.Types[0]
	}
	switch typ {
	case "object":
		v := map[string]any{}
		for _, name := range s.Required {
			v[name] = coverageValue(s.Properties[name])
		}
		return v
	case "array":
		return []any{}
	case "integer":
		return 1
	case "number":
		return 1.0
	case "boolean":
		return true
	case "string":
		switch s.Format {
		case "uuid":
			return "00000000-0000-4000-8000-000000000001"
		case "date":
			return "2026-09-01"
		case "date-time":
			return "2026-09-01T00:00:00Z"
		}
		if strings.Contains(s.Pattern, "2000") || strings.Contains(s.Pattern, "[0-9]{4}") {
			return "2026-09-01"
		}
		return "synthetic"
	default:
		return nil
	}
}
