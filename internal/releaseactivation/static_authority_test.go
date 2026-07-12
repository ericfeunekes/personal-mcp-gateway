package releaseactivation

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLifecycleArtifactsHaveOneProductionWriter(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	packageDir := filepath.Dir(current)
	repoRoot := filepath.Clean(filepath.Join(packageDir, "..", ".."))
	found, err := findLifecycleArtifactWriters(repoRoot, packageDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, match := range found {
		t.Errorf("production lifecycle artifact marker appears outside authority package: %s", match)
	}
}

func TestLifecycleArtifactWriterScannerRejectsOutsideWriter(t *testing.T) {
	root := t.TempDir()
	packageDir := filepath.Join(root, "internal", "releaseactivation")
	if err := os.MkdirAll(packageDir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "cmd", "other", "main.go")
	if err := os.MkdirAll(filepath.Dir(outside), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("package main\nconst artifact = `manifest.json`\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	found, err := findLifecycleArtifactWriters(root, packageDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || !strings.Contains(found[0], outside) {
		t.Fatalf("scanner failed to identify negative fixture: %v", found)
	}
}

func findLifecycleArtifactWriters(repoRoot, packageDir string) ([]string, error) {
	forbidden := []string{"manifest.json", "active.next.", "cleanup."}
	var found []string
	err := filepath.WalkDir(repoRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".build", ".gocache", "scratch":
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Dir(path) == packageDir || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		ext := filepath.Ext(path)
		if ext != ".go" && ext != ".sh" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, marker := range forbidden {
			if strings.Contains(string(data), marker) {
				found = append(found, marker+":"+path)
			}
		}
		return nil
	})
	return found, err
}
