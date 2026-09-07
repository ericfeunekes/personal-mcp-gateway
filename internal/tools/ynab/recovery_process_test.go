package ynab

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"personal-mcp-gateway/internal/audit"
	localmcp "personal-mcp-gateway/internal/mcp"
)

const recoveryProcessClock = "2026-09-07T12:00:00Z"

// This helper runs the real MCP descriptor, stdio transport, HTTP adapter and
// SQLite store in a separate OS process. Only the private test constructor can
// select an API URL; no production API override or real token is involved.
func TestRecoveryProcessHelper(t *testing.T) {
	if os.Getenv("YNAB_RECOVERY_HELPER") != "1" {
		return
	}
	err := runRecoveryProcessHelper()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0) // The Go test runner's PASS line must not enter the MCP stream.
}

func runRecoveryProcessHelper() error {
	endpoint, err := url.Parse(os.Getenv("YNAB_RECOVERY_ENDPOINT"))
	if err != nil || endpoint.Scheme != "http" || !net.ParseIP(endpoint.Hostname()).IsLoopback() {
		return fmt.Errorf("fixture endpoint must be loopback HTTP")
	}
	token := os.Getenv("YNAB_RECOVERY_TOKEN")
	if !strings.HasPrefix(token, "synthetic-recovery-") {
		return fmt.Errorf("fixture requires synthetic credentials")
	}
	now, err := time.Parse(time.RFC3339, os.Getenv("YNAB_RECOVERY_CLOCK"))
	if err != nil {
		return err
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != endpoint.Host {
			return nil, fmt.Errorf("fixture refuses non-provider destination")
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}}
	adapter := newTools(Options{Token: token, ExportRoot: os.Getenv("YNAB_RECOVERY_EXPORTS")}, endpoint.String(), &http.Client{
		Transport: transport, Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	})
	if err := adapter.quota.db.Close(); err != nil {
		return err
	}
	adapter.quota, err = openQuotaStore(os.Getenv("YNAB_RECOVERY_DB"))
	if err != nil {
		return err
	}
	defer adapter.Close()
	adapter.now = func() time.Time { return now }
	descriptors, err := adapter.Descriptors()
	if err != nil {
		return err
	}
	server, _, err := localmcp.NewNamedServer(sdk.Implementation{Name: "ynab", Version: "recovery-fixture"}, audit.Disabled(), "stdio", descriptors)
	if err != nil {
		return err
	}
	return localmcp.RunStdio(context.Background(), server)
}

type recoveryProcess struct {
	command *exec.Cmd
	session *sdk.ClientSession
	ctx     context.Context
}

func startRecoveryProcess(t *testing.T, endpoint, db, token, clock string) *recoveryProcess {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRecoveryProcessHelper$")
	// Deliberately do not inherit the parent environment, especially YNAB_TOKEN,
	// proxies, or credentials. The helper only has synthetic test configuration.
	command.Env = []string{
		"YNAB_RECOVERY_HELPER=1", "YNAB_RECOVERY_ENDPOINT=" + endpoint,
		"YNAB_RECOVERY_DB=" + db, "YNAB_RECOVERY_TOKEN=" + token,
		"YNAB_RECOVERY_CLOCK=" + clock, "YNAB_RECOVERY_EXPORTS=" + filepath.Join(filepath.Dir(db), "exports"),
	}
	command.Stderr = os.Stderr
	client := sdk.NewClient(&sdk.Implementation{Name: "recovery-process-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &sdk.CommandTransport{Command: command, TerminateDuration: time.Second}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return &recoveryProcess{command: command, session: session, ctx: ctx}
}

func (p *recoveryProcess) list(count int) ([]Result, error) {
	items := make([]Item, count)
	for i := range items {
		items[i] = Item{Type: "plan"}
	}
	output, err := p.session.CallTool(p.ctx, &sdk.CallToolParams{Name: "list", Arguments: Input{Items: items}})
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(output.StructuredContent)
	if err != nil {
		return nil, err
	}
	var decoded Output
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return nil, err
	}
	if len(decoded.Results) != count {
		return nil, fmt.Errorf("expected %d results, received %s", count, encoded)
	}
	return decoded.Results, nil
}

func (p *recoveryProcess) successfulReads(count int) error {
	for count > 0 {
		batch := min(count, 10)
		results, err := p.list(batch)
		if err != nil {
			return err
		}
		for _, result := range results {
			if result.Status != "success" {
				return fmt.Errorf("read failed: %+v", result)
			}
		}
		count -= batch
	}
	return nil
}

func assertProcessRateLimited(t *testing.T, process *recoveryProcess) {
	t.Helper()
	results, err := process.list(1)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Status != "not_attempted" || results[0].ErrorCode != "rate_limited" || results[0].Recovery != "wait" || results[0].RetryAt == "" {
		t.Fatalf("missing local rejection and recovery guidance: %+v", results)
	}
}

func TestRecoveryProcessesShareRollingQuotaAndIsolateTokens(t *testing.T) {
	var requests atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{"data":{"plans":[]}}`))
	}))
	defer provider.Close()
	db := filepath.Join(t.TempDir(), "private", "quota.sqlite")
	first := startRecoveryProcess(t, provider.URL, db, "synthetic-recovery-shared", recoveryProcessClock)
	second := startRecoveryProcess(t, provider.URL, db, "synthetic-recovery-shared", recoveryProcessClock)
	finished := make(chan error, 2)
	go func() { finished <- first.successfulReads(99) }()
	go func() { finished <- second.successfulReads(99) }()
	for range 2 {
		if err := <-finished; err != nil {
			t.Fatal(err)
		}
	}
	// Both processes compete for the final two slots with four requests. This
	// exercises atomic admission at the limit, not merely shared readback.
	start := make(chan struct{})
	finalResults := make(chan []Result, 2)
	for _, process := range []*recoveryProcess{first, second} {
		go func() {
			<-start
			results, err := process.list(2)
			finalResults <- results
			finished <- err
		}()
	}
	close(start)
	successes, rejected := 0, 0
	for range 2 {
		for _, result := range <-finalResults {
			switch result.Status {
			case "success":
				successes++
			case "not_attempted":
				rejected++
			default:
				t.Fatalf("unexpected quota outcome: %+v", result)
			}
		}
		if err := <-finished; err != nil {
			t.Fatal(err)
		}
	}
	if successes != 2 || rejected != 2 {
		t.Fatalf("final two shared slots: %d successes, %d rejected", successes, rejected)
	}
	assertProcessRateLimited(t, first)
	assertProcessRateLimited(t, second)
	if requests.Load() != 200 {
		t.Fatalf("shared quota dispatched %d requests, want exactly 200", requests.Load())
	}
	other := startRecoveryProcess(t, provider.URL, db, "synthetic-recovery-other", recoveryProcessClock)
	if err := other.successfulReads(1); err != nil {
		t.Fatalf("another token inherited the exhausted quota: %v", err)
	}
	// A fresh process after the rolling window releases the original token.
	recovered := startRecoveryProcess(t, provider.URL, db, "synthetic-recovery-shared", "2026-09-07T13:00:01Z")
	if err := recovered.successfulReads(1); err != nil {
		t.Fatalf("rolling quota did not recover: %v", err)
	}
	if requests.Load() != 202 {
		t.Fatalf("unexpected provider count after recovery: %d", requests.Load())
	}
}

func TestRecoveryProcessCooldownSurvivesRestart(t *testing.T) {
	var requests atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"plans":[]}}`))
	}))
	defer provider.Close()
	db := filepath.Join(t.TempDir(), "private", "quota.sqlite")
	first := startRecoveryProcess(t, provider.URL, db, "synthetic-recovery-cooldown", recoveryProcessClock)
	results, err := first.list(1)
	if err != nil || results[0].ErrorCode != "rate_limited" || results[0].HTTPStatus != 429 {
		t.Fatalf("expected provider rate limit: %+v, %v", results, err)
	}
	_ = first.session.Close()
	restarted := startRecoveryProcess(t, provider.URL, db, "synthetic-recovery-cooldown", "2026-09-07T12:01:59Z")
	assertProcessRateLimited(t, restarted)
	if requests.Load() != 1 {
		t.Fatalf("restart lost cooldown: %d provider requests", requests.Load())
	}
	_ = restarted.session.Close()
	recovered := startRecoveryProcess(t, provider.URL, db, "synthetic-recovery-cooldown", "2026-09-07T12:02:01Z")
	if err := recovered.successfulReads(1); err != nil {
		t.Fatalf("expired cooldown did not recover: %v", err)
	}
}

func TestRecoveryProcessCrashKeepsDispatchedReservation(t *testing.T) {
	var requests atomic.Int32
	inFlight := make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 200 {
			close(inFlight)
			<-r.Context().Done()
			return
		}
		_, _ = w.Write([]byte(`{"data":{"plans":[]}}`))
	}))
	defer provider.Close()
	db := filepath.Join(t.TempDir(), "private", "quota.sqlite")
	first := startRecoveryProcess(t, provider.URL, db, "synthetic-recovery-crash", recoveryProcessClock)
	if err := first.successfulReads(199); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { _, err := first.list(1); finished <- err }()
	select {
	case <-inFlight:
	case <-first.ctx.Done():
		t.Fatal("request never reached provider")
	}
	if err := first.command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := <-finished; err == nil {
		t.Fatal("killed MCP process unexpectedly returned success")
	}
	restarted := startRecoveryProcess(t, provider.URL, db, "synthetic-recovery-crash", recoveryProcessClock)
	assertProcessRateLimited(t, restarted)
	if requests.Load() != 200 {
		t.Fatalf("crash lost the dispatched reservation: %d requests", requests.Load())
	}
}
