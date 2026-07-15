package obsidian

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"personal-mcp-gateway/internal/fsx"
)

func TestCursorRoundTripAndValidation(t *testing.T) {
	query := LSQueryHash("Projects/Élan", 100)
	source := fsx.SourceFingerprint(sha256.Sum256([]byte("source")))
	want := fsx.Position{NFC: "é.md", Stored: "é.md"}

	encoded, err := EncodeCursor(ToolLS, query, source, want)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(encoded, "=") || len(encoded) > MaxCursorBytes {
		t.Fatalf("cursor is not bounded raw base64url: len=%d", len(encoded))
	}
	decoded, err := DecodeCursor(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Tool != ToolLS || decoded.Query != query || decoded.Source != source || decoded.Position != want {
		t.Fatalf("decoded cursor = %#v", decoded)
	}
	got, err := ValidateCursor(encoded, ToolLS, query, source)
	if err != nil || got != want {
		t.Fatalf("ValidateCursor() = %#v, %v", got, err)
	}
}

func TestLSQueryHashBindsCanonicalPathLimitAndResponseContract(t *testing.T) {
	base := LSQueryHash("Projects/Élan", 100)
	if base != LSQueryHash("Projects/Élan", 100) {
		t.Fatal("identical normalized query produced different hashes")
	}
	if base == LSQueryHash("projects/élan", 100) {
		t.Fatal("canonical path change did not change hash")
	}
	if base == LSQueryHash("Projects/Élan", 99) {
		t.Fatal("effective limit change did not change hash")
	}
}

func TestValidateCursorMapsMismatchAndStaleness(t *testing.T) {
	query := LSQueryHash(".", 1)
	source := fsx.SourceFingerprint(sha256.Sum256([]byte("source")))
	encoded, err := EncodeCursor(ToolLS, query, source, fsx.Position{NFC: "a", Stored: "a"})
	if err != nil {
		t.Fatal(err)
	}

	otherSource := fsx.SourceFingerprint(sha256.Sum256([]byte("other")))
	tests := []struct {
		name   string
		tool   string
		query  CursorQueryHash
		source fsx.SourceFingerprint
		want   error
		code   string
	}{
		{name: "tool", tool: "read", query: query, source: source, want: ErrCursorMismatch, code: CursorMismatchCode},
		{name: "query", tool: ToolLS, query: LSQueryHash(".", 2), source: source, want: ErrCursorMismatch, code: CursorMismatchCode},
		{name: "source", tool: ToolLS, query: query, source: otherSource, want: ErrCursorStale, code: CursorStaleCode},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, gotErr := ValidateCursor(encoded, tt.tool, tt.query, tt.source)
			if !errors.Is(gotErr, tt.want) || CursorErrorCode(gotErr) != tt.code || CursorErrorMessage(gotErr) == "" {
				t.Fatalf("error = %v code=%q message=%q", gotErr, CursorErrorCode(gotErr), CursorErrorMessage(gotErr))
			}
		})
	}
}

func TestDecodeCursorRejectsMalformedStrictly(t *testing.T) {
	queryDigest := sha256.Sum256([]byte("query"))
	sourceDigest := sha256.Sum256([]byte("source"))
	query := encodeDigest(queryDigest[:])
	source := encodeDigest(sourceDigest[:])
	valid := cursorEnvelope{
		Version: cursorFormatV1,
		Tool:    ToolLS,
		Query:   query,
		Source:  source,
		Position: cursorPosition{
			NFC:    "a",
			Stored: "a",
		},
	}
	validJSON, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	unknown := strings.TrimSuffix(string(validJSON), "}") + `,"unknown":true}`
	unsupported := valid
	unsupported.Version++
	unsupportedJSON, err := json.Marshal(unsupported)
	if err != nil {
		t.Fatal(err)
	}
	missing := valid
	missing.Tool = ""
	missingJSON, err := json.Marshal(missing)
	if err != nil {
		t.Fatal(err)
	}

	tests := []string{
		"",
		"not+base64",
		base64.RawURLEncoding.EncodeToString(validJSON) + "=",
		base64.RawURLEncoding.EncodeToString([]byte(unknown)),
		base64.RawURLEncoding.EncodeToString(append(validJSON, []byte(` {}`)...)),
		base64.RawURLEncoding.EncodeToString(unsupportedJSON),
		base64.RawURLEncoding.EncodeToString(missingJSON),
		strings.Repeat("A", MaxCursorBytes),
		strings.Repeat("A", MaxCursorBytes+1),
	}
	for i, encoded := range tests {
		if _, gotErr := DecodeCursor(encoded); !errors.Is(gotErr, ErrCursorInvalid) {
			t.Fatalf("case %d error = %v, want cursor_invalid", i, gotErr)
		}
	}
}

func TestEncodeCursorRejectsOverBoundedFields(t *testing.T) {
	query := LSQueryHash(".", 1)
	source := fsx.SourceFingerprint(sha256.Sum256([]byte("source")))
	exact := strings.Repeat("a", maxCursorFieldBytes)
	encoded, err := EncodeCursor(ToolLS, query, source, fsx.Position{NFC: exact, Stored: exact})
	if err != nil {
		t.Fatalf("exact field boundary failed: %v", err)
	}
	if _, err := DecodeCursor(encoded); err != nil {
		t.Fatalf("decode exact field boundary: %v", err)
	}

	_, err = EncodeCursor(ToolLS, query, source, fsx.Position{
		NFC:    strings.Repeat("a", maxCursorFieldBytes+1),
		Stored: "a",
	})
	if !errors.Is(err, ErrCursorInvalid) {
		t.Fatalf("error = %v, want cursor_invalid", err)
	}
}

func TestUnsignedCursorPositionRemainsUntrustedButStructurallyValid(t *testing.T) {
	query := LSQueryHash(".", 1)
	source := fsx.SourceFingerprint(sha256.Sum256([]byte("source")))
	encoded, err := EncodeCursor(ToolLS, query, source, fsx.Position{NFC: "a", Stored: "a"})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeCursor(encoded)
	if err != nil {
		t.Fatal(err)
	}
	rewritten := fsx.Position{NFC: "z", Stored: "z"}
	encoded, err = EncodeCursor(decoded.Tool, decoded.Query, decoded.Source, rewritten)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ValidateCursor(encoded, ToolLS, query, source)
	if err != nil || got != rewritten {
		t.Fatalf("ValidateCursor() = %#v, %v", got, err)
	}
}
