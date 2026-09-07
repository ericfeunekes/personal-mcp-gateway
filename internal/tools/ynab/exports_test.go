package ynab

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestExportPlanWritesExactJSONAndOnlyMetadata(t *testing.T) {
	raw := []byte(`{"data":{"plan":{"id":"p","accounts":[{"id":"a","balance":9007199254740993}]}}}`)
	tools := testTools(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/plans/p" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write(raw)
	}))
	r := tools.export(context.Background(), 4, Item{PlanID: "p", Source: "plan", Format: "json", Filename: "plan.json"})
	if r.Status != "success" || r.Artifact == nil || r.Data != nil || r.Artifact.Source != "plan" || r.Artifact.PlanID != "p" {
		t.Fatalf("result = %+v", r)
	}
	got, err := os.ReadFile(filepath.Join(tools.exportRoot, "plan.json"))
	if err != nil || string(got) != string(raw) {
		t.Fatalf("file = %q, err = %v", got, err)
	}
	b, err := json.Marshal(r)
	if err != nil || strings.Contains(string(b), "9007199254740993") {
		t.Fatalf("model result leaks export: %s (%v)", b, err)
	}
}

func TestExportRejectsExistingDestinationAndSymlinkRoot(t *testing.T) {
	tools := testTools(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"data":{"plan":{"id":"p"}}}`)) }))
	dst := filepath.Join(tools.exportRoot, "taken.json")
	if err := os.WriteFile(dst, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	r := tools.export(context.Background(), 0, Item{PlanID: "p", Source: "plan", Format: "json", Filename: "taken.json"})
	if r.Status != "error" {
		t.Fatalf("existing result = %+v", r)
	}
	got, err := os.ReadFile(dst)
	if err != nil || string(got) != "keep" {
		t.Fatalf("existing changed to %q: %v", got, err)
	}

	outside := t.TempDir()
	link := filepath.Join(t.TempDir(), "exports")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	tools.exportRoot = link
	r = tools.export(context.Background(), 0, Item{PlanID: "p", Source: "plan", Format: "json", Filename: "escape.json"})
	if r.Status != "error" {
		t.Fatalf("symlink root result = %+v", r)
	}
	if _, err := os.Stat(filepath.Join(outside, "escape.json")); !os.IsNotExist(err) {
		t.Fatalf("symlink escape created file: %v", err)
	}

	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "actual"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(base, "actual"), filepath.Join(base, "alias")); err != nil {
		t.Fatal(err)
	}
	tools.exportRoot = filepath.Join(base, "alias", "nested")
	r = tools.export(context.Background(), 0, Item{PlanID: "p", Source: "plan", Format: "json", Filename: "ancestor.json"})
	if r.Status != "error" {
		t.Fatalf("symlink ancestor result = %+v", r)
	}
	if _, err := os.Stat(filepath.Join(base, "actual", "nested", "ancestor.json")); !os.IsNotExist(err) {
		t.Fatalf("ancestor symlink escape created file: %v", err)
	}
}

func TestExportRejectsMalformedProviderEnvelope(t *testing.T) {
	tools := testTools(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`<html>not YNAB</html>`)) }))
	r := tools.export(context.Background(), 0, Item{PlanID: "p", Source: "plan", Format: "json", Filename: "bad.json"})
	if r.Status != "error" || r.Artifact != nil || len(r.Data) != 0 {
		t.Fatalf("result = %+v", r)
	}
	if _, err := os.Stat(filepath.Join(tools.exportRoot, "bad.json")); !os.IsNotExist(err) {
		t.Fatalf("malformed provider published file: %v", err)
	}
}

func TestExportConcurrentSameNamePublishesOnlyOnce(t *testing.T) {
	tools := testTools(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"plan":{"id":"p"}}}`))
	}))
	start := make(chan struct{})
	results := make(chan Result, 2)
	for range 2 {
		go func() {
			<-start
			results <- tools.export(context.Background(), 0, Item{PlanID: "p", Source: "plan", Format: "json", Filename: "same.json"})
		}()
	}
	close(start)
	first, second := <-results, <-results
	if (first.Status == "success") == (second.Status == "success") {
		t.Fatalf("results = %+v, %+v", first, second)
	}
	if b, err := os.ReadFile(filepath.Join(tools.exportRoot, "same.json")); err != nil || !strings.Contains(string(b), `"id":"p"`) {
		t.Fatalf("published file = %q, err = %v", b, err)
	}
}

func TestExportSerializesDistinctDestinationsBeforeProviderFetch(t *testing.T) {
	var requests atomic.Int32
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	tools := testTools(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			close(firstStarted)
			<-releaseFirst
		}
		_, _ = w.Write([]byte(`{"data":{"plan":{"id":"p"}}}`))
	}))
	results := make(chan Result, 2)
	go func() {
		results <- tools.export(context.Background(), 0, Item{PlanID: "p", Source: "plan", Format: "json", Filename: "one.json"})
	}()
	<-firstStarted
	go func() {
		results <- tools.export(context.Background(), 1, Item{PlanID: "p", Source: "plan", Format: "json", Filename: "two.json"})
	}()
	time.Sleep(25 * time.Millisecond)
	if got := requests.Load(); got != 1 {
		t.Fatalf("provider requests admitted while first export blocks = %d", got)
	}
	close(releaseFirst)
	if first, second := <-results, <-results; first.Status != "success" || second.Status != "success" {
		t.Fatalf("results = %+v, %+v", first, second)
	}
}

func TestExportRemovesPrivateStageAfterCanceledWrite(t *testing.T) {
	tools := testTools(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"data":{"plan":{"id":"p"}}}`)) }))
	ctx, cancel := context.WithCancel(context.Background())
	oldWriter := writeExportPayload
	writeExportPayload = func(ctx context.Context, f *os.File, payload []byte) error {
		if _, err := f.Write(payload[:1]); err != nil {
			return err
		}
		cancel()
		return ctx.Err()
	}
	t.Cleanup(func() { writeExportPayload = oldWriter })
	r := tools.export(ctx, 0, Item{PlanID: "p", Source: "plan", Format: "json", Filename: "cancel-mid-write.json"})
	if r.Status != "error" || r.Artifact != nil || !strings.Contains(r.Error, "canceled") {
		t.Fatalf("result = %+v", r)
	}
	entries, err := os.ReadDir(tools.exportRoot)
	if err != nil || len(entries) != 0 {
		t.Fatalf("private stage remains: %v, err = %v", entries, err)
	}
}

func TestExportSizeLimits(t *testing.T) {
	if !withinExportLimit(maxExportBytes) || withinExportLimit(maxExportBytes+1) {
		t.Fatal("64 MiB boundary not enforced")
	}
	tools := testTools(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("x"), int(maxProviderBytes+1)))
	}))
	r := tools.export(context.Background(), 0, Item{PlanID: "p", Source: "plan", Format: "json", Filename: "oversize.json"})
	if r.Status != "error" || !strings.Contains(r.Error, "32 MiB") {
		t.Fatalf("provider limit result = %+v", r)
	}
	if _, err := os.Stat(filepath.Join(tools.exportRoot, "oversize.json")); !os.IsNotExist(err) {
		t.Fatalf("oversize response was written: %v", err)
	}
}

func TestExportAdmissionHonorsCancellation(t *testing.T) {
	called := false
	tools := testTools(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	tools.exportSlots <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := tools.export(ctx, 0, Item{PlanID: "p", Source: "plan", Format: "json", Filename: "cancel.json"})
	<-tools.exportSlots
	if r.Status != "not_attempted" || called {
		t.Fatalf("result = %+v called=%v", r, called)
	}
	entries, err := os.ReadDir(tools.exportRoot)
	if err != nil || len(entries) != 0 {
		t.Fatalf("incomplete files = %v, err = %v", entries, err)
	}
}

func TestTransactionCSVPreservesIntegersNullsAndLiteralText(t *testing.T) {
	raw := []byte(`{"data":{"transactions":[{"id":"id-1","account_id":null,"date":"2026-01-01","amount":9007199254740993,"category_id":null,"payee_name":"=literal","memo":"snowman ☃\nand comma, too","cleared":null,"approved":true,"flag_color":null,"deleted":false,"subtransactions":[{"id":"split","amount":-9007199254740993}]}]}}`)
	b, err := transactionCSV(raw)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(strings.NewReader(string(b))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %#v", rows)
	}
	row := rows[1]
	if row[1] != "" || row[3] != "9007199254740993" || row[4] != "" || row[5] != "'=literal" || row[6] != "snowman ☃\nand comma, too" || row[7] != "" || row[9] != "" {
		t.Fatalf("row = %#v", row)
	}
	if !strings.Contains(row[11], `"amount":-9007199254740993`) {
		t.Fatalf("split integer lost: %q", row[11])
	}
	negative, err := transactionCSV([]byte(`{"data":{"transactions":[{"id":"negative","amount":-12345}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	negativeRows, err := csv.NewReader(strings.NewReader(string(negative))).ReadAll()
	if err != nil || negativeRows[1][3] != "-12345" {
		t.Fatalf("negative integer was escaped: %#v, %v", negativeRows, err)
	}
}
