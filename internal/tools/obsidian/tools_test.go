package obsidian

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"personal-mcp-gateway/internal/fsx"
	"personal-mcp-gateway/internal/testutil"
)

func TestLSCursorIsStableAcrossVaultInstancesAndBoundToRoot(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.md", "b.md"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	firstVault, err := fsx.NewVault(root)
	if err != nil {
		t.Fatal(err)
	}
	_, first, err := New(firstVault).LS(context.Background(), nil, LSInput{Path: ".", Limit: 1})
	if err != nil || !first.OK || first.Coverage.NextCursor == "" {
		t.Fatalf("first = %#v err=%v", first, err)
	}

	secondVault, err := fsx.NewVault(root)
	if err != nil {
		t.Fatal(err)
	}
	_, second, err := New(secondVault).LS(context.Background(), nil, LSInput{
		Path: ".", Limit: 1, Cursor: first.Coverage.NextCursor,
	})
	if err != nil || !second.OK || len(second.Entries) != 1 || second.Entries[0].Name != "b.md" {
		t.Fatalf("same-root continuation = %#v err=%v", second, err)
	}

	otherRoot := t.TempDir()
	for _, name := range []string{"a.md", "b.md"} {
		if err := os.WriteFile(filepath.Join(otherRoot, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	otherVault, err := fsx.NewVault(otherRoot)
	if err != nil {
		t.Fatal(err)
	}
	_, rejected, err := New(otherVault).LS(context.Background(), nil, LSInput{
		Path: ".", Limit: 1, Cursor: first.Coverage.NextCursor,
	})
	if err != nil || rejected.OK || rejected.Error == nil || rejected.Error.Code != CursorInvalidCode || rejected.Coverage.FilesScanned != 0 {
		t.Fatalf("different-root continuation = %#v err=%v", rejected, err)
	}
}

func TestLSTimeoutReturnsStructuredToolError(t *testing.T) {
	root := testutil.FixtureVault(t)
	vault, err := fsx.NewVault(root)
	if err != nil {
		t.Fatal(err)
	}
	tools := New(vault)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	before := testutil.Snapshot(t, root)

	result, out, err := tools.LS(ctx, nil, LSInput{Path: ".", Limit: 10})
	if err != nil {
		t.Fatalf("LS() error = %v, want structured tool error", err)
	}
	if result == nil || !result.IsError {
		t.Fatalf("CallToolResult = %#v, want IsError", result)
	}
	if out.OK || out.Error == nil || out.Error.Code != "timeout" || out.Coverage.Continuation != "restart" ||
		out.Coverage.StoppedBy != "timeout" || out.Coverage.ScopeComplete || len(out.Entries) != 0 {
		t.Fatalf("LSOutput = %#v, want timeout error", out)
	}
	if after := testutil.Snapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatalf("timeout call mutated vault:\nbefore=%#v\nafter=%#v", before, after)
	}
}

func TestLSMidScanCancellationReturnsActualWorkAndRestartCoverage(t *testing.T) {
	directory := &stubListDirectory{
		resolved: fsx.Resolved{Rel: ".", Exists: true, Kind: fsx.KindDir},
		page:     fsx.ListPage{FilesScanned: 64, BytesScanned: 0},
		err:      &fsx.Error{Code: fsx.CodeCanceled},
	}
	tools := &Tools{
		openDir: func(context.Context, string, string) (listDirectory, error) {
			return directory, nil
		},
	}

	result, out, err := tools.LS(context.Background(), nil, LSInput{Path: ".", Limit: 10})
	if err != nil {
		t.Fatalf("LS() error = %v, want structured tool error", err)
	}
	if result == nil || !result.IsError || out.Error == nil || out.Error.Code != "canceled" {
		t.Fatalf("result=%#v output=%#v", result, out)
	}
	if out.Coverage.FilesScanned != 64 || out.Coverage.BytesScanned != 0 || out.Coverage.ScopeComplete ||
		out.Coverage.Consistency != "best_effort" || out.Coverage.StoppedBy != "canceled" || out.Coverage.Continuation != "restart" ||
		len(out.Entries) != 0 {
		t.Fatalf("canceled coverage = %#v entries=%#v", out.Coverage, out.Entries)
	}
	if !directory.closed {
		t.Fatal("canceled directory was not closed")
	}
}

func TestLSActiveSourceChangeReturnsActualWorkAndRestartCoverage(t *testing.T) {
	directory := &stubListDirectory{
		resolved: fsx.Resolved{Rel: ".", Exists: true, Kind: fsx.KindDir},
		page:     fsx.ListPage{FilesScanned: 41, BytesScanned: 0},
		err:      &fsx.Error{Code: fsx.CodeSourceChanged},
	}
	tools := &Tools{
		openDir: func(context.Context, string, string) (listDirectory, error) {
			return directory, nil
		},
	}

	result, out, err := tools.LS(context.Background(), nil, LSInput{Path: ".", Limit: 10})
	if err != nil || result == nil || !result.IsError || out.Error == nil || out.Error.Code != "source_changed" {
		t.Fatalf("err=%v result=%#v output=%#v", err, result, out)
	}
	if out.Coverage.FilesScanned != 41 || out.Coverage.ScopeComplete || out.Coverage.Consistency != "best_effort" ||
		out.Coverage.StoppedBy != "source_change" || out.Coverage.Continuation != "restart" || len(out.Entries) != 0 {
		t.Fatalf("source-change coverage = %#v entries=%#v", out.Coverage, out.Entries)
	}
	if !directory.closed {
		t.Fatal("source-changed directory was not closed")
	}
}

type stubListDirectory struct {
	resolved fsx.Resolved
	page     fsx.ListPage
	err      error
	closed   bool
}

func (d *stubListDirectory) Resolved() fsx.Resolved { return d.resolved }

func (d *stubListDirectory) ListPage(context.Context, fsx.ListOptions) (fsx.ListPage, error) {
	return d.page, d.err
}

func (d *stubListDirectory) Close() error {
	d.closed = true
	return nil
}
