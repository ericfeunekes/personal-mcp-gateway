package fsx

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const regularFileOpenFlags = unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK

var fileSourceDomain = []byte("personal-mcp-gateway/fsx-file-source/v1\x00")

// OpenFile opens one vault-confined regular file through a descriptor-anchored
// stored-spelling path walk. It never validates by path and then reopens by an
// absolute host pathname.
func (v *Vault) OpenFile(ctx context.Context, base, input string) (*File, error) {
	endActivity := v.activity.Begin()
	opened, err := v.openFile(ctx, base, input)
	if err != nil {
		if endActivity != nil {
			endActivity()
		}
		return nil, err
	}
	opened.endActivity = endActivity
	return opened, nil
}

func (v *Vault) openFile(ctx context.Context, base, input string) (*File, error) {
	if err := ctx.Err(); err != nil {
		return nil, contextError(err)
	}
	requested, err := normalizeRel(base, input)
	if err != nil {
		return nil, err
	}
	segments := relSegments(requested)
	if len(segments) == 0 {
		return nil, &Error{Code: CodeNotFile}
	}

	current, err := v.openRoot(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = current.Close() }()

	canonical := make([]string, 0, len(segments))
	for depth, callerSegment := range segments {
		if err := ctx.Err(); err != nil {
			return nil, contextError(err)
		}
		stored, found, err := matchStoredSegment(ctx, current.file, callerSegment)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, &Error{Code: CodeNotFound}
		}
		canonical = append(canonical, stored)

		parentFD, err := current.fd()
		if err != nil {
			return nil, err
		}
		var baseline unix.Stat_t
		if err := unix.Fstatat(parentFD, stored, &baseline, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return nil, mapPathError(err)
		}
		kind := kindFromUnixMode(baseline.Mode)
		last := depth == len(segments)-1
		if !last {
			if kind == KindSymlink {
				return nil, &Error{Code: CodeSymlinkDenied}
			}
			if kind != KindDir {
				return nil, &Error{Code: CodeNotDirectory}
			}
			if hook := v.testHooks; hook != nil && hook.beforeOpenSegment != nil {
				hook.beforeOpenSegment(depth)
			}
			childFD, err := unix.Openat(parentFD, stored, directoryOpenFlags, 0)
			if err != nil {
				var currentStat unix.Stat_t
				if statErr := unix.Fstatat(parentFD, stored, &currentStat, unix.AT_SYMLINK_NOFOLLOW); statErr == nil {
					if kindFromUnixMode(currentStat.Mode) == KindSymlink {
						return nil, &Error{Code: CodeSymlinkDenied}
					}
					if !sameEntryIdentity(&baseline, &currentStat) {
						return nil, &Error{Code: CodeSourceChanged}
					}
				} else if errors.Is(statErr, unix.ENOENT) {
					return nil, &Error{Code: CodeSourceChanged}
				}
				return nil, mapOpenDirError(err)
			}
			child := os.NewFile(uintptr(childFD), "<vault-directory>")
			if child == nil {
				_ = unix.Close(childFD)
				return nil, &Error{Code: CodeNotDirectory}
			}
			var openedStat unix.Stat_t
			if err := unix.Fstat(childFD, &openedStat); err != nil {
				_ = child.Close()
				return nil, mapPathError(err)
			}
			if !sameEntryIdentity(&baseline, &openedStat) {
				_ = child.Close()
				return nil, &Error{Code: CodeSourceChanged}
			}
			previous := current
			current = &Directory{
				file:      child,
				resolved:  resolvedFromStat(strings.Join(canonical, "/"), &openedStat),
				testHooks: v.testHooks,
				activity:  v.activity,
			}
			_ = previous.Close()
			continue
		}

		if kind == KindSymlink {
			return nil, &Error{Code: CodeSymlinkDenied}
		}
		if kind != KindFile {
			return nil, &Error{Code: CodeNotFile}
		}
		if hook := v.testHooks; hook != nil && hook.beforeOpenFile != nil {
			hook.beforeOpenFile()
		}
		fileFD, err := unix.Openat(parentFD, stored, regularFileOpenFlags, 0)
		if err != nil {
			var currentStat unix.Stat_t
			if statErr := unix.Fstatat(parentFD, stored, &currentStat, unix.AT_SYMLINK_NOFOLLOW); statErr == nil {
				if kindFromUnixMode(currentStat.Mode) == KindSymlink {
					return nil, &Error{Code: CodeSymlinkDenied}
				}
				if !sameFileIdentity(&baseline, &currentStat) {
					return nil, &Error{Code: CodeSourceChanged}
				}
			} else if errors.Is(statErr, unix.ENOENT) {
				return nil, &Error{Code: CodeSourceChanged}
			}
			return nil, mapOpenFileError(err)
		}
		file := os.NewFile(uintptr(fileFD), "<vault-file>")
		if file == nil {
			_ = unix.Close(fileFD)
			return nil, &Error{Code: CodeNotFile}
		}
		var openedStat unix.Stat_t
		if err := unix.Fstat(fileFD, &openedStat); err != nil {
			_ = file.Close()
			return nil, &Error{Code: CodeNotFile}
		}
		if kindFromUnixMode(openedStat.Mode) != KindFile {
			_ = file.Close()
			return nil, &Error{Code: CodeSourceChanged}
		}
		if !sameFileIdentity(&baseline, &openedStat) {
			_ = file.Close()
			return nil, &Error{Code: CodeSourceChanged}
		}
		identity := identityFromStat(&openedStat)
		rel := strings.Join(canonical, "/")
		return &File{
			file:        file,
			resolved:    resolvedFileFromStat(rel, &openedStat),
			identity:    identity,
			fingerprint: fingerprintFile(rel, identity),
			testHooks:   v.testHooks,
		}, nil
	}
	return nil, &Error{Code: CodeNotFound}
}

func identityFromStat(stat *unix.Stat_t) fileIdentity {
	return fileIdentity{
		dev:       uint64(uint32(stat.Dev)),
		ino:       stat.Ino,
		mode:      uint32(stat.Mode) & unix.S_IFMT,
		size:      stat.Size,
		mtimeSec:  stat.Mtim.Sec,
		mtimeNsec: stat.Mtim.Nsec,
		ctimeSec:  stat.Ctim.Sec,
		ctimeNsec: stat.Ctim.Nsec,
	}
}

func sameFileIdentity(before, after *unix.Stat_t) bool {
	return identityFromStat(before) == identityFromStat(after)
}

func resolvedFileFromStat(rel string, stat *unix.Stat_t) Resolved {
	return Resolved{
		Rel:      rel,
		Exists:   true,
		Kind:     KindFile,
		Size:     stat.Size,
		Modified: time.Unix(stat.Mtim.Sec, stat.Mtim.Nsec).UTC(),
	}
}

func fingerprintFile(rel string, identity fileIdentity) SourceFingerprint {
	framed := make([]byte, 0, len(fileSourceDomain)+8+len(rel)+8*8)
	framed = append(framed, fileSourceDomain...)
	framed = appendUint64(framed, uint64(len(rel)))
	framed = append(framed, rel...)
	framed = appendUint64(framed, identity.dev)
	framed = appendUint64(framed, identity.ino)
	framed = appendUint64(framed, uint64(identity.mode))
	framed = appendUint64(framed, uint64(identity.size))
	framed = appendUint64(framed, uint64(identity.mtimeSec))
	framed = appendUint64(framed, uint64(identity.mtimeNsec))
	framed = appendUint64(framed, uint64(identity.ctimeSec))
	framed = appendUint64(framed, uint64(identity.ctimeNsec))
	return sha256.Sum256(framed)
}

func mapOpenFileError(err error) error {
	switch {
	case errors.Is(err, unix.ELOOP):
		return &Error{Code: CodeSymlinkDenied}
	case errors.Is(err, unix.ENOENT):
		return &Error{Code: CodeNotFound}
	case errors.Is(err, unix.EISDIR), errors.Is(err, unix.ENXIO), errors.Is(err, unix.ENODEV):
		return &Error{Code: CodeNotFile}
	default:
		return &Error{Code: CodePathDenied}
	}
}
