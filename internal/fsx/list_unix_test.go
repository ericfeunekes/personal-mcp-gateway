package fsx

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"personal-mcp-gateway/internal/testutil"
)

func TestListPageConcatenatesInCanonicalOrderWithBoundedRetention(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"z.md", "a.md", "B.md", "b.md", "é.md", "e\u0301.md"} {
		mustWriteFile(t, filepath.Join(root, name))
	}
	mustWriteFile(t, filepath.Join(root, ".hidden"))
	vault := mustVault(t, root)
	rawEntries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	wantScanned := uint64(len(rawEntries))

	var got []string
	var after *Position
	var source SourceFingerprint
	for {
		dir, err := vault.OpenDir(context.Background(), "", ".")
		if err != nil {
			t.Fatal(err)
		}
		page, err := dir.ListPage(context.Background(), ListOptions{Limit: 2, After: after})
		_ = dir.Close()
		if err != nil {
			t.Fatal(err)
		}
		if !page.BoundaryFound {
			t.Fatal("issued boundary was not found")
		}
		if page.CandidatesRetained > 3 {
			t.Fatalf("CandidatesRetained = %d, want <= 3", page.CandidatesRetained)
		}
		if page.BytesScanned != 0 || page.FilesScanned != wantScanned {
			t.Fatalf("work = files:%d bytes:%d", page.FilesScanned, page.BytesScanned)
		}
		if source == (SourceFingerprint{}) {
			source = page.Source
		} else if source != page.Source {
			t.Fatalf("source changed between stable pages")
		}
		for _, entry := range page.Entries {
			got = append(got, entry.Name)
		}
		if !page.HasMore {
			break
		}
		position := page.Entries[len(page.Entries)-1].Position
		after = &position
	}

	dir, err := vault.OpenDir(context.Background(), "", ".")
	if err != nil {
		t.Fatal(err)
	}
	all, err := dir.ListPage(context.Background(), ListOptions{Limit: 100})
	_ = dir.Close()
	if err != nil {
		t.Fatal(err)
	}
	want := names(all.Entries)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("paged = %#v, want %#v", got, want)
	}
	seen := make(map[string]bool, len(got))
	for _, name := range got {
		if seen[name] {
			t.Fatalf("duplicate entry %q", name)
		}
		seen[name] = true
	}
}

func TestMembershipFingerprintIsOrderIndependent(t *testing.T) {
	identity := directoryIdentity{dev: 1, ino: 2, size: 3, mtimeSec: 4, mtimeNsec: 5}
	positions := []Position{
		{NFC: "A", Stored: "A"},
		{NFC: "B", Stored: "b"},
		{NFC: "C", Stored: "c"},
	}
	var forward membershipAccumulator
	for _, position := range positions {
		forward.add(position)
	}
	var reverse membershipAccumulator
	for i := len(positions) - 1; i >= 0; i-- {
		reverse.add(positions[i])
	}
	if got, want := sourceFingerprint(identity, forward), sourceFingerprint(identity, reverse); got != want {
		t.Fatalf("fingerprints differ: %x != %x", got, want)
	}
}

func TestMembershipFingerprintBindsStoredSpelling(t *testing.T) {
	identity := directoryIdentity{dev: 1, ino: 2, size: 3, mtimeSec: 4, mtimeNsec: 5}
	var left membershipAccumulator
	left.add(Position{NFC: "café", Stored: "Café"})
	var right membershipAccumulator
	right.add(Position{NFC: "café", Stored: "café"})
	if sourceFingerprint(identity, left) == sourceFingerprint(identity, right) {
		t.Fatal("fingerprint ignored stored spelling")
	}
}

func TestMembershipFingerprintMatchesFramedReference(t *testing.T) {
	identity := directoryIdentity{dev: 1, ino: 2, size: 3, mtimeSec: 4, mtimeNsec: 5}
	positions := []Position{
		{NFC: "café/A.md", Stored: "cafe\u0301/A.md"},
		{NFC: "café/B.md", Stored: "Café/B.md"},
		{NFC: "z.md", Stored: "z.md"},
	}
	var optimized membershipAccumulator
	for _, position := range positions {
		optimized.add(position)
	}
	reference := framedMembershipReference(positions)
	if got, want := sourceFingerprint(identity, optimized), sourceFingerprint(identity, reference); got != want {
		t.Fatalf("optimized fingerprint = %x, want framed reference %x", got, want)
	}
}

func TestListPageRejectsMissingBoundaryWithoutEmittingEntries(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.md", "b.md", "c.md"} {
		mustWriteFile(t, filepath.Join(root, name))
	}
	vault := mustVault(t, root)
	dir, err := vault.OpenDir(context.Background(), "", ".")
	if err != nil {
		t.Fatal(err)
	}
	page, err := dir.ListPage(context.Background(), ListOptions{
		Limit: 1,
		After: &Position{NFC: "bb.md", Stored: "bb.md"},
	})
	_ = dir.Close()
	if err != nil {
		t.Fatal(err)
	}
	if page.BoundaryFound || len(page.Entries) != 0 || page.HasMore {
		t.Fatalf("page = %#v", page)
	}
}

func TestListPageDetectsDirectoryMutation(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "a.md"))
	vault := mustVault(t, root)
	vault.testHooks = &vaultTestHooks{
		beforeListScan: func() {
			mustWriteFile(t, filepath.Join(root, "b.md"))
		},
	}
	dir, err := vault.OpenDir(context.Background(), "", ".")
	if err != nil {
		t.Fatal(err)
	}
	page, err := dir.ListPage(context.Background(), ListOptions{Limit: 1})
	_ = dir.Close()
	assertCode(t, err, CodeSourceChanged)
	if len(page.Entries) != 0 || page.FilesScanned != 2 {
		t.Fatalf("page = %#v", page)
	}
}

func TestListPageDetectsSelectedEntryReplacement(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "a.md"))
	vault := mustVault(t, root)
	vault.testHooks = &vaultTestHooks{
		afterEntryBaseline: func() {
			if err := os.Rename(filepath.Join(root, "a.md"), filepath.Join(root, "old.md")); err != nil {
				t.Fatal(err)
			}
			mustMkdirAll(t, filepath.Join(root, "a.md"))
		},
	}
	dir, err := vault.OpenDir(context.Background(), "", ".")
	if err != nil {
		t.Fatal(err)
	}
	page, err := dir.ListPage(context.Background(), ListOptions{Limit: 1})
	_ = dir.Close()
	assertCode(t, err, CodeSourceChanged)
	if len(page.Entries) != 0 {
		t.Fatalf("entries = %#v, want none", page.Entries)
	}
}

func TestListPageStopsPromptlyWhenCanceledAfterReadBatch(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 200; i++ {
		mustWriteFile(t, filepath.Join(root, fmt.Sprintf("note-%04d.md", i)))
	}
	before := testutil.Snapshot(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	batches := 0
	vault := mustVault(t, root)
	vault.testHooks = &vaultTestHooks{
		afterListBatch: func(filesScanned uint64) {
			batches++
			if filesScanned == 0 {
				t.Fatal("afterListBatch ran before work was recorded")
			}
			cancel()
		},
	}
	dir, err := vault.OpenDir(context.Background(), "", ".")
	if err != nil {
		t.Fatal(err)
	}
	page, err := dir.ListPage(ctx, ListOptions{Limit: 7})
	_ = dir.Close()
	assertCode(t, err, CodeCanceled)
	if batches != 1 {
		t.Fatalf("batches = %d, want 1", batches)
	}
	if page.FilesScanned == 0 || page.FilesScanned >= 200 {
		t.Fatalf("files scanned = %d, want nonzero partial work", page.FilesScanned)
	}
	if len(page.Entries) != 0 || page.HasMore || page.Source != (SourceFingerprint{}) {
		t.Fatalf("canceled page exposed results: %#v", page)
	}
	if after := testutil.Snapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatalf("canceled listing mutated vault:\nbefore=%#v\nafter=%#v", before, after)
	}
}

func TestListPageLargeDirectoryRetainsOnlyLimitPlusOne(t *testing.T) {
	root := t.TempDir()
	for i := 999; i >= 0; i-- {
		mustWriteFile(t, filepath.Join(root, fmt.Sprintf("note-%04d.md", i)))
	}
	vault := mustVault(t, root)
	dir, err := vault.OpenDir(context.Background(), "", ".")
	if err != nil {
		t.Fatal(err)
	}
	page, err := dir.ListPage(context.Background(), ListOptions{Limit: 7})
	_ = dir.Close()
	if err != nil {
		t.Fatal(err)
	}
	if page.CandidatesRetained != 8 || len(page.Entries) != 7 || !page.HasMore {
		t.Fatalf("page = retained:%d entries:%d has_more:%t", page.CandidatesRetained, len(page.Entries), page.HasMore)
	}
	for i, entry := range page.Entries {
		want := fmt.Sprintf("note-%04d.md", i)
		if entry.Name != want {
			t.Fatalf("entry[%d] = %q, want %q", i, entry.Name, want)
		}
	}
}

func BenchmarkMembershipAccumulatorAdd(b *testing.B) {
	position := Position{NFC: "workouts/strength/café.md", Stored: "workouts/strength/cafe\u0301.md"}
	var accumulator membershipAccumulator
	accumulator.add(position)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		accumulator.add(position)
	}
}

func framedMembershipReference(positions []Position) membershipAccumulator {
	var accumulator membershipAccumulator
	for _, position := range positions {
		key := []byte(position.NFC)
		stored := []byte(position.Stored)
		framed := make([]byte, 16+len(key)+len(stored))
		binary.BigEndian.PutUint64(framed[:8], uint64(len(key)))
		copy(framed[8:], key)
		offset := 8 + len(key)
		binary.BigEndian.PutUint64(framed[offset:offset+8], uint64(len(stored)))
		copy(framed[offset+8:], stored)

		hasher := sha256.New()
		_, _ = hasher.Write(membershipDomain)
		_, _ = hasher.Write(framed)
		item := hasher.Sum(nil)
		accumulator.count++
		accumulator.framedSize += uint64(len(framed))
		carry := uint16(0)
		for i := sha256.Size - 1; i >= 0; i-- {
			accumulator.xor[i] ^= item[i]
			total := uint16(accumulator.sum[i]) + uint16(item[i]) + carry
			accumulator.sum[i] = byte(total)
			carry = total >> 8
		}
	}
	return accumulator
}
