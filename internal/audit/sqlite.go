package audit

import (
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"

	_ "github.com/mattn/go-sqlite3"
)

type SQLiteSink struct {
	db   *sql.DB
	stmt *sql.Stmt
}

func (s *SQLiteSink) Name() string {
	return "sqlite"
}

func NewSQLite(dbPath, runID string) (*Logger, error) {
	sink, err := OpenSQLiteSink(dbPath)
	if err != nil {
		return nil, err
	}
	return New(runID, sink), nil
}

func OpenSQLiteSink(dbPath string) (*SQLiteSink, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return nil, err
	}

	u := url.URL{Scheme: "file", Path: dbPath}
	q := u.Query()
	q.Set("_busy_timeout", "2000")
	q.Set("_journal_mode", "WAL")
	q.Set("_synchronous", "NORMAL")
	u.RawQuery = q.Encode()

	db, err := sql.Open("sqlite3", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := initSQLite(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	stmt, err := db.Prepare(`
INSERT INTO audit_events (
  ts, run_id, seq, event, transport, method, tool, outcome, error_code, duration_ms, body_json
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`)
	if err != nil {
		_ = db.Close()
		return nil, err
	}

	return &SQLiteSink{db: db, stmt: stmt}, nil
}

func initSQLite(db *sql.DB) error {
	statements := []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA synchronous=NORMAL`,
		`PRAGMA busy_timeout=2000`,
		`CREATE TABLE IF NOT EXISTS audit_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			ts TEXT NOT NULL,
			run_id TEXT NOT NULL,
			seq INTEGER NOT NULL,
			event TEXT NOT NULL,
			transport TEXT,
			method TEXT,
			tool TEXT,
			outcome TEXT,
			error_code TEXT,
			duration_ms INTEGER,
			body_json TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_events_ts ON audit_events(ts)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_events_event_ts ON audit_events(event, ts)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_events_tool_ts ON audit_events(tool, ts)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_events_outcome_ts ON audit_events(outcome, ts)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_events_error_code_ts ON audit_events(error_code, ts)`,
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLiteSink) WriteEvent(record map[string]any) error {
	body, err := json.Marshal(record)
	if err != nil {
		return err
	}
	_, err = s.stmt.Exec(
		stringValue(record["ts"]),
		stringValue(record["run_id"]),
		intValue(record["seq"]),
		stringValue(record["event"]),
		nullableString(record["transport"]),
		nullableString(record["method"]),
		nullableString(record["tool"]),
		nullableString(record["outcome"]),
		nullableString(record["error_code"]),
		nullableInt(record["duration_ms"]),
		string(body),
	)
	return err
}

func (s *SQLiteSink) Close() error {
	var first error
	if s.stmt != nil {
		if err := s.stmt.Close(); err != nil {
			first = err
		}
	}
	if s.db != nil {
		if err := s.db.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func stringValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func nullableString(v any) any {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return nil
}

func nullableInt(v any) any {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return n
	case uint64:
		return n
	case float64:
		return int64(n)
	default:
		return nil
	}
}

func intValue(v any) int64 {
	switch n := v.(type) {
	case int:
		return int64(n)
	case int64:
		return n
	case uint64:
		return int64(n)
	case float64:
		return int64(n)
	default:
		return 0
	}
}
