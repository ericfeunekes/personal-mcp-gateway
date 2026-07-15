package fsx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"golang.org/x/sys/unix"
	"golang.org/x/text/unicode/norm"
)

func TestResolveUsesStoredSpellingAndUniqueFold(t *testing.T) {
	root := t.TempDir()
	mustMkdirAll(t, filepath.Join(root, "Projects", "Résumé"))
	mustWriteFile(t, filepath.Join(root, "Projects", "Résumé", "Plan.MD"))
	vault := mustVault(t, root)

	resolved, err := vault.Resolve(context.Background(), "", "projects/résumé/plan.md")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := resolved.Rel, "Projects/Résumé/Plan.MD"; got != want {
		t.Fatalf("Rel = %q, want %q", got, want)
	}
	if !resolved.Exists || resolved.Kind != KindFile {
		t.Fatalf("resolved = %#v", resolved)
	}
}

func TestSegmentMatcherAppliesExactNFCAndFoldPrecedence(t *testing.T) {
	tests := []struct {
		name   string
		caller string
		stored []string
		want   string
		found  bool
	}{
		{name: "raw exact wins", caller: "Note.md", stored: []string{"note.md", "Note.md"}, want: "Note.md", found: true},
		{name: "unique NFC", caller: "Café.md", stored: []string{"Cafe\u0301.md"}, want: "Cafe\u0301.md", found: true},
		{name: "unique fold", caller: "PLAN.MD", stored: []string{"Plan.md"}, want: "Plan.md", found: true},
		{name: "ambiguous NFC", caller: "Å.md", stored: []string{"Å.md", "A\u030a.md"}, found: false},
		{name: "ambiguous fold", caller: "NOTE.MD", stored: []string{"Note.md", "note.md"}, found: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matcher := segmentMatcher{caller: tt.caller, callerNFC: norm.NFC.String(tt.caller)}
			for _, stored := range tt.stored {
				if exact, ok := matcher.observe(stored); ok {
					if exact != tt.want || !tt.found {
						t.Fatalf("exact = %q, found = %t", exact, ok)
					}
					return
				}
			}
			got, found, err := matcher.result()
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want || found != tt.found {
				t.Fatalf("result = %q, %t; want %q, %t", got, found, tt.want, tt.found)
			}
		})
	}
}

func TestResolveExactStoredSpellingWinsFoldedAmbiguity(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "Note.md"))
	mustWriteFile(t, filepath.Join(root, "note.md"))
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Skip("filesystem does not retain case-distinct stored names")
	}
	vault := mustVault(t, root)

	exact, err := vault.Resolve(context.Background(), "", "Note.md")
	if err != nil {
		t.Fatal(err)
	}
	if !exact.Exists || exact.Rel != "Note.md" {
		t.Fatalf("exact = %#v", exact)
	}

	ambiguous, err := vault.Resolve(context.Background(), "", "NOTE.MD")
	if err != nil {
		t.Fatal(err)
	}
	if ambiguous.Exists || ambiguous.Rel != "NOTE.MD" {
		t.Fatalf("ambiguous = %#v", ambiguous)
	}
}

func TestResolveCanonicalizesMissingSuffixToNFC(t *testing.T) {
	root := t.TempDir()
	mustMkdirAll(t, filepath.Join(root, "Stored"))
	vault := mustVault(t, root)

	resolved, err := vault.Resolve(context.Background(), "", "stored/Cafe\u0301/Missing.md")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := resolved.Rel, "Stored/Café/Missing.md"; got != want {
		t.Fatalf("Rel = %q, want %q", got, want)
	}
	if resolved.Exists {
		t.Fatalf("Exists = true, want false")
	}
}

func TestResolveRejectsAmbiguousCanonicalEquivalentNames(t *testing.T) {
	root := t.TempDir()
	composed := "Café.md"
	decomposed := "Cafe\u0301.md"
	mustWriteFile(t, filepath.Join(root, composed))
	if err := os.WriteFile(filepath.Join(root, decomposed), []byte("synthetic\n"), 0o600); err != nil {
		if errors.Is(err, os.ErrExist) {
			t.Skip("filesystem canonicalizes equivalent Unicode names")
		}
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Skip("filesystem does not retain canonically equivalent stored names")
	}
	vault := mustVault(t, root)

	resolved, err := vault.Resolve(context.Background(), "", "CAFÉ.MD")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Exists {
		t.Fatalf("resolved = %#v, want ambiguous missing result", resolved)
	}
}

func TestOpenDirFailsClosedWhenMatchedSegmentBecomesSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	mustMkdirAll(t, filepath.Join(root, "inside"))
	mustWriteFile(t, filepath.Join(outside, "secret.md"))
	vault := mustVault(t, root)
	called := false
	vault.testHooks = &vaultTestHooks{
		beforeOpenSegment: func(depth int) {
			if depth != 0 || called {
				return
			}
			called = true
			if err := os.Rename(filepath.Join(root, "inside"), filepath.Join(root, "inside-old")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(root, "inside")); err != nil {
				t.Fatal(err)
			}
		},
	}

	dir, err := vault.OpenDir(context.Background(), "", "inside")
	if dir != nil {
		_ = dir.Close()
	}
	assertCode(t, err, CodeSymlinkDenied)
	if !called {
		t.Fatal("swap hook was not called")
	}
}

func TestOpenDirCloseIsIdempotent(t *testing.T) {
	root := t.TempDir()
	mustMkdirAll(t, filepath.Join(root, "dir"))
	vault := mustVault(t, root)
	dir, err := vault.OpenDir(context.Background(), "", "dir")
	if err != nil {
		t.Fatal(err)
	}
	if err := dir.Close(); err != nil {
		t.Fatal(err)
	}
	if err := dir.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestResolveAndOpenDirRecoverDescriptors(t *testing.T) {
	root := t.TempDir()
	mustMkdirAll(t, filepath.Join(root, "one", "two"))
	mustWriteFile(t, filepath.Join(root, "one", "two", "note.md"))
	vault := mustVault(t, root)
	before := openFDs(t)

	for range 100 {
		if _, err := vault.Resolve(context.Background(), "", "one/two/note.md"); err != nil {
			t.Fatal(err)
		}
		dir, err := vault.OpenDir(context.Background(), "", "one/two")
		if err != nil {
			t.Fatal(err)
		}
		if err := dir.Close(); err != nil {
			t.Fatal(err)
		}
	}

	after := openFDs(t)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("descriptor set changed:\nbefore=%v\nafter=%v", before, after)
	}
}

func openFDs(t *testing.T) []int {
	t.Helper()
	open := make([]int, 0, 16)
	for fd := 0; fd < unix.Getdtablesize(); fd++ {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err == nil {
			open = append(open, fd)
		}
	}
	return open
}

func mustVault(t *testing.T, root string) *Vault {
	t.Helper()
	vault, err := NewVault(root)
	if err != nil {
		t.Fatal(err)
	}
	return vault
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
}

func mustWriteFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("synthetic\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}
