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

const documentTransferProbeSHA256 = "f8005c1abc16d9be4631d22d3a07e242f39a837187482669343d448b99067cc4"

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
	if hex.EncodeToString(digest[:]) != documentTransferProbeSHA256 {
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
	resultBytes, err := json.Marshal(result)
	if err != nil {
		return errors.New("document transfer probe result was invalid")
	}
	if report != nil {
		report.SDKResultCount++
		report.ToolCalls.add(obsidian.ToolDocumentTransferProbe)
		if len(resultBytes) > report.MaxSDKResultBytes {
			report.MaxSDKResultBytes = len(resultBytes)
		}
		if len(encoded) > report.MaxStructuredResultBytes {
			report.MaxStructuredResultBytes = len(encoded)
		}
		if latency := time.Since(started).Microseconds(); latency > report.MaxClientLatencyMicroseconds {
			report.MaxClientLatencyMicroseconds = latency
		}
	}
	return nil
}
