package obsidian

const (
	MaxCursorBytes     = 16 * 1024
	MaxSDKResultBytes  = 64 * 1024
	SDKResultReserve   = 1024
	ResponseContractV1 = 1
)

type Coverage struct {
	ResultComplete bool   `json:"result_complete" jsonschema:"whether this result contains every matching entry after the requested position; when false with cursor continuation, use next_cursor instead of widening limit"`
	ScopeComplete  bool   `json:"scope_complete" jsonschema:"whether the complete shallow directory scope was examined"`
	Consistency    string `json:"consistency" jsonschema:"stable when the directory source was unchanged; best_effort otherwise"`
	FilesScanned   uint64 `json:"files_scanned" jsonschema:"directory entries examined, including denied entries"`
	BytesScanned   uint64 `json:"bytes_scanned" jsonschema:"file-content bytes read; always zero for metadata-only ls"`
	StoppedBy      string `json:"stopped_by" jsonschema:"scope, result_limit, response_limit, timeout, canceled, source_change, or error"`
	Continuation   string `json:"continuation" jsonschema:"complete means done; cursor means the next call must pass next_cursor as cursor with identical path, base, and limit, because omitting it restarts at the first entry; restart means begin again without a cursor"`
	NextCursor     string `json:"next_cursor,omitempty" jsonschema:"opaque continuation required for the next page when continuation is cursor; pass it unchanged only in the cursor input field, never path or base, with identical path, base, and limit; never widen limit to continue"`
}
