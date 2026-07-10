package fsx

import (
	"context"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

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
	root string
}

type Resolved struct {
	Rel      string
	Abs      string
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
}

type ListResult struct {
	Dir       Resolved
	Entries   []Entry
	Truncated bool
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

func (v *Vault) Resolve(ctx context.Context, base, input string) (Resolved, error) {
	if err := ctx.Err(); err != nil {
		return Resolved{}, contextError(err)
	}

	rel, err := normalizeRel(base, input)
	if err != nil {
		return Resolved{}, err
	}

	abs := v.absPath(rel)
	resolved := Resolved{Rel: rel, Abs: abs}
	info, err := v.lstatConfined(rel)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			resolved.Exists = false
			return resolved, nil
		}
		return Resolved{}, err
	}

	resolved.Exists = true
	resolved.Kind = kindOf(info.Mode())
	resolved.Size = info.Size()
	resolved.Modified = info.ModTime().UTC()
	return resolved, nil
}

func (v *Vault) List(ctx context.Context, base, input string, limit int) (ListResult, error) {
	if err := ctx.Err(); err != nil {
		return ListResult{}, contextError(err)
	}
	if limit == 0 {
		limit = DefaultLimit
	}
	if limit < 0 || limit > MaxLimit {
		return ListResult{}, &Error{Code: CodeLimitExceeded}
	}

	resolved, err := v.Resolve(ctx, base, input)
	if err != nil {
		return ListResult{}, err
	}
	if !resolved.Exists {
		return ListResult{}, &Error{Code: CodeNotFound}
	}
	if resolved.Kind == KindSymlink {
		return ListResult{}, &Error{Code: CodeSymlinkDenied}
	}
	if resolved.Kind != KindDir {
		return ListResult{}, &Error{Code: CodeNotDirectory}
	}

	dir, err := os.Open(resolved.Abs)
	if err != nil {
		return ListResult{}, &Error{Code: CodeNotDirectory}
	}
	defer dir.Close()

	var selected []string
	truncated := false
	visible := 0
	for {
		if err := ctx.Err(); err != nil {
			return ListResult{}, contextError(err)
		}

		infos, err := dir.ReadDir(64)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return ListResult{}, &Error{Code: CodeNotFound}
			}
			if errors.Is(err, os.ErrClosed) {
				return ListResult{}, &Error{Code: CodeNotDirectory}
			}
			if errors.Is(err, io.EOF) {
				break
			}
			if errors.Is(err, os.ErrPermission) {
				return ListResult{}, &Error{Code: CodeNotDirectory}
			}
			if len(infos) == 0 {
				break
			}
		}
		if len(infos) == 0 {
			break
		}

		for _, dirEntry := range infos {
			if err := ctx.Err(); err != nil {
				return ListResult{}, contextError(err)
			}
			name := dirEntry.Name()
			if deniedSegment(name) {
				continue
			}

			visible++
			if len(selected) < limit {
				selected = append(selected, name)
				continue
			}
			truncated = true
			maxIndex := maxNameIndex(selected)
			if maxIndex >= 0 && name < selected[maxIndex] {
				selected[maxIndex] = name
			}
		}
	}

	if visible > limit {
		truncated = true
	}

	sort.Strings(selected)
	entries := make([]Entry, 0, len(selected))
	for _, name := range selected {
		if err := ctx.Err(); err != nil {
			return ListResult{}, contextError(err)
		}
		info, err := os.Lstat(filepath.Join(resolved.Abs, name))
		if err != nil {
			continue
		}
		entries = append(entries, Entry{
			Name:     name,
			Rel:      joinRel(resolved.Rel, name),
			Kind:     kindOf(info.Mode()),
			Size:     info.Size(),
			Modified: info.ModTime().UTC(),
		})
	}

	return ListResult{
		Dir:       resolved,
		Entries:   entries,
		Truncated: truncated,
	}, nil
}

func (v *Vault) lstatConfined(rel string) (os.FileInfo, error) {
	segments := relSegments(rel)
	current := v.root
	var info os.FileInfo
	for i, segment := range segments {
		current = filepath.Join(current, segment)
		got, err := os.Lstat(current)
		if err != nil {
			return nil, err
		}
		info = got
		if got.Mode()&os.ModeSymlink != 0 && i < len(segments)-1 {
			return nil, &Error{Code: CodeSymlinkDenied}
		}
	}
	if len(segments) == 0 {
		return os.Lstat(v.root)
	}
	return info, nil
}

func (v *Vault) absPath(rel string) string {
	if rel == "." {
		return v.root
	}
	return filepath.Join(v.root, filepath.FromSlash(rel))
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

func kindOf(mode os.FileMode) Kind {
	switch {
	case mode&os.ModeSymlink != 0:
		return KindSymlink
	case mode.IsDir():
		return KindDir
	case mode.IsRegular():
		return KindFile
	default:
		return KindOther
	}
}

func joinRel(parent, name string) string {
	if parent == "." || parent == "" {
		return name
	}
	return parent + "/" + name
}

func maxNameIndex(names []string) int {
	if len(names) == 0 {
		return -1
	}
	maxIndex := 0
	for i := 1; i < len(names); i++ {
		if names[i] > names[maxIndex] {
			maxIndex = i
		}
	}
	return maxIndex
}

func contextError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{Code: CodeTimeout}
	}
	return &Error{Code: CodeCanceled}
}
