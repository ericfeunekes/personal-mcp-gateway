package ynab

import (
	"encoding/json"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const maxResultBytes = 192 << 10

// Reserve space for the SDK 2026 resultType/server metadata and JSON-RPC framing.
// An emitted-wire boundary test protects this margin when identity changes.
const resultEnvelopeReserve = 8 << 10

func finishOutput(out Output, mutation bool) (*sdk.CallToolResult, Output, error) {
	for i := range out.Results {
		out.Results[i] = normalizeResult(out.Results[i])
	}
	// The cheap size lower bound avoids a second encoding of a very large read.
	tooLarge := false
	bytes := 0
	for _, r := range out.Results {
		for _, rows := range r.collections {
			for _, row := range rows {
				bytes += len(row)
			}
		}
	}
	if bytes > maxResultBytes-resultEnvelopeReserve {
		tooLarge = true
	}
	if !tooLarge {
		candidate := callOutput(out)
		encoded, err := json.Marshal(candidate)
		tooLarge = err != nil || len(encoded) > maxResultBytes-resultEnvelopeReserve
	}
	if tooLarge {
		for i, r := range out.Results {
			if r.Status == "not_attempted" {
				continue
			}
			status := "error"
			message := "result exceeds 192 KiB; narrow the dates or scope, reduce the batch, or request a file export"
			if mutation {
				status = "uncertain"
				message = "provider operation was dispatched but its result cannot be delivered within the response limit; do not replay automatically"
			}
			out.Results[i] = normalizeResult(Result{Index: r.Index, Status: status, Error: message, ErrorCode: "response_too_large", Recovery: "fix_request"})
		}
		return errorCallOutput(out), out, nil
	}
	return callOutput(out), out, nil
}
