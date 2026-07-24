package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"personal-mcp-gateway/internal/tools/obsidian"
)

func verifyDocumentTransferProbe(ctx context.Context, session *sdk.ClientSession, report *smokeReport) error {
	started := time.Now()
	result, err := session.CallTool(ctx, &sdk.CallToolParams{
		Name:      obsidian.ToolDocumentTransferProbe,
		Arguments: map[string]any{},
	})
	if err != nil || result == nil || result.IsError || len(result.Content) != 1 {
		return errors.New("document transfer probe call failed")
	}
	embedded, ok := result.Content[0].(*sdk.EmbeddedResource)
	if !ok || embedded.Resource == nil || embedded.Resource.URI != obsidian.DocumentTransferProbeURI ||
		embedded.Resource.MIMEType != obsidian.DocumentTransferProbeMIME {
		return errors.New("document transfer probe resource identity changed")
	}
	digest := sha256.Sum256(embedded.Resource.Blob)
	expectedSHA, expectedSize := obsidian.DocumentTransferProbeSHA256ForSize(len(embedded.Resource.Blob))
	if !expectedSize || hex.EncodeToString(digest[:]) != expectedSHA {
		return errors.New("document transfer probe bytes changed")
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return errors.New("document transfer probe metadata was invalid")
	}
	var out obsidian.DocumentTransferProbeOutput
	if json.Unmarshal(encoded, &out) != nil || out.MIMEType != obsidian.DocumentTransferProbeMIME ||
		out.RawBytes != len(embedded.Resource.Blob) {
		return errors.New("document transfer probe metadata did not match the resource")
	}
	if report != nil {
		report.SDKResultCount++
		report.ToolCalls.add(obsidian.ToolDocumentTransferProbe)
		if len(encoded) > report.MaxStructuredResultBytes {
			report.MaxStructuredResultBytes = len(encoded)
		}
		if latency := time.Since(started).Microseconds(); latency > report.MaxClientLatencyMicroseconds {
			report.MaxClientLatencyMicroseconds = latency
		}
	}
	return nil
}
