package testutil

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
)

func FixtureVault(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "home", "projects"))
	mustMkdir(t, filepath.Join(root, "learning"))
	mustMkdir(t, filepath.Join(root, ".obsidian"))
	mustMkdir(t, filepath.Join(root, ".git"))

	mustWrite(t, filepath.Join(root, "README.md"), []byte("synthetic root note\n"))
	mustWrite(t, filepath.Join(root, "home", "projects", "alpha.md"), []byte("synthetic alpha note\n"))
	mustWrite(t, filepath.Join(root, "home", "projects", "beta.md"), []byte("synthetic beta note\n"))
	mustWrite(t, filepath.Join(root, ".obsidian", "workspace.json"), []byte("{}\n"))
	mustWrite(t, filepath.Join(root, ".git", "config"), []byte("[core]\n"))
	mustWrite(t, filepath.Join(root, ".DS_Store"), []byte("synthetic metadata\n"))

	if runtime.GOOS != "windows" {
		mustSymlink(t, filepath.Join(root, "home", "projects"), filepath.Join(root, "project-link"))
		outside := t.TempDir()
		mustWrite(t, filepath.Join(outside, "secret.md"), []byte("outside synthetic content\n"))
		mustSymlink(t, outside, filepath.Join(root, "outside-link"))
	}

	return root
}

func Snapshot(t *testing.T, root string) []string {
	t.Helper()

	var paths []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		paths = append(paths, rel+"|"+info.Mode().String())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	return paths
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}
