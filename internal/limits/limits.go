package limits

import "time"

const (
	HTTPRequestBodyBytes int64 = 1 << 20
	StdioMessageBytes    int64 = 1 << 20

	TelemetryArgsBytes   = 64 << 10
	TelemetryMaxKeys     = 64
	TelemetryMaxKeyBytes = 128
	TelemetryEventBytes  = 16 << 10

	PathMaxBytes    = 4096
	PathMaxSegments = 128

	ToolOperationTimeout = 2 * time.Second
)
