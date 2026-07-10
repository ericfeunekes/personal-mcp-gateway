package audit

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"sync"
	"time"

	"personal-mcp-gateway/internal/limits"
)

type Sink interface {
	WriteEvent(record map[string]any) error
	Close() error
}

type namedSink interface {
	Name() string
}

type Degradation struct {
	Degraded  bool
	Sink      string
	Operation string
	ErrorCode string
	At        time.Time
	Count     uint64
}

type Logger struct {
	mu          sync.Mutex
	sinks       []Sink
	runID       string
	enabled     bool
	seq         uint64
	degradation Degradation
	onDegrade   func(Degradation)
}

func NewJSONL(w io.Writer, runID string) *Logger {
	if w == nil {
		return Disabled()
	}
	return New(runID, &jsonlSink{enc: json.NewEncoder(w)})
}

func New(runID string, sinks ...Sink) *Logger {
	if len(sinks) == 0 {
		return Disabled()
	}
	if runID == "" {
		runID = NewRunID()
	}
	return &Logger{
		sinks:   sinks,
		runID:   runID,
		enabled: true,
	}
}

func Disabled() *Logger {
	return &Logger{}
}

func NewRunID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return time.Now().UTC().Format("20060102T150405.000000000Z")
	}
	return hex.EncodeToString(b[:])
}

func (l *Logger) RunID() string {
	if l == nil {
		return ""
	}
	return l.runID
}

func (l *Logger) Enabled() bool {
	return l != nil && l.enabled
}

func (l *Logger) SetDegradationHandler(handler func(Degradation)) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.onDegrade = handler
}

func (l *Logger) Degraded() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.degradation.Degraded
}

func (l *Logger) Degradation() Degradation {
	if l == nil {
		return Degradation{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.degradation
}

func (l *Logger) Event(name string, attrs map[string]any) {
	if l == nil || !l.enabled {
		return
	}

	l.mu.Lock()

	l.seq++
	record := map[string]any{
		"ts":     time.Now().UTC().Format(time.RFC3339Nano),
		"event":  name,
		"run_id": l.runID,
		"seq":    l.seq,
	}
	for k, v := range attrs {
		if k == "ts" || k == "event" || k == "run_id" || k == "seq" {
			continue
		}
		record[k] = v
	}
	record = enforceRecordBudget(record)

	var notify *Degradation
	for _, sink := range l.sinks {
		if err := sink.WriteEvent(record); err != nil {
			if d := l.markDegradedLocked(sinkName(sink), "write"); d != nil {
				notify = d
			}
		}
	}
	handler := l.onDegrade
	l.mu.Unlock()

	if notify != nil && handler != nil {
		handler(*notify)
	}
}

func (l *Logger) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()

	var first error
	var notify *Degradation
	for _, sink := range l.sinks {
		if err := sink.Close(); err != nil {
			if first == nil {
				first = err
			}
			if d := l.markDegradedLocked(sinkName(sink), "close"); d != nil {
				notify = d
			}
		}
	}
	handler := l.onDegrade
	l.mu.Unlock()

	if notify != nil && handler != nil {
		handler(*notify)
	}
	return first
}

func (l *Logger) markDegradedLocked(sink, operation string) *Degradation {
	l.degradation.Count++
	if l.degradation.Degraded {
		return nil
	}
	l.degradation.Degraded = true
	l.degradation.Sink = sink
	l.degradation.Operation = operation
	l.degradation.ErrorCode = operation + "_failed"
	l.degradation.At = time.Now().UTC()
	out := l.degradation
	return &out
}

func sinkName(sink Sink) string {
	if sink == nil {
		return "unknown"
	}
	if named, ok := sink.(namedSink); ok {
		return named.Name()
	}
	return "sink"
}

func enforceRecordBudget(record map[string]any) map[string]any {
	body, err := json.Marshal(record)
	if err != nil || len(body) <= limits.TelemetryEventBytes {
		return record
	}

	out := map[string]any{
		"body_truncated": true,
		"body_bytes":     len(body),
	}
	for _, key := range []string{
		"ts", "event", "run_id", "seq", "transport", "method", "tool",
		"outcome", "error_code", "duration_ms", "route", "status",
		"is_error", "ok", "exists", "result_type", "truncated", "entry_count",
	} {
		if value, ok := record[key]; ok {
			out[key] = value
		}
	}
	if args, ok := record["args"]; ok {
		out["args"] = truncateNestedSummary(args)
	}
	return out
}

func truncateNestedSummary(value any) map[string]any {
	out := map[string]any{
		"truncated": true,
	}
	if m, ok := value.(map[string]any); ok {
		for _, key := range []string{"present", "bytes", "too_large", "shape"} {
			if v, found := m[key]; found {
				out[key] = v
			}
		}
	}
	return out
}

type jsonlSink struct {
	enc *json.Encoder
}

func (s *jsonlSink) Name() string {
	return "jsonl"
}

func (s *jsonlSink) WriteEvent(record map[string]any) error {
	return s.enc.Encode(record)
}

func (s *jsonlSink) Close() error {
	return nil
}
