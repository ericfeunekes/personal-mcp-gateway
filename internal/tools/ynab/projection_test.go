package ynab

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProjectionPreservesEmptyCollectionsAndIntegerPrecision(t *testing.T) {
	r := projectProviderResult(0, json.RawMessage(`{"data":{"accounts":[],"transactions":[{"id":"t","amount":9007199254740993,"memo":null,"account_id":"a"}],"server_knowledge":17}}`))
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"accounts":[]`) || !strings.Contains(string(b), `9007199254740993`) || strings.Contains(string(b), `"data":`) {
		t.Fatalf("wrong projection %s", b)
	}
}

func TestProjectionRetainsNestedRelationshipsWithoutPlanDump(t *testing.T) {
	r := projectProviderResult(2, json.RawMessage(`{"data":{"month":{"month":"2026-09-01","categories":[{"id":"c","balance":2}]},"transactions":[{"id":"t","amount":-3,"subtransactions":[{"id":"s","amount":-3}]}]}}`))
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"transaction_id":"t"`) || !strings.Contains(string(b), `"categories":[{"balance":2,"id":"c","month":"2026-09-01"}]`) {
		t.Fatalf("relationships lost %s", b)
	}
	raw := json.RawMessage(`{"data":{"plan":{"id":"p","name":"Plan","accounts":[{"id":"a"}],"transactions":[{"memo":"private-export-content"}]},"server_knowledge":8}}`)
	r = projectProviderResult(0, raw)
	b, _ = json.Marshal(r)
	if strings.Contains(string(b), "private-export-content") || !strings.Contains(string(b), `"plans":[{"id":"p","name":"Plan"}]`) || string(r.Data) != string(raw) {
		t.Fatalf("plan export leaked or raw lost %s", b)
	}
}

func TestProjectionWriteAcknowledgementKeepsProviderIDs(t *testing.T) {
	r := projectProviderResult(0, json.RawMessage(`{"data":{"transactions":[{"id":"t1","memo":"large-source"}],"transaction_ids":["t1"],"duplicate_import_ids":["i2"],"server_knowledge":9}}`)).acknowledge()
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), "large-source") || !strings.Contains(string(b), `"ids":["t1"]`) || !strings.Contains(string(b), `"duplicate_import_ids":["i2"]`) {
		t.Fatalf("wrong ack %s", b)
	}
}
