package ynab

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

const quotaLimit = 200
const quotaWindow = time.Hour
const circuitThreshold = 3
const circuitCooldown = 30 * time.Second
const probeLease = 45 * time.Second

type quotaStore struct{ db *sql.DB }

func (q *quotaStore) Close() error { return q.db.Close() }

type attemptPermit struct {
	token, family string
	epoch         int64
	probe         bool
}
type attemptOutcome uint8

const (
	attemptHealthy attemptOutcome = iota
	attemptTransient
	attemptNeutral
)

type circuitState struct {
	failures              int
	epoch                 int64
	recoveryFloor         int64
	openUntil, leaseUntil int64
}

// admit is the executable CLOSED / OPEN / HALF_OPEN model. A lease bounds
// recovery after a process dies while holding the sole half-open probe.
func (s circuitState) admit(now time.Time) (circuitState, bool, time.Time) {
	n := now.UnixNano()
	if s.openUntil > n {
		return s, false, time.Unix(0, s.openUntil)
	}
	if s.leaseUntil > n {
		return s, false, time.Unix(0, s.leaseUntil)
	}
	if s.openUntil != 0 || s.leaseUntil != 0 {
		s.epoch++
		s.openUntil = 0
		s.leaseUntil = now.Add(probeLease).UnixNano()
		return s, true, time.Time{}
	}
	return s, false, time.Time{}
}

func (s circuitState) complete(p attemptPermit, outcome attemptOutcome, now time.Time) circuitState {
	// A late probe or a success admitted before a newer failure cannot heal it.
	if p.probe && p.epoch != s.epoch {
		return s
	}
	if p.epoch < s.recoveryFloor {
		return s
	}
	if outcome == attemptNeutral {
		// Cancellation is not evidence of health. A canceled probe yields its
		// lease into cooldown, without erasing the failures that opened it.
		if p.probe {
			s.epoch++
			s.openUntil = now.Add(circuitCooldown).UnixNano()
			s.leaseUntil = 0
		}
		return s
	}
	if outcome == attemptHealthy {
		if p.epoch == s.epoch {
			s.failures, s.openUntil, s.leaseUntil = 0, 0, 0
			if p.probe {
				s.recoveryFloor = p.epoch
			}
		}
		return s
	}
	if !p.probe && (s.openUntil != 0 || s.leaseUntil != 0) {
		return s
	}
	s.epoch++
	s.failures++
	if p.probe || s.failures >= circuitThreshold {
		s.openUntil = now.Add(circuitCooldown).UnixNano()
		s.leaseUntil = 0
	}
	return s
}

func tokenKey(token string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(token))) }

// An empty path is a genuinely isolated database for HTTP fixture tests.
func openQuotaStore(path string) (*quotaStore, error) {
	dsn := ":memory:?_txlock=immediate&_busy_timeout=1000"
	if path != "" {
		if !filepath.IsAbs(path) {
			return nil, fmt.Errorf("provider state path must be absolute")
		}
		root, err := openExportRoot(filepath.Dir(path))
		if err != nil {
			return nil, err
		}
		defer root.Close()
		info, err := root.Stat(".")
		if err != nil || info.Mode().Perm()&0077 != 0 {
			return nil, fmt.Errorf("provider state directory must be private")
		}
		name := filepath.Base(path)
		f, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		if os.IsExist(err) {
			info, statErr := root.Lstat(name)
			if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
				return nil, fmt.Errorf("provider state must be a private regular file")
			}
		} else if err != nil {
			return nil, err
		} else {
			f.Close()
		}
		dsn = (&url.URL{Scheme: "file", Path: path}).String() + "?mode=rw&_txlock=immediate&_busy_timeout=1000"
	}
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS reservations (token TEXT NOT NULL, at INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS reservations_token_at ON reservations(token,at);
CREATE TABLE IF NOT EXISTS cooldowns (token TEXT PRIMARY KEY, until_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS circuits (token TEXT NOT NULL, family TEXT NOT NULL, failures INTEGER NOT NULL, epoch INTEGER NOT NULL, open_until INTEGER NOT NULL, lease_until INTEGER NOT NULL, recovery_floor INTEGER NOT NULL, PRIMARY KEY(token,family));`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &quotaStore{db: db}, nil
}

func readCircuit(ctx context.Context, tx *sql.Tx, token, family string) (circuitState, error) {
	var s circuitState
	err := tx.QueryRowContext(ctx, `SELECT failures,epoch,open_until,lease_until,recovery_floor FROM circuits WHERE token=? AND family=?`, token, family).Scan(&s.failures, &s.epoch, &s.openUntil, &s.leaseUntil, &s.recoveryFloor)
	if err == sql.ErrNoRows {
		err = nil
	}
	return s, err
}
func writeCircuit(ctx context.Context, tx *sql.Tx, token, family string, s circuitState) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO circuits VALUES(?,?,?,?,?,?,?) ON CONFLICT(token,family) DO UPDATE SET failures=excluded.failures,epoch=excluded.epoch,open_until=excluded.open_until,lease_until=excluded.lease_until,recovery_floor=excluded.recovery_floor`, token, family, s.failures, s.epoch, s.openUntil, s.leaseUntil, s.recoveryFloor)
	return err
}
func stateFailure() Result {
	return Result{Status: "not_attempted", Error: "local provider state unavailable; request not dispatched", ErrorCode: "local_state", Recovery: "contact_operator"}
}
func waitResult(code string, until time.Time) Result {
	message := "provider outage circuit is cooling down"
	if code == "rate_limited" {
		message = "provider rate limited; shared request allowance is unavailable"
	}
	return Result{Status: "not_attempted", Error: message, ErrorCode: code, Recovery: "wait", RetryAt: until.UTC().Format(time.RFC3339)}
}

// Reservation and probe admission commit together before HTTP dispatch. Slots
// are never refunded: after a crash the provider may already have counted one.
func (q *quotaStore) reserve(ctx context.Context, token, family string, now time.Time) (attemptPermit, Result) {
	p := attemptPermit{token: token, family: family}
	if q == nil {
		return p, stateFailure()
	}
	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return p, stateFailure()
	}
	defer tx.Rollback()
	var until int64
	err = tx.QueryRowContext(ctx, `SELECT until_at FROM cooldowns WHERE token=?`, token).Scan(&until)
	if err != nil && err != sql.ErrNoRows {
		return p, stateFailure()
	}
	if until > now.UnixNano() {
		return p, waitResult("rate_limited", time.Unix(0, until))
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM reservations WHERE at<=?`, now.Add(-quotaWindow).UnixNano()); err != nil {
		return p, stateFailure()
	}
	var count int
	var earliest sql.NullInt64
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),MIN(at) FROM reservations WHERE token=?`, token).Scan(&count, &earliest); err != nil {
		return p, stateFailure()
	}
	if count >= quotaLimit {
		return p, waitResult("rate_limited", time.Unix(0, earliest.Int64).Add(quotaWindow))
	}
	s, err := readCircuit(ctx, tx, token, family)
	if err != nil {
		return p, stateFailure()
	}
	s, probe, retryAt := s.admit(now)
	p.probe = probe
	if !retryAt.IsZero() {
		return p, waitResult("upstream_unavailable", retryAt)
	}
	p.epoch = s.epoch
	if err = writeCircuit(ctx, tx, token, family, s); err != nil {
		return p, stateFailure()
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO reservations(token,at) VALUES(?,?)`, token, now.UnixNano()); err != nil {
		return p, stateFailure()
	}
	if err = tx.Commit(); err != nil {
		return p, stateFailure()
	}
	return p, Result{}
}
func (q *quotaStore) complete(ctx context.Context, p attemptPermit, transient bool, now time.Time, deferUntil ...time.Time) error {
	var familyUntil time.Time
	if len(deferUntil) > 0 {
		familyUntil = deferUntil[0]
	}
	outcome := attemptHealthy
	if transient {
		outcome = attemptTransient
	}
	_, err := q.record(ctx, p, outcome, now, familyUntil, time.Time{})
	return err
}
func (q *quotaStore) record(ctx context.Context, p attemptPermit, outcome attemptOutcome, now, familyUntil, tokenUntil time.Time) (time.Time, error) {
	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return time.Time{}, err
	}
	defer tx.Rollback()
	s, err := readCircuit(ctx, tx, p.token, p.family)
	if err != nil {
		return time.Time{}, err
	}
	s = s.complete(p, outcome, now)
	if familyUntil.After(now) {
		// A server-directed delay is scoped to this operation family. It
		// fences in-flight successes just like an outage opening the circuit.
		s.epoch++
		if familyUntil.UnixNano() > s.openUntil {
			s.openUntil = familyUntil.UnixNano()
		}
		s.leaseUntil = 0
	}
	if err = writeCircuit(ctx, tx, p.token, p.family, s); err != nil {
		return time.Time{}, err
	}
	if !tokenUntil.IsZero() {
		if _, err := tx.ExecContext(ctx, `INSERT INTO cooldowns(token,until_at) VALUES(?,?) ON CONFLICT(token) DO UPDATE SET until_at=MAX(until_at,excluded.until_at)`, p.token, tokenUntil.UnixNano()); err != nil {
			return time.Time{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return time.Time{}, err
	}
	if s.openUntil > now.UnixNano() {
		return time.Unix(0, s.openUntil), nil
	}
	return time.Time{}, nil
}
