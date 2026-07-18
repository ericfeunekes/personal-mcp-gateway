package obsidian

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"

	"personal-mcp-gateway/internal/fsx"
)

const (
	cursorFormatV2      = 2
	maxCursorToolBytes  = 64
	maxCursorFieldBytes = 4096
)

const cursorSealDomain = "personal-mcp-gateway/obsidian-cursor-envelope/v2"

const (
	CursorInvalidCode  = "cursor_invalid"
	CursorMismatchCode = "cursor_mismatch"
	CursorStaleCode    = "cursor_stale"
)

var (
	ErrCursorInvalid  = errors.New(CursorInvalidCode)
	ErrCursorMismatch = errors.New(CursorMismatchCode)
	ErrCursorStale    = errors.New(CursorStaleCode)
)

// CursorQueryHash binds a cursor to one normalized public query.
type CursorQueryHash [sha256.Size]byte

// DecodedCursor is the authenticated, self-contained cursor payload. Source,
// query, and canonical-boundary membership still require live revalidation.
type DecodedCursor struct {
	Tool     string
	Query    CursorQueryHash
	Source   fsx.SourceFingerprint
	Position fsx.Position
	State    json.RawMessage
}

type cursorEnvelope struct {
	Version  int             `json:"version"`
	Tool     string          `json:"tool"`
	Query    string          `json:"query"`
	Source   string          `json:"source"`
	Position cursorPosition  `json:"position"`
	State    json.RawMessage `json:"state,omitempty"`
	Seal     string          `json:"seal"`
}

type cursorEnvelopeFields struct {
	Version  int             `json:"version"`
	Tool     string          `json:"tool"`
	Query    string          `json:"query"`
	Source   string          `json:"source"`
	Position cursorPosition  `json:"position"`
	State    json.RawMessage `json:"state,omitempty"`
}

type cursorPosition struct {
	NFC    string `json:"nfc"`
	Stored string `json:"stored"`
}

type cursorSealer interface {
	BindOpaque(string, []byte) fsx.OpaqueBinding
	VerifyOpaque(string, []byte, fsx.OpaqueBinding) bool
}

// LSQueryHash hashes the canonical directory identity, effective limit, and
// response contract version with length framing. Caller spelling must not be
// passed here after canonical resolution is available.
func LSQueryHash(canonicalDirectory string, effectiveLimit int) CursorQueryHash {
	h := sha256.New()
	writeHashField(h, []byte(ToolLS))
	writeHashField(h, []byte(canonicalDirectory))
	var number [8]byte
	binary.BigEndian.PutUint64(number[:], uint64(effectiveLimit))
	writeHashField(h, number[:])
	binary.BigEndian.PutUint64(number[:], uint64(ResponseContractV1))
	writeHashField(h, number[:])

	var out CursorQueryHash
	copy(out[:], h.Sum(nil))
	return out
}

// RetrievalQueryHash binds a normalized typed query to one tool and response
// contract. Callers must normalize caller path identity and apply defaults
// before calling it; canonical source identity remains independently bound by
// opaque fingerprints, and cursor payloads never retain the query itself.
func RetrievalQueryHash(tool string, normalized any) (CursorQueryHash, error) {
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return CursorQueryHash{}, err
	}
	h := sha256.New()
	writeHashField(h, []byte(tool))
	writeHashField(h, encoded)
	var number [8]byte
	binary.BigEndian.PutUint64(number[:], uint64(ResponseContractV1))
	writeHashField(h, number[:])
	var out CursorQueryHash
	copy(out[:], h.Sum(nil))
	return out, nil
}

func writeHashField(w io.Writer, value []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = w.Write(size[:])
	_, _ = w.Write(value)
}

// EncodeCursor returns a sealed raw, unpadded base64url cursor. Live source and
// boundary checks still run on continuation after authenticity is established.
func EncodeCursor(sealer cursorSealer, tool string, query CursorQueryHash, source fsx.SourceFingerprint, position fsx.Position) (string, error) {
	if !validCursorString(tool, maxCursorToolBytes) ||
		!validCursorString(position.NFC, maxCursorFieldBytes) ||
		!validCursorString(position.Stored, maxCursorFieldBytes) {
		return "", ErrCursorInvalid
	}

	envelope := cursorEnvelope{
		Version: cursorFormatV2,
		Tool:    tool,
		Query:   encodeDigest(query[:]),
		Source:  encodeDigest(source[:]),
		Position: cursorPosition{
			NFC:    position.NFC,
			Stored: position.Stored,
		},
	}
	return encodeSealedCursorEnvelope(sealer, envelope)
}

// EncodeCursorState uses the same strict sealed cursor envelope for bounded
// retrieval-specific state. The typed state must contain only positions,
// digests, counters, and compact observations—not request or content values.
func EncodeCursorState(sealer cursorSealer, tool string, query CursorQueryHash, state any) (string, error) {
	if !validCursorString(tool, maxCursorToolBytes) || state == nil {
		return "", ErrCursorInvalid
	}
	encodedState, err := json.Marshal(state)
	if err != nil || len(encodedState) == 0 || string(encodedState) == "null" {
		return "", ErrCursorInvalid
	}
	return encodeSealedCursorEnvelope(sealer, cursorEnvelope{
		Version: cursorFormatV2,
		Tool:    tool,
		Query:   encodeDigest(query[:]),
		State:   encodedState,
	})
}

func encodeSealedCursorEnvelope(sealer cursorSealer, envelope cursorEnvelope) (string, error) {
	if sealer == nil {
		return "", ErrCursorInvalid
	}
	fields, err := json.Marshal(envelope.fields())
	if err != nil {
		return "", ErrCursorInvalid
	}
	seal := sealer.BindOpaque(cursorSealDomain, fields)
	if seal == (fsx.OpaqueBinding{}) {
		return "", ErrCursorInvalid
	}
	envelope.Seal = encodeDigest(seal[:])
	decoded, err := json.Marshal(envelope)
	if err != nil || len(decoded) > MaxCursorBytes || base64.RawURLEncoding.EncodedLen(len(decoded)) > MaxCursorBytes {
		return "", ErrCursorInvalid
	}
	return base64.RawURLEncoding.EncodeToString(decoded), nil
}

func (envelope cursorEnvelope) fields() cursorEnvelopeFields {
	return cursorEnvelopeFields{
		Version: envelope.Version, Tool: envelope.Tool, Query: envelope.Query,
		Source: envelope.Source, Position: envelope.Position, State: envelope.State,
	}
}

// DecodeCursor validates the canonical v2 shape and root-bound seal before
// returning any derived cursor state. Query, source, and membership checks
// remain operation-boundary responsibilities.
func DecodeCursor(sealer cursorSealer, encoded string) (DecodedCursor, error) {
	if encoded == "" || len(encoded) > MaxCursorBytes || !validRawBase64URL(encoded) {
		return DecodedCursor{}, ErrCursorInvalid
	}
	decodedLen := base64.RawURLEncoding.DecodedLen(len(encoded))
	if decodedLen <= 0 || decodedLen > MaxCursorBytes {
		return DecodedCursor{}, ErrCursorInvalid
	}
	decoded := make([]byte, decodedLen)
	n, err := base64.RawURLEncoding.Decode(decoded, []byte(encoded))
	if err != nil || n <= 0 || n > MaxCursorBytes {
		return DecodedCursor{}, ErrCursorInvalid
	}
	decoded = decoded[:n]

	var envelope cursorEnvelope
	decoder := json.NewDecoder(bytes.NewReader(decoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return DecodedCursor{}, ErrCursorInvalid
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return DecodedCursor{}, ErrCursorInvalid
	}
	canonical, err := json.Marshal(envelope)
	if err != nil || !bytes.Equal(decoded, canonical) {
		return DecodedCursor{}, ErrCursorInvalid
	}
	if envelope.Version != cursorFormatV2 || !validCursorString(envelope.Tool, maxCursorToolBytes) || sealer == nil {
		return DecodedCursor{}, ErrCursorInvalid
	}
	sealBytes, err := decodeDigest(envelope.Seal)
	if err != nil {
		return DecodedCursor{}, ErrCursorInvalid
	}
	var seal fsx.OpaqueBinding
	copy(seal[:], sealBytes)
	fields, err := json.Marshal(envelope.fields())
	if err != nil || !sealer.VerifyOpaque(cursorSealDomain, fields, seal) {
		return DecodedCursor{}, ErrCursorInvalid
	}

	queryBytes, err := decodeDigest(envelope.Query)
	if err != nil {
		return DecodedCursor{}, ErrCursorInvalid
	}
	var query CursorQueryHash
	copy(query[:], queryBytes)
	if len(envelope.State) > 0 {
		if envelope.Source != "" || envelope.Position.NFC != "" || envelope.Position.Stored != "" ||
			!json.Valid(envelope.State) || string(envelope.State) == "null" {
			return DecodedCursor{}, ErrCursorInvalid
		}
		return DecodedCursor{Tool: envelope.Tool, Query: query, State: append(json.RawMessage(nil), envelope.State...)}, nil
	}
	if !validCursorString(envelope.Position.NFC, maxCursorFieldBytes) ||
		!validCursorString(envelope.Position.Stored, maxCursorFieldBytes) {
		return DecodedCursor{}, ErrCursorInvalid
	}
	sourceBytes, err := decodeDigest(envelope.Source)
	if err != nil {
		return DecodedCursor{}, ErrCursorInvalid
	}
	var source fsx.SourceFingerprint
	copy(source[:], sourceBytes)
	return DecodedCursor{
		Tool:   envelope.Tool,
		Query:  query,
		Source: source,
		Position: fsx.Position{
			NFC:    envelope.Position.NFC,
			Stored: envelope.Position.Stored,
		},
	}, nil
}

// DecodeCursorState validates the shared envelope, tool, and normalized query,
// then strictly decodes the tool-owned bounded state.
func DecodeCursorState[T any](sealer cursorSealer, encoded, expectedTool string, expectedQuery CursorQueryHash) (T, error) {
	var zero T
	decoded, err := DecodeCursor(sealer, encoded)
	if err != nil {
		return zero, ErrCursorInvalid
	}
	if decoded.Tool != expectedTool || decoded.Query != expectedQuery {
		return zero, ErrCursorMismatch
	}
	if len(decoded.State) == 0 {
		return zero, ErrCursorInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(decoded.State))
	decoder.DisallowUnknownFields()
	var state T
	if err := decoder.Decode(&state); err != nil {
		return zero, ErrCursorInvalid
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return zero, ErrCursorInvalid
	}
	return state, nil
}

func validRawBase64URL(value string) bool {
	for i := 0; i < len(value); i++ {
		b := value[i]
		if (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') ||
			(b >= '0' && b <= '9') || b == '-' || b == '_' {
			continue
		}
		return false
	}
	return true
}

// ValidateCursor maps structural failures, query changes, and source changes
// to distinct stable public errors. Confinement and boundary-existence checks
// remain mandatory after this function returns the untrusted position.
func ValidateCursor(sealer cursorSealer, encoded, expectedTool string, expectedQuery CursorQueryHash, expectedSource fsx.SourceFingerprint) (fsx.Position, error) {
	decoded, err := DecodeCursor(sealer, encoded)
	if err != nil {
		return fsx.Position{}, ErrCursorInvalid
	}
	if decoded.Tool != expectedTool || decoded.Query != expectedQuery {
		return fsx.Position{}, ErrCursorMismatch
	}
	if len(decoded.State) != 0 {
		return fsx.Position{}, ErrCursorInvalid
	}
	if decoded.Source != expectedSource {
		return fsx.Position{}, ErrCursorStale
	}
	return decoded.Position, nil
}

// CursorErrorCode returns the stable public code for a cursor validation error.
func CursorErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrCursorInvalid):
		return CursorInvalidCode
	case errors.Is(err, ErrCursorMismatch):
		return CursorMismatchCode
	case errors.Is(err, ErrCursorStale):
		return CursorStaleCode
	default:
		return ""
	}
}

// CursorErrorMessage returns the sanitized model-facing cursor error message.
func CursorErrorMessage(err error) string {
	switch CursorErrorCode(err) {
	case CursorMismatchCode:
		return "cursor does not match query"
	case CursorStaleCode:
		return "cursor source changed"
	case CursorInvalidCode:
		return "cursor is invalid"
	default:
		return ""
	}
}

func validCursorString(value string, max int) bool {
	return value != "" && len([]byte(value)) <= max
}

func encodeDigest(value []byte) string {
	return base64.RawURLEncoding.EncodeToString(value)
}

func decodeDigest(value string) ([]byte, error) {
	if len(value) != base64.RawURLEncoding.EncodedLen(sha256.Size) {
		return nil, ErrCursorInvalid
	}
	decoded := make([]byte, sha256.Size)
	n, err := base64.RawURLEncoding.Decode(decoded, []byte(value))
	if err != nil || n != sha256.Size {
		return nil, ErrCursorInvalid
	}
	return decoded, nil
}
