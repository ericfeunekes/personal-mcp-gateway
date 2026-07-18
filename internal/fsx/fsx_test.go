package fsx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"personal-mcp-gateway/internal/testutil"
)

func TestResolveNormalizesVaultRelativePaths(t *testing.T) {
	vault := newTestVault(t)

	resolved, err := vault.Resolve(context.Background(), "home/projects", "../projects/alpha.md")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Rel != "home/projects/alpha.md" {
		t.Fatalf("Rel = %q", resolved.Rel)
	}
	if !resolved.Exists || resolved.Kind != KindFile {
		t.Fatalf("resolved = %#v", resolved)
	}
}

func TestResolveDeniesUnsafePaths(t *testing.T) {
	vault := newTestVault(t)
	tests := []struct {
		name string
		base string
		path string
		code Code
	}{
		{name: "absolute path", path: filepath.Join(t.TempDir(), "outside"), code: CodePathDenied},
		{name: "escape root", path: "../../outside", code: CodePathDenied},
		{name: "hidden dir", path: ".obsidian/workspace.json", code: CodePathDenied},
		{name: "hidden nested", path: "home/.private", code: CodePathDenied},
		{name: "hidden file", path: ".DS_Store", code: CodePathDenied},
		{name: "hidden base", base: ".git", path: "config", code: CodePathDenied},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := vault.Resolve(context.Background(), tt.base, tt.path)
			assertCode(t, err, tt.code)
			assertNoHostPath(t, err.Error(), vault.Root())
		})
	}
}

func TestResolveRejectsOversizedPathInputs(t *testing.T) {
	vault := newTestVault(t)
	_, err := vault.Resolve(context.Background(), "", strings.Repeat("a", 4097))
	assertCode(t, err, CodeInputTooLarge)

	segments := strings.Repeat("a/", 129) + "note.md"
	_, err = vault.Resolve(context.Background(), "", segments)
	assertCode(t, err, CodeInputTooLarge)
}

func TestResolveReportsDeadlineExceededAsTimeout(t *testing.T) {
	vault := newTestVault(t)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	_, err := vault.Resolve(ctx, "", "README.md")
	assertCode(t, err, CodeTimeout)
}

func TestResolveReportsMissingWithoutError(t *testing.T) {
	vault := newTestVault(t)
	resolved, err := vault.Resolve(context.Background(), "", "missing.md")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Exists {
		t.Fatalf("Exists = true, want false")
	}
	if resolved.Rel != "missing.md" {
		t.Fatalf("Rel = %q", resolved.Rel)
	}
}

func TestListIsShallowSortedBoundedAndFiltersDeniedEntries(t *testing.T) {
	vault := newTestVault(t)

	listed, err := listPage(context.Background(), vault, "", ".", 2)
	if err != nil {
		t.Fatal(err)
	}
	got := names(listed.Entries)
	want := []string{"README.md", "home"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("names = %#v, want %#v", got, want)
	}
	if !listed.HasMore {
		t.Fatalf("HasMore = false, want true")
	}
	for _, entry := range listed.Entries {
		if entry.Name == ".obsidian" || entry.Name == ".git" || entry.Name == ".DS_Store" {
			t.Fatalf("denied entry leaked: %#v", entry)
		}
		if stringsContainsHostPath(entry.Rel, vault.Root()) {
			t.Fatalf("entry path leaked host root: %#v", entry)
		}
	}
}

func TestListRejectsLimitAboveMaximum(t *testing.T) {
	vault := newTestVault(t)
	_, err := listPage(context.Background(), vault, "", ".", MaxLimit+1)
	assertCode(t, err, CodeLimitExceeded)
}

func TestListLargeDirectoryReturnsLexicalPrefixAndTruncates(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "many")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 599; i >= 0; i-- {
		name := fmt.Sprintf("note-%03d.md", i)
		if err := os.WriteFile(filepath.Join(dir, name), []byte("synthetic\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, ".hidden"), []byte("synthetic\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	vault, err := NewVault(root)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	listed, err := listPage(ctx, vault, "", "many", 3)
	if err != nil {
		t.Fatal(err)
	}
	if !listed.HasMore {
		t.Fatalf("HasMore = false, want true")
	}
	got := names(listed.Entries)
	want := []string{"note-000.md", "note-001.md", "note-002.md"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("names = %#v, want %#v", got, want)
	}
}

func TestListRejectsNonDirectoryAndMissingPath(t *testing.T) {
	vault := newTestVault(t)

	_, err := listPage(context.Background(), vault, "", "README.md", 0)
	assertCode(t, err, CodeNotDirectory)

	_, err = listPage(context.Background(), vault, "", "missing", 0)
	assertCode(t, err, CodeNotFound)
}

func TestSymlinkTraversalDenied(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink setup differs on windows")
	}
	vault := newTestVault(t)

	resolved, err := vault.Resolve(context.Background(), "", "project-link")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Kind != KindSymlink {
		t.Fatalf("Kind = %q, want symlink", resolved.Kind)
	}

	_, err = listPage(context.Background(), vault, "", "project-link", 0)
	assertCode(t, err, CodeSymlinkDenied)

	_, err = vault.Resolve(context.Background(), "", "outside-link/secret.md")
	assertCode(t, err, CodeSymlinkDenied)
	assertNoHostPath(t, err.Error(), vault.Root())
}

func TestListHonorsCanceledContext(t *testing.T) {
	vault := newTestVault(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := listPage(ctx, vault, "", ".", 0)
	assertCode(t, err, CodeCanceled)
}

func TestResolveAndListDoNotMutateVault(t *testing.T) {
	root := testutil.FixtureVault(t)
	before := testutil.Snapshot(t, root)
	vault, err := NewVault(root)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := vault.Resolve(context.Background(), "", "home/projects/alpha.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := listPage(context.Background(), vault, "", "home/projects", 0); err != nil {
		t.Fatal(err)
	}

	after := testutil.Snapshot(t, root)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("fixture mutated:\nbefore=%#v\nafter=%#v", before, after)
	}
}

func newTestVault(t *testing.T) *Vault {
	t.Helper()
	vault, err := NewVault(testutil.FixtureVault(t))
	if err != nil {
		t.Fatal(err)
	}
	return vault
}

func listPage(ctx context.Context, vault *Vault, base, input string, limit int) (ListPage, error) {
	directory, err := vault.OpenDir(ctx, base, input)
	if err != nil {
		return ListPage{}, err
	}
	defer directory.Close()
	return directory.ListPage(ctx, ListOptions{Limit: limit})
}

func assertCode(t *testing.T, err error, code Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("err = nil, want %s", code)
	}
	var fsErr *Error
	if !errors.As(err, &fsErr) {
		t.Fatalf("err = %T %v, want *Error", err, err)
	}
	if fsErr.Code != code {
		t.Fatalf("code = %s, want %s", fsErr.Code, code)
	}
}

func assertNoHostPath(t *testing.T, text, root string) {
	t.Helper()
	if stringsContainsHostPath(text, root) {
		t.Fatalf("text leaked host path: %q", text)
	}
}

func stringsContainsHostPath(text, root string) bool {
	return text != "" && (contains(text, root) || contains(text, filepath.Dir(root)) || contains(text, os.Getenv("HOME")))
}

func contains(text, substr string) bool {
	return substr != "" && len(substr) > 1 && strings.Contains(text, substr)
}

func names(entries []Entry) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Name)
	}
	return out
}
