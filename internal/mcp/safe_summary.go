package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	"personal-mcp-gateway/internal/audit"
	"personal-mcp-gateway/internal/limits"
)

type SummarySection uint8
type BoolMetric uint8
type CounterMetric uint8
type EnumMetric uint8
type EnumValue uint8
type PathSlot uint8
type JSONKind uint8

const (
	SectionArguments SummarySection = iota + 1
	SectionResult
)

const (
	BoolPresent BoolMetric = iota + 1
	BoolOK
	BoolExists
	BoolIsError
	BoolTruncated
	BoolResultComplete
	BoolScopeComplete
	BoolTooLarge
	BoolUnknownKeyTruncated
	BoolRegex
	BoolCaseSensitive
)

const (
	CounterRawBytes CounterMetric = iota + 1
	CounterEntryCount
	CounterFilesScanned
	CounterBytesScanned
	CounterResponseBytes
	CounterLimit
	CounterUnknownKeyCount
	CounterUnknownKeyTooLarge
	CounterRequestCount
	CounterItemCount
	CounterItemErrorCount
	CounterMatchCount
	CounterRemainingCount
	CounterSourceEntriesValidated
	CounterPatternBytes
	CounterMaxFiles
	CounterMaxBytes
	CounterContextLines
)

const (
	EnumShape EnumMetric = iota + 1
	EnumResultType
	EnumStoppedBy
	EnumContinuation
	EnumConsistency
	EnumErrorCode
	EnumSelectorKind
)

const (
	ValueInvalidJSON EnumValue = iota + 1
	ValueFile
	ValueDirectory
	ValueSymlink
	ValueOther
	ValueScope
	ValueResultLimit
	ValueResponseLimit
	ValueTimeout
	ValueCanceled
	ValueSourceChange
	ValueError
	ValueComplete
	ValueCursor
	ValueRestart
	ValueStable
	ValueBestEffort
	ValuePathDenied
	ValueSymlinkDenied
	ValueNotFound
	ValueNotDirectory
	ValueLimitExceeded
	ValueInputTooLarge
	ValueCursorInvalid
	ValueCursorMismatch
	ValueCursorStale
	ValueResponseTooLarge
	ValueNull
	ValueString
	ValueBoolean
	ValueNumber
	ValueArray
	ValueObject
	ValueFileLimit
	ValueByteLimit
	ValueContent
	ValueHeading
	ValueBlock
	ValueFrontmatter
	ValueOutline
	ValueUnsupportedFile
	ValueInvalidUTF8
	ValueInvalidSelector
	ValueSelectorNotFound
	ValueSelectorAmbiguous
	ValueInvalidRegex
)

const (
	PathInput PathSlot = iota + 1
	PathBase
)

const (
	JSONNull JSONKind = iota + 1
	JSONString
	JSONBoolean
	JSONNumber
	JSONArray
	JSONObject
)

type SafeSummaryBuilder struct {
	runID    string
	sections map[string]map[string]any
	sealed   bool
}

type safeSummaryEnvelope struct{ raw json.RawMessage }

var errSafeSummaryTooLarge = errors.New("safe summary exceeds its byte budget")

func newSafeSummaryBuilder(runID string) *SafeSummaryBuilder {
	return &SafeSummaryBuilder{runID: runID, sections: map[string]map[string]any{}}
}

func (b *SafeSummaryBuilder) Bool(section SummarySection, metric BoolMetric, value bool) error {
	key, ok := boolMetricName(metric)
	if !ok {
		return errors.New("invalid boolean metric")
	}
	if !boolMetricAllowed(section, metric) {
		return errors.New("boolean metric is not valid for section")
	}
	part, err := b.section(section)
	if err != nil {
		return err
	}
	return setSummaryField(part, key, value)
}

func (b *SafeSummaryBuilder) Counter(section SummarySection, metric CounterMetric, value uint64) error {
	key, ok := counterMetricName(metric)
	if !ok {
		return errors.New("invalid counter metric")
	}
	if !counterMetricAllowed(section, metric) {
		return errors.New("counter metric is not valid for section")
	}
	part, err := b.section(section)
	if err != nil {
		return err
	}
	return setSummaryField(part, key, value)
}

func (b *SafeSummaryBuilder) Enum(section SummarySection, metric EnumMetric, value EnumValue) error {
	key, ok := enumMetricName(metric)
	if !ok {
		return errors.New("invalid enum metric")
	}
	encoded, ok := enumValueName(value)
	if !ok {
		return errors.New("invalid enum value")
	}
	if !enumMetricAllowed(section, metric) {
		return errors.New("enum metric is not valid for section")
	}
	if !enumValueAllowed(metric, value) {
		return errors.New("enum value is not valid for metric")
	}
	if metric == EnumErrorCode && value == ValueSourceChange {
		encoded = "source_changed"
	}
	part, err := b.section(section)
	if err != nil {
		return err
	}
	return setSummaryField(part, key, encoded)
}

func (b *SafeSummaryBuilder) Path(slot PathSlot, present bool, kind JSONKind, value string) error {
	if b == nil || b.sealed {
		return errors.New("summary builder is sealed")
	}
	key := ""
	switch slot {
	case PathInput:
		key = "path"
	case PathBase:
		key = "base"
	default:
		return errors.New("invalid path slot")
	}
	encodedKind, ok := jsonKindName(kind)
	if !ok {
		return errors.New("invalid JSON kind")
	}
	part, err := b.section(SectionArguments)
	if err != nil {
		return err
	}
	if _, exists := part[key]; exists {
		return errors.New("duplicate summary field")
	}
	shape := map[string]any{"present": present, "type": encodedKind}
	if present && kind == JSONString {
		valueBytes := len([]byte(value))
		shape["bytes"] = valueBytes
		if valueBytes > limits.PathMaxBytes {
			shape["too_large"] = true
			shape["hash"] = audit.HashString(b.runID, value)
			return setSummaryField(part, key, shape)
		}
		normalized := strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
		shape["empty"] = normalized == ""
		shape["absolute"] = strings.HasPrefix(normalized, "/")
		shape["hash"] = audit.HashString(b.runID, normalized)
		segments := summaryPathSegments(path.Clean(normalized))
		shape["segments"] = len(segments)
		shape["too_many_segments"] = len(segments) > limits.PathMaxSegments
		for _, segment := range segments {
			if segment == ".." {
				shape["traversal"] = true
			}
			if strings.HasPrefix(segment, ".") {
				shape["hidden_segment"] = true
			}
		}
	}
	return setSummaryField(part, key, shape)
}

func (b *SafeSummaryBuilder) Cursor(present bool, byteLen uint64) error {
	part, err := b.section(SectionArguments)
	if err != nil {
		return err
	}
	return setSummaryField(part, "cursor", map[string]any{"present": present, "bytes": byteLen})
}

// UnknownArgumentKeys records bounded metadata about argument keys that are
// outside a descriptor's schema. Values are deliberately never inspected or
// hashed. Sorting before hashing makes the summary independent of Go map order.
func (b *SafeSummaryBuilder) UnknownArgumentKeys(object map[string]any, knownKeys ...string) error {
	if b == nil || b.sealed {
		return errors.New("summary builder is sealed")
	}
	known := make(map[string]struct{}, len(knownKeys))
	for _, key := range knownKeys {
		known[key] = struct{}{}
	}

	unknown := make([]string, 0, len(object))
	for key := range object {
		if _, ok := known[key]; !ok {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)

	if err := b.Counter(SectionArguments, CounterUnknownKeyCount, uint64(len(unknown))); err != nil {
		return err
	}
	tooLarge := 0
	hashes := make([]string, 0, min(len(unknown), limits.TelemetryMaxKeys))
	for _, key := range unknown {
		if len([]byte(key)) > limits.TelemetryMaxKeyBytes {
			tooLarge++
		}
		if len(hashes) < limits.TelemetryMaxKeys {
			hashes = append(hashes, audit.HashString(b.runID, key))
		}
	}
	if tooLarge > 0 {
		if err := b.Counter(SectionArguments, CounterUnknownKeyTooLarge, uint64(tooLarge)); err != nil {
			return err
		}
	}
	if len(unknown) > len(hashes) {
		if err := b.Bool(SectionArguments, BoolUnknownKeyTruncated, true); err != nil {
			return err
		}
	}
	return b.unknownKeyHashes(hashes)
}

func (b *SafeSummaryBuilder) unknownKeyHashes(hashes []string) error {
	part, err := b.section(SectionArguments)
	if err != nil {
		return err
	}
	return setSummaryField(part, "unknown_key_hashes", append([]string(nil), hashes...))
}

func setSummaryField(part map[string]any, key string, value any) error {
	if _, exists := part[key]; exists {
		return errors.New("duplicate summary field")
	}
	part[key] = value
	return nil
}

func (b *SafeSummaryBuilder) section(section SummarySection) (map[string]any, error) {
	if b == nil || b.sealed {
		return nil, errors.New("summary builder is sealed")
	}
	name := ""
	switch section {
	case SectionArguments:
		name = "arguments"
	case SectionResult:
		name = "result"
	default:
		return nil, errors.New("invalid summary section")
	}
	if b.sections[name] == nil {
		b.sections[name] = map[string]any{}
	}
	return b.sections[name], nil
}

func (b *SafeSummaryBuilder) seal() (safeSummaryEnvelope, error) {
	return b.sealWithLimit(limits.TelemetryEventBytes)
}

func (b *SafeSummaryBuilder) sealWithLimit(maxBytes int) (safeSummaryEnvelope, error) {
	if b == nil || b.sealed {
		return safeSummaryEnvelope{}, errors.New("summary builder is sealed")
	}
	b.sealed = true
	out := make(map[string]any, len(b.sections))
	for key, section := range b.sections {
		copySection := make(map[string]any, len(section))
		for metric, value := range section {
			copySection[metric] = value
		}
		out[key] = copySection
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return safeSummaryEnvelope{}, fmt.Errorf("marshal safe summary: %w", err)
	}
	if len(raw) > maxBytes {
		return safeSummaryEnvelope{}, errSafeSummaryTooLarge
	}
	var validated map[string]map[string]any
	if err := json.Unmarshal(raw, &validated); err != nil {
		return safeSummaryEnvelope{}, fmt.Errorf("validate safe summary: %w", err)
	}
	for section := range validated {
		if section != "arguments" && section != "result" {
			return safeSummaryEnvelope{}, errors.New("invalid safe summary section")
		}
	}
	return safeSummaryEnvelope{raw: append(json.RawMessage(nil), raw...)}, nil
}

func (e safeSummaryEnvelope) MarshalJSON() ([]byte, error) {
	if len(e.raw) == 0 {
		return []byte("{}"), nil
	}
	return append([]byte(nil), e.raw...), nil
}

func (b *SafeSummaryBuilder) resultErrorCode() (string, bool) {
	if b == nil {
		return "", false
	}
	result := b.sections["result"]
	if result == nil {
		return "", false
	}
	code, ok := result["error_code"].(string)
	return code, ok
}

func boolMetricName(v BoolMetric) (string, bool) {
	names := map[BoolMetric]string{BoolPresent: "present", BoolOK: "ok", BoolExists: "exists", BoolIsError: "is_error", BoolTruncated: "truncated", BoolResultComplete: "result_complete", BoolScopeComplete: "scope_complete", BoolTooLarge: "too_large", BoolUnknownKeyTruncated: "unknown_key_truncated", BoolRegex: "regex", BoolCaseSensitive: "case_sensitive"}
	s, ok := names[v]
	return s, ok
}
func counterMetricName(v CounterMetric) (string, bool) {
	names := map[CounterMetric]string{CounterRawBytes: "raw_bytes", CounterEntryCount: "entry_count", CounterFilesScanned: "files_scanned", CounterBytesScanned: "bytes_scanned", CounterResponseBytes: "response_bytes", CounterLimit: "limit", CounterUnknownKeyCount: "unknown_key_count", CounterUnknownKeyTooLarge: "unknown_key_too_large_count", CounterRequestCount: "request_count", CounterItemCount: "item_count", CounterItemErrorCount: "item_error_count", CounterMatchCount: "match_count", CounterRemainingCount: "remaining_count", CounterSourceEntriesValidated: "source_entries_validated", CounterPatternBytes: "pattern_bytes", CounterMaxFiles: "max_files", CounterMaxBytes: "max_bytes", CounterContextLines: "context_lines"}
	s, ok := names[v]
	return s, ok
}
func enumMetricName(v EnumMetric) (string, bool) {
	names := map[EnumMetric]string{EnumShape: "shape", EnumResultType: "result_type", EnumStoppedBy: "stopped_by", EnumContinuation: "continuation", EnumConsistency: "consistency", EnumErrorCode: "error_code", EnumSelectorKind: "selector_kind"}
	s, ok := names[v]
	return s, ok
}
func enumValueName(v EnumValue) (string, bool) {
	names := map[EnumValue]string{ValueInvalidJSON: "invalid_json", ValueFile: "file", ValueDirectory: "directory", ValueSymlink: "symlink", ValueOther: "other", ValueScope: "scope", ValueResultLimit: "result_limit", ValueResponseLimit: "response_limit", ValueTimeout: "timeout", ValueCanceled: "canceled", ValueSourceChange: "source_change", ValueError: "error", ValueComplete: "complete", ValueCursor: "cursor", ValueRestart: "restart", ValueStable: "stable", ValueBestEffort: "best_effort", ValuePathDenied: "path_denied", ValueSymlinkDenied: "symlink_denied", ValueNotFound: "not_found", ValueNotDirectory: "not_directory", ValueLimitExceeded: "limit_exceeded", ValueInputTooLarge: "input_too_large", ValueCursorInvalid: "cursor_invalid", ValueCursorMismatch: "cursor_mismatch", ValueCursorStale: "cursor_stale", ValueResponseTooLarge: "response_too_large", ValueNull: "null", ValueString: "string", ValueBoolean: "boolean", ValueNumber: "number", ValueArray: "array", ValueObject: "object", ValueFileLimit: "file_limit", ValueByteLimit: "byte_limit", ValueContent: "content", ValueHeading: "heading", ValueBlock: "block", ValueFrontmatter: "frontmatter", ValueOutline: "outline", ValueUnsupportedFile: "unsupported_file", ValueInvalidUTF8: "invalid_utf8", ValueInvalidSelector: "invalid_selector", ValueSelectorNotFound: "selector_not_found", ValueSelectorAmbiguous: "selector_ambiguous", ValueInvalidRegex: "invalid_regex"}
	s, ok := names[v]
	return s, ok
}

func boolMetricAllowed(section SummarySection, metric BoolMetric) bool {
	switch section {
	case SectionArguments:
		return metric == BoolPresent || metric == BoolTooLarge || metric == BoolUnknownKeyTruncated || metric == BoolRegex || metric == BoolCaseSensitive
	case SectionResult:
		return metric == BoolPresent || metric == BoolOK || metric == BoolExists || metric == BoolIsError ||
			metric == BoolTruncated || metric == BoolResultComplete || metric == BoolScopeComplete
	default:
		return false
	}
}

func counterMetricAllowed(section SummarySection, metric CounterMetric) bool {
	switch section {
	case SectionArguments:
		return metric == CounterRawBytes || metric == CounterLimit || metric == CounterUnknownKeyCount || metric == CounterUnknownKeyTooLarge ||
			metric == CounterRequestCount || metric == CounterPatternBytes || metric == CounterMaxFiles || metric == CounterMaxBytes || metric == CounterContextLines
	case SectionResult:
		return metric == CounterEntryCount || metric == CounterFilesScanned || metric == CounterBytesScanned || metric == CounterResponseBytes ||
			metric == CounterItemCount || metric == CounterItemErrorCount || metric == CounterMatchCount || metric == CounterRemainingCount || metric == CounterSourceEntriesValidated
	default:
		return false
	}
}

func enumMetricAllowed(section SummarySection, metric EnumMetric) bool {
	switch section {
	case SectionArguments:
		return metric == EnumShape || metric == EnumSelectorKind
	case SectionResult:
		return metric == EnumResultType || metric == EnumStoppedBy || metric == EnumContinuation ||
			metric == EnumConsistency || metric == EnumErrorCode
	default:
		return false
	}
}

func enumValueAllowed(metric EnumMetric, value EnumValue) bool {
	switch metric {
	case EnumShape:
		return value == ValueInvalidJSON || value == ValueNull || value == ValueString || value == ValueBoolean ||
			value == ValueNumber || value == ValueArray || value == ValueObject || value == ValueOther
	case EnumResultType:
		return value == ValueFile || value == ValueDirectory || value == ValueSymlink || value == ValueOther
	case EnumStoppedBy:
		return value == ValueScope || value == ValueResultLimit || value == ValueResponseLimit || value == ValueTimeout ||
			value == ValueCanceled || value == ValueSourceChange || value == ValueError || value == ValueFileLimit || value == ValueByteLimit
	case EnumContinuation:
		return value == ValueComplete || value == ValueCursor || value == ValueRestart
	case EnumConsistency:
		return value == ValueStable || value == ValueBestEffort
	case EnumErrorCode:
		return value == ValuePathDenied || value == ValueSymlinkDenied || value == ValueNotFound || value == ValueNotDirectory ||
			value == ValueLimitExceeded || value == ValueInputTooLarge || value == ValueTimeout || value == ValueCanceled ||
			value == ValueSourceChange || value == ValueCursorInvalid || value == ValueCursorMismatch || value == ValueCursorStale ||
			value == ValueResponseTooLarge || value == ValueUnsupportedFile || value == ValueInvalidUTF8 || value == ValueInvalidSelector ||
			value == ValueSelectorNotFound || value == ValueSelectorAmbiguous || value == ValueInvalidRegex
	case EnumSelectorKind:
		return value == ValueContent || value == ValueHeading || value == ValueBlock || value == ValueFrontmatter || value == ValueOutline
	default:
		return false
	}
}
func jsonKindName(v JSONKind) (string, bool) {
	names := map[JSONKind]string{JSONNull: "null", JSONString: "string", JSONBoolean: "boolean", JSONNumber: "number", JSONArray: "array", JSONObject: "object"}
	s, ok := names[v]
	return s, ok
}

func summaryPathSegments(value string) []string {
	if value == "" || value == "." {
		return nil
	}
	parts := strings.Split(value, "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" && part != "." {
			out = append(out, part)
		}
	}
	return out
}
