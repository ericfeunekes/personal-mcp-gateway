package obsidian

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"personal-mcp-gateway/internal/fsx"
)

func TestReadToolsReturnPathsInTheRequestBaseCoordinate(t *testing.T) {
	root := t.TempDir()
	mustWriteCoordinateFixture(t, root, "finance/a.md", "needle finance\n")
	mustWriteCoordinateFixture(t, root, "home/b.md", "needle home\n")
	vault, err := fsx.NewVault(root)
	if err != nil {
		t.Fatal(err)
	}
	tools := New(vault)

	_, resolved, err := tools.Resolve(context.Background(), nil, ResolveInput{Base: "finance", Path: "../home/b.md"})
	if err != nil || !resolved.OK || resolved.Path != "../home/b.md" {
		t.Fatalf("resolve = %#v err=%v", resolved, err)
	}
	_, reused, err := tools.Resolve(context.Background(), nil, ResolveInput{Base: "finance", Path: resolved.Path})
	if err != nil || !reused.OK || reused.Path != resolved.Path {
		t.Fatalf("reused resolve = %#v err=%v", reused, err)
	}

	_, listed, err := tools.LS(context.Background(), nil, LSInput{Base: "finance", Path: "."})
	if err != nil || !listed.OK || listed.Path != "." || len(listed.Entries) != 1 || listed.Entries[0].Path != "a.md" {
		t.Fatalf("ls = %#v err=%v", listed, err)
	}

	_, read, err := tools.Read(context.Background(), nil, ReadInput{Base: "finance", Path: "../home/b.md"})
	if err != nil || !read.OK || read.Path != "../home/b.md" {
		t.Fatalf("read = %#v err=%v", read, err)
	}

	_, batch, err := tools.ReadMany(context.Background(), nil, ReadManyInput{
		Base: "finance",
		Requests: []ReadRequest{
			{Path: "a.md"},
			{Path: "../home/b.md"},
		},
	})
	if err != nil || !batch.OK || len(batch.Items) != 2 || batch.Items[0].Path != "a.md" || batch.Items[1].Path != "../home/b.md" {
		t.Fatalf("read_many = %#v err=%v", batch, err)
	}

	_, grep, err := tools.Grep(context.Background(), nil, GrepInput{Base: "finance", Path: "../home", Pattern: "needle"})
	if err != nil || !grep.OK || grep.Path != "../home" || len(grep.Matches) != 1 || grep.Matches[0].Path != "../home/b.md" {
		t.Fatalf("grep = %#v err=%v", grep, err)
	}
}

func TestReadManyCursorBindsTopLevelBaseCoordinate(t *testing.T) {
	root := t.TempDir()
	mustWriteCoordinateFixture(t, root, "finance/long.md", "abcdef\n")
	vault, err := fsx.NewVault(root)
	if err != nil {
		t.Fatal(err)
	}
	tools := New(vault)
	_, first, err := tools.ReadMany(context.Background(), nil, ReadManyInput{
		Base: "finance", Requests: []ReadRequest{{Path: "long.md", MaxBytes: 1}}, MaxBytes: 1,
	})
	if err != nil || !first.OK || first.Coverage.NextCursor == "" {
		t.Fatalf("first read_many = %#v err=%v", first, err)
	}
	_, continued, err := tools.ReadMany(context.Background(), nil, ReadManyInput{
		Base: "finance", Requests: []ReadRequest{{Path: "long.md", MaxBytes: 1}}, MaxBytes: 1, Cursor: first.Coverage.NextCursor,
	})
	if err != nil || !continued.OK || len(continued.Items) != 1 || continued.Items[0].Path != "long.md" {
		t.Fatalf("continued read_many = %#v err=%v", continued, err)
	}
	_, changed, err := tools.ReadMany(context.Background(), nil, ReadManyInput{
		Base: ".", Requests: []ReadRequest{{Path: "finance/long.md", MaxBytes: 1}}, MaxBytes: 1, Cursor: first.Coverage.NextCursor,
	})
	if err != nil || changed.OK || changed.Error == nil || changed.Error.Code != CursorMismatchCode {
		t.Fatalf("changed-base continuation = %#v err=%v", changed, err)
	}
}

func TestCursorToolsPreserveAndBindTheRequestBaseCoordinate(t *testing.T) {
	root := t.TempDir()
	mustWriteCoordinateFixture(t, root, "finance/a.md", "needle alpha\n")
	mustWriteCoordinateFixture(t, root, "finance/b.md", "needle beta\n")
	vault, err := fsx.NewVault(root)
	if err != nil {
		t.Fatal(err)
	}
	tools := New(vault)

	t.Run("ls", func(t *testing.T) {
		_, first, err := tools.LS(context.Background(), nil, LSInput{Base: "finance", Path: ".", Limit: 1})
		if err != nil || !first.OK || first.Path != "." || len(first.Entries) != 1 || first.Entries[0].Path != "a.md" || first.Coverage.NextCursor == "" {
			t.Fatalf("first ls = %#v err=%v", first, err)
		}
		_, continued, err := tools.LS(context.Background(), nil, LSInput{Base: "finance", Path: ".", Limit: 1, Cursor: first.Coverage.NextCursor})
		if err != nil || !continued.OK || continued.Path != "." || len(continued.Entries) != 1 || continued.Entries[0].Path != "b.md" {
			t.Fatalf("continued ls = %#v err=%v", continued, err)
		}
		_, changed, err := tools.LS(context.Background(), nil, LSInput{Base: ".", Path: "finance", Limit: 1, Cursor: first.Coverage.NextCursor})
		if err != nil || changed.OK || changed.Error == nil || changed.Error.Code != CursorMismatchCode {
			t.Fatalf("changed-base ls = %#v err=%v", changed, err)
		}
	})

	t.Run("read", func(t *testing.T) {
		_, first, err := tools.Read(context.Background(), nil, ReadInput{Base: "finance", Path: "a.md", MaxBytes: 1})
		if err != nil || !first.OK || first.Path != "a.md" || first.Coverage.NextCursor == "" {
			t.Fatalf("first read = %#v err=%v", first, err)
		}
		_, continued, err := tools.Read(context.Background(), nil, ReadInput{Base: "finance", Path: "a.md", MaxBytes: 1, Cursor: first.Coverage.NextCursor})
		if err != nil || !continued.OK || continued.Path != "a.md" {
			t.Fatalf("continued read = %#v err=%v", continued, err)
		}
		_, changed, err := tools.Read(context.Background(), nil, ReadInput{Base: ".", Path: "finance/a.md", MaxBytes: 1, Cursor: first.Coverage.NextCursor})
		if err != nil || changed.OK || changed.Error == nil || changed.Error.Code != CursorMismatchCode {
			t.Fatalf("changed-base read = %#v err=%v", changed, err)
		}
	})

	t.Run("grep", func(t *testing.T) {
		_, first, err := tools.Grep(context.Background(), nil, GrepInput{Base: "finance", Path: ".", Pattern: "absent", MaxFiles: 1})
		if err != nil || !first.OK || first.Path != "." || first.Coverage.NextCursor == "" {
			t.Fatalf("first grep = %#v err=%v", first, err)
		}
		_, continued, err := tools.Grep(context.Background(), nil, GrepInput{Base: "finance", Path: ".", Pattern: "absent", MaxFiles: 1, Cursor: first.Coverage.NextCursor})
		if err != nil || !continued.OK || continued.Path != "." {
			t.Fatalf("continued grep = %#v err=%v", continued, err)
		}
		_, changed, err := tools.Grep(context.Background(), nil, GrepInput{Base: ".", Path: "finance", Pattern: "absent", MaxFiles: 1, Cursor: first.Coverage.NextCursor})
		if err != nil || changed.OK || changed.Error == nil || changed.Error.Code != CursorMismatchCode {
			t.Fatalf("changed-base grep = %#v err=%v", changed, err)
		}
	})
}

func TestReadManyRejectsUnsafeTopLevelBaseAsBatchError(t *testing.T) {
	root := t.TempDir()
	mustWriteCoordinateFixture(t, root, "note.md", "content\n")
	vault, err := fsx.NewVault(root)
	if err != nil {
		t.Fatal(err)
	}
	_, out, err := New(vault).ReadMany(context.Background(), nil, ReadManyInput{
		Base: "..", Requests: []ReadRequest{{Path: "note.md"}},
	})
	if err != nil || out.OK || out.Error == nil || out.Error.Code != string(fsx.CodePathDenied) || len(out.Items) != 0 {
		t.Fatalf("read_many = %#v err=%v", out, err)
	}
}

func mustWriteCoordinateFixture(t testing.TB, root, relative, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func BenchmarkLSLargeDirectoryWithBase(b *testing.B) {
	root := b.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "finance", "domain"), 0o700); err != nil {
		b.Fatal(err)
	}
	for i := 0; i < 1000; i++ {
		mustWriteCoordinateFixture(b, root, filepath.Join("home", fmt.Sprintf("note-%04d.md", i)), "synthetic\n")
	}
	vault, err := fsx.NewVault(root)
	if err != nil {
		b.Fatal(err)
	}
	tools := New(vault)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, out, err := tools.LS(context.Background(), nil, LSInput{Base: "finance/domain", Path: "../../home", Limit: 10})
		if err != nil || !out.OK || len(out.Entries) != 10 || !out.Truncated {
			b.Fatalf("ls entries=%d truncated=%t err=%v", len(out.Entries), out.Truncated, err)
		}
	}
}
