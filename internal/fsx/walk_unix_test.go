package fsx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"personal-mcp-gateway/internal/testutil"
)

func TestWalkFilesIsCanonicalBoundedHiddenAndSymlinkSafe(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	for _, rel := range []string{"z.md", "a.md", "folder/c.md", "folder/B.md", "folder/deeper/d.md"} {
		path := filepath.Join(root, filepath.FromSlash(rel))
		mustMkdirAll(t, filepath.Dir(path))
		if err := os.WriteFile(path, []byte(rel), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mustWriteFile(t, filepath.Join(root, ".hidden.md"))
	mustMkdirAll(t, filepath.Join(root, ".hidden-dir"))
	mustWriteFile(t, filepath.Join(root, ".hidden-dir", "secret.md"))
	mustWriteFile(t, filepath.Join(outside, "outside.md"))
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	vault := mustVault(t, root)

	var got []string
	stats, err := vault.WalkFiles(context.Background(), "", ".", func(ctx context.Context, entry WalkFile) (WalkAction, error) {
		file, err := entry.Open(ctx)
		if err != nil {
			return WalkContinue, err
		}
		defer file.Close()
		buffer := make([]byte, entry.Resolved.Size)
		n, err := file.Read(ctx, buffer)
		if err != nil && !errors.Is(err, io.EOF) {
			return WalkContinue, err
		}
		if string(buffer[:n]) != entry.Resolved.Rel {
			t.Fatalf("content for %q = %q", entry.Resolved.Rel, buffer[:n])
		}
		got = append(got, entry.Resolved.Rel)
		return WalkContinue, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a.md", "folder/B.md", "folder/c.md", "folder/deeper/d.md", "z.md"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("walk = %#v, want %#v", got, want)
	}
	if stats.FilesVisited != uint64(len(want)) || stats.DirectoriesScanned != 3 {
		t.Fatalf("stats = %#v", stats)
	}
	if stats.PeakCandidatesRetained > walkDirectoryEntryLimit {
		t.Fatalf("retained = %d", stats.PeakCandidatesRetained)
	}
}

func TestWalkFilesUsesStrictFullPathOrderForPrefixingDirectoryNames(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"a/z.md", "a.md", "a!/inside.md", "a!.md", "a-/inside.md"} {
		full := filepath.Join(root, filepath.FromSlash(rel))
		mustMkdirAll(t, filepath.Dir(full))
		mustWriteFile(t, full)
	}
	vault := mustVault(t, root)
	var got []string
	stats, err := vault.WalkFiles(context.Background(), "", ".", func(_ context.Context, entry WalkFile) (WalkAction, error) {
		got = append(got, entry.Resolved.Rel)
		return WalkContinue, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a!.md", "a!/inside.md", "a-/inside.md", "a.md", "a/z.md"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("walk = %#v, want strict full-path order %#v", got, want)
	}
	if stats.PeakDirectoriesDeferred != 0 {
		t.Fatalf("stats = %#v, want no deferred directory catalog", stats)
	}
}

func TestWalkFilesCrossesFormerPageBoundariesInTwoLinearScans(t *testing.T) {
	root := t.TempDir()
	entryCount := walkReadBatchSize*3 + 1
	for i := 0; i < entryCount; i++ {
		name := filepath.Join(root, formatWalkFixture(i))
		mustWriteFile(t, name)
	}
	vault := mustVault(t, root)
	var visited uint64
	stats, err := vault.WalkFiles(context.Background(), "", ".", func(_ context.Context, _ WalkFile) (WalkAction, error) {
		visited++
		return WalkContinue, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if visited != uint64(entryCount) || stats.FilesVisited != visited {
		t.Fatalf("visited = %d, stats = %#v", visited, stats)
	}
	if stats.EntriesScanned != uint64(2*entryCount) {
		t.Fatalf("entries scanned = %d, want exactly two linear scans of %d", stats.EntriesScanned, entryCount)
	}
	if stats.PeakCandidatesRetained != entryCount {
		t.Fatalf("peak retained = %d, want one bounded directory sort of %d", stats.PeakCandidatesRetained, entryCount)
	}
}

func TestWalkFilesWorkRemainsLinearAtRealisticLimits(t *testing.T) {
	for _, entryCount := range []int{10_000, 50_000} {
		t.Run(fmt.Sprintf("entries-%d", entryCount), func(t *testing.T) {
			root := t.TempDir()
			for i := 0; i < entryCount; i++ {
				mustWriteFile(t, filepath.Join(root, formatWalkFixture(i)))
			}
			vault := mustVault(t, root)
			var visited int
			var previous Position
			stats, err := vault.WalkFiles(context.Background(), "", ".", func(_ context.Context, entry WalkFile) (WalkAction, error) {
				if visited > 0 && comparePosition(previous, entry.Position) >= 0 {
					t.Fatalf("position %q did not follow %q", entry.Position.Stored, previous.Stored)
				}
				previous = entry.Position
				visited++
				return WalkContinue, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if visited != entryCount || stats.FilesVisited != uint64(entryCount) {
				t.Fatalf("visited = %d, stats = %#v", visited, stats)
			}
			if stats.EntriesScanned != uint64(2*entryCount) {
				t.Fatalf("entries scanned = %d, want %d", stats.EntriesScanned, 2*entryCount)
			}
			if stats.PeakCandidatesRetained != entryCount || stats.PeakCandidatesRetained > walkDirectoryEntryLimit {
				t.Fatalf("retention was not one bounded directory: %#v", stats)
			}
			if stats.PeakDirectoriesDeferred != 0 {
				t.Fatalf("unexpected deferred directory catalog: %#v", stats)
			}
		})
	}
}

func TestWalkFilesSupportsEarlyStopAndCancellation(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.md", "b.md", "c.md"} {
		mustWriteFile(t, filepath.Join(root, name))
	}
	vault := mustVault(t, root)

	stats, err := vault.WalkFiles(context.Background(), "", ".", func(_ context.Context, _ WalkFile) (WalkAction, error) {
		return WalkStop, nil
	})
	if err != nil || stats.FilesVisited != 1 {
		t.Fatalf("early stop stats = %#v, err = %v", stats, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	stats, err = vault.WalkFiles(ctx, "", ".", func(_ context.Context, _ WalkFile) (WalkAction, error) {
		cancel()
		return WalkContinue, nil
	})
	assertCode(t, err, CodeCanceled)
	if stats.FilesVisited != 1 {
		t.Fatalf("canceled stats = %#v", stats)
	}
}

func TestWalkFilesDetectsDirectoryMutationAndDoesNotMutateVault(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "a.md"))
	before := testutil.Snapshot(t, root)
	vault := mustVault(t, root)
	vault.testHooks = &vaultTestHooks{
		afterWalkFile: func(filesVisited uint64) {
			if filesVisited == 1 {
				mustWriteFile(t, filepath.Join(root, "b.md"))
			}
		},
	}

	_, err := vault.WalkFiles(context.Background(), "", ".", func(_ context.Context, _ WalkFile) (WalkAction, error) {
		return WalkContinue, nil
	})
	assertCode(t, err, CodeSourceChanged)
	if err := os.Remove(filepath.Join(root, "b.md")); err != nil {
		t.Fatal(err)
	}
	after := testutil.Snapshot(t, root)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("walk changed existing files:\nbefore=%#v\nafter=%#v", before, after)
	}
}

func TestWalkFilesPinsNestedAncestorAcrossRenameToSymlinkAndRecoversDescriptors(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	insideFile := filepath.Join(root, "parent", "child", "note.md")
	mustMkdirAll(t, filepath.Dir(insideFile))
	if err := os.WriteFile(insideFile, []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(outside, "child", "note.md")
	mustMkdirAll(t, filepath.Dir(outsideFile))
	if err := os.WriteFile(outsideFile, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}

	vault := mustVault(t, root)
	var swapped bool
	vault.testHooks = &vaultTestHooks{
		beforeWalkDescend: func(rel string) {
			if swapped || rel != "parent/child" {
				return
			}
			swapped = true
			if err := os.Rename(filepath.Join(root, "parent"), filepath.Join(root, "parent-original")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(root, "parent")); err != nil {
				t.Fatal(err)
			}
		},
	}

	before := openFDs(t)
	var gotContent string
	_, err := vault.WalkFiles(context.Background(), "", ".", func(ctx context.Context, entry WalkFile) (WalkAction, error) {
		file, err := entry.Open(ctx)
		if err != nil {
			return WalkContinue, err
		}
		defer file.Close()
		buffer := make([]byte, 16)
		n, readErr := file.Read(ctx, buffer)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return WalkContinue, readErr
		}
		gotContent = string(buffer[:n])
		return WalkContinue, nil
	})
	assertCode(t, err, CodeSourceChanged)
	if !swapped {
		t.Fatal("nested ancestor swap hook did not run")
	}
	if gotContent != "inside" {
		t.Fatalf("opened content = %q, want pinned inside content", gotContent)
	}
	if after := openFDs(t); !reflect.DeepEqual(before, after) {
		t.Fatalf("descriptor set changed after swap rejection:\nbefore=%v\nafter=%v", before, after)
	}
}

func TestWalkFilesRejectsExplicitSymlinkAndRecoversDescriptors(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	mustWriteFile(t, filepath.Join(outside, "secret.md"))
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(root, "note.md"))
	vault := mustVault(t, root)
	visitor := func(_ context.Context, _ WalkFile) (WalkAction, error) { return WalkContinue, nil }

	_, err := vault.WalkFiles(context.Background(), "", "linked", visitor)
	assertCode(t, err, CodeSymlinkDenied)
	before := openFDs(t)
	for range 20 {
		if _, err := vault.WalkFiles(context.Background(), "", ".", visitor); err != nil {
			t.Fatal(err)
		}
	}
	if after := openFDs(t); !reflect.DeepEqual(before, after) {
		t.Fatalf("descriptor set changed:\nbefore=%v\nafter=%v", before, after)
	}
}

func formatWalkFixture(index int) string {
	const digits = "0123456789"
	value := []byte("note-00000.md")
	for position := 9; position >= 5; position-- {
		value[position] = digits[index%10]
		index /= 10
	}
	return string(value)
}
