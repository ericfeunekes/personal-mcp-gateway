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
	vault := newCursorTestVault(t, t.TempDir())
	query := LSQueryHash("Projects/Élan", 100)
	source := fsx.SourceFingerprint(sha256.Sum256([]byte("source")))
	want := fsx.Position{NFC: "é.md", Stored: "é.md"}

	encoded, err := EncodeCursor(vault, ToolLS, query, source, want)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(encoded, "=") || len(encoded) > MaxCursorBytes {
		t.Fatalf("cursor is not bounded raw base64url: len=%d", len(encoded))
	}
	decoded, err := DecodeCursor(vault, encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Tool != ToolLS || decoded.Query != query || decoded.Source != source || decoded.Position != want {
		t.Fatalf("decoded cursor = %#v", decoded)
	}
	got, err := ValidateCursor(vault, encoded, ToolLS, query, source)
	if err != nil || got != want {
		t.Fatalf("ValidateCursor() = %#v, %v", got, err)
	}
}

func TestCursorSealIsRestartStableAndDifferentRootInvalid(t *testing.T) {
	root := t.TempDir()
	first := newCursorTestVault(t, root)
	second := newCursorTestVault(t, root)
	query := LSQueryHash(".", 1)
	source := fsx.SourceFingerprint(sha256.Sum256([]byte("source")))
	token, err := EncodeCursor(first, ToolLS, query, source, fsx.Position{NFC: "a", Stored: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeCursor(second, token); err != nil {
		t.Fatalf("second same-root Vault rejected unchanged token: %v", err)
	}
	other := newCursorTestVault(t, t.TempDir())
	if _, err := DecodeCursor(other, token); !errors.Is(err, ErrCursorInvalid) {
		t.Fatalf("different-root error = %v, want cursor_invalid", err)
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
	vault := newCursorTestVault(t, t.TempDir())
	query := LSQueryHash(".", 1)
	source := fsx.SourceFingerprint(sha256.Sum256([]byte("source")))
	encoded, err := EncodeCursor(vault, ToolLS, query, source, fsx.Position{NFC: "a", Stored: "a"})
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
			_, gotErr := ValidateCursor(vault, encoded, tt.tool, tt.query, tt.source)
			if !errors.Is(gotErr, tt.want) || CursorErrorCode(gotErr) != tt.code || CursorErrorMessage(gotErr) == "" {
				t.Fatalf("error = %v code=%q message=%q", gotErr, CursorErrorCode(gotErr), CursorErrorMessage(gotErr))
			}
		})
	}
}

func TestCursorSealRejectsEveryRangeValidEnvelopeFieldEdit(t *testing.T) {
	vault := newCursorTestVault(t, t.TempDir())
	query := LSQueryHash(".", 1)
	source := fsx.SourceFingerprint(sha256.Sum256([]byte("source")))
	lsToken, err := EncodeCursor(vault, ToolLS, query, source, fsx.Position{NFC: "a", Stored: "a"})
	if err != nil {
		t.Fatal(err)
	}
	stateToken, err := EncodeCursorState(vault, ToolRead, query, map[string]any{"next": 7, "hash": "bounded"})
	if err != nil {
		t.Fatal(err)
	}
	otherDigest := encodeDigest(sha256.New().Sum(make([]byte, 0))[:sha256.Size])

	tests := []struct {
		name  string
		token string
		edit  func(*cursorEnvelope)
	}{
		{name: "version", token: lsToken, edit: func(e *cursorEnvelope) { e.Version = 3 }},
		{name: "tool", token: lsToken, edit: func(e *cursorEnvelope) { e.Tool = ToolRead }},
		{name: "query", token: lsToken, edit: func(e *cursorEnvelope) { e.Query = otherDigest }},
		{name: "source", token: lsToken, edit: func(e *cursorEnvelope) { e.Source = otherDigest }},
		{name: "position nfc", token: lsToken, edit: func(e *cursorEnvelope) { e.Position.NFC = "b" }},
		{name: "position stored", token: lsToken, edit: func(e *cursorEnvelope) { e.Position.Stored = "b" }},
		{name: "state", token: stateToken, edit: func(e *cursorEnvelope) { e.State = json.RawMessage(`{"next":8,"hash":"bounded"}`) }},
		{name: "seal", token: lsToken, edit: func(e *cursorEnvelope) { e.Seal = otherDigest }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tampered := tamperCursorEnvelope(t, tt.token, tt.edit)
			if _, err := DecodeCursor(vault, tampered); !errors.Is(err, ErrCursorInvalid) {
				t.Fatalf("error = %v, want cursor_invalid", err)
			}
		})
	}
}

func TestDecodeCursorRejectsMalformedStrictly(t *testing.T) {
	vault := newCursorTestVault(t, t.TempDir())
	query := LSQueryHash(".", 1)
	source := fsx.SourceFingerprint(sha256.Sum256([]byte("source")))
	valid, err := EncodeCursor(vault, ToolLS, query, source, fsx.Position{NFC: "a", Stored: "a"})
	if err != nil {
		t.Fatal(err)
	}
	validJSON, err := base64.RawURLEncoding.DecodeString(valid)
	if err != nil {
		t.Fatal(err)
	}
	unknown := strings.TrimSuffix(string(validJSON), "}") + `,"unknown":true}`
	unsupported := tamperCursorEnvelope(t, valid, func(envelope *cursorEnvelope) { envelope.Version++ })

	tests := []string{
		"",
		"not+base64",
		valid + "=",
		base64.RawURLEncoding.EncodeToString([]byte(unknown)),
		base64.RawURLEncoding.EncodeToString(append(validJSON, []byte(` {}`)...)),
		unsupported,
		base64.RawURLEncoding.EncodeToString([]byte(" {\"version\":2}")),
		strings.Repeat("A", MaxCursorBytes),
		strings.Repeat("A", MaxCursorBytes+1),
	}
	for i, encoded := range tests {
		if _, gotErr := DecodeCursor(vault, encoded); !errors.Is(gotErr, ErrCursorInvalid) {
			t.Fatalf("case %d error = %v, want cursor_invalid", i, gotErr)
		}
	}
}

func TestEncodeCursorRejectsOverBoundedFields(t *testing.T) {
	vault := newCursorTestVault(t, t.TempDir())
	query := LSQueryHash(".", 1)
	source := fsx.SourceFingerprint(sha256.Sum256([]byte("source")))
	exact := strings.Repeat("a", maxCursorFieldBytes)
	encoded, err := EncodeCursor(vault, ToolLS, query, source, fsx.Position{NFC: exact, Stored: exact})
	if err != nil {
		t.Fatalf("exact field boundary failed: %v", err)
	}
	if _, err := DecodeCursor(vault, encoded); err != nil {
		t.Fatalf("decode exact field boundary: %v", err)
	}

	_, err = EncodeCursor(vault, ToolLS, query, source, fsx.Position{
		NFC: strings.Repeat("a", maxCursorFieldBytes+1), Stored: "a",
	})
	if !errors.Is(err, ErrCursorInvalid) {
		t.Fatalf("error = %v, want cursor_invalid", err)
	}
}

func newCursorTestVault(t *testing.T, root string) *fsx.Vault {
	t.Helper()
	vault, err := fsx.NewVault(root)
	if err != nil {
		t.Fatal(err)
	}
	return vault
}

func tamperCursorEnvelope(t *testing.T, token string, edit func(*cursorEnvelope)) string {
	t.Helper()
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		t.Fatal(err)
	}
	var envelope cursorEnvelope
	if err := json.Unmarshal(decoded, &envelope); err != nil {
		t.Fatal(err)
	}
	edit(&envelope)
	tampered, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(tampered)
}

func tamperCursorState(t *testing.T, token string, edit func(map[string]any)) string {
	t.Helper()
	return tamperCursorEnvelope(t, token, func(envelope *cursorEnvelope) {
		var state map[string]any
		if err := json.Unmarshal(envelope.State, &state); err != nil {
			t.Fatal(err)
		}
		edit(state)
		encoded, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		envelope.State = encoded
	})
}
