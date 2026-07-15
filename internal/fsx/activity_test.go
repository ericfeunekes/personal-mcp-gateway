package fsx

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestActivityCounterTracksTotalAndInFlightOperations(t *testing.T) {
	var absent *ActivityCounter
	if end := absent.Begin(); end != nil {
		t.Fatal("nil counter returned an end function")
	}
	if got := absent.Snapshot(); got != (ActivitySnapshot{}) {
		t.Fatalf("nil snapshot = %#v", got)
	}

	counter := &ActivityCounter{}
	firstEnd := counter.Begin()
	if got := counter.Snapshot(); got != (ActivitySnapshot{Total: 1, Active: 1}) {
		t.Fatalf("active snapshot = %#v", got)
	}
	secondEnd := counter.Begin()
	if got := counter.Snapshot(); got != (ActivitySnapshot{Total: 2, Active: 2}) {
		t.Fatalf("two-active snapshot = %#v", got)
	}
	firstEnd()
	if got := counter.Snapshot(); got != (ActivitySnapshot{Total: 2, Active: 1}) {
		t.Fatalf("partially recovered snapshot = %#v", got)
	}
	secondEnd()
	if got := counter.Snapshot(); got != (ActivitySnapshot{Total: 2, Active: 0}) {
		t.Fatalf("recovered snapshot = %#v", got)
	}
}

func TestResolveKeepsActivityActiveUntilOperationReturns(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "inside"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "inside", "note.md"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	counter := &ActivityCounter{}
	vault, err := NewVaultWithActivity(root, counter)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	vault.testHooks = &vaultTestHooks{beforeOpenSegment: func(int) {
		close(entered)
		<-release
	}}
	result := make(chan error, 1)
	go func() {
		_, resolveErr := vault.Resolve(context.Background(), "", "inside/note.md")
		result <- resolveErr
	}()
	<-entered
	if got := counter.Snapshot(); got != (ActivitySnapshot{Total: 1, Active: 1}) {
		t.Fatalf("in-flight resolve snapshot = %#v", got)
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if got := counter.Snapshot(); got != (ActivitySnapshot{Total: 1, Active: 0}) {
		t.Fatalf("returned resolve snapshot = %#v", got)
	}
}
