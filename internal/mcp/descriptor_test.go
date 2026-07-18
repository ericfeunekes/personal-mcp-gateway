package mcp

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"personal-mcp-gateway/internal/audit"
)

func TestNewServerRejectsInvalidDescriptorCollections(t *testing.T) {
	descriptor := descriptorForServerTest(t, "alpha", sdk.Tool{})
	tests := []struct {
		name        string
		descriptors []ToolDescriptor
	}{
		{name: "empty"},
		{name: "duplicate", descriptors: []ToolDescriptor{descriptor, descriptor}},
		{name: "incomplete", descriptors: []ToolDescriptor{{tool: sdk.Tool{Name: "alpha"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, names, err := NewServer(audit.Disabled(), "stdio", tt.descriptors)
			if err == nil || server != nil || names != nil {
				t.Fatalf("NewServer() = server %#v, names %#v, err %v", server, names, err)
			}
		})
	}
}

func TestNewServerReturnsTypedRegistrationError(t *testing.T) {
	descriptor := descriptorForServerTest(t, "alpha", sdk.Tool{
		InputSchema: map[string]any{"type": "string"},
	})
	server, names, err := NewServer(audit.Disabled(), "stdio", []ToolDescriptor{descriptor})
	if err == nil || server != nil || names != nil {
		t.Fatalf("NewServer() = server %#v, names %#v, err %v", server, names, err)
	}
}

func TestNewServerRegistersTypedDescriptorsAndDerivesNames(t *testing.T) {
	beta := descriptorForServerTest(t, "beta", sdk.Tool{})
	alpha := descriptorForServerTest(t, "alpha", sdk.Tool{})
	server, names, err := NewServer(audit.Disabled(), "stdio", []ToolDescriptor{beta, alpha})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"alpha", "beta"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("names = %#v, want %#v", names, want)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	serverTransport, clientTransport := sdk.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()

	listed, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	listedNames := make([]string, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		listedNames = append(listedNames, tool.Name)
	}
	if !reflect.DeepEqual(listedNames, names) {
		t.Fatalf("SDK tools = %#v, descriptor names %#v", listedNames, names)
	}

	invalid, err := clientSession.CallTool(ctx, &sdk.CallToolParams{Name: "alpha", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if !invalid.IsError {
		t.Fatalf("missing required input was accepted: %#v", invalid)
	}

	valid, err := clientSession.CallTool(ctx, &sdk.CallToolParams{Name: "alpha", Arguments: map[string]any{"value": "accepted"}})
	if err != nil {
		t.Fatal(err)
	}
	if valid.IsError {
		t.Fatalf("valid typed call failed: %#v", valid)
	}
	data, err := json.Marshal(valid.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var output descriptorServerOutput
	if err := json.Unmarshal(data, &output); err != nil {
		t.Fatal(err)
	}
	if output.Value != "accepted" {
		t.Fatalf("output = %#v", output)
	}
}

type descriptorServerInput struct {
	Value string `json:"value"`
}

type descriptorServerOutput struct {
	Value string `json:"value"`
}

func descriptorForServerTest(t *testing.T, name string, overrides sdk.Tool) ToolDescriptor {
	t.Helper()
	overrides.Name = name
	descriptor, err := NewToolDescriptor(
		overrides,
		func(_ context.Context, _ *sdk.CallToolRequest, input descriptorServerInput) (*sdk.CallToolResult, descriptorServerOutput, error) {
			return nil, descriptorServerOutput{Value: input.Value}, nil
		},
		noArgumentSummary,
		noResultSummary,
	)
	if err != nil {
		t.Fatal(err)
	}
	return descriptor
}
