package obsidian

const (
	ToolRead     = "read"
	ToolReadMany = "read_many"
	ToolGrep     = "grep"

	ReadDescription     = "Read one bounded Markdown source unit from a canonical vault path. Choose content, heading, block, frontmatter, or outline with selector; preserve the same path, selector, and max_bytes when continuing with coverage.next_cursor. Use grep to discover notes and read_many to fetch several known notes."
	ReadManyDescription = "Read one to 20 known Markdown source units in input order with one aggregate byte budget; use it after grep has found the paths you need. Item errors do not discard other results. Accumulate only the new items returned on each page; an item split across pages repeats its index with the next non-overlapping range. Continue with the identical requests and max_bytes plus coverage.next_cursor."
	GrepDescription     = "Search Markdown contents in deterministic canonical-path order, then use returned canonical paths with read_many. This is content grep, not filename ranking or semantic search. Results are matching lines with bounded context; follow coverage.next_cursor with the identical pattern, scope, mode, context, result limit, and scan budgets until complete, and restart without a cursor only when continuation is restart."
)

const (
	SelectorContent     = "content"
	SelectorHeading     = "heading"
	SelectorBlock       = "block"
	SelectorFrontmatter = "frontmatter"
	SelectorOutline     = "outline"
)

const (
	UnsupportedFileCode   = "unsupported_file"
	InvalidUTF8Code       = "invalid_utf8"
	InvalidSelectorCode   = "invalid_selector"
	SelectorNotFoundCode  = "selector_not_found"
	SelectorAmbiguousCode = "selector_ambiguous"
	InvalidRegexCode      = "invalid_regex"
)

// ReadSelector is the typed handler representation of the closed selector
// union. The advertised schema supplies the oneOf/closed-world constraint;
// validateReadSelector repeats it at the domain boundary.
type ReadSelector struct {
	Kind       string `json:"kind"`
	StartLine  int    `json:"start_line,omitempty"`
	Heading    string `json:"heading,omitempty"`
	Occurrence int    `json:"occurrence,omitempty"`
	BlockID    string `json:"block_id,omitempty"`
}

type ReadInput struct {
	Path     string        `json:"path" jsonschema:"canonical vault-relative Markdown path returned by resolve, ls, or grep"`
	Base     string        `json:"base,omitempty" jsonschema:"optional vault-relative base used only to resolve path"`
	Selector *ReadSelector `json:"selector,omitempty" jsonschema:"closed source-unit selector; defaults to content from line 1"`
	MaxBytes int           `json:"max_bytes,omitempty" jsonschema:"selected source-byte work for this call, 1 through 262144; defaults to 65536 and never widens the 65536-byte complete SDK result cap"`
	Cursor   string        `json:"cursor,omitempty" jsonschema:"coverage.next_cursor from the prior read page; repeat the identical path, base, selector, and max_bytes"`
}

type ReadRequest struct {
	Path     string        `json:"path" jsonschema:"canonical vault-relative Markdown path returned by resolve, ls, or grep"`
	Base     string        `json:"base,omitempty" jsonschema:"optional vault-relative base used only to resolve path"`
	Selector *ReadSelector `json:"selector,omitempty" jsonschema:"closed source-unit selector; defaults to content from line 1"`
	MaxBytes int           `json:"max_bytes,omitempty" jsonschema:"selected source-byte work for this item, 1 through 262144; defaults to 65536"`
}

type OutlineEntry struct {
	Line  int    `json:"line"`
	Level int    `json:"level"`
	Text  string `json:"text"`
}

// ReadResult is shared by read and successful read_many items. Exactly one of
// Content and Outline is non-nil on success.
type ReadResult struct {
	Path        string          `json:"path,omitempty"`
	Selector    *ReadSelector   `json:"selector,omitempty"`
	StartLine   int             `json:"start_line,omitempty"`
	EndLine     int             `json:"end_line,omitempty"`
	TotalLines  *int            `json:"total_lines,omitempty"`
	Modified    string          `json:"modified,omitempty"`
	Fingerprint string          `json:"fingerprint,omitempty"`
	Truncated   bool            `json:"truncated"`
	Coverage    Coverage        `json:"coverage"`
	Content     *string         `json:"content,omitempty"`
	Outline     *[]OutlineEntry `json:"outline,omitempty"`
}

type ReadOutput struct {
	OK bool `json:"ok"`
	ReadResult
	Error *ToolError `json:"error,omitempty"`
}

type ReadManyInput struct {
	Requests []ReadRequest `json:"requests" jsonschema:"one to 20 first-call read request shapes, processed strictly in order"`
	MaxBytes int           `json:"max_bytes,omitempty" jsonschema:"aggregate selected source-byte work for this page, 1 through 262144; defaults to 65536"`
	Cursor   string        `json:"cursor,omitempty" jsonschema:"coverage.next_cursor from the prior read_many page; repeat the identical ordered requests and max_bytes"`
}

type ReadManyItem struct {
	Index int  `json:"index"`
	OK    bool `json:"ok"`
	ReadResult
	Error *ToolError `json:"error,omitempty"`
}

type ReadManyOutput struct {
	OK                    bool           `json:"ok"`
	Items                 []ReadManyItem `json:"items"`
	NextRequestIndex      int            `json:"next_request_index"`
	RemainingRequestCount int            `json:"remaining_request_count"`
	Truncated             bool           `json:"truncated"`
	Coverage              Coverage       `json:"coverage"`
	Error                 *ToolError     `json:"error,omitempty"`
}

type GrepInput struct {
	Pattern       string `json:"pattern" jsonschema:"non-empty UTF-8 Go RE2 pattern or literal, at most 4096 bytes; never retained in telemetry"`
	Path          string `json:"path,omitempty" jsonschema:"vault-relative directory or Markdown file scope; defaults to ."`
	Base          string `json:"base,omitempty" jsonschema:"optional vault-relative base used only to resolve path"`
	Regex         *bool  `json:"regex,omitempty" jsonschema:"true for Go RE2 syntax and false for a literal; defaults to true"`
	CaseSensitive bool   `json:"case_sensitive,omitempty" jsonschema:"case-sensitive matching when true; defaults to false"`
	ContextLines  *int   `json:"context_lines,omitempty" jsonschema:"complete source lines before and after each match, 0 through 3; defaults to 1"`
	Limit         int    `json:"limit,omitempty" jsonschema:"matching-line result limit, 1 through 200; defaults to 50"`
	MaxFiles      int    `json:"max_files,omitempty" jsonschema:"Markdown files opened for new content work, 1 through 50000; defaults to 10000"`
	MaxBytes      int64  `json:"max_bytes,omitempty" jsonschema:"source bytes read for new content work, 1 through 1073741824; defaults to 268435456"`
	Cursor        string `json:"cursor,omitempty" jsonschema:"coverage.next_cursor from the prior grep page; repeat every other field identically"`
}

type GrepContextLine struct {
	Line int    `json:"line"`
	Text string `json:"text"`
}

type GrepMatch struct {
	Path        string            `json:"path"`
	Line        int               `json:"line"`
	Column      int               `json:"column"`
	Occurrences int               `json:"occurrences"`
	Text        string            `json:"text"`
	Before      []GrepContextLine `json:"before"`
	After       []GrepContextLine `json:"after"`
	Fingerprint string            `json:"fingerprint"`
}

type GrepOutput struct {
	OK        bool        `json:"ok"`
	Path      string      `json:"path,omitempty"`
	Matches   []GrepMatch `json:"matches"`
	Truncated bool        `json:"truncated"`
	Coverage  Coverage    `json:"coverage"`
	Error     *ToolError  `json:"error,omitempty"`
}
