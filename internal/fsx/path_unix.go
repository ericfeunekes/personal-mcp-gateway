package fsx

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/text/unicode/norm"
)

const directoryOpenFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC

// Directory owns one per-operation directory descriptor. Callers must close it.
type Directory struct {
	file      *os.File
	resolved  Resolved
	testHooks *vaultTestHooks
	listed    bool
}

type segmentMatcher struct {
	caller             string
	callerNFC          string
	nfcCandidate       string
	nfcCandidateCount  int
	foldCandidate      string
	foldCandidateCount int
}

func (d *Directory) Resolved() Resolved {
	if d == nil {
		return Resolved{}
	}
	return d.resolved
}

func (d *Directory) Close() error {
	if d == nil || d.file == nil {
		return nil
	}
	file := d.file
	d.file = nil
	err := file.Close()
	if errors.Is(err, os.ErrClosed) {
		return nil
	}
	return err
}

func (d *Directory) fd() (int, error) {
	if d == nil || d.file == nil {
		return -1, &Error{Code: CodeNotDirectory}
	}
	return int(d.file.Fd()), nil
}

func (v *Vault) Resolve(ctx context.Context, base, input string) (Resolved, error) {
	resolved, dir, err := v.resolve(ctx, base, input, false)
	if dir != nil {
		_ = dir.Close()
	}
	return resolved, err
}

func (v *Vault) OpenDir(ctx context.Context, base, input string) (*Directory, error) {
	resolved, dir, err := v.resolve(ctx, base, input, true)
	if err != nil {
		if dir != nil {
			_ = dir.Close()
		}
		return nil, err
	}
	if !resolved.Exists {
		if dir != nil {
			_ = dir.Close()
		}
		return nil, &Error{Code: CodeNotFound}
	}
	return dir, nil
}

func (v *Vault) resolve(ctx context.Context, base, input string, openFinalDir bool) (Resolved, *Directory, error) {
	if err := ctx.Err(); err != nil {
		return Resolved{}, nil, contextError(err)
	}
	requested, err := normalizeRel(base, input)
	if err != nil {
		return Resolved{}, nil, err
	}

	current, err := v.openRoot(ctx)
	if err != nil {
		return Resolved{}, nil, err
	}
	segments := relSegments(requested)
	if len(segments) == 0 {
		if openFinalDir {
			return current.resolved, current, nil
		}
		resolved := current.resolved
		_ = current.Close()
		return resolved, nil, nil
	}

	canonical := make([]string, 0, len(segments))
	for depth, callerSegment := range segments {
		if err := ctx.Err(); err != nil {
			_ = current.Close()
			return Resolved{}, nil, contextError(err)
		}

		stored, found, err := matchStoredSegment(ctx, current.file, callerSegment)
		if err != nil {
			_ = current.Close()
			return Resolved{}, nil, err
		}
		if !found {
			missing := append(append([]string(nil), canonical...), segments[depth:]...)
			_ = current.Close()
			return Resolved{Rel: nfcRel(missing), Exists: false}, nil, nil
		}

		var stat unix.Stat_t
		fd, fdErr := current.fd()
		if fdErr != nil {
			_ = current.Close()
			return Resolved{}, nil, fdErr
		}
		if err := unix.Fstatat(fd, stored, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			if errors.Is(err, unix.ENOENT) {
				missing := append(append([]string(nil), canonical...), segments[depth:]...)
				_ = current.Close()
				return Resolved{Rel: nfcRel(missing), Exists: false}, nil, nil
			}
			_ = current.Close()
			return Resolved{}, nil, mapPathError(err)
		}

		canonical = append(canonical, stored)
		resolved := resolvedFromStat(strings.Join(canonical, "/"), &stat)
		last := depth == len(segments)-1
		if last && !openFinalDir {
			_ = current.Close()
			return resolved, nil, nil
		}
		if kindFromUnixMode(stat.Mode) == KindSymlink {
			_ = current.Close()
			return Resolved{}, nil, &Error{Code: CodeSymlinkDenied}
		}
		if kindFromUnixMode(stat.Mode) != KindDir {
			_ = current.Close()
			return Resolved{}, nil, &Error{Code: CodeNotDirectory}
		}

		if hook := v.testHooks; hook != nil && hook.beforeOpenSegment != nil {
			hook.beforeOpenSegment(depth)
		}
		childFD, err := unix.Openat(fd, stored, directoryOpenFlags, 0)
		if err != nil {
			var currentStat unix.Stat_t
			if statErr := unix.Fstatat(fd, stored, &currentStat, unix.AT_SYMLINK_NOFOLLOW); statErr == nil && kindFromUnixMode(currentStat.Mode) == KindSymlink {
				_ = current.Close()
				return Resolved{}, nil, &Error{Code: CodeSymlinkDenied}
			}
			_ = current.Close()
			return Resolved{}, nil, mapOpenDirError(err)
		}
		childFile := os.NewFile(uintptr(childFD), "<vault-directory>")
		if childFile == nil {
			_ = unix.Close(childFD)
			_ = current.Close()
			return Resolved{}, nil, &Error{Code: CodeNotDirectory}
		}
		var openedStat unix.Stat_t
		if err := unix.Fstat(childFD, &openedStat); err != nil {
			_ = childFile.Close()
			_ = current.Close()
			return Resolved{}, nil, mapPathError(err)
		}
		openedResolved := resolvedFromStat(strings.Join(canonical, "/"), &openedStat)
		_ = current.Close()
		current = &Directory{file: childFile, resolved: openedResolved, testHooks: v.testHooks}
		if last {
			return openedResolved, current, nil
		}
	}

	_ = current.Close()
	return Resolved{}, nil, &Error{Code: CodeNotFound}
}

func (v *Vault) openRoot(ctx context.Context) (*Directory, error) {
	if err := ctx.Err(); err != nil {
		return nil, contextError(err)
	}
	fd, err := unix.Open(v.root, directoryOpenFlags, 0)
	if err != nil {
		return nil, mapOpenDirError(err)
	}
	file := os.NewFile(uintptr(fd), "<vault-root>")
	if file == nil {
		_ = unix.Close(fd)
		return nil, &Error{Code: CodeNotDirectory}
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = file.Close()
		return nil, mapPathError(err)
	}
	return &Directory{
		file:      file,
		resolved:  resolvedFromStat(".", &stat),
		testHooks: v.testHooks,
	}, nil
}

func matchStoredSegment(ctx context.Context, dir *os.File, caller string) (string, bool, error) {
	matcher := segmentMatcher{caller: caller, callerNFC: norm.NFC.String(caller)}

	for {
		if err := ctx.Err(); err != nil {
			return "", false, contextError(err)
		}
		entries, err := dir.ReadDir(64)
		if err != nil && !errors.Is(err, io.EOF) {
			return "", false, mapReadDirError(err)
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return "", false, contextError(err)
			}
			stored := entry.Name()
			if deniedSegment(stored) {
				continue
			}
			if exact, ok := matcher.observe(stored); ok {
				return exact, true, nil
			}
		}
		if errors.Is(err, io.EOF) || len(entries) == 0 {
			break
		}
	}
	return matcher.result()
}

func (m *segmentMatcher) observe(stored string) (string, bool) {
	if stored == m.caller {
		return stored, true
	}
	storedNFC := norm.NFC.String(stored)
	if storedNFC == m.callerNFC {
		m.nfcCandidate = stored
		m.nfcCandidateCount++
	}
	if strings.EqualFold(storedNFC, m.callerNFC) {
		m.foldCandidate = stored
		m.foldCandidateCount++
	}
	return "", false
}

func (m *segmentMatcher) result() (string, bool, error) {
	if m.nfcCandidateCount == 1 {
		return m.nfcCandidate, true, nil
	}
	if m.nfcCandidateCount == 0 && m.foldCandidateCount == 1 {
		return m.foldCandidate, true, nil
	}
	return "", false, nil
}

func resolvedFromStat(rel string, stat *unix.Stat_t) Resolved {
	return Resolved{
		Rel:      rel,
		Exists:   true,
		Kind:     kindFromUnixMode(stat.Mode),
		Size:     stat.Size,
		Modified: time.Unix(stat.Mtim.Sec, stat.Mtim.Nsec).UTC(),
	}
}

func kindFromUnixMode(mode uint16) Kind {
	switch uint32(mode) & unix.S_IFMT {
	case unix.S_IFLNK:
		return KindSymlink
	case unix.S_IFDIR:
		return KindDir
	case unix.S_IFREG:
		return KindFile
	default:
		return KindOther
	}
}

func mapOpenDirError(err error) error {
	switch {
	case errors.Is(err, unix.ELOOP):
		return &Error{Code: CodeSymlinkDenied}
	case errors.Is(err, unix.ENOENT):
		return &Error{Code: CodeNotFound}
	case errors.Is(err, unix.ENOTDIR):
		return &Error{Code: CodeNotDirectory}
	default:
		return &Error{Code: CodeNotDirectory}
	}
}

func mapPathError(err error) error {
	switch {
	case errors.Is(err, unix.ELOOP):
		return &Error{Code: CodeSymlinkDenied}
	case errors.Is(err, unix.ENOENT):
		return &Error{Code: CodeNotFound}
	case errors.Is(err, unix.ENOTDIR):
		return &Error{Code: CodeNotDirectory}
	default:
		return &Error{Code: CodePathDenied}
	}
}

func mapReadDirError(err error) error {
	if errors.Is(err, os.ErrClosed) {
		return &Error{Code: CodeNotDirectory}
	}
	return &Error{Code: CodeNotDirectory}
}
