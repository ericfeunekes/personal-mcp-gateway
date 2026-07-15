package obsidian

const (
	MaxCursorBytes     = 16 * 1024
	MaxSDKResultBytes  = 64 * 1024
	SDKResultReserve   = 1024
	ResponseContractV1 = 1
)

type Coverage struct {
	ResultComplete bool   `json:"result_complete" jsonschema:"whether this result contains every matching entry after the requested position"`
	ScopeComplete  bool   `json:"scope_complete" jsonschema:"whether the complete shallow directory scope was examined"`
	Consistency    string `json:"consistency" jsonschema:"stable when the directory source was unchanged; best_effort otherwise"`
	FilesScanned   uint64 `json:"files_scanned" jsonschema:"directory entries examined, including denied entries"`
	BytesScanned   uint64 `json:"bytes_scanned" jsonschema:"file-content bytes read; always zero for metadata-only ls"`
	StoppedBy      string `json:"stopped_by" jsonschema:"scope, result_limit, response_limit, timeout, canceled, source_change, or error"`
	Continuation   string `json:"continuation" jsonschema:"complete means done; cursor means repeat identical path, base, and limit with next_cursor; restart means begin again without a cursor"`
	NextCursor     string `json:"next_cursor,omitempty" jsonschema:"opaque continuation returned by ls; do not edit and reuse only with identical path, base, and limit"`
}
