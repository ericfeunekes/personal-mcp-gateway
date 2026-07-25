package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"
)

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
