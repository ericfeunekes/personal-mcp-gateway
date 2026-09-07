package ynab

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestProductionTransportCountsBrokenConnectionAttempts(t *testing.T) {
	var mu sync.Mutex
	var peers []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		peers = append(peers, r.RemoteAddr)
		call := len(peers)
		mu.Unlock()
		if call == 2 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":{"plans":[]}}`))
	}))
	defer server.Close()
	adapter := newTools(Options{Token: "synthetic"}, server.URL, &http.Client{Transport: newProviderTransport(), Timeout: time.Second})
	defer adapter.Close()
	adapter.sleep = func(context.Context, time.Duration) error { return nil }
	for i := 0; i < 2; i++ {
		_, out, err := adapter.execute(context.Background(), ToolList, Input{Items: []Item{{Type: "plan"}}})
		if err != nil || out.Results[0].Status != "success" {
			t.Fatalf("call %d: %+v %v", i, out, err)
		}
	}
	var reservations int
	if err := adapter.quota.db.QueryRow(`SELECT COUNT(*) FROM reservations`).Scan(&reservations); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(peers) != 3 || reservations != 3 {
		t.Fatalf("hidden or missing attempt: HTTP %d reservations %d", len(peers), reservations)
	}
	if peers[0] == peers[1] || peers[1] == peers[2] {
		t.Fatalf("connections reused: %v", peers)
	}
}

func TestProductionTransportNegotiatesHTTP1Only(t *testing.T) {
	protocol := make(chan int, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protocol <- r.ProtoMajor
		w.Write([]byte(`{"data":{"plans":[]}}`))
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	transport := newProviderTransport()
	// Trust the fixture certificate without replacing production ALPN settings.
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	}
	transport.TLSClientConfig.RootCAs = server.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	adapter := newTools(Options{Token: "synthetic"}, server.URL, &http.Client{Transport: transport, Timeout: time.Second})
	defer adapter.Close()
	_, out, err := adapter.execute(context.Background(), ToolList, Input{Items: []Item{{Type: "plan"}}})
	if err != nil || out.Results[0].Status != "success" {
		t.Fatalf("%+v %v", out, err)
	}
	if got := <-protocol; got != 1 {
		t.Fatalf("HTTP/%d negotiated", got)
	}
}
