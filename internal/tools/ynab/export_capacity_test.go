package ynab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestRenderedCSVLimitStopsPublication(t *testing.T) {
	// Valid-sized source text can grow in a CSV JSON-details cell because HTML
	// characters are escaped in the nested JSON representation.
	var body strings.Builder
	body.WriteString(`{"data":{"transactions":[{"id":"t","amount":23000,"subtransactions":[`)
	for i := 0; i < 23000; i++ {
		if i > 0 {
			body.WriteByte(',')
		}
		fmt.Fprintf(&body, `{"id":"s%d","amount":1,"memo":"%s"}`, i, strings.Repeat("<", 500))
	}
	body.WriteString(`]}]}}`)
	if int64(body.Len()) >= maxProviderBytes {
		t.Fatal("fixture bypassed the provider ceiling")
	}
	tools := testTools(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body.String())) }))
	result := tools.export(context.Background(), 0, Item{PlanID: "p", Source: "transactions", Format: "csv", Filename: "large.csv", SinceDate: "2026-01-01", UntilDate: "2026-01-31"})
	if result.Status != "error" || result.Artifact != nil || !strings.Contains(result.Error, "64 MiB") {
		t.Fatalf("rendered limit not enforced %+v", result)
	}
	entries, err := os.ReadDir(tools.exportRoot)
	if err != nil || len(entries) != 0 {
		t.Fatalf("oversized artifact published %v %v", entries, err)
	}
}

func TestEmptyExportReportsKnownZero(t *testing.T) {
	tools := testTools(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"transactions":[],"server_knowledge":1}}`))
	}))
	result := tools.export(context.Background(), 0, Item{PlanID: "p", Source: "transactions", Format: "json", Filename: "empty.json", SinceDate: "2026-01-01", UntilDate: "2026-01-31"})
	b, _ := json.Marshal(result)
	if result.Status != "success" || !strings.Contains(string(b), `"record_count":0`) {
		t.Fatalf("known zero lost %s", b)
	}
}
