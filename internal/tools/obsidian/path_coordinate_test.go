package obsidian

import (
	"testing"

	"personal-mcp-gateway/internal/fsx"
)

func TestPathCoordinateProjectsCanonicalPathsRelativeToBase(t *testing.T) {
	tests := []struct {
		name      string
		base      string
		canonical string
		want      string
	}{
		{name: "omitted base", canonical: "finance/tax.md", want: "finance/tax.md"},
		{name: "root base", base: ".", canonical: "finance/tax.md", want: "finance/tax.md"},
		{name: "descendant", base: "finance", canonical: "finance/tax.md", want: "tax.md"},
		{name: "same path", base: "finance", canonical: "finance", want: "."},
		{name: "sibling", base: "finance", canonical: "home/budget.md", want: "../home/budget.md"},
		{name: "nested sibling", base: "finance/2026", canonical: "home/budget.md", want: "../../home/budget.md"},
		{name: "normalized base", base: "finance/2026/..", canonical: "finance/tax.md", want: "tax.md"},
		{name: "stored spelling remains reusable", base: "finance", canonical: "Finance/Tax.md", want: "../Finance/Tax.md"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			coordinate, err := newPathCoordinate(tt.base)
			if err != nil {
				t.Fatal(err)
			}
			if got := coordinate.project(tt.canonical); got != tt.want {
				t.Fatalf("project(%q) = %q, want %q", tt.canonical, got, tt.want)
			}
		})
	}
}

func TestPathCoordinateRejectsUnsafeBase(t *testing.T) {
	for _, base := range []string{"../finance", "/finance", ".hidden"} {
		t.Run(base, func(t *testing.T) {
			if _, err := newPathCoordinate(base); err == nil {
				t.Fatalf("newPathCoordinate(%q) succeeded", base)
			}
		})
	}
}

func TestProjectChildReusesTheProjectedDirectoryCoordinate(t *testing.T) {
	if got := projectChild("../../home", "budget.md"); got != "../../home/budget.md" {
		t.Fatalf("projectChild sibling = %q", got)
	}
	if got := projectChild(".", "budget.md"); got != "budget.md" {
		t.Fatalf("projectChild working directory = %q", got)
	}
}

func TestMutationResultBuildersUseTheRequestBaseCoordinate(t *testing.T) {
	coordinate, err := newPathCoordinate("finance")
	if err != nil {
		t.Fatal(err)
	}
	resolved := fsx.Resolved{Rel: "finance/note.md", Exists: true, Kind: fsx.KindFile}
	if got := statOutput(fsx.MutationTarget{Resolved: resolved}, coordinate).Path; got != "note.md" {
		t.Fatalf("stat path = %q", got)
	}
	result := fsx.MutationResult{Resolved: resolved}
	if got := mutationOutput(result, coordinate).Path; got != "note.md" {
		t.Fatalf("mutation path = %q", got)
	}
	if got := deleteOutput(fsx.DeleteResult{Resolved: resolved}, coordinate).Path; got != "note.md" {
		t.Fatalf("delete path = %q", got)
	}
}
