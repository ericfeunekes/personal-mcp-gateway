package fsx

import (
	"context"
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/text/unicode/norm"

	"personal-mcp-gateway/internal/limits"
)

const (
	DefaultLimit = 100
	MaxLimit     = 500
)

type Code string

const (
	CodePathDenied    Code = "path_denied"
	CodeSymlinkDenied Code = "symlink_denied"
	CodeNotFound      Code = "not_found"
	CodeNotDirectory  Code = "not_directory"
	CodeLimitExceeded Code = "limit_exceeded"
	CodeCanceled      Code = "canceled"
	CodeInputTooLarge Code = "input_too_large"
	CodeSourceChanged Code = "source_changed"
	CodeTimeout       Code = "timeout"
)

type Error struct {
	Code Code
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return string(e.Code)
}

func IsCode(err error, code Code) bool {
	var fsErr *Error
	return errors.As(err, &fsErr) && fsErr.Code == code
}

type Kind string

const (
	KindFile    Kind = "file"
	KindDir     Kind = "directory"
	KindSymlink Kind = "symlink"
	KindOther   Kind = "other"
)

type Vault struct {
	root      string
	testHooks *vaultTestHooks
}

// vaultTestHooks are instance-local deterministic race seams. They are
// intentionally unavailable outside this package and nil in production.
type vaultTestHooks struct {
	beforeOpenSegment  func(depth int)
	beforeListScan     func()
	afterListBatch     func(filesScanned uint64)
	afterEntryBaseline func()
}

type Resolved struct {
	Rel      string
	Exists   bool
	Kind     Kind
	Size     int64
	Modified time.Time
}

type Entry struct {
	Name     string
	Rel      string
	Kind     Kind
	Size     int64
	Modified time.Time
	Position Position
}

func NewVault(root string) (*Vault, error) {
	clean := filepath.Clean(root)
	if evaluated, err := filepath.EvalSymlinks(clean); err == nil {
		clean = filepath.Clean(evaluated)
	}
	info, err := os.Stat(clean)
	if err != nil || !info.IsDir() {
		return nil, &Error{Code: CodeNotDirectory}
	}
	return &Vault{root: clean}, nil
}

func (v *Vault) Root() string {
	return v.root
}

func normalizeRel(base, input string) (string, error) {
	if strings.TrimSpace(input) == "" {
		return "", &Error{Code: CodePathDenied}
	}
	if len([]byte(base)) > limits.PathMaxBytes || len([]byte(input)) > limits.PathMaxBytes {
		return "", &Error{Code: CodeInputTooLarge}
	}
	if isAbsToolPath(base) || isAbsToolPath(input) {
		return "", &Error{Code: CodePathDenied}
	}

	base = slashClean(base)
	input = slashClean(input)
	if base == "" {
		base = "."
	}

	combined := input
	if base != "." && input != "." {
		combined = path.Join(base, input)
	} else if input == "." {
		combined = base
	}
	clean := path.Clean(combined)
	if clean == "/" || clean == "." {
		return ".", nil
	}
	if len([]byte(clean)) > limits.PathMaxBytes {
		return "", &Error{Code: CodeInputTooLarge}
	}
	if strings.HasPrefix(clean, "../") || clean == ".." {
		return "", &Error{Code: CodePathDenied}
	}

	segments := strings.Split(clean, "/")
	if len(segments) > limits.PathMaxSegments {
		return "", &Error{Code: CodeInputTooLarge}
	}
	for _, segment := range segments {
		if segment == "" || segment == "." {
			continue
		}
		if deniedSegment(segment) {
			return "", &Error{Code: CodePathDenied}
		}
	}
	return clean, nil
}

func slashClean(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\\", "/"))
	if s == "" {
		return ""
	}
	return path.Clean(s)
}

func isAbsToolPath(s string) bool {
	if s == "" {
		return false
	}
	if strings.HasPrefix(s, "/") || strings.HasPrefix(s, "\\") {
		return true
	}
	return filepath.IsAbs(s) || filepath.VolumeName(s) != ""
}

func deniedSegment(segment string) bool {
	return strings.HasPrefix(segment, ".")
}

func relSegments(rel string) []string {
	if rel == "." || rel == "" {
		return nil
	}
	return strings.Split(rel, "/")
}

func joinRel(parent, name string) string {
	if parent == "." || parent == "" {
		return name
	}
	return parent + "/" + name
}

func nfcRel(parts []string) string {
	if len(parts) == 0 {
		return "."
	}
	normalized := make([]string, len(parts))
	for i, part := range parts {
		normalized[i] = norm.NFC.String(part)
	}
	return strings.Join(normalized, "/")
}

func contextError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{Code: CodeTimeout}
	}
	return &Error{Code: CodeCanceled}
}
