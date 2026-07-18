package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type ArgumentSummarizer func(*SafeSummaryBuilder, json.RawMessage) error
type ResultSummarizer func(*SafeSummaryBuilder, *sdk.CallToolResult) error

// ToolDescriptor is the single executable authority for one activated tool.
// Its registration closure retains the SDK's typed AddTool path.
type ToolDescriptor struct {
	tool            sdk.Tool
	register        func(*sdk.Server)
	summarizeArgs   ArgumentSummarizer
	summarizeResult ResultSummarizer
}

func NewToolDescriptor[In, Out any](tool sdk.Tool, handler sdk.ToolHandlerFor[In, Out], args ArgumentSummarizer, result ResultSummarizer) (ToolDescriptor, error) {
	if err := validateToolName(tool.Name); err != nil {
		return ToolDescriptor{}, err
	}
	if handler == nil || args == nil || result == nil {
		return ToolDescriptor{}, fmt.Errorf("tool %q has an incomplete descriptor", tool.Name)
	}
	toolCopy := tool
	return ToolDescriptor{
		tool: toolCopy,
		register: func(server *sdk.Server) {
			sdk.AddTool(server, &toolCopy, handler)
		},
		summarizeArgs:   args,
		summarizeResult: result,
	}, nil
}

func (d ToolDescriptor) Name() string { return d.tool.Name }

func validateDescriptors(descriptors []ToolDescriptor) ([]ToolDescriptor, []string, error) {
	if len(descriptors) == 0 {
		return nil, nil, errors.New("at least one tool descriptor is required")
	}
	ordered := append([]ToolDescriptor(nil), descriptors...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Name() < ordered[j].Name() })

	names := make([]string, 0, len(ordered))
	seen := make(map[string]struct{}, len(descriptors))
	for _, descriptor := range ordered {
		name := descriptor.Name()
		if err := validateToolName(name); err != nil {
			return nil, nil, err
		}
		if descriptor.register == nil || descriptor.summarizeArgs == nil || descriptor.summarizeResult == nil {
			return nil, nil, fmt.Errorf("tool %q has an incomplete descriptor", name)
		}
		if _, exists := seen[name]; exists {
			return nil, nil, fmt.Errorf("duplicate tool descriptor %q", name)
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return ordered, names, nil
}

func descriptorLookup(descriptors []ToolDescriptor) map[string]ToolDescriptor {
	out := make(map[string]ToolDescriptor, len(descriptors))
	for _, descriptor := range descriptors {
		out[descriptor.Name()] = descriptor
	}
	return out
}

func validateToolName(name string) error {
	if name == "" {
		return errors.New("tool name is required")
	}
	if len(name) > 128 {
		return fmt.Errorf("tool name %q exceeds 128 bytes", name)
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' {
			continue
		}
		return fmt.Errorf("tool name %q contains an invalid character", name)
	}
	return nil
}
