//go:build darwin

package fsx

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestMutationBuiltGatewayCandidateCreateReplace(t *testing.T) {
	candidate := filepath.Join(t.TempDir(), "personal-mcp-gateway")
	cmd := exec.Command("go", "build", "-buildvcs=false", "-trimpath", "-o", candidate, "../../cmd/gateway")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build gateway candidate: %v\n%s", err, output)
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "Notes"), 0o700); err != nil {
		t.Fatal(err)
	}
	vault, err := NewVault(root)
	if err != nil {
		t.Fatal(err)
	}
	mutator := NewMutator(vault)
	mutator.helperExecutable = candidate
	created, err := mutator.CreateFile(context.Background(), ".", "notes/New.md", []byte("first"))
	if err != nil {
		t.Fatal(err)
	}
	if created.Resolved.Rel != "Notes/New.md" || created.Resolved.Size != 5 || created.Fingerprint == (SourceFingerprint{}) {
		t.Fatalf("create result = %+v", created)
	}
	createdStat, err := mutator.StatMutationTarget(context.Background(), ".", "Notes/New.md")
	if err != nil || createdStat != (MutationTarget{Resolved: created.Resolved, Fingerprint: created.Fingerprint}) {
		t.Fatalf("create follow-up stat = %+v, %v; result = %+v", createdStat, err, created)
	}
	if _, err := mutator.CreateFile(context.Background(), ".", "Notes/new.MD", []byte("collision")); !IsCode(err, CodeDestinationExists) {
		t.Fatalf("canonical collision err = %v", err)
	}
	replaced, err := mutator.ReplaceFile(context.Background(), ".", "Notes/New.md", created.Fingerprint, []byte("complete replacement"))
	if err != nil {
		t.Fatal(err)
	}
	if replaced.Fingerprint == created.Fingerprint || replaced.Resolved.Size != int64(len("complete replacement")) {
		t.Fatalf("replace result = %+v", replaced)
	}
	replacedStat, err := mutator.StatMutationTarget(context.Background(), ".", "Notes/New.md")
	if err != nil || replacedStat != (MutationTarget{Resolved: replaced.Resolved, Fingerprint: replaced.Fingerprint}) {
		t.Fatalf("replace follow-up stat = %+v, %v; result = %+v", replacedStat, err, replaced)
	}
	if _, err := mutator.ReplaceFile(context.Background(), ".", "Notes/New.md", created.Fingerprint, []byte("stale")); !IsCode(err, CodeSourceChanged) {
		t.Fatalf("stale replace err = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "Notes", "New.md"))
	if err != nil || string(got) != "complete replacement" {
		t.Fatalf("target = %q, %v", got, err)
	}
	stages, err := filepath.Glob(filepath.Join(root, "Notes", ".pmcg-stage-*"))
	if err != nil || len(stages) != 0 {
		t.Fatalf("stages = %v, %v", stages, err)
	}
}

func TestMutationCreateAndMoveDestinationConfinement(t *testing.T) {
	candidate := buildMutationGatewayCandidate(t)
	container := t.TempDir()
	root := filepath.Join(container, "vault")
	outside := filepath.Join(container, "outside")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "source.md"), []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "non-target.md"), []byte("non-target"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "keep.md"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	mutator := mutationTestMutator(t, root, candidate)
	source, err := mutator.StatMutationTarget(context.Background(), ".", "source.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		path string
		code Code
	}{
		{name: "traversal", path: "../escape.md", code: CodePathDenied},
		{name: "absolute", path: filepath.Join(outside, "absolute.md"), code: CodePathDenied},
		{name: "hidden", path: ".hidden.md", code: CodePathDenied},
		{name: "symlinked parent", path: "linked/escape.md", code: CodeSymlinkDenied},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := mutator.CreateFile(context.Background(), ".", test.path, []byte("create")); !IsCode(err, test.code) {
				t.Fatalf("create err = %v, want %s", err, test.code)
			}
			if _, err := mutator.Move(context.Background(), ".", "source.md", test.path, source.Fingerprint); !IsCode(err, test.code) {
				t.Fatalf("move err = %v, want %s", err, test.code)
			}
			assertMutationFileContent(t, filepath.Join(root, "source.md"), "source")
			assertMutationFileContent(t, filepath.Join(root, "non-target.md"), "non-target")
			assertMutationFileContent(t, filepath.Join(outside, "keep.md"), "outside")
			outsideEntries, err := os.ReadDir(outside)
			if err != nil || len(outsideEntries) != 1 || outsideEntries[0].Name() != "keep.md" {
				t.Fatalf("outside entries = %v, %v", outsideEntries, err)
			}
			if _, err := os.Stat(filepath.Join(container, "escape.md")); !os.IsNotExist(err) {
				t.Fatalf("traversal target exists: %v", err)
			}
			if _, err := os.Stat(filepath.Join(root, ".hidden.md")); !os.IsNotExist(err) {
				t.Fatalf("hidden target exists: %v", err)
			}
		})
	}
	assertMutationNoStages(t, root)
}
