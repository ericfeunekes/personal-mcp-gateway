package fsx

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"golang.org/x/sys/unix"
)

func TestOpenFileUsesStoredSpellingReadsSeeksAndFingerprints(t *testing.T) {
	root := t.TempDir()
	mustMkdirAll(t, filepath.Join(root, "Stored", "Résumé"))
	path := filepath.Join(root, "Stored", "Résumé", "Plan.MD")
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	vault := mustVault(t, root)

	file, err := vault.OpenFile(context.Background(), "stored", "résumé/plan.md")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if got, want := file.Resolved().Rel, "Stored/Résumé/Plan.MD"; got != want {
		t.Fatalf("Rel = %q, want %q", got, want)
	}
	if file.Resolved().Kind != KindFile || file.Resolved().Size != int64(len("alpha\nbeta\n")) {
		t.Fatalf("resolved = %#v", file.Resolved())
	}
	if file.Fingerprint() == (SourceFingerprint{}) {
		t.Fatal("fingerprint is empty")
	}

	buffer := make([]byte, 5)
	if n, err := file.Read(context.Background(), buffer); err != nil || n != 5 || string(buffer) != "alpha" {
		t.Fatalf("Read = %d, %q, %v", n, buffer, err)
	}
	if err := file.Seek(context.Background(), 6); err != nil {
		t.Fatal(err)
	}
	buffer = make([]byte, 5)
	if n, err := file.Read(context.Background(), buffer); err != nil || n != 5 || string(buffer) != "beta\n" {
		t.Fatalf("continued Read = %d, %q, %v", n, buffer, err)
	}
	if n, err := file.Read(context.Background(), buffer); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("EOF Read = %d, %v", n, err)
	}
	if err := file.Revalidate(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertCode(t, file.Seek(context.Background(), file.Resolved().Size+1), CodeLimitExceeded)
	assertCode(t, file.Seek(context.Background(), -1), CodeLimitExceeded)
}

func TestOpenFileRejectsDeniedMissingSymlinkAndNonRegularSources(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "note.md"))
	mustWriteFile(t, filepath.Join(outside, "secret.md"))
	mustMkdirAll(t, filepath.Join(root, "folder"))
	if err := os.Symlink(filepath.Join(outside, "secret.md"), filepath.Join(root, "link.md")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(root, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	vault := mustVault(t, root)

	tests := []struct {
		name string
		base string
		path string
		code Code
	}{
		{name: "absolute", path: filepath.Join(root, "note.md"), code: CodePathDenied},
		{name: "traversal", path: "../note.md", code: CodePathDenied},
		{name: "hidden", path: ".secret", code: CodePathDenied},
		{name: "missing", path: "missing.md", code: CodeNotFound},
		{name: "directory", path: "folder", code: CodeNotFile},
		{name: "symlink", path: "link.md", code: CodeSymlinkDenied},
		{name: "fifo", path: "pipe", code: CodeNotFile},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file, err := vault.OpenFile(context.Background(), tt.base, tt.path)
			if file != nil {
				_ = file.Close()
				t.Fatal("unexpected file")
			}
			assertCode(t, err, tt.code)
		})
	}
}

func TestOpenFileFailsClosedWhenFinalEntryBecomesSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "note.md"))
	mustWriteFile(t, filepath.Join(outside, "secret.md"))
	vault := mustVault(t, root)
	called := false
	vault.testHooks = &vaultTestHooks{
		beforeOpenFile: func() {
			if called {
				return
			}
			called = true
			if err := os.Rename(filepath.Join(root, "note.md"), filepath.Join(root, "old.md")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(outside, "secret.md"), filepath.Join(root, "note.md")); err != nil {
				t.Fatal(err)
			}
		},
	}

	file, err := vault.OpenFile(context.Background(), "", "note.md")
	if file != nil {
		_ = file.Close()
	}
	assertCode(t, err, CodeSymlinkDenied)
	if !called {
		t.Fatal("swap hook was not called")
	}
}

func TestOpenFileFailsClosedWhenIntermediateEntryBecomesSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	mustMkdirAll(t, filepath.Join(root, "inside"))
	mustWriteFile(t, filepath.Join(root, "inside", "note.md"))
	mustWriteFile(t, filepath.Join(outside, "note.md"))
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

	file, err := vault.OpenFile(context.Background(), "", "inside/note.md")
	if file != nil {
		_ = file.Close()
	}
	assertCode(t, err, CodeSymlinkDenied)
	if !called {
		t.Fatal("intermediate swap hook was not called")
	}
}

func TestFileReadDiscardsEvidenceWhenSourceChangesDuringRead(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "note.md")
	if err := os.WriteFile(path, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	vault := mustVault(t, root)
	called := false
	vault.testHooks = &vaultTestHooks{
		afterFileRead: func() {
			if called {
				return
			}
			called = true
			if err := os.WriteFile(path, []byte("after!\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
	}
	file, err := vault.OpenFile(context.Background(), "", "note.md")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	buffer := make([]byte, 32)
	n, err := file.Read(context.Background(), buffer)
	assertCode(t, err, CodeSourceChanged)
	if n != 0 {
		t.Fatalf("Read returned %d changed-source bytes", n)
	}
	if !called {
		t.Fatal("read mutation hook was not called")
	}
}

func TestFileMethodsHonorCancellation(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "note.md"))
	vault := mustVault(t, root)
	file, err := vault.OpenFile(context.Background(), "", "note.md")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if n, err := file.Read(ctx, make([]byte, 8)); n != 0 {
		t.Fatalf("Read returned %d canceled bytes", n)
	} else {
		assertCode(t, err, CodeCanceled)
	}
	assertCode(t, file.Seek(ctx, 0), CodeCanceled)
	assertCode(t, file.Revalidate(ctx), CodeCanceled)
}

func TestOpenFileCloseRecoversDescriptorsAndActivity(t *testing.T) {
	root := t.TempDir()
	mustMkdirAll(t, filepath.Join(root, "one", "two"))
	mustWriteFile(t, filepath.Join(root, "one", "two", "note.md"))
	activity := &ActivityCounter{}
	vault, err := NewVaultWithActivity(root, activity)
	if err != nil {
		t.Fatal(err)
	}
	before := openFDs(t)

	for range 100 {
		file, err := vault.OpenFile(context.Background(), "", "one/two/note.md")
		if err != nil {
			t.Fatal(err)
		}
		if snapshot := activity.Snapshot(); snapshot.Active != 1 {
			t.Fatalf("active with open file = %d, want 1", snapshot.Active)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if got := activity.Snapshot(); got.Total != 100 || got.Active != 0 {
		t.Fatalf("activity = %#v", got)
	}
	if after := openFDs(t); !reflect.DeepEqual(before, after) {
		t.Fatalf("descriptor set changed:\nbefore=%v\nafter=%v", before, after)
	}
}
