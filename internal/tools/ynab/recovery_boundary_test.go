package ynab

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"personal-mcp-gateway/internal/audit"
	localmcp "personal-mcp-gateway/internal/mcp"
)

// The simulator supplies controlled provider faults over real HTTP. Its client
// cannot use environment proxies, redirects, non-loopback addresses, or tokens
// from the user's environment. It is deliberately not a production API override.
type recoveryReply struct {
	status     int
	retryAfter string
	body       string
}
type recoverySimulator struct {
	mu       sync.Mutex
	replies  []recoveryReply
	requests int
	writes   int
	blockAt  int
	entered  chan struct{}
	release  chan struct{}
}

func newRecoverySimulator(t *testing.T, replies ...recoveryReply) (*Tools, *recoverySimulator) {
	t.Helper()
	sim := &recoverySimulator{replies: replies}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer quota-free-test-token" {
			t.Error("unexpected credential")
		}
		sim.mu.Lock()
		n := sim.requests
		sim.requests++
		if r.Method != http.MethodGet {
			sim.writes++
		}
		reply := recoveryReply{status: 200, body: `{"data":{"accounts":[]}}`}
		if n < len(sim.replies) {
			reply = sim.replies[n]
		}
		blocked, entered, release := sim.blockAt == n+1, sim.entered, sim.release
		sim.mu.Unlock()
		if blocked {
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if reply.retryAfter != "" {
			w.Header().Set("Retry-After", reply.retryAfter)
		}
		w.WriteHeader(reply.status)
		_, _ = w.Write([]byte(reply.body))
	}))
	t.Cleanup(server.Close)
	transport := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(address)
		if err != nil || !net.ParseIP(host).IsLoopback() {
			t.Errorf("non-local provider attempted: %s", address)
			return nil, errors.New("test provider must be loopback")
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}}
	t.Cleanup(transport.CloseIdleConnections)
	tools := newTools(Options{Token: "quota-free-test-token", ExportRoot: t.TempDir()}, server.URL, &http.Client{Transport: transport, Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }})
	t.Cleanup(func() { _ = tools.Close() })
	return tools, sim
}

func (s *recoverySimulator) count() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests, s.writes
}

func recoveryMCPClient(t *testing.T, tools *Tools) *sdk.ClientSession {
	t.Helper()
	descriptors, err := tools.Descriptors()
	if err != nil {
		t.Fatal(err)
	}
	server, _, err := localmcp.NewNamedServer(sdk.Implementation{Name: "ynab", Version: "recovery-test"}, audit.Disabled(), "stdio", descriptors)
	if err != nil {
		t.Fatal(err)
	}
	ct, st := sdk.NewInMemoryTransports()
	ss, err := server.Connect(context.Background(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	client := sdk.NewClient(&sdk.Implementation{Name: "quota-free-test", Version: "1"}, nil)
	cs, err := client.Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func callRecoveryMCP(t *testing.T, client *sdk.ClientSession, verb string, items []Item) (*sdk.CallToolResult, Output) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := client.CallTool(ctx, &sdk.CallToolParams{Name: verb, Arguments: Input{Items: items}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out Output
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Results) != len(items) {
		t.Fatalf("wrong indexed results: %s", raw)
	}
	return result, out
}

func TestRecoveryMCPRateLimitPersistsAcrossCallsWithoutHint(t *testing.T) {
	tools, sim := newRecoverySimulator(t, recoveryReply{status: 429})
	client := recoveryMCPClient(t, tools)
	items := []Item{{Type: "account", PlanID: "p"}, {Type: "account", PlanID: "p"}}
	for n := 0; n < 2; n++ {
		result, out := callRecoveryMCP(t, client, ToolList, items)
		if !result.IsError || out.Results[0].ErrorCode != "rate_limited" || out.Results[0].Recovery != "wait" || out.Results[0].RetryAt == "" || out.Results[1].Status != "not_attempted" {
			t.Fatalf("call %d lost recovery contract: %+v %+v", n, result, out)
		}
	}
	if calls, _ := sim.count(); calls != 1 {
		t.Fatalf("cooldown sent %d requests, want 1", calls)
	}
}

func TestRecoveryMCPAuthenticationStopsBatch(t *testing.T) {
	for _, status := range []int{401, 403} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			tools, sim := newRecoverySimulator(t, recoveryReply{status: status})
			result, out := callRecoveryMCP(t, recoveryMCPClient(t, tools), ToolList, []Item{{Type: "account", PlanID: "p"}, {Type: "account", PlanID: "p"}})
			if !result.IsError || out.Results[0].HTTPStatus != status || out.Results[0].Recovery != "fix_credentials" || out.Results[1].Status != "not_attempted" {
				t.Fatalf("credential failure: %+v", out)
			}
			if calls, _ := sim.count(); calls != 1 {
				t.Fatalf("auth failure sent %d requests", calls)
			}
		})
	}
}

func TestRecoveryMCPReadRetriesButWriteDoesNot(t *testing.T) {
	t.Run("read", func(t *testing.T) {
		tools, sim := newRecoverySimulator(t, recoveryReply{status: 503})
		result, out := callRecoveryMCP(t, recoveryMCPClient(t, tools), ToolList, []Item{{Type: "account", PlanID: "p"}})
		if result.IsError || out.Results[0].Status != "success" {
			t.Fatalf("read did not recover: %+v", out)
		}
		if calls, _ := sim.count(); calls != 2 {
			t.Fatalf("read attempts=%d", calls)
		}
	})
	t.Run("write", func(t *testing.T) {
		tools, sim := newRecoverySimulator(t, recoveryReply{status: 503})
		result, out := callRecoveryMCP(t, recoveryMCPClient(t, tools), ToolCreate, []Item{{Type: "payee", PlanID: "p", Data: json.RawMessage(`{"name":"fixture"}`)}})
		if !result.IsError || out.Results[0].Status != "uncertain" || out.Results[0].Recovery != "inspect_before_retry" {
			t.Fatalf("ambiguous write: %+v", out)
		}
		if calls, writes := sim.count(); calls != 1 || writes != 1 {
			t.Fatalf("write replayed: requests=%d writes=%d", calls, writes)
		}
	})
}

func TestRecoveryMCPRetryConsumesQuotaAndReportsAttempted(t *testing.T) {
	replies := make([]recoveryReply, 200)
	for i := range replies {
		replies[i] = recoveryReply{status: 200, body: `{"data":{"accounts":[]}}`}
	}
	replies[199] = recoveryReply{status: 503}
	tools, sim := newRecoverySimulator(t, replies...)
	item := Item{Type: "account", PlanID: "p"}
	for i := 0; i < 199; i++ {
		_, out, _ := tools.list(context.Background(), nil, Input{Items: []Item{item}})
		if out.Results[0].Status != "success" {
			t.Fatalf("setup request %d: %+v", i, out)
		}
	}
	result, out := callRecoveryMCP(t, recoveryMCPClient(t, tools), ToolList, []Item{item, item})
	if !result.IsError || out.Results[0].Status != "error" || out.Results[0].ErrorCode != "rate_limited" || out.Results[1].Status != "not_attempted" {
		t.Fatalf("retry admission lost prior attempt: %+v", out)
	}
	if calls, _ := sim.count(); calls != 200 {
		t.Fatalf("quota allowed retry: calls=%d", calls)
	}
}

func TestRecoveryMCPItemFailureAndMalformedResponse(t *testing.T) {
	t.Run("item failure preserves subsequent read", func(t *testing.T) {
		tools, sim := newRecoverySimulator(t, recoveryReply{status: 404})
		result, out := callRecoveryMCP(t, recoveryMCPClient(t, tools), ToolList, []Item{{Type: "account", PlanID: "p"}, {Type: "account", PlanID: "p"}})
		if !result.IsError || out.Results[0].ErrorCode != "not_found" || out.Results[1].Status != "success" {
			t.Fatalf("partial batch: %+v", out)
		}
		if calls, _ := sim.count(); calls != 2 {
			t.Fatalf("item failure requests=%d", calls)
		}
	})
	t.Run("malformed success is not retried", func(t *testing.T) {
		tools, sim := newRecoverySimulator(t, recoveryReply{status: 200, body: `not JSON`})
		result, out := callRecoveryMCP(t, recoveryMCPClient(t, tools), ToolList, []Item{{Type: "account", PlanID: "p"}})
		if !result.IsError || out.Results[0].ErrorCode != "invalid_response" || out.Results[0].Recovery != "contact_operator" {
			t.Fatalf("invalid response: %+v", out)
		}
		if calls, _ := sim.count(); calls != 1 {
			t.Fatalf("malformed response retried: %d", calls)
		}
	})
}

func TestRecoveryCircuitHTTPProbeAndClosedStore(t *testing.T) {
	t.Run("canceled probe does not heal outage", func(t *testing.T) {
		tools, sim := newRecoverySimulator(t, recoveryReply{status: 503}, recoveryReply{status: 503}, recoveryReply{status: 503})
		var clock atomic.Int64
		clock.Store(time.Now().UnixNano())
		tools.now = func() time.Time { return time.Unix(0, clock.Load()) }
		tools.sleep = func(context.Context, time.Duration) error { return nil }
		items := []Item{{Type: "account", PlanID: "p"}}
		for i := 0; i < 2; i++ {
			_, _, _ = tools.list(context.Background(), nil, Input{Items: items})
		}
		clock.Add(int64(31 * time.Second))
		sim.mu.Lock()
		sim.blockAt = 4
		sim.entered = make(chan struct{})
		sim.release = make(chan struct{})
		sim.mu.Unlock()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan Output, 1)
		go func() { _, out, _ := tools.list(ctx, nil, Input{Items: items}); done <- out }()
		select {
		case <-sim.entered:
		case <-time.After(3 * time.Second):
			t.Fatal("probe did not reach HTTP")
		}
		cancel()
		select {
		case out := <-done:
			if out.Results[0].ErrorCode != "canceled" {
				t.Fatalf("canceled probe: %+v", out)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("canceled probe did not return")
		}
		result, out := callRecoveryMCP(t, recoveryMCPClient(t, tools), ToolList, items)
		if !result.IsError || out.Results[0].Status != "not_attempted" || out.Results[0].Recovery != "wait" {
			t.Fatalf("canceled probe healed outage: %+v", out)
		}
		if calls, _ := sim.count(); calls != 4 {
			t.Fatalf("unproved recovery sent another request: %d", calls)
		}
	})
	t.Run("one recovery probe", func(t *testing.T) {
		tools, sim := newRecoverySimulator(t, recoveryReply{status: 503}, recoveryReply{status: 503}, recoveryReply{status: 503})
		var clock atomic.Int64
		clock.Store(time.Now().UnixNano())
		tools.now = func() time.Time { return time.Unix(0, clock.Load()) }
		tools.sleep = func(context.Context, time.Duration) error { return nil }
		client := recoveryMCPClient(t, tools)
		items := []Item{{Type: "account", PlanID: "p"}}
		for i := 0; i < 3; i++ {
			result, _ := callRecoveryMCP(t, client, ToolList, items)
			if !result.IsError {
				t.Fatal("outage returned success")
			}
		}
		if calls, _ := sim.count(); calls != 3 {
			t.Fatalf("open circuit sent %d requests", calls)
		}
		clock.Add(int64(31 * time.Second))
		sim.mu.Lock()
		sim.blockAt = 4
		sim.entered = make(chan struct{})
		sim.release = make(chan struct{})
		sim.mu.Unlock()
		done := make(chan *sdk.CallToolResult, 1)
		go func() {
			result, err := client.CallTool(context.Background(), &sdk.CallToolParams{Name: ToolList, Arguments: Input{Items: items}})
			if err != nil {
				t.Error(err)
			}
			done <- result
		}()
		select {
		case <-sim.entered:
		case <-time.After(3 * time.Second):
			t.Fatal("recovery probe did not reach HTTP")
		}
		result, out := callRecoveryMCP(t, client, ToolList, items)
		if !result.IsError || out.Results[0].Status != "not_attempted" || out.Results[0].Recovery != "wait" {
			t.Fatalf("concurrent probe admitted: %+v", out)
		}
		if calls, _ := sim.count(); calls != 4 {
			t.Fatalf("multiple HTTP probes: %d", calls)
		}
		close(sim.release)
		select {
		case result := <-done:
			if result == nil || result.IsError {
				t.Fatalf("probe did not recover: %+v", result)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("probe stalled")
		}
		result, _ = callRecoveryMCP(t, client, ToolList, items)
		if result.IsError {
			t.Fatal("successful probe did not reopen circuit")
		}
	})
	t.Run("state unavailable sends nothing", func(t *testing.T) {
		tools, sim := newRecoverySimulator(t)
		if err := tools.quota.db.Close(); err != nil {
			t.Fatal(err)
		}
		result, out := callRecoveryMCP(t, recoveryMCPClient(t, tools), ToolList, []Item{{Type: "account", PlanID: "p"}})
		if !result.IsError || out.Results[0].Status != "not_attempted" || out.Results[0].ErrorCode != "local_state" {
			t.Fatalf("state failure: %+v", out)
		}
		if calls, _ := sim.count(); calls != 0 {
			t.Fatalf("unaccounted requests: %d", calls)
		}
	})
}
