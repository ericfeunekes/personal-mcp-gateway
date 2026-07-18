package main

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"personal-mcp-gateway/internal/audit"
	"personal-mcp-gateway/internal/fsx"
	localmcp "personal-mcp-gateway/internal/mcp"
	"personal-mcp-gateway/internal/testutil"
	"personal-mcp-gateway/internal/tools/obsidian"
)

func TestExactCandidateToolGrammarRejectsAgentVisibleDrift(t *testing.T) {
	tools := listedCandidateTools(t)
	if !exactCandidateToolGrammar(tools) {
		t.Fatal("exact five-tool grammar was rejected")
	}
	for _, mutation := range []struct {
		name string
		edit func([]*sdk.Tool)
	}{
		{name: "annotation", edit: func(items []*sdk.Tool) {
			for _, tool := range items {
				if tool.Name == obsidian.ToolRead {
					tool.Annotations.ReadOnlyHint = false
				}
			}
		}},
		{name: "ls maximum", edit: func(items []*sdk.Tool) {
			for _, tool := range items {
				if tool.Name == obsidian.ToolLS {
					schema, _ := normalizedSchema(tool.InputSchema)
					property(schema, "limit")["maximum"] = float64(501)
					tool.InputSchema = schema
				}
			}
		}},
		{name: "selector variant", edit: func(items []*sdk.Tool) {
			for _, tool := range items {
				if tool.Name == obsidian.ToolRead {
					schema, _ := normalizedSchema(tool.InputSchema)
					selector := property(schema, "selector")
					selector["oneOf"] = selector["oneOf"].([]any)[:4]
					tool.InputSchema = schema
				}
			}
		}},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			changed := cloneTools(t, tools)
			mutation.edit(changed)
			if exactCandidateToolGrammar(changed) {
				t.Fatal("drifted five-tool grammar was accepted")
			}
		})
	}
}

func listedCandidateTools(t *testing.T) []*sdk.Tool {
	t.Helper()
	vault, err := fsx.NewVault(testutil.FixtureVault(t))
	if err != nil {
		t.Fatal(err)
	}
	descriptors, err := obsidian.Descriptors(vault)
	if err != nil {
		t.Fatal(err)
	}
	server, _, err := localmcp.NewServer(audit.Disabled(), "stdio", descriptors)
	if err != nil {
		t.Fatal(err)
	}
	serverReader, clientWriter := io.Pipe()
	clientReader, serverWriter := io.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- server.Run(ctx, &sdk.IOTransport{Reader: serverReader, Writer: serverWriter})
	}()
	client := sdk.NewClient(&sdk.Implementation{Name: "contract-test", Version: "v0.0.1"}, nil)
	session, err := client.Connect(ctx, &sdk.IOTransport{Reader: clientReader, Writer: clientWriter}, nil)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = session.Close()
	cancel()
	_ = clientWriter.Close()
	_ = clientReader.Close()
	_ = serverWriter.Close()
	_ = serverReader.Close()
	select {
	case <-serverDone:
	case <-time.After(2 * time.Second):
		t.Fatal("candidate contract server did not stop")
	}
	return listed.Tools
}

func cloneTools(t *testing.T, tools []*sdk.Tool) []*sdk.Tool {
	t.Helper()
	data, err := json.Marshal(tools)
	if err != nil {
		t.Fatal(err)
	}
	var clone []*sdk.Tool
	if err := json.Unmarshal(data, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}
