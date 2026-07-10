package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"personal-mcp-gateway/internal/limits"
)

func SafeIdentifier(value, runID, unknownLabel string, allowed ...string) (string, map[string]any) {
	trimmed := strings.TrimSpace(value)
	out := map[string]any{
		"present": trimmed != "",
		"known":   false,
		"bytes":   len([]byte(value)),
	}
	if len([]byte(value)) > limits.TelemetryMaxKeyBytes {
		out["too_large"] = true
	}

	for _, allowedValue := range allowed {
		if trimmed == allowedValue {
			out["known"] = true
			out["value"] = allowedValue
			return allowedValue, out
		}
	}

	if trimmed != "" {
		out["hash"] = HashString(runID, trimmed)
		if len([]byte(trimmed)) > limits.TelemetryMaxKeyBytes {
			out["truncated"] = true
		}
	}
	return unknownLabel, out
}

func HashString(runID, value string) string {
	sum := sha256.Sum256([]byte(runID + "\x00" + value))
	return hex.EncodeToString(sum[:8])
}
