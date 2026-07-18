package fsx

import (
	"context"
	"errors"
	"io"
	"os"
	"sort"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/text/unicode/norm"
)

const (
	walkReadBatchSize       = 64
	walkDirectoryEntryLimit = 100_000
)

type walkCandidate struct {
	entry    Entry
	baseline unix.Stat_t
}

// WalkFiles walks a confined file or directory without following hidden paths
// or symlinks. It performs one bounded canonical sort per directory and never
// constructs a whole-vault catalog.
func (v *Vault) WalkFiles(ctx context.Context, base, input string, visit WalkVisitor) (WalkStats, error) {
	var stats WalkStats
	if end := v.activity.Begin(); end != nil {
		defer end()
	}
	if err := ctx.Err(); err != nil {
		return stats, contextError(err)
	}
	if visit == nil {
		return stats, &Error{Code: CodePathDenied}
	}

	resolved, err := v.Resolve(ctx, base, input)
	if err != nil {
		return stats, err
	}
	if !resolved.Exists {
		return stats, &Error{Code: CodeNotFound}
	}
	switch resolved.Kind {
	case KindSymlink:
		return stats, &Error{Code: CodeSymlinkDenied}
	case KindFile:
		position := Position{NFC: nfcRel(relSegments(resolved.Rel)), Stored: resolved.Rel}
		entry := v.walkResolvedFile(resolved, position)
		action, err := visit(ctx, entry)
		stats.FilesVisited = 1
		if err != nil {
			return stats, err
		}
		if action != WalkContinue && action != WalkStop {
			return stats, &Error{Code: CodePathDenied}
		}
		return stats, nil
	case KindDir:
		dir, err := v.OpenDir(ctx, "", resolved.Rel)
		if err != nil {
			return stats, err
		}
		defer dir.Close()
		_, err = v.walkOpenedDirectory(ctx, dir, visit, &stats)
		return stats, err
	default:
		return stats, &Error{Code: CodeNotFile}
	}
}

func (v *Vault) walkOpenedDirectory(ctx context.Context, dir *Directory, visit WalkVisitor, stats *WalkStats) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, contextError(err)
	}
	stats.DirectoriesScanned++

	candidates, identity, err := scanWalkDirectory(ctx, dir, stats)
	if err != nil {
		return false, err
	}
	for i := range candidates {
		if err := ctx.Err(); err != nil {
			return false, contextError(err)
		}
		candidate := &candidates[i]
		switch candidate.entry.Kind {
		case KindDir:
			if hook := v.testHooks; hook != nil && hook.beforeWalkDescend != nil {
				hook.beforeWalkDescend(candidate.entry.Rel)
			}
			child, err := v.openWalkDirectory(ctx, dir, candidate)
			if err != nil {
				return false, err
			}
			stopped, walkErr := v.walkOpenedDirectory(ctx, child, visit, stats)
			closeErr := child.Close()
			if walkErr != nil {
				return false, walkErr
			}
			if closeErr != nil {
				return false, &Error{Code: CodePathDenied}
			}
			if stopped {
				return true, nil
			}
		case KindFile:
			stats.FilesVisited++
			action, err := visit(ctx, v.walkCandidateFile(dir, candidate))
			if hook := v.testHooks; hook != nil && hook.afterWalkFile != nil {
				hook.afterWalkFile(stats.FilesVisited)
			}
			if err != nil {
				return false, err
			}
			switch action {
			case WalkContinue:
			case WalkStop:
				return true, nil
			default:
				return false, &Error{Code: CodePathDenied}
			}
		}
	}

	if err := verifyWalkDirectory(ctx, dir, identity, candidates, stats); err != nil {
		return false, err
	}
	return false, nil
}

// scanWalkDirectory performs the one canonical materialization scan for an
// opened directory. The explicit raw-entry ceiling bounds hidden and skipped
// entries as well as candidates retained for sorting.
func scanWalkDirectory(ctx context.Context, dir *Directory, stats *WalkStats) ([]walkCandidate, directoryIdentity, error) {
	if err := ctx.Err(); err != nil {
		return nil, directoryIdentity{}, contextError(err)
	}
	fd, err := dir.fd()
	if err != nil {
		return nil, directoryIdentity{}, err
	}
	before, err := statDirectory(fd)
	if err != nil {
		return nil, directoryIdentity{}, mapPathError(err)
	}

	var candidates []walkCandidate
	var entriesScanned int
	for {
		if err := ctx.Err(); err != nil {
			return nil, directoryIdentity{}, contextError(err)
		}
		entries, readErr := dir.file.ReadDir(walkReadBatchSize)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, directoryIdentity{}, mapReadDirError(readErr)
		}
		for _, dirEntry := range entries {
			entriesScanned++
			stats.EntriesScanned++
			if entriesScanned > walkDirectoryEntryLimit {
				return nil, directoryIdentity{}, &Error{Code: CodeInputTooLarge}
			}
			if err := ctx.Err(); err != nil {
				return nil, directoryIdentity{}, contextError(err)
			}
			name := dirEntry.Name()
			if deniedSegment(name) {
				continue
			}
			rel := joinRel(dir.resolved.Rel, name)
			position := Position{NFC: norm.NFC.String(rel), Stored: rel}
			var baseline unix.Stat_t
			if err := unix.Fstatat(fd, name, &baseline, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				return nil, directoryIdentity{}, &Error{Code: CodeSourceChanged}
			}
			candidates = append(candidates, walkCandidate{
				entry: Entry{
					Name:     name,
					Rel:      rel,
					Kind:     kindFromUnixMode(baseline.Mode),
					Size:     baseline.Size,
					Modified: unixStatModified(baseline),
					Position: position,
				},
				baseline: baseline,
			})
		}
		if errors.Is(readErr, io.EOF) || len(entries) == 0 {
			break
		}
	}

	after, err := statDirectory(fd)
	if err != nil || before != after {
		return nil, directoryIdentity{}, &Error{Code: CodeSourceChanged}
	}
	sort.Slice(candidates, func(i, j int) bool {
		return comparePosition(walkOrderPosition(candidates[i].entry), walkOrderPosition(candidates[j].entry)) < 0
	})
	if len(candidates) > stats.PeakCandidatesRetained {
		stats.PeakCandidatesRetained = len(candidates)
	}
	return candidates, before, nil
}

func verifyWalkDirectory(ctx context.Context, dir *Directory, expectedIdentity directoryIdentity, candidates []walkCandidate, stats *WalkStats) error {
	fd, err := dir.fd()
	if err != nil {
		return err
	}
	verifyFD, err := unix.Openat(fd, ".", directoryOpenFlags, 0)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return contextError(ctxErr)
		}
		return &Error{Code: CodeSourceChanged}
	}
	file := os.NewFile(uintptr(verifyFD), "<vault-walk-verification>")
	if file == nil {
		_ = unix.Close(verifyFD)
		return &Error{Code: CodeSourceChanged}
	}
	verificationDir := &Directory{file: file, resolved: dir.resolved}
	defer verificationDir.Close()
	identity, err := statDirectory(verifyFD)
	if err != nil || identity != expectedIdentity {
		return &Error{Code: CodeSourceChanged}
	}
	expected := make(map[string]struct{}, len(candidates))
	for i := range candidates {
		expected[candidates[i].entry.Name] = struct{}{}
	}
	entriesScanned := 0
	for {
		if err := ctx.Err(); err != nil {
			return contextError(err)
		}
		entries, readErr := verificationDir.file.ReadDir(walkReadBatchSize)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return &Error{Code: CodeSourceChanged}
		}
		for _, entry := range entries {
			entriesScanned++
			stats.EntriesScanned++
			if entriesScanned > walkDirectoryEntryLimit {
				return &Error{Code: CodeInputTooLarge}
			}
			name := entry.Name()
			if deniedSegment(name) {
				continue
			}
			if _, ok := expected[name]; !ok {
				return &Error{Code: CodeSourceChanged}
			}
			delete(expected, name)
		}
		if errors.Is(readErr, io.EOF) || len(entries) == 0 {
			break
		}
	}
	after, err := statDirectory(verifyFD)
	if err != nil || after != expectedIdentity || len(expected) != 0 {
		return &Error{Code: CodeSourceChanged}
	}
	return nil
}

func (v *Vault) openWalkDirectory(ctx context.Context, parent *Directory, candidate *walkCandidate) (*Directory, error) {
	if err := ctx.Err(); err != nil {
		return nil, contextError(err)
	}
	parentFD, err := parent.fd()
	if err != nil {
		return nil, err
	}
	childFD, err := unix.Openat(parentFD, candidate.entry.Name, directoryOpenFlags, 0)
	if err != nil {
		return nil, classifyWalkDirectoryOpenError(parentFD, candidate, err)
	}
	childFile := os.NewFile(uintptr(childFD), "<vault-walk-directory>")
	if childFile == nil {
		_ = unix.Close(childFD)
		return nil, &Error{Code: CodeNotDirectory}
	}
	var opened unix.Stat_t
	if err := unix.Fstat(childFD, &opened); err != nil {
		_ = childFile.Close()
		return nil, &Error{Code: CodeSourceChanged}
	}
	if kindFromUnixMode(opened.Mode) != KindDir || !sameEntryIdentity(&candidate.baseline, &opened) {
		_ = childFile.Close()
		return nil, &Error{Code: CodeSourceChanged}
	}
	return &Directory{
		file:      childFile,
		resolved:  resolvedFromStat(candidate.entry.Rel, &opened),
		testHooks: v.testHooks,
		activity:  v.activity,
	}, nil
}

func classifyWalkDirectoryOpenError(parentFD int, candidate *walkCandidate, openErr error) error {
	var current unix.Stat_t
	if err := unix.Fstatat(parentFD, candidate.entry.Name, &current, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return &Error{Code: CodeSourceChanged}
		}
		return mapOpenDirError(openErr)
	}
	if !sameEntryIdentity(&candidate.baseline, &current) {
		return &Error{Code: CodeSourceChanged}
	}
	return mapOpenDirError(openErr)
}

// A directory's file descendants begin at canonicalPath+"/". Sorting that
// stream key against sibling file keys and descending immediately yields
// strict canonical full-path byte order.
func walkOrderPosition(entry Entry) Position {
	position := entry.Position
	if entry.Kind == KindDir {
		position.NFC += "/"
		position.Stored += "/"
	}
	return position
}

func (v *Vault) walkCandidateFile(parent *Directory, candidate *walkCandidate) WalkFile {
	return WalkFile{
		Resolved: candidate.entryResolved(),
		Position: candidate.entry.Position,
		Open: func(ctx context.Context) (*File, error) {
			return v.openWalkFile(ctx, parent, candidate)
		},
	}
}

func (v *Vault) openWalkFile(ctx context.Context, parent *Directory, candidate *walkCandidate) (*File, error) {
	if err := ctx.Err(); err != nil {
		return nil, contextError(err)
	}
	parentFD, err := parent.fd()
	if err != nil {
		// A visitor may retain the generic opener beyond the callback. Preserve
		// that behavior with a fresh confined path walk, then bind the result to
		// the entry identity captured by this directory's canonical scan.
		opened, openErr := v.OpenFile(ctx, "", candidate.entry.Rel)
		if openErr != nil {
			return nil, openErr
		}
		if opened.identity != identityFromStat(&candidate.baseline) {
			_ = opened.Close()
			return nil, &Error{Code: CodeSourceChanged}
		}
		return opened, nil
	}
	if hook := v.testHooks; hook != nil && hook.beforeOpenFile != nil {
		hook.beforeOpenFile()
	}
	fileFD, err := unix.Openat(parentFD, candidate.entry.Name, regularFileOpenFlags, 0)
	if err != nil {
		return nil, classifyWalkFileOpenError(parentFD, candidate, err)
	}
	file := os.NewFile(uintptr(fileFD), "<vault-walk-file>")
	if file == nil {
		_ = unix.Close(fileFD)
		return nil, &Error{Code: CodeNotFile}
	}
	var opened unix.Stat_t
	if err := unix.Fstat(fileFD, &opened); err != nil {
		_ = file.Close()
		return nil, &Error{Code: CodeSourceChanged}
	}
	if kindFromUnixMode(opened.Mode) != KindFile || !sameFileIdentity(&candidate.baseline, &opened) {
		_ = file.Close()
		return nil, &Error{Code: CodeSourceChanged}
	}
	identity := identityFromStat(&opened)
	return &File{
		file:        file,
		resolved:    resolvedFileFromStat(candidate.entry.Rel, &opened),
		identity:    identity,
		fingerprint: fingerprintFile(candidate.entry.Rel, identity),
		testHooks:   v.testHooks,
	}, nil
}

func classifyWalkFileOpenError(parentFD int, candidate *walkCandidate, openErr error) error {
	var current unix.Stat_t
	if err := unix.Fstatat(parentFD, candidate.entry.Name, &current, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return &Error{Code: CodeSourceChanged}
		}
		return mapOpenFileError(openErr)
	}
	if !sameFileIdentity(&candidate.baseline, &current) {
		return &Error{Code: CodeSourceChanged}
	}
	return mapOpenFileError(openErr)
}

func (candidate *walkCandidate) entryResolved() Resolved {
	return Resolved{
		Rel:      candidate.entry.Rel,
		Exists:   true,
		Kind:     KindFile,
		Size:     candidate.entry.Size,
		Modified: candidate.entry.Modified,
	}
}

func (v *Vault) walkResolvedFile(resolved Resolved, position Position) WalkFile {
	rel := resolved.Rel
	return WalkFile{
		Resolved: resolved,
		Position: position,
		Open: func(ctx context.Context) (*File, error) {
			return v.OpenFile(ctx, "", rel)
		},
	}
}

func unixStatModified(stat unix.Stat_t) time.Time {
	return time.Unix(stat.Mtim.Sec, stat.Mtim.Nsec).UTC()
}
