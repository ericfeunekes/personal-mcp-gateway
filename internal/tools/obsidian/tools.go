package obsidian

import (
	"context"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"personal-mcp-gateway/internal/fsx"
	"personal-mcp-gateway/internal/limits"
)

const (
	ToolResolve = "resolve"
	ToolLS      = "ls"
)

func ToolNames() []string {
	return []string{ToolResolve, ToolLS}
}

type Tools struct {
	vault *fsx.Vault
}

func New(vault *fsx.Vault) *Tools {
	return &Tools{vault: vault}
}

func Register(server *sdk.Server, vault *fsx.Vault) {
	tools := New(vault)

	sdk.AddTool(server, &sdk.Tool{
		Name:        ToolResolve,
		Description: "Normalize a vault-relative Obsidian path and report whether it exists.",
		Annotations: readOnlyToolAnnotations(),
	}, tools.Resolve)

	sdk.AddTool(server, &sdk.Tool{
		Name:        ToolLS,
		Description: "List one directory level under a vault-relative Obsidian path.",
		Annotations: readOnlyToolAnnotations(),
	}, tools.LS)
}

func readOnlyToolAnnotations() *sdk.ToolAnnotations {
	destructive := false
	openWorld := false
	return &sdk.ToolAnnotations{
		ReadOnlyHint:    true,
		DestructiveHint: &destructive,
		OpenWorldHint:   &openWorld,
	}
}

type ResolveInput struct {
	Path string `json:"path" jsonschema:"vault-relative path to resolve"`
	Base string `json:"base,omitempty" jsonschema:"optional vault-relative base path"`
}

type ResolveOutput struct {
	OK       bool       `json:"ok"`
	Path     string     `json:"path,omitempty"`
	Exists   bool       `json:"exists"`
	Type     string     `json:"type,omitempty"`
	Size     int64      `json:"size,omitempty"`
	Modified string     `json:"modified,omitempty"`
	Error    *ToolError `json:"error,omitempty"`
}

type LSInput struct {
	Path  string `json:"path" jsonschema:"vault-relative directory path to list"`
	Base  string `json:"base,omitempty" jsonschema:"optional vault-relative base path"`
	Limit int    `json:"limit,omitempty" jsonschema:"maximum entries to return"`
}

type LSOutput struct {
	OK        bool       `json:"ok"`
	Path      string     `json:"path,omitempty"`
	Entries   []LSEntry  `json:"entries,omitempty"`
	Truncated bool       `json:"truncated"`
	Error     *ToolError `json:"error,omitempty"`
}

type LSEntry struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Type     string `json:"type"`
	Size     int64  `json:"size,omitempty"`
	Modified string `json:"modified,omitempty"`
}

type ToolError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (t *Tools) Resolve(ctx context.Context, _ *sdk.CallToolRequest, input ResolveInput) (*sdk.CallToolResult, ResolveOutput, error) {
	toolCtx, cancel := context.WithTimeout(ctx, limits.ToolOperationTimeout)
	defer cancel()

	resolved, err := t.vault.Resolve(toolCtx, input.Base, input.Path)
	if err != nil {
		return resolveError(err)
	}

	out := ResolveOutput{
		OK:     true,
		Path:   resolved.Rel,
		Exists: resolved.Exists,
	}
	if resolved.Exists {
		out.Type = string(resolved.Kind)
		out.Size = resolved.Size
		out.Modified = resolved.Modified.Format("2006-01-02T15:04:05Z07:00")
	}
	return nil, out, nil
}

func (t *Tools) LS(ctx context.Context, _ *sdk.CallToolRequest, input LSInput) (*sdk.CallToolResult, LSOutput, error) {
	toolCtx, cancel := context.WithTimeout(ctx, limits.ToolOperationTimeout)
	defer cancel()

	listed, err := t.vault.List(toolCtx, input.Base, input.Path, input.Limit)
	if err != nil {
		return lsError(err)
	}

	out := LSOutput{
		OK:        true,
		Path:      listed.Dir.Rel,
		Truncated: listed.Truncated,
		Entries:   make([]LSEntry, 0, len(listed.Entries)),
	}
	for _, entry := range listed.Entries {
		out.Entries = append(out.Entries, LSEntry{
			Name:     entry.Name,
			Path:     entry.Rel,
			Type:     string(entry.Kind),
			Size:     entry.Size,
			Modified: entry.Modified.Format("2006-01-02T15:04:05Z07:00"),
		})
	}
	return nil, out, nil
}

func resolveError(err error) (*sdk.CallToolResult, ResolveOutput, error) {
	code := errorCode(err)
	return &sdk.CallToolResult{IsError: true}, ResolveOutput{
		OK: false,
		Error: &ToolError{
			Code:    code,
			Message: sanitizedMessage(code),
		},
	}, nil
}

func lsError(err error) (*sdk.CallToolResult, LSOutput, error) {
	code := errorCode(err)
	return &sdk.CallToolResult{IsError: true}, LSOutput{
		OK: false,
		Error: &ToolError{
			Code:    code,
			Message: sanitizedMessage(code),
		},
	}, nil
}

func errorCode(err error) string {
	var code fsx.Code
	switch {
	case fsx.IsCode(err, fsx.CodePathDenied):
		code = fsx.CodePathDenied
	case fsx.IsCode(err, fsx.CodeSymlinkDenied):
		code = fsx.CodeSymlinkDenied
	case fsx.IsCode(err, fsx.CodeNotFound):
		code = fsx.CodeNotFound
	case fsx.IsCode(err, fsx.CodeNotDirectory):
		code = fsx.CodeNotDirectory
	case fsx.IsCode(err, fsx.CodeLimitExceeded):
		code = fsx.CodeLimitExceeded
	case fsx.IsCode(err, fsx.CodeInputTooLarge):
		code = fsx.CodeInputTooLarge
	case fsx.IsCode(err, fsx.CodeTimeout):
		code = fsx.CodeTimeout
	case fsx.IsCode(err, fsx.CodeCanceled):
		code = fsx.CodeCanceled
	default:
		code = fsx.CodePathDenied
	}
	return string(code)
}

func sanitizedMessage(code string) string {
	switch code {
	case string(fsx.CodePathDenied):
		return "path denied"
	case string(fsx.CodeSymlinkDenied):
		return "symlink traversal denied"
	case string(fsx.CodeNotFound):
		return "path not found"
	case string(fsx.CodeNotDirectory):
		return "path is not a directory"
	case string(fsx.CodeLimitExceeded):
		return "limit exceeds maximum"
	case string(fsx.CodeInputTooLarge):
		return "input too large"
	case string(fsx.CodeTimeout):
		return "operation timed out"
	case string(fsx.CodeCanceled):
		return "operation canceled"
	default:
		return "tool call failed"
	}
}
