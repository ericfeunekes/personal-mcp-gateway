package obsidian

const (
	MaxCursorBytes           = 16 * 1024
	MaxSDKResultBytes        = 64 * 1024
	SDKResultReserve         = 1024
	ResponseContractV1       = 1
	MaxMarkdownSourceBytes   = 8 * 1024 * 1024
	MaxMarkdownSourceLines   = 50_000
	MaxGrepPhysicalLineBytes = 1024 * 1024
	DefaultReadBytes         = 64 * 1024
	MaxReadBytes             = 256 * 1024
	DefaultReadManyBytes     = 64 * 1024
	MaxReadManyBytes         = 256 * 1024
	MaxReadManyRequests      = 20
	DefaultGrepLimit         = 50
	MaxGrepLimit             = 200
	DefaultGrepContextLines  = 1
	MaxGrepContextLines      = 3
	MaxGrepPatternBytes      = 4 * 1024
	DefaultGrepMaxFiles      = 10_000
	MaxGrepMaxFiles          = 50_000
	DefaultGrepMaxBytes      = 256 * 1024 * 1024
	MaxGrepMaxBytes          = 1024 * 1024 * 1024
)

type Coverage struct {
	ResultComplete         bool   `json:"result_complete" jsonschema:"whether every result or selected source unit discovered for this page boundary fit; when false with cursor continuation, use next_cursor without widening limits or budgets"`
	ScopeComplete          bool   `json:"scope_complete" jsonschema:"whether every source required by the declared query scope and boundary was examined"`
	Consistency            string `json:"consistency" jsonschema:"stable when the observed source and catalog stamps were unchanged; best_effort otherwise"`
	FilesScanned           uint64 `json:"files_scanned" jsonschema:"source entries or files whose metadata or content was examined during this call"`
	BytesScanned           uint64 `json:"bytes_scanned" jsonschema:"source-content bytes read during this call; zero for metadata-only work"`
	SourceEntriesValidated uint64 `json:"source_entries_validated" jsonschema:"metadata source entries revalidated before returning this page; zero on a first call"`
	StoppedBy              string `json:"stopped_by" jsonschema:"scope, result_limit, response_limit, file_limit, byte_limit, timeout, canceled, source_change, or error"`
	Continuation           string `json:"continuation" jsonschema:"complete means done; cursor means the next call must pass next_cursor unchanged as cursor and repeat every other query field, because omitting it restarts at the first page; restart means begin again without a cursor"`
	NextCursor             string `json:"next_cursor,omitempty" jsonschema:"opaque continuation required for the next page when continuation is cursor; pass it unchanged only in the cursor input field, never path or base, repeat every other query field, and never widen a limit or budget to continue"`
}
