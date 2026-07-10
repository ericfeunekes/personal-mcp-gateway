package obsidian

import (
	"context"
	"testing"
	"time"

	"personal-mcp-gateway/internal/fsx"
	"personal-mcp-gateway/internal/testutil"
)

func TestLSTimeoutReturnsStructuredToolError(t *testing.T) {
	vault, err := fsx.NewVault(testutil.FixtureVault(t))
	if err != nil {
		t.Fatal(err)
	}
	tools := New(vault)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	result, out, err := tools.LS(ctx, nil, LSInput{Path: ".", Limit: 10})
	if err != nil {
		t.Fatalf("LS() error = %v, want structured tool error", err)
	}
	if result == nil || !result.IsError {
		t.Fatalf("CallToolResult = %#v, want IsError", result)
	}
	if out.OK || out.Error == nil || out.Error.Code != "timeout" {
		t.Fatalf("LSOutput = %#v, want timeout error", out)
	}
}
