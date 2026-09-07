package ynab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func failureMutationItems(native bool) []Item {
	payee := Item{Type: "payee", PlanID: "p", Data: json.RawMessage(`{"name":"later"}`)}
	if !native {
		return []Item{payee, payee}
	}
	return []Item{
		{Type: "transaction", PlanID: "p", Data: json.RawMessage(`{"account_id":"a","date":"2026-01-01","amount":1,"import_id":"first"}`)},
		{Type: "transaction", PlanID: "p", Data: json.RawMessage(`{"account_id":"a","date":"2026-01-01","amount":2,"import_id":"second"}`)},
		payee,
	}
}

// The local provider controls failures at the actual HTTP transport seam, not
// by substituting a RoundTripper or calling the result classifier directly.
func TestHTTPReadContinuesAfterBadRequest(t *testing.T) {
	var calls atomic.Int32
	tools := testTools(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"accounts":[]}}`))
	}))
	_, out, err := tools.list(context.Background(), nil, Input{Items: []Item{{Type: "account", PlanID: "p"}, {Type: "account", PlanID: "p"}}})
	if err != nil || len(out.Results) != 2 || out.Results[0].Status != "error" || out.Results[1].Status != "success" || calls.Load() != 2 {
		t.Fatalf("calls=%d output=%+v error=%v", calls.Load(), out, err)
	}
}

func TestHTTPRateLimitStopsRemainingItems(t *testing.T) {
	for _, mode := range []string{"read", "single mutation", "native mutation"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			tools := testTools(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", "120")
				w.WriteHeader(http.StatusTooManyRequests)
			}))
			verb := ToolCreate
			items := failureMutationItems(mode == "native mutation")
			if mode == "read" {
				verb = ToolList
				items = []Item{{Type: "account", PlanID: "p"}, {Type: "account", PlanID: "p"}}
			}
			_, out, err := tools.execute(context.Background(), verb, Input{Items: items})
			if err != nil || len(out.Results) != len(items) || calls.Load() != 1 {
				t.Fatalf("calls=%d output=%+v error=%v", calls.Load(), out, err)
			}
			for i, result := range out.Results[:len(items)-1] {
				if result.Index != i || result.Status != "error" || result.RetryAfter != "120" {
					t.Fatalf("rate limit result=%+v", result)
				}
			}
			if out.Results[len(items)-1].Status != "not_attempted" {
				t.Fatalf("later item dispatched: %+v", out)
			}
		})
	}
}

func TestHTTPMutationAmbiguousFailuresStopRemainingItems(t *testing.T) {
	for _, native := range []bool{false, true} {
		for _, failure := range []string{"timeout", "cancel after dispatch", "truncated body", "HTTP 408", "HTTP 500", "HTTP 503"} {
			t.Run(fmt.Sprintf("native=%t/%s", native, failure), func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				var calls atomic.Int32
				release := make(chan struct{})
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					switch failure {
					case "timeout":
						<-release
					case "cancel after dispatch":
						cancel()
						<-release
					case "truncated body":
						w.Header().Set("Content-Length", "1000")
						_, _ = w.Write([]byte(`{"data":`))
					case "HTTP 408":
						w.WriteHeader(http.StatusRequestTimeout)
					case "HTTP 500":
						w.WriteHeader(http.StatusInternalServerError)
					case "HTTP 503":
						w.WriteHeader(http.StatusServiceUnavailable)
					}
				}))
				defer server.Close()
				defer close(release)
				client := server.Client()
				transport := http.DefaultTransport.(*http.Transport).Clone()
				defer transport.CloseIdleConnections()
				if failure == "timeout" {
					transport.ResponseHeaderTimeout = 100 * time.Millisecond
				}
				client.Transport = transport
				tools := newTools(Options{Token: "synthetic", ExportRoot: t.TempDir()}, server.URL, client)
				items := failureMutationItems(native)
				_, out, err := tools.create(ctx, nil, Input{Items: items})
				if err != nil || len(out.Results) != len(items) || calls.Load() != 1 {
					t.Fatalf("calls=%d output=%+v error=%v", calls.Load(), out, err)
				}
				for i, result := range out.Results[:len(items)-1] {
					if result.Index != i || result.Status != "uncertain" {
						t.Fatalf("ambiguous mutation must not claim success or rejection: %+v", result)
					}
				}
				if out.Results[len(items)-1].Status != "not_attempted" {
					t.Fatalf("later item dispatched: %+v", out)
				}
			})
		}
	}
}

func TestHTTPProviderConcurrencyIsBoundedAtTwo(t *testing.T) {
	entered := make(chan struct{}, 3)
	release := make(chan struct{})
	var active, peak atomic.Int32
	tools := testTools(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		entered <- struct{}{}
		<-release
		_, _ = w.Write([]byte(`{"data":{"accounts":[]}}`))
	}))
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan Output, 3)
	for i := 0; i < 3; i++ {
		go func() {
			_, out, _ := tools.list(ctx, nil, Input{Items: []Item{{Type: "account", PlanID: "p"}}})
			done <- out
		}()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("two independent requests did not reach provider concurrently")
		}
	}
	select {
	case <-entered:
		t.Fatal("third request reached provider while both slots were occupied")
	case <-time.After(100 * time.Millisecond):
	}
	// Release exactly one request. The third must then reach the real server.
	release <- struct{}{}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("queued request did not dispatch after a provider slot was freed")
	}
	for i := 0; i < 2; i++ {
		release <- struct{}{}
	}
	for i := 0; i < 3; i++ {
		select {
		case out := <-done:
			if len(out.Results) != 1 || out.Results[0].Status != "success" {
				t.Fatalf("request failed: %+v", out)
			}
		case <-ctx.Done():
			t.Fatal("provider calls did not complete")
		}
	}
	if peak.Load() != 2 {
		t.Fatalf("peak upstream concurrency = %d, want 2", peak.Load())
	}
}
