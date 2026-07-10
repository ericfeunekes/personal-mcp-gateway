package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunProbesBuiltGatewayCandidate(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	candidate := filepath.Join(t.TempDir(), "personal-mcp-gateway")
	build := exec.Command("go", "build", "-buildvcs=false", "-o", candidate, "./cmd/gateway")
	build.Dir = repoRoot
	build.Env = os.Environ()
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build candidate: %v\n%s", err, output)
	}

	vault := t.TempDir()
	var stderr bytes.Buffer
	if err := run([]string{
		"--gateway-bin", candidate,
		"--obsidian-root", vault,
	}, &stderr); err != nil {
		t.Fatalf("run() failed: %v\nstderr=%s", err, stderr.String())
	}
}

func TestRunSanitizesCandidateStartFailure(t *testing.T) {
	privateCandidate := filepath.Join(t.TempDir(), "missing-gateway")
	privateVault := t.TempDir()
	var stderr bytes.Buffer
	err := run([]string{
		"--gateway-bin", privateCandidate,
		"--obsidian-root", privateVault,
	}, &stderr)
	if err == nil {
		t.Fatal("run() succeeded with missing candidate")
	}
	for _, privatePath := range []string{privateCandidate, privateVault, filepath.Dir(privateVault)} {
		if strings.Contains(err.Error(), privatePath) || strings.Contains(stderr.String(), privatePath) {
			t.Fatalf("smoke failure leaked private path %q: err=%q stderr=%q", privatePath, err, stderr.String())
		}
	}
}
