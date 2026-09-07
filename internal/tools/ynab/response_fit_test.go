package ynab

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOversizedReadHasNoDataOrChangeCheckpoint(t *testing.T) {
	raw := json.RawMessage(`{"data":{"transactions":[{"id":"t","memo":"` + strings.Repeat("x", maxResultBytes) + `"}],"server_knowledge":91}}`)
	r := projectProviderResult(0, raw)
	result, out, err := finishOutput(Output{Results: []Result{r}}, false)
	if err != nil || !result.IsError || out.Results[0].ServerKnowledge != nil || out.Results[0].Status != "error" {
		t.Fatalf("wrong result %+v %v", out, err)
	}
	b, _ := json.Marshal(result)
	if len(b) >= maxResultBytes || strings.Contains(string(b), `"memo"`) {
		t.Fatalf("oversized content leaked: %d", len(b))
	}
}

func TestBatchResponseBudgetAccountsForEncoding(t *testing.T) {
	rows := make([]Result, 10)
	for i := range rows {
		rows[i] = projectProviderResult(i, json.RawMessage(`{"data":{"transactions":[{"id":"t","memo":"`+strings.Repeat("<", 5000)+`"}]}}`))
	}
	result, out, err := finishOutput(Output{Results: rows}, false)
	if err != nil || !result.IsError {
		t.Fatal("encoded aggregate not limited")
	}
	for _, r := range out.Results {
		if r.Status != "error" || r.ServerKnowledge != nil {
			t.Fatal("partial success claimed")
		}
	}
}
