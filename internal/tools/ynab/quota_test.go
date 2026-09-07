package ynab

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCircuitTransitionModel(t *testing.T) {
	now := time.Unix(1000, 0)
	for _, tc := range []struct {
		name        string
		state       circuitState
		permit      attemptPermit
		transient   bool
		failures    int
		open, lease bool
	}{
		{"first failure", circuitState{}, attemptPermit{}, true, 1, false, false},
		{"third failure", circuitState{failures: 2, epoch: 2}, attemptPermit{epoch: 2}, true, 3, true, false},
		{"current success", circuitState{failures: 2, epoch: 2}, attemptPermit{epoch: 2}, false, 0, false, false},
		{"stale success", circuitState{failures: 2, epoch: 2}, attemptPermit{epoch: 1}, false, 2, false, false},
		{"probe success", circuitState{failures: 3, epoch: 4, leaseUntil: now.Add(probeLease).UnixNano()}, attemptPermit{epoch: 4, probe: true}, false, 0, false, false},
		{"probe failure", circuitState{failures: 3, epoch: 4, leaseUntil: now.Add(probeLease).UnixNano()}, attemptPermit{epoch: 4, probe: true}, true, 4, true, false},
		{"expired probe success", circuitState{failures: 3, epoch: 5, leaseUntil: now.Add(probeLease).UnixNano()}, attemptPermit{epoch: 4, probe: true}, false, 3, false, true},
		{"expired probe failure", circuitState{failures: 3, epoch: 5, leaseUntil: now.Add(probeLease).UnixNano()}, attemptPermit{epoch: 4, probe: true}, true, 3, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outcome := attemptHealthy
			if tc.transient {
				outcome = attemptTransient
			}
			s := tc.state.complete(tc.permit, outcome, now)
			if s.failures != tc.failures || (s.openUntil != 0) != tc.open || (s.leaseUntil != 0) != tc.lease {
				t.Fatalf("state %+v", s)
			}
		})
	}
	for _, tc := range []struct {
		name          string
		state         circuitState
		probe, denied bool
	}{
		{"closed", circuitState{}, false, false},
		{"open", circuitState{openUntil: now.Add(time.Second).UnixNano()}, false, true},
		{"open expired", circuitState{openUntil: now.UnixNano()}, true, false},
		{"probe held", circuitState{leaseUntil: now.Add(time.Second).UnixNano()}, false, true},
		{"probe crashed", circuitState{leaseUntil: now.UnixNano()}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, probe, at := tc.state.admit(now)
			if probe != tc.probe || !at.IsZero() != tc.denied {
				t.Fatalf("probe %v retry %v", probe, at)
			}
		})
	}
}

func TestCircuitNeutralCompletionAndRecoveredGeneration(t *testing.T) {
	now := time.Unix(1000, 0)
	s := circuitState{failures: 2, epoch: 2}
	if got := s.complete(attemptPermit{epoch: 2}, attemptNeutral, now); got != s {
		t.Fatalf("cancellation cleared ordinary failures: %+v", got)
	}
	// Three concurrent failures belong to one closed generation and all count.
	s = circuitState{}
	old := attemptPermit{epoch: 0}
	for i := 0; i < 3; i++ {
		s = s.complete(old, attemptTransient, now)
	}
	if s.failures != 3 || s.openUntil == 0 {
		t.Fatalf("concurrent failures lost: %+v", s)
	}
	now = now.Add(30 * time.Second)
	s, probe, retryAt := s.admit(now)
	if !probe || !retryAt.IsZero() {
		t.Fatal("missing probe")
	}
	p := attemptPermit{epoch: s.epoch, probe: true}
	canceled := s.complete(p, attemptNeutral, now)
	if canceled.failures != 3 || canceled.openUntil != now.Add(30*time.Second).UnixNano() || canceled.leaseUntil != 0 {
		t.Fatalf("canceled probe healed outage: %+v", canceled)
	}
	if _, _, at := canceled.admit(now); at.IsZero() {
		t.Fatal("canceled probe immediately readmitted")
	}
	s = s.complete(p, attemptHealthy, now)
	if s.failures != 0 || s.recoveryFloor != p.epoch {
		t.Fatalf("probe failed to establish recovered generation: %+v", s)
	}
	if late := s.complete(old, attemptTransient, now); late != s {
		t.Fatalf("old failure contaminated recovered generation: %+v", late)
	}
	newPermit := attemptPermit{epoch: s.epoch}
	for i := 0; i < 3; i++ {
		s = s.complete(newPermit, attemptTransient, now)
	}
	if s.failures != 3 || s.openUntil == 0 {
		t.Fatal("new-generation concurrent failures were discarded")
	}
}

func TestQuotaReservedCanceledProbeDoesNotHeal(t *testing.T) {
	q, err := openQuotaStore("")
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	ctx := context.Background()
	now := time.Unix(1000, 0)
	key := tokenKey("token")
	for i := 0; i < 3; i++ {
		p, r := q.reserve(ctx, key, "GET:plan", now)
		if r.Status != "" {
			t.Fatal(r)
		}
		if err := q.complete(ctx, p, true, now); err != nil {
			t.Fatal(err)
		}
	}
	now = now.Add(30 * time.Second)
	p, r := q.reserve(ctx, key, "GET:plan", now)
	if r.Status != "" || !p.probe {
		t.Fatal("missing probe")
	}
	// The caller cancels after reservation but before any HTTP request. The
	// reserved attempt remains counted, and neutral finalization is not health.
	adapter := baseTools(Options{}, "http://127.0.0.1", &http.Client{})
	adapter.now = func() time.Time { return now }
	if _, err := adapter.finishAttempt(q, p, attemptNeutral, time.Time{}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, r := q.reserve(ctx, key, "GET:plan", now); r.Recovery != "wait" {
		t.Fatalf("canceled probe admitted work: %+v", r)
	}
	var count int
	if err := q.db.QueryRow(`SELECT COUNT(*) FROM reservations`).Scan(&count); err != nil || count != 4 {
		t.Fatalf("lost reserved attempt: count %d err %v", count, err)
	}
}

func TestQuotaStoreRollingBoundaryAndPrivacy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "provider-state.sqlite")
	q, err := openQuotaStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	ctx := context.Background()
	now := time.Unix(1000, 0)
	key := tokenKey("secret-sentinel")
	for i := 0; i < 200; i++ {
		_, r := q.reserve(ctx, key, "GET:plan", now)
		if r.Status != "" {
			t.Fatalf("reservation %d: %+v", i, r)
		}
	}
	_, r := q.reserve(ctx, key, "GET:account", now.Add(time.Hour-time.Nanosecond))
	if r.ErrorCode != "rate_limited" {
		t.Fatalf("early admission %+v", r)
	}
	if _, r = q.reserve(ctx, key, "GET:plan", now.Add(time.Hour)); r.Status != "" {
		t.Fatalf("boundary rejection %+v", r)
	}
	for _, entry := range []struct {
		path string
		mode os.FileMode
	}{{path, 0600}, {filepath.Dir(path), 0700}} {
		info, err := os.Stat(entry.path)
		if err != nil || info.Mode().Perm() != entry.mode {
			t.Fatalf("permissions %v %v", info, err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "secret-sentinel") {
		t.Fatal("secret in persisted database")
	}
}

func TestQuotaStoreSingleProbeAndCrashLease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "state.sqlite")
	a, err := openQuotaStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := openQuotaStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	ctx := context.Background()
	now := time.Unix(1000, 0)
	key := tokenKey("token")
	for i := 0; i < 3; i++ {
		p, r := a.reserve(ctx, key, "GET:plan", now)
		if r.Status != "" {
			t.Fatal(r)
		}
		if err := a.complete(ctx, p, true, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, r := b.reserve(ctx, key, "GET:plan", now); r.ErrorCode != "upstream_unavailable" {
		t.Fatal(r)
	}
	// An unrelated operation and token remain healthy.
	for _, scope := range [][2]string{{key, "GET:account"}, {tokenKey("other"), "GET:plan"}} {
		if _, r := b.reserve(ctx, scope[0], scope[1], now); r.Status != "" {
			t.Fatal(r)
		}
	}
	now = now.Add(30 * time.Second)
	var wg sync.WaitGroup
	permits := make(chan attemptPermit, 20)
	errors := make(chan Result, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			q := a
			if i%2 == 0 {
				q = b
			}
			p, r := q.reserve(ctx, key, "GET:plan", now)
			if r.Status == "" {
				permits <- p
			} else {
				errors <- r
			}
		}(i)
	}
	wg.Wait()
	close(permits)
	close(errors)
	if len(permits) != 1 || len(errors) != 19 {
		t.Fatalf("admitted %d denied %d", len(permits), len(errors))
	}
	old := <-permits
	for r := range errors {
		if r.ErrorCode != "upstream_unavailable" {
			t.Fatal(r)
		}
	}
	// Process death leaves its lease. Exactly at expiry another process probes.
	now = now.Add(45 * time.Second)
	newer, r := b.reserve(ctx, key, "GET:plan", now)
	if r.Status != "" || !newer.probe {
		t.Fatalf("lease recovery %+v %+v", newer, r)
	}
	if err := a.complete(ctx, old, false, now); err != nil {
		t.Fatal(err)
	}
	if _, r := a.reserve(ctx, key, "GET:plan", now); r.ErrorCode != "upstream_unavailable" {
		t.Fatalf("stale completion healed newer probe: %+v", r)
	}
	if err := b.complete(ctx, newer, false, now); err != nil {
		t.Fatal(err)
	}
	if _, r := a.reserve(ctx, key, "GET:plan", now); r.Status != "" {
		t.Fatalf("successful probe failed to close: %+v", r)
	}
}

func TestQuotaStoreRejectsUnsafeFileAndFailsClosed(t *testing.T) {
	for _, kind := range []string{"symlink", "public"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "state.sqlite")
			if kind == "symlink" {
				target := filepath.Join(dir, "target")
				if err := os.WriteFile(target, nil, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, nil, 0644); err != nil {
				t.Fatal(err)
			}
			if q, err := openQuotaStore(path); err == nil {
				q.Close()
				t.Fatal("accepted unsafe state")
			}
		})
	}
	q, err := openQuotaStore("")
	if err != nil {
		t.Fatal(err)
	}
	q.Close()
	if _, r := q.reserve(context.Background(), tokenKey("secret"), "GET:plan", time.Now()); r.ErrorCode != "local_state" || r.Status != "not_attempted" {
		t.Fatal(r)
	}
}

func TestQuotaStorePrivateChildOfTelemetryDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ynab")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "provider-state", "state.sqlite")
	q, err := openQuotaStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	info, err := os.Stat(filepath.Dir(path))
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("private child %v %v", info, err)
	}
	info, err = os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0755 {
		t.Fatalf("changed telemetry parent %v %v", info, err)
	}
}

func TestQuotaStoreCloseRacesLazyInitialization(t *testing.T) {
	for i := 0; i < 20; i++ {
		adapter := baseTools(Options{Token: "synthetic"}, "http://127.0.0.1", &http.Client{})
		adapter.quotaPath = filepath.Join(t.TempDir(), "private", "state.sqlite")
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); <-start; adapter.providerQuota() }()
		go func() { defer wg.Done(); <-start; adapter.Close() }()
		close(start)
		wg.Wait()
		q := adapter.providerQuota()
		if q != nil {
			if _, r := q.reserve(context.Background(), tokenKey("synthetic"), "GET:plan", time.Now()); r.ErrorCode != "local_state" {
				t.Fatal("store remained open after close")
			}
		}
	}
}

func TestQuotaStoreServerDelayIsScopedAndPersistent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "state.sqlite")
	q, err := openQuotaStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Unix(1000, 0)
	key := tokenKey("token")
	p, r := q.reserve(ctx, key, "GET:plan", now)
	if r.Status != "" {
		t.Fatal(r)
	}
	if err = q.complete(ctx, p, true, now, now.Add(120*time.Second)); err != nil {
		t.Fatal(err)
	}
	q.Close()
	q, err = openQuotaStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	if _, r = q.reserve(ctx, key, "GET:plan", now.Add(119*time.Second)); r.Recovery != "wait" {
		t.Fatal(r)
	}
	if _, r = q.reserve(ctx, key, "GET:account", now); r.Status != "" {
		t.Fatal(r)
	}
	p, r = q.reserve(ctx, key, "GET:plan", now.Add(120*time.Second))
	if r.Status != "" || !p.probe {
		t.Fatalf("not admitted as recovery probe %+v %+v", p, r)
	}
}

func TestRetryTaxonomyAndHints(t *testing.T) {
	for status := 100; status <= 599; status++ {
		want := status == 408 || status == 500 || status == 502 || status == 503 || status == 504
		if transientStatus(status) != want {
			t.Fatalf("status %d", status)
		}
	}
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		value string
		delta time.Duration
		valid bool
	}{{"30", 30 * time.Second, true}, {"0", 0, true}, {"Mon, 07 Sep 2026 12:01:00 GMT", time.Minute, true}, {"Mon, 07 Sep 2026 11:00:00 GMT", 0, true}, {"", 0, false}, {"-1", 0, false}, {"1.5", 0, false}, {"Fri, 31 Dec 9999 23:59:59 GMT", 0, false}, {"999999999999999999999999999", 0, false}} {
		got, ok := retryTime(tc.value, now)
		if ok != tc.valid || (ok && got.Sub(now) != tc.delta) {
			t.Fatalf("hint %q = %v %v", tc.value, got, ok)
		}
	}
	seen := map[time.Duration]bool{}
	for i := 0; i < 100; i++ {
		d := fullJitter(time.Second)
		if d < 0 || d > time.Second {
			t.Fatal(d)
		}
		seen[d] = true
	}
	if len(seen) < 2 {
		t.Fatal("jitter degenerated to fixed delay")
	}
}
