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
	cursorFormatV1      = 1
	maxCursorToolBytes  = 64
	maxCursorFieldBytes = 4096
)

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

// DecodedCursor is the validated, self-contained cursor payload. Position is
// still untrusted and must be checked against the current canonical listing.
type DecodedCursor struct {
	Tool     string
	Query    CursorQueryHash
	Source   fsx.SourceFingerprint
	Position fsx.Position
}

type cursorEnvelope struct {
	Version  int            `json:"version"`
	Tool     string         `json:"tool"`
	Query    string         `json:"query"`
	Source   string         `json:"source"`
	Position cursorPosition `json:"position"`
}

type cursorPosition struct {
	NFC    string `json:"nfc"`
	Stored string `json:"stored"`
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

func writeHashField(w io.Writer, value []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = w.Write(size[:])
	_, _ = w.Write(value)
}

// EncodeCursor returns a raw, unpadded base64url cursor. It deliberately does
// not authenticate the payload; all fields are revalidated on continuation.
func EncodeCursor(tool string, query CursorQueryHash, source fsx.SourceFingerprint, position fsx.Position) (string, error) {
	if !validCursorString(tool, maxCursorToolBytes) ||
		!validCursorString(position.NFC, maxCursorFieldBytes) ||
		!validCursorString(position.Stored, maxCursorFieldBytes) {
		return "", ErrCursorInvalid
	}

	envelope := cursorEnvelope{
		Version: cursorFormatV1,
		Tool:    tool,
		Query:   encodeDigest(query[:]),
		Source:  encodeDigest(source[:]),
		Position: cursorPosition{
			NFC:    position.NFC,
			Stored: position.Stored,
		},
	}
	decoded, err := json.Marshal(envelope)
	if err != nil || len(decoded) > MaxCursorBytes {
		return "", ErrCursorInvalid
	}
	if base64.RawURLEncoding.EncodedLen(len(decoded)) > MaxCursorBytes {
		return "", ErrCursorInvalid
	}
	return base64.RawURLEncoding.EncodeToString(decoded), nil
}

// DecodeCursor performs structural validation only. Query, source, and the
// untrusted position are validated by ValidateCursor at the operation boundary.
func DecodeCursor(encoded string) (DecodedCursor, error) {
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
	if envelope.Version != cursorFormatV1 ||
		!validCursorString(envelope.Tool, maxCursorToolBytes) ||
		!validCursorString(envelope.Position.NFC, maxCursorFieldBytes) ||
		!validCursorString(envelope.Position.Stored, maxCursorFieldBytes) {
		return DecodedCursor{}, ErrCursorInvalid
	}

	queryBytes, err := decodeDigest(envelope.Query)
	if err != nil {
		return DecodedCursor{}, ErrCursorInvalid
	}
	sourceBytes, err := decodeDigest(envelope.Source)
	if err != nil {
		return DecodedCursor{}, ErrCursorInvalid
	}

	var query CursorQueryHash
	copy(query[:], queryBytes)
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
func ValidateCursor(encoded, expectedTool string, expectedQuery CursorQueryHash, expectedSource fsx.SourceFingerprint) (fsx.Position, error) {
	decoded, err := DecodeCursor(encoded)
	if err != nil {
		return fsx.Position{}, ErrCursorInvalid
	}
	if decoded.Tool != expectedTool || decoded.Query != expectedQuery {
		return fsx.Position{}, ErrCursorMismatch
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
