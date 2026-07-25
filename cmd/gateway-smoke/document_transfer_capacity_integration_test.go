package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"personal-mcp-gateway/internal/tools/obsidian"
)

func TestVaultProofSnapshotDetectsSameSizeNestedContentMutation(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(nested, "proof.pdf")
	if err := os.WriteFile(path, []byte("same-size-a"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	before, err := vaultProofSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("same-size-b"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	after, err := vaultProofSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("recursive vault snapshot missed same-size content mutation")
	}
}

// This exact-size gate is opt-in because it transfers three 49,999,999-byte
// resources over each production transport and observes bounded cleanup. The
// release workflow runs the same production paths unconditionally before
// activation.
func TestExactNativeDocumentCapacityCandidate(t *testing.T) {
	if os.Getenv("ISSUE4_EXACT_DOCUMENT_GATE") != "1" {
		t.Skip("set ISSUE4_EXACT_DOCUMENT_GATE=1 after building the PDF candidate")
	}
	candidate, err := filepath.Abs(filepath.Join("..", "..", ".build", "personal-mcp-gateway"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(candidate)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	root := t.TempDir()
	artifact := filepath.Join(root, "capacity.pdf")
	report, err := probeDocumentTransferCapacity(ctx, candidate, artifact, candidateProvenance{
		Commit: "local-exact-gate", CandidateSHA256: hex.EncodeToString(digest[:]), DependencySHA256: "local-exact-gate",
	}, systemResourceSampler{})
	if err != nil {
		t.Fatalf("exact native document gate: %v; report=%+v", err, report)
	}
	if !report.Passed || report.SequentialCallCount != 3 {
		t.Fatalf("exact native document report did not pass: %+v", report)
	}
}

func TestExactNativeDocumentHTTPCandidate(t *testing.T) {
	if os.Getenv("ISSUE4_EXACT_DOCUMENT_HTTP_GATE") != "1" {
		t.Skip("set ISSUE4_EXACT_DOCUMENT_HTTP_GATE=1 after building the PDF candidate")
	}
	candidate, err := filepath.Abs(filepath.Join("..", "..", ".build", "personal-mcp-gateway"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	artifact := filepath.Join(root, "capacity.pdf")
	fixture, err := obsidian.GenerateDocumentCapacityFixture()
	if err != nil || os.WriteFile(artifact, fixture, 0o600) != nil {
		t.Fatal("write exact HTTP fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	report, err := probeExactHTTPDocumentTransfer(ctx, candidate, root, filepath.Base(artifact), systemResourceSampler{})
	if err != nil {
		t.Fatalf("exact native document HTTP gate: %v; report=%+v", err, report)
	}
	if !documentTransferHTTPReportPasses(report) {
		t.Fatalf("exact native document HTTP report did not pass: %+v", report)
	}
}
