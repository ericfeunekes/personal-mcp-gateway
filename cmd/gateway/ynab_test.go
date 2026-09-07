package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestYNABStdioChild(t *testing.T) {
	if os.Getenv("GATEWAY_TEST_YNAB_CHILD") != "1" {
		return
	}
	os.Exit(run([]string{"stdio", "--server", "ynab", "--telemetry", "stderr"}, os.Stderr))
}

func TestYNABStdioEntrypoint(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestYNABStdioChild$")
	cmd.Env = append(os.Environ(), "GATEWAY_TEST_YNAB_CHILD=1", "YNAB_TOKEN=fixture-private-token", "YNAB_EXPORT_ROOT="+filepath.Join(t.TempDir(), "exports"))
	var stderr synchronizedBuffer
	cmd.Stderr = &stderr
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client := sdk.NewClient(&sdk.Implementation{Name: "ynab-entrypoint-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &sdk.CommandTransport{Command: cmd, TerminateDuration: 2 * time.Second}, nil)
	if err != nil {
		t.Fatalf("connect: %v stderr: %s", err, stderr.String())
	}
	defer session.Close()
	info := session.InitializeResult().ServerInfo
	if info == nil || info.Name != "ynab" || len(info.Icons) != 0 {
		t.Fatalf("wrong integration identity: %#v", info)
	}
	result, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Tools) != 6 {
		t.Fatalf("got %d tools", len(result.Tools))
	}
	if strings.Contains(stderr.String(), "fixture-private-token") {
		t.Fatal("credential in logs")
	}
}
