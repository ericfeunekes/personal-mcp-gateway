package obsidian

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"personal-mcp-gateway/internal/fsx"
)

func TestMutationFingerprintRequiresCanonicalRawBase64URL(t *testing.T) {
	fingerprint := fsx.SourceFingerprint{0xfb, 2, 3}
	encoded := base64.RawURLEncoding.EncodeToString(fingerprint[:])
	got, err := decodeMutationFingerprint(encoded)
	if err != nil || got != fingerprint {
		t.Fatalf("decodeMutationFingerprint() = %x, %v", got, err)
	}
	for _, input := range []string{"", encoded + "=", base64.RawStdEncoding.EncodeToString(fingerprint[:])} {
		if _, err := decodeMutationFingerprint(input); err == nil {
			t.Fatalf("decodeMutationFingerprint(%q) unexpectedly succeeded", input)
		}
	}
}

func TestApplyMutationPatchRequiresUniqueNonOverlappingMatches(t *testing.T) {
	got, err := applyMutationPatch([]byte("one two three"), MutationEncodingUTF8, []EditReplacement{{Old: "three", New: "3"}, {Old: "one", New: "1"}})
	if err != nil || string(got) != "1 two 3" {
		t.Fatalf("applyMutationPatch() = %q, %v", got, err)
	}
	for _, replacements := range [][]EditReplacement{
		{{Old: "one two", New: "1"}, {Old: "two", New: "2"}},
		{{Old: "o", New: "x"}},
	} {
		if _, err := applyMutationPatch([]byte("one two one"), MutationEncodingUTF8, replacements); err == nil {
			t.Fatalf("applyMutationPatch(%#v) unexpectedly succeeded", replacements)
		}
	}
	if _, err := applyMutationPatch([]byte("aaa"), MutationEncodingUTF8, []EditReplacement{{Old: "aa", New: "x"}}); !errors.Is(err, errMutationPatch) {
		t.Fatalf("overlapping ambiguous match err = %v, want invalid patch", err)
	}
}

func TestDecodeMutationValueRejectsNonCanonicalOrInvalidUTF8(t *testing.T) {
	if _, err := decodeMutationValue(MutationEncodingBase64, "YQ", MutationMaxValueBytes); err == nil {
		t.Fatal("unpadded standard base64 unexpectedly succeeded")
	}
	if _, err := decodeMutationValue(MutationEncodingUTF8, string([]byte{0xff}), MutationMaxValueBytes); err == nil {
		t.Fatal("invalid UTF-8 unexpectedly succeeded")
	}
	got, err := decodeMutationValue(MutationEncodingBase64, "YQ==", MutationMaxValueBytes)
	if err != nil || string(got) != "a" {
		t.Fatalf("decodeMutationValue() = %q, %v", got, err)
	}
}

func TestApplyMutationPatchEnforcesAggregateDecodedValueLimit(t *testing.T) {
	old := strings.Repeat("a", MutationMaxValueBytes/2)
	atLimit := strings.Repeat("b", MutationMaxValueBytes-len(old))
	if _, err := applyMutationPatch([]byte(old), MutationEncodingUTF8, []EditReplacement{{Old: old, New: atLimit}}); err != nil {
		t.Fatalf("aggregate patch at limit failed: %v", err)
	}
	overLimit := atLimit + "b"
	if _, err := applyMutationPatch([]byte(old), MutationEncodingUTF8, []EditReplacement{{Old: old, New: overLimit}}); !errors.Is(err, errMutationPatch) {
		t.Fatalf("aggregate patch over limit err = %v, want invalid patch", err)
	}
}

func TestApplyMutationPatchEnforcesResultLimit(t *testing.T) {
	source := append(bytes.Repeat([]byte{'a'}, MutationMaxFileBytes-1), 'z')
	if got, err := applyMutationPatch(source, MutationEncodingUTF8, []EditReplacement{{Old: "z", New: "y"}}); err != nil || len(got) != MutationMaxFileBytes {
		t.Fatalf("result at limit len=%d err=%v", len(got), err)
	}
	if _, err := applyMutationPatch(source, MutationEncodingUTF8, []EditReplacement{{Old: "z", New: "zz"}}); !fsx.IsCode(err, fsx.CodeInputTooLarge) {
		t.Fatalf("result over limit err = %v, want input_too_large", err)
	}
}
