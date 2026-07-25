package obsidian

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestDocumentCapacityFixtureIdentity(t *testing.T) {
	data, err := GenerateDocumentCapacityFixture()
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != DocumentFixtureMaxBytes || !bytes.HasPrefix(data, []byte("%PDF-1.7")) || !bytes.HasSuffix(data, []byte("%%EOF\n")) {
		t.Fatalf("fixture boundary = %d bytes", len(data))
	}
	digest := sha256.Sum256(data)
	if got := hex.EncodeToString(digest[:]); got != DocumentFixtureSHA256 {
		t.Fatalf("fixture sha256=%s, want %s", got, DocumentFixtureSHA256)
	}
}

func TestDocumentCapacityBoundary(t *testing.T) {
	if !DocumentSizeAllowed(DocumentFixtureMaxBytes) || DocumentSizeAllowed(RejectedDocumentBytes) {
		t.Fatal("document capacity boundary changed")
	}
}

func TestDocumentReadingBuildModes(t *testing.T) {
	original := documentReadingBuild
	t.Cleanup(func() { documentReadingBuild = original })
	documentReadingBuild = "disabled"
	if DocumentReadingCandidateEnabled() {
		t.Fatal("normal build enabled native document reading")
	}
	documentReadingBuild = "pdf_candidate"
	if !DocumentReadingCandidateEnabled() {
		t.Fatal("PDF candidate build did not enable native document reading")
	}
}
