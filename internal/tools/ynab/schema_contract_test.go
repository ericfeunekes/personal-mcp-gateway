package ynab

import (
	"encoding/json"
	"testing"
)

func TestOnlyCreateAdvertisesExportArtifact(t *testing.T) {
	for _, verb := range []string{ToolList, ToolGet, ToolCreate, ToolUpdate, ToolDelete, ToolImport} {
		schema, err := outputSchemaForVerb(verb)
		if err != nil {
			t.Fatal(err)
		}
		_, present := schema.Properties["results"].Items.Properties["artifact"]
		if present != (verb == ToolCreate) {
			t.Fatalf("%s advertises impossible export results: %v", verb, present)
		}
	}
}

func TestOutputSchemasRetainRealProviderFields(t *testing.T) {
	samples := []string{
		`{"data":{"accounts":[{"id":"a","name":"Main","type":"checking","on_budget":true,"closed":false,"balance":100000,"cleared_balance":99000,"uncleared_balance":1000,"transfer_payee_id":null,"deleted":false,"note":null,"direct_import_linked":true,"direct_import_in_error":false,"last_reconciled_at":null}]}}`,
		`{"data":{"transactions":[{"id":"t","account_id":"a","account_name":"Main","date":"2026-09-01","amount":-1000,"memo":null,"cleared":"cleared","approved":true,"deleted":false,"payee_id":null,"payee_name":null,"category_id":null,"category_name":null,"transfer_account_id":null,"transfer_transaction_id":null,"matched_transaction_id":null,"import_id":null,"flag_color":null,"flag_name":null,"subtransactions":[]}]}}`,
	}
	s, err := outputSchemaForVerb(ToolList)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := s.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, sample := range samples {
		out := Output{Results: []Result{projectProviderResult(0, json.RawMessage(sample))}}
		b, err := json.Marshal(out)
		if err != nil {
			t.Fatal(err)
		}
		var decoded any
		if err = json.Unmarshal(b, &decoded); err != nil {
			t.Fatal(err)
		}
		if err = resolved.Validate(decoded); err != nil {
			t.Fatalf("real resource fields rejected: %v", err)
		}
	}
}

func TestInputSchemasPreserveProviderEnumsAndFields(t *testing.T) {
	for _, c := range []struct {
		verb, body string
		valid      bool
	}{
		{ToolCreate, `{"items":[{"type":"category","plan_id":"p","data":{"name":"Home","category_group_id":"g","note":null,"goal_target":500000,"goal_frequency":"yearly"}}]}`, true},
		{ToolUpdate, `{"items":[{"type":"transaction","plan_id":"p","transaction_id":"t","data":{"flag_color":null}}]}`, true},
		{ToolUpdate, `{"items":[{"type":"transaction","plan_id":"p","transaction_id":"t","data":{"flag_color":""}}]}`, true},
		{ToolUpdate, `{"items":[{"type":"transaction","plan_id":"p","transaction_id":"t","data":{"flag_color":"none"}}]}`, false},
		{ToolUpdate, `{"items":[{"type":"scheduled_transaction","plan_id":"p","scheduled_transaction_id":"s","data":{"memo":"x"}}]}`, false},
		{ToolList, `{"items":[{"type":"account","plan_id":"p","last_knowledge_of_server":12}]}`, true},
		{ToolImport, `{"items":[{"type":"bank_import","plan_id":"p"}]}`, true},
	} {
		schema, err := inputSchemaForVerb(c.verb)
		if err != nil {
			t.Fatal(err)
		}
		resolved, err := schema.Resolve(nil)
		if err != nil {
			t.Fatal(err)
		}
		var data any
		if err = json.Unmarshal([]byte(c.body), &data); err != nil {
			t.Fatal(err)
		}
		if err = resolved.Validate(data); (err == nil) != c.valid {
			t.Fatalf("%s valid=%v: %v", c.verb, c.valid, err)
		}
	}
}
