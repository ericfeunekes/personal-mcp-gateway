package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"personal-mcp-gateway/internal/tools/obsidian"
)

const smokeTimeout = 10 * time.Second

func main() {
	if err := run(os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "gateway smoke failed:", err)
		os.Exit(1)
	}
	fmt.Println("gateway smoke passed: resolve(.) returned an existing directory")
}

func run(args []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("gateway-smoke", flag.ContinueOnError)
	flags.SetOutput(stderr)
	gatewayBin := flags.String("gateway-bin", "", "gateway executable to probe")
	obsidianRoot := flags.String("obsidian-root", "", "vault root used for the probe")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *gatewayBin == "" || *obsidianRoot == "" {
		return errors.New("--gateway-bin and --obsidian-root are required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), smokeTimeout)
	defer cancel()

	cmd := exec.Command(*gatewayBin,
		"stdio",
		"--obsidian-root", *obsidianRoot,
		"--telemetry", "off",
	)
	client := sdk.NewClient(&sdk.Implementation{Name: "local-release-smoke", Version: "v1"}, nil)
	session, err := client.Connect(ctx, &sdk.CommandTransport{
		Command:           cmd,
		TerminateDuration: 2 * time.Second,
	}, nil)
	if err != nil {
		return errors.New("candidate MCP connection failed")
	}
	defer session.Close()

	result, err := session.CallTool(ctx, &sdk.CallToolParams{
		Name: obsidian.ToolResolve,
		Arguments: map[string]any{
			"path": ".",
		},
	})
	if err != nil {
		return errors.New("candidate resolve call failed")
	}
	if result.IsError {
		return errors.New("resolve returned a tool error")
	}

	var out obsidian.ResolveOutput
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return fmt.Errorf("encode resolve result: %w", err)
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return fmt.Errorf("decode resolve result: %w", err)
	}
	if !out.OK || !out.Exists || out.Type != "directory" {
		return errors.New("resolve did not return an existing directory")
	}
	return nil
}
