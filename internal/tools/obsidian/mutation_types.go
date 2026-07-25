package obsidian

const (
	ToolStat   = "stat"
	ToolWrite  = "write"
	ToolEdit   = "edit"
	ToolMove   = "move"
	ToolDelete = "delete"

	MutationEncodingUTF8            = "utf8"
	MutationEncodingBase64          = "base64"
	MutationPreconditionAbsent      = "absent"
	MutationPreconditionFingerprint = "fingerprint"
	MutationMaxValueBytes           = 512 * 1024
	MutationMaxFileBytes            = 8 * 1024 * 1024
	MutationMaxReplacements         = 64
)

type MutationPrecondition struct {
	Kind        string `json:"kind"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

type StatInput struct {
	Path string `json:"path"`
	Base string `json:"base,omitempty"`
}

type StatOutput struct {
	OK          bool       `json:"ok"`
	Path        string     `json:"path,omitempty"`
	Type        string     `json:"type,omitempty"`
	Size        int64      `json:"size"`
	Modified    string     `json:"modified,omitempty"`
	Fingerprint string     `json:"fingerprint,omitempty"`
	Error       *ToolError `json:"error,omitempty"`
}

type WriteInput struct {
	Path         string               `json:"path"`
	Base         string               `json:"base,omitempty"`
	Encoding     string               `json:"encoding"`
	Value        string               `json:"value"`
	Precondition MutationPrecondition `json:"precondition"`
}

type EditReplacement struct {
	Old string `json:"old"`
	New string `json:"new"`
}

type EditInput struct {
	Path         string            `json:"path"`
	Base         string            `json:"base,omitempty"`
	Fingerprint  string            `json:"fingerprint"`
	Encoding     string            `json:"encoding"`
	Replacements []EditReplacement `json:"replacements"`
}

type MoveInput struct {
	Source                  string               `json:"source"`
	Destination             string               `json:"destination"`
	Base                    string               `json:"base,omitempty"`
	Fingerprint             string               `json:"fingerprint"`
	DestinationPrecondition MutationPrecondition `json:"destination_precondition"`
}

type DeleteInput struct {
	Path        string `json:"path"`
	Base        string `json:"base,omitempty"`
	Fingerprint string `json:"fingerprint"`
}

type MutationOutput struct {
	OK          bool       `json:"ok"`
	Path        string     `json:"path,omitempty"`
	Type        string     `json:"type,omitempty"`
	Size        int64      `json:"size"`
	Modified    string     `json:"modified,omitempty"`
	Fingerprint string     `json:"fingerprint,omitempty"`
	Error       *ToolError `json:"error,omitempty"`
}

type DeleteOutput struct {
	OK        bool       `json:"ok"`
	Path      string     `json:"path,omitempty"`
	Permanent bool       `json:"permanent"`
	Error     *ToolError `json:"error,omitempty"`
}
