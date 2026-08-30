package obsidian

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"sort"
	"unicode/utf8"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"personal-mcp-gateway/internal/fsx"
	"personal-mcp-gateway/internal/limits"
	localmcp "personal-mcp-gateway/internal/mcp"
)

const (
	StatDescription   = "Return safe metadata and the current opaque fingerprint for one allowed existing regular file or empty directory. It does not return content and does not modify the vault."
	WriteDescription  = "Create an absent file or replace one complete existing file only when the explicit absence or fingerprint precondition matches. Values are bounded and atomically visible as complete prior or final bytes; this is not a full CAS guarantee."
	EditDescription   = "Apply one bounded, atomic structured patch to an existing regular file only when its fingerprint and every exact replacement context match the same original source. Patches never infer an encoding."
	MoveDescription   = "Move one allowed file or empty directory to an explicitly absent destination relative to optional base only when the source fingerprint and absent-destination precondition match. The returned destination path uses that same base-relative coordinate, and the source path is removed."
	DeleteDescription = "Permanently remove one allowed file or empty directory only when its current fingerprint matches. This is destructive and has no trash, undo, or recovery promise."
)

var (
	errMutationFingerprint = errors.New("invalid mutation fingerprint")
	errMutationEncoding    = errors.New("invalid mutation encoding")
	errMutationPatch       = errors.New("invalid mutation patch")
)

// mutationDescriptors is wired by the public Obsidian descriptor set. Keeping
// the mutation registration here makes the narrow mutation surface auditable
// independently of discovery and retrieval tools.
func mutationDescriptors(tools *Tools) ([]localmcp.ToolDescriptor, error) {
	if tools == nil || tools.mutator == nil {
		return nil, errors.New("obsidian mutation tools are unavailable")
	}
	stat, err := localmcp.NewToolDescriptor(sdk.Tool{Name: ToolStat, Description: StatDescription, Annotations: mutationReadOnlyAnnotations(), InputSchema: statInputSchema()}, tools.Stat, summarizeStatArgs, summarizeStatResult)
	if err != nil {
		return nil, err
	}
	write, err := localmcp.NewToolDescriptor(sdk.Tool{Name: ToolWrite, Description: WriteDescription, Annotations: mutationWriteAnnotations(), InputSchema: writeInputSchema()}, tools.Write, summarizeWriteArgs, summarizeMutationResult)
	if err != nil {
		return nil, err
	}
	edit, err := localmcp.NewToolDescriptor(sdk.Tool{Name: ToolEdit, Description: EditDescription, Annotations: mutationWriteAnnotations(), InputSchema: editInputSchema()}, tools.Edit, summarizeEditArgs, summarizeMutationResult)
	if err != nil {
		return nil, err
	}
	move, err := localmcp.NewToolDescriptor(sdk.Tool{Name: ToolMove, Description: MoveDescription, Annotations: mutationWriteAnnotations(), InputSchema: moveInputSchema()}, tools.Move, summarizeMoveArgs, summarizeMutationResult)
	if err != nil {
		return nil, err
	}
	deleteTool, err := localmcp.NewToolDescriptor(sdk.Tool{Name: ToolDelete, Description: DeleteDescription, Annotations: mutationWriteAnnotations(), InputSchema: deleteInputSchema()}, tools.Delete, summarizeDeleteArgs, summarizeDeleteResult)
	if err != nil {
		return nil, err
	}
	return []localmcp.ToolDescriptor{stat, write, edit, move, deleteTool}, nil
}

func mutationReadOnlyAnnotations() *sdk.ToolAnnotations {
	destructive, openWorld := false, false
	return &sdk.ToolAnnotations{ReadOnlyHint: true, DestructiveHint: &destructive, IdempotentHint: true, OpenWorldHint: &openWorld}
}

func mutationWriteAnnotations() *sdk.ToolAnnotations {
	destructive, openWorld := true, false
	return &sdk.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive, IdempotentHint: true, OpenWorldHint: &openWorld}
}

func (t *Tools) Stat(ctx context.Context, _ *sdk.CallToolRequest, input StatInput) (*sdk.CallToolResult, StatOutput, error) {
	toolCtx, cancel := context.WithTimeout(ctx, limits.ToolOperationTimeout)
	defer cancel()
	coordinate, err := newPathCoordinate(input.Base)
	if err != nil {
		return mutationStatError(err)
	}
	target, err := t.mutator.StatMutationTarget(toolCtx, input.Base, input.Path)
	if err != nil {
		return mutationStatError(err)
	}
	return successCallResult(), statOutput(target, coordinate), nil
}

func (t *Tools) Write(ctx context.Context, _ *sdk.CallToolRequest, input WriteInput) (*sdk.CallToolResult, MutationOutput, error) {
	toolCtx, cancel := context.WithTimeout(ctx, limits.ToolOperationTimeout)
	defer cancel()
	coordinate, err := newPathCoordinate(input.Base)
	if err != nil {
		return mutationError(err)
	}
	payload, err := decodeMutationValue(input.Encoding, input.Value, MutationMaxValueBytes)
	if err != nil {
		return mutationError(err)
	}
	result, err := mutationWrite(t.mutator, toolCtx, input, payload)
	if err != nil {
		return mutationError(err)
	}
	return successCallResult(), mutationOutput(result, coordinate), nil
}

func mutationWrite(mutator *fsx.Mutator, ctx context.Context, input WriteInput, payload []byte) (fsx.MutationResult, error) {
	switch input.Precondition.Kind {
	case MutationPreconditionAbsent:
		if input.Precondition.Fingerprint != "" {
			return fsx.MutationResult{}, errMutationPatch
		}
		return mutator.CreateFile(ctx, input.Base, input.Path, payload)
	case MutationPreconditionFingerprint:
		fingerprint, err := decodeMutationFingerprint(input.Precondition.Fingerprint)
		if err != nil {
			return fsx.MutationResult{}, err
		}
		return mutator.ReplaceFile(ctx, input.Base, input.Path, fingerprint, payload)
	default:
		return fsx.MutationResult{}, errMutationPatch
	}
}

func (t *Tools) Edit(ctx context.Context, _ *sdk.CallToolRequest, input EditInput) (*sdk.CallToolResult, MutationOutput, error) {
	toolCtx, cancel := context.WithTimeout(ctx, limits.ToolOperationTimeout)
	defer cancel()
	coordinate, err := newPathCoordinate(input.Base)
	if err != nil {
		return mutationError(err)
	}
	fingerprint, err := decodeMutationFingerprint(input.Fingerprint)
	if err != nil {
		return mutationError(err)
	}
	source, err := mutationSource(toolCtx, t.vault, input.Base, input.Path, fingerprint)
	if err != nil {
		return mutationError(err)
	}
	payload, err := applyMutationPatch(source, input.Encoding, input.Replacements)
	if err != nil {
		return mutationError(err)
	}
	result, err := t.mutator.ReplaceFile(toolCtx, input.Base, input.Path, fingerprint, payload)
	if err != nil {
		return mutationError(err)
	}
	return successCallResult(), mutationOutput(result, coordinate), nil
}

func (t *Tools) Move(ctx context.Context, _ *sdk.CallToolRequest, input MoveInput) (*sdk.CallToolResult, MutationOutput, error) {
	toolCtx, cancel := context.WithTimeout(ctx, limits.ToolOperationTimeout)
	defer cancel()
	coordinate, err := newPathCoordinate(input.Base)
	if err != nil {
		return mutationError(err)
	}
	if input.DestinationPrecondition.Kind != MutationPreconditionAbsent || input.DestinationPrecondition.Fingerprint != "" {
		return mutationError(errMutationPatch)
	}
	fingerprint, err := decodeMutationFingerprint(input.Fingerprint)
	if err != nil {
		return mutationError(err)
	}
	result, err := t.mutator.Move(toolCtx, input.Base, input.Source, input.Destination, fingerprint)
	if err != nil {
		return mutationError(err)
	}
	return successCallResult(), mutationOutput(result, coordinate), nil
}

func (t *Tools) Delete(ctx context.Context, _ *sdk.CallToolRequest, input DeleteInput) (*sdk.CallToolResult, DeleteOutput, error) {
	toolCtx, cancel := context.WithTimeout(ctx, limits.ToolOperationTimeout)
	defer cancel()
	coordinate, err := newPathCoordinate(input.Base)
	if err != nil {
		return mutationDeleteError(err)
	}
	fingerprint, err := decodeMutationFingerprint(input.Fingerprint)
	if err != nil {
		return mutationDeleteError(err)
	}
	result, err := t.mutator.Delete(toolCtx, input.Base, input.Path, fingerprint)
	if err != nil {
		return mutationDeleteError(err)
	}
	return successCallResult(), deleteOutput(result, coordinate), nil
}

func mutationSource(ctx context.Context, vault *fsx.Vault, base, path string, fingerprint fsx.SourceFingerprint) ([]byte, error) {
	file, err := vault.OpenFile(ctx, base, path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if file.Fingerprint() != fingerprint {
		return nil, &fsx.Error{Code: fsx.CodeSourceChanged}
	}
	if file.Resolved().Size > MutationMaxFileBytes {
		return nil, &fsx.Error{Code: fsx.CodeInputTooLarge}
	}
	source := make([]byte, 0, file.Resolved().Size)
	buffer := make([]byte, 32*1024)
	for {
		remaining := MutationMaxFileBytes + 1 - len(source)
		if remaining <= 0 {
			return nil, &fsx.Error{Code: fsx.CodeInputTooLarge}
		}
		readSize := min(len(buffer), remaining)
		n, readErr := file.Read(ctx, buffer[:readSize])
		source = append(source, buffer[:n]...)
		if len(source) > MutationMaxFileBytes {
			return nil, &fsx.Error{Code: fsx.CodeInputTooLarge}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return nil, readErr
		}
	}
	if err := file.Revalidate(ctx); err != nil {
		return nil, err
	}
	return source, nil
}

type mutationSpan struct {
	start, end  int
	replacement []byte
}

func applyMutationPatch(source []byte, encoding string, replacements []EditReplacement) ([]byte, error) {
	if len(replacements) < 1 || len(replacements) > MutationMaxReplacements {
		return nil, errMutationPatch
	}
	spans := make([]mutationSpan, 0, len(replacements))
	total := 0
	for _, replacement := range replacements {
		old, err := decodeMutationValue(encoding, replacement.Old, MutationMaxValueBytes)
		if err != nil || len(old) == 0 {
			return nil, errMutationPatch
		}
		newValue, err := decodeMutationValue(encoding, replacement.New, MutationMaxValueBytes)
		if err != nil {
			return nil, errMutationPatch
		}
		total += len(old) + len(newValue)
		if total > MutationMaxValueBytes {
			return nil, errMutationPatch
		}
		start := bytes.Index(source, old)
		if start < 0 || bytes.Index(source[start+1:], old) >= 0 {
			return nil, errMutationPatch
		}
		spans = append(spans, mutationSpan{start: start, end: start + len(old), replacement: newValue})
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	finalLen, cursor := len(source), 0
	for _, span := range spans {
		if span.start < cursor {
			return nil, errMutationPatch
		}
		cursor = span.end
		finalLen += len(span.replacement) - (span.end - span.start)
	}
	if finalLen < 0 || finalLen > MutationMaxFileBytes {
		return nil, &fsx.Error{Code: fsx.CodeInputTooLarge}
	}
	result := make([]byte, 0, finalLen)
	cursor = 0
	for _, span := range spans {
		result = append(result, source[cursor:span.start]...)
		result = append(result, span.replacement...)
		cursor = span.end
	}
	return append(result, source[cursor:]...), nil
}

func decodeMutationFingerprint(value string) (fsx.SourceFingerprint, error) {
	var fingerprint fsx.SourceFingerprint
	if len(value) != 43 {
		return fingerprint, errMutationFingerprint
	}
	n, err := base64.RawURLEncoding.Strict().Decode(fingerprint[:], []byte(value))
	if err != nil || n != len(fingerprint) || base64.RawURLEncoding.EncodeToString(fingerprint[:]) != value {
		return fsx.SourceFingerprint{}, errMutationFingerprint
	}
	return fingerprint, nil
}

func decodeMutationValue(encoding, value string, maxBytes int) ([]byte, error) {
	var decoded []byte
	switch encoding {
	case MutationEncodingUTF8:
		if !utf8.ValidString(value) {
			return nil, errMutationEncoding
		}
		decoded = []byte(value)
	case MutationEncodingBase64:
		if len(value) > base64.StdEncoding.EncodedLen(maxBytes) {
			return nil, &fsx.Error{Code: fsx.CodeInputTooLarge}
		}
		candidate, err := base64.StdEncoding.Strict().DecodeString(value)
		if err != nil || base64.StdEncoding.EncodeToString(candidate) != value {
			return nil, errMutationEncoding
		}
		decoded = candidate
	default:
		return nil, errMutationEncoding
	}
	if len(decoded) > maxBytes {
		return nil, &fsx.Error{Code: fsx.CodeInputTooLarge}
	}
	return decoded, nil
}

func statOutput(target fsx.MutationTarget, coordinate pathCoordinate) StatOutput {
	r := target.Resolved
	return StatOutput{OK: true, Path: coordinate.project(r.Rel), Type: string(r.Kind), Size: r.Size, Modified: r.Modified.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"), Fingerprint: fingerprintString(target.Fingerprint)}
}

func mutationOutput(result fsx.MutationResult, coordinate pathCoordinate) MutationOutput {
	r := result.Resolved
	return MutationOutput{OK: true, Path: coordinate.project(r.Rel), Type: string(r.Kind), Size: r.Size, Modified: r.Modified.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"), Fingerprint: fingerprintString(result.Fingerprint)}
}

func deleteOutput(result fsx.DeleteResult, coordinate pathCoordinate) DeleteOutput {
	return DeleteOutput{OK: true, Path: coordinate.project(result.Resolved.Rel), Permanent: true}
}

func mutationError(err error) (*sdk.CallToolResult, MutationOutput, error) {
	return errorCallResult(), MutationOutput{OK: false, Error: mutationToolError(err)}, nil
}
func mutationStatError(err error) (*sdk.CallToolResult, StatOutput, error) {
	return errorCallResult(), StatOutput{OK: false, Error: mutationToolError(err)}, nil
}
func mutationDeleteError(err error) (*sdk.CallToolResult, DeleteOutput, error) {
	return errorCallResult(), DeleteOutput{OK: false, Error: mutationToolError(err)}, nil
}

func mutationToolError(err error) *ToolError {
	code := mutationErrorCode(err)
	return &ToolError{Code: code, Message: mutationSanitizedMessage(code)}
}

func mutationErrorCode(err error) string {
	switch {
	case errors.Is(err, errMutationFingerprint):
		return "invalid_fingerprint"
	case errors.Is(err, errMutationEncoding):
		return "invalid_encoding"
	case errors.Is(err, errMutationPatch):
		return "invalid_patch"
	case fsx.IsCode(err, fsx.CodeDestinationExists):
		return string(fsx.CodeDestinationExists)
	case fsx.IsCode(err, fsx.CodeNotEmpty):
		return string(fsx.CodeNotEmpty)
	case fsx.IsCode(err, fsx.CodeNotFile):
		return string(fsx.CodeUnsupported)
	case fsx.IsCode(err, fsx.CodeUnsupported):
		return string(fsx.CodeUnsupported)
	case fsx.IsCode(err, fsx.CodeUncertain):
		return string(fsx.CodeUncertain)
	default:
		return errorCode(err)
	}
}

func mutationSanitizedMessage(code string) string {
	switch code {
	case "invalid_fingerprint":
		return "fingerprint is invalid"
	case "invalid_encoding":
		return "value encoding is invalid"
	case "invalid_patch":
		return "patch is invalid"
	case string(fsx.CodeDestinationExists):
		return "destination already exists"
	case string(fsx.CodeNotEmpty):
		return "directory is not empty"
	case string(fsx.CodeUnsupported):
		return "operation is unsupported"
	case string(fsx.CodeUncertain):
		return "operation outcome is uncertain; do not replay automatically"
	default:
		return sanitizedMessage(code)
	}
}
