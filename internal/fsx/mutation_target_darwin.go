//go:build darwin

package fsx

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/text/unicode/norm"
)

var mutationDirectoryDomain = []byte("personal-mcp-gateway/fsx-mutation-empty-directory/v1\x00")

type mutationNameState uint8

const (
	mutationNameAbsent mutationNameState = iota
	mutationNameExact
	mutationNameUniqueNFC
	mutationNameUniqueFold
	mutationNameAmbiguous
)

type mutationNameMatch struct {
	state  mutationNameState
	stored string
}

type mutationTargetIdentity struct {
	dev       uint64
	ino       uint64
	mode      uint32
	uid       uint32
	nlink     uint64
	size      int64
	mtimeSec  int64
	mtimeNsec int64
	ctimeSec  int64
	ctimeNsec int64
}

type preparedMutationTarget struct {
	parent      *Directory
	leaf        string
	rel         string
	source      *os.File
	identity    mutationTargetIdentity
	resolved    Resolved
	fingerprint SourceFingerprint
}

func (p *preparedMutationTarget) close() {
	if p == nil {
		return
	}
	if p.source != nil {
		_ = p.source.Close()
		p.source = nil
	}
	if p.parent != nil {
		_ = p.parent.Close()
		p.parent = nil
	}
}

func (m *Mutator) StatMutationTarget(ctx context.Context, base, input string) (MutationTarget, error) {
	if m == nil || m.vault == nil {
		return MutationTarget{}, &Error{Code: CodePathDenied}
	}
	if end := m.vault.activity.Begin(); end != nil {
		defer end()
	}
	prepared, err := m.vault.prepareMutationTarget(ctx, base, input)
	if err != nil {
		return MutationTarget{}, err
	}
	defer prepared.close()
	return MutationTarget{Resolved: prepared.resolved, Fingerprint: prepared.fingerprint}, nil
}

func (v *Vault) prepareMutationTarget(ctx context.Context, base, input string) (*preparedMutationTarget, error) {
	root, err := v.openRoot(ctx)
	if err != nil {
		return nil, err
	}
	return v.prepareMutationTargetFromRoot(ctx, root, base, input)
}

// prepareMutationTargetFromRoot consumes root, preserving one descriptor
// generation for callers that prepare multiple paths in one mutation.
func (v *Vault) prepareMutationTargetFromRoot(ctx context.Context, root *Directory, base, input string) (*preparedMutationTarget, error) {
	parent, leaf, relPrefix, err := v.prepareMutationParentFromRoot(ctx, root, base, input)
	if err != nil {
		return nil, err
	}
	match, err := matchMutationName(ctx, int(parent.file.Fd()), leaf)
	if err != nil {
		_ = parent.Close()
		return nil, err
	}
	if match.state == mutationNameAbsent {
		_ = parent.Close()
		return nil, &Error{Code: CodeNotFound}
	}
	if match.state == mutationNameAmbiguous {
		_ = parent.Close()
		return nil, &Error{Code: CodeSourceChanged}
	}
	stored := match.stored
	parentFD := int(parent.file.Fd())
	var baseline unix.Stat_t
	if err := unix.Fstatat(parentFD, stored, &baseline, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		_ = parent.Close()
		return nil, mapPathError(err)
	}
	kind := kindFromUnixMode(baseline.Mode)
	if kind == KindSymlink {
		_ = parent.Close()
		return nil, &Error{Code: CodeSymlinkDenied}
	}
	if kind != KindFile && kind != KindDir {
		_ = parent.Close()
		return nil, &Error{Code: CodeNotFile}
	}
	flags := regularFileOpenFlags
	if kind == KindDir {
		flags = directoryOpenFlags
	}
	fd, err := unix.Openat(parentFD, stored, flags, 0)
	if err != nil {
		_ = parent.Close()
		return nil, &Error{Code: CodeSourceChanged}
	}
	source := os.NewFile(uintptr(fd), "<vault-mutation-source>")
	if source == nil {
		_ = unix.Close(fd)
		_ = parent.Close()
		return nil, &Error{Code: CodePathDenied}
	}
	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil || !sameMutationStat(&baseline, &opened) {
		_ = source.Close()
		_ = parent.Close()
		return nil, &Error{Code: CodeSourceChanged}
	}
	if kind == KindDir {
		empty, err := mutationDirectoryEmpty(ctx, fd)
		if err != nil {
			_ = source.Close()
			_ = parent.Close()
			return nil, err
		}
		if !empty {
			_ = source.Close()
			_ = parent.Close()
			return nil, &Error{Code: CodeNotEmpty}
		}
	}
	rel := joinRel(relPrefix, stored)
	identity := mutationIdentityFromStat(&opened)
	resolved := resolvedFromMutationStat(rel, kind, &opened)
	fingerprint := mutationFingerprint(rel, kind, identity)
	return &preparedMutationTarget{parent: parent, leaf: stored, rel: rel, source: source, identity: identity, resolved: resolved, fingerprint: fingerprint}, nil
}

// prepareMutationParentFromRoot consumes root and returns either the prepared
// parent descriptor or a closed root on failure.
func (v *Vault) prepareMutationParentFromRoot(ctx context.Context, root *Directory, base, input string) (*Directory, string, string, error) {
	if root == nil || root.file == nil {
		return nil, "", "", &Error{Code: CodePathDenied}
	}
	requested, err := normalizeRel(base, input)
	if err != nil {
		_ = root.Close()
		return nil, "", "", err
	}
	segments := relSegments(requested)
	if len(segments) == 0 {
		_ = root.Close()
		return nil, "", "", &Error{Code: CodePathDenied}
	}
	leaf := norm.NFC.String(segments[len(segments)-1])
	current := root
	canonical := make([]string, 0, len(segments)-1)
	for _, caller := range segments[:len(segments)-1] {
		if err := ctx.Err(); err != nil {
			_ = current.Close()
			return nil, "", "", contextError(err)
		}
		match, err := matchMutationName(ctx, int(current.file.Fd()), caller)
		if err != nil {
			_ = current.Close()
			return nil, "", "", err
		}
		if match.state == mutationNameAbsent {
			_ = current.Close()
			return nil, "", "", &Error{Code: CodeNotFound}
		}
		if match.state == mutationNameAmbiguous {
			_ = current.Close()
			return nil, "", "", &Error{Code: CodeSourceChanged}
		}
		parentFD := int(current.file.Fd())
		var baseline unix.Stat_t
		if err := unix.Fstatat(parentFD, match.stored, &baseline, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			_ = current.Close()
			return nil, "", "", mapPathError(err)
		}
		if kindFromUnixMode(baseline.Mode) == KindSymlink {
			_ = current.Close()
			return nil, "", "", &Error{Code: CodeSymlinkDenied}
		}
		if kindFromUnixMode(baseline.Mode) != KindDir {
			_ = current.Close()
			return nil, "", "", &Error{Code: CodeNotDirectory}
		}
		childFD, err := unix.Openat(parentFD, match.stored, directoryOpenFlags, 0)
		if err != nil {
			_ = current.Close()
			return nil, "", "", mapOpenDirError(err)
		}
		var opened unix.Stat_t
		if err := unix.Fstat(childFD, &opened); err != nil || !sameEntryIdentity(&baseline, &opened) {
			_ = unix.Close(childFD)
			_ = current.Close()
			return nil, "", "", &Error{Code: CodeSourceChanged}
		}
		child := os.NewFile(uintptr(childFD), "<vault-mutation-parent>")
		if child == nil {
			_ = unix.Close(childFD)
			_ = current.Close()
			return nil, "", "", &Error{Code: CodePathDenied}
		}
		_ = current.Close()
		canonical = append(canonical, match.stored)
		current = &Directory{file: child, resolved: resolvedFromStat(strings.Join(canonical, "/"), &opened), testHooks: v.testHooks, activity: v.activity}
	}
	parentRel := "."
	if len(canonical) != 0 {
		parentRel = strings.Join(canonical, "/")
	}
	return current, leaf, parentRel, nil
}

func cloneMutationRoot(root *Directory) (*Directory, error) {
	if root == nil || root.file == nil {
		return nil, &Error{Code: CodePathDenied}
	}
	var before unix.Stat_t
	if err := unix.Fstat(int(root.file.Fd()), &before); err != nil {
		return nil, &Error{Code: CodeSourceChanged}
	}
	fd, err := unix.Openat(int(root.file.Fd()), ".", directoryOpenFlags, 0)
	if err != nil {
		return nil, mapOpenDirError(err)
	}
	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil || !sameEntryIdentity(&before, &opened) {
		_ = unix.Close(fd)
		return nil, &Error{Code: CodeSourceChanged}
	}
	file := os.NewFile(uintptr(fd), "<vault-mutation-root>")
	if file == nil {
		_ = unix.Close(fd)
		return nil, &Error{Code: CodePathDenied}
	}
	return &Directory{file: file, resolved: root.resolved, testHooks: root.testHooks, activity: root.activity}, nil
}

func matchMutationName(ctx context.Context, dirFD int, caller string) (mutationNameMatch, error) {
	fd, err := unix.Openat(dirFD, ".", directoryOpenFlags, 0)
	if err != nil {
		return mutationNameMatch{}, mapOpenDirError(err)
	}
	dir := os.NewFile(uintptr(fd), "<vault-mutation-scan>")
	if dir == nil {
		_ = unix.Close(fd)
		return mutationNameMatch{}, &Error{Code: CodeNotDirectory}
	}
	defer dir.Close()
	var storedNames []string
	for {
		if err := ctx.Err(); err != nil {
			return mutationNameMatch{}, contextError(err)
		}
		entries, readErr := dir.ReadDir(64)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return mutationNameMatch{}, mapReadDirError(readErr)
		}
		for _, entry := range entries {
			stored := entry.Name()
			if deniedSegment(stored) {
				continue
			}
			storedNames = append(storedNames, stored)
		}
		if errors.Is(readErr, io.EOF) || len(entries) == 0 {
			break
		}
	}
	return classifyMutationNames(caller, storedNames), nil
}

func classifyMutationNames(caller string, storedNames []string) mutationNameMatch {
	callerNFC := norm.NFC.String(caller)
	var nfc, fold []string
	for _, stored := range storedNames {
		if stored == caller {
			return mutationNameMatch{state: mutationNameExact, stored: stored}
		}
		storedNFC := norm.NFC.String(stored)
		if storedNFC == callerNFC {
			nfc = append(nfc, stored)
		} else if strings.EqualFold(storedNFC, callerNFC) {
			fold = append(fold, stored)
		}
	}
	if len(nfc) == 1 {
		return mutationNameMatch{state: mutationNameUniqueNFC, stored: nfc[0]}
	}
	if len(nfc) > 1 {
		return mutationNameMatch{state: mutationNameAmbiguous}
	}
	if len(fold) == 1 {
		return mutationNameMatch{state: mutationNameUniqueFold, stored: fold[0]}
	}
	if len(fold) > 1 {
		return mutationNameMatch{state: mutationNameAmbiguous}
	}
	return mutationNameMatch{state: mutationNameAbsent}
}

func mutationDirectoryEmpty(ctx context.Context, fd int) (bool, error) {
	scanFD, err := unix.Openat(fd, ".", directoryOpenFlags, 0)
	if err != nil {
		return false, &Error{Code: CodeSourceChanged}
	}
	dir := os.NewFile(uintptr(scanFD), "<vault-empty-directory-check>")
	if dir == nil {
		_ = unix.Close(scanFD)
		return false, &Error{Code: CodeSourceChanged}
	}
	defer dir.Close()
	if err := ctx.Err(); err != nil {
		return false, contextError(err)
	}
	entries, err := dir.ReadDir(1)
	if err != nil && !errors.Is(err, io.EOF) {
		return false, &Error{Code: CodeSourceChanged}
	}
	return len(entries) == 0, nil
}

func mutationIdentityFromStat(stat *unix.Stat_t) mutationTargetIdentity {
	return mutationTargetIdentity{dev: uint64(uint32(stat.Dev)), ino: stat.Ino, mode: uint32(stat.Mode), uid: stat.Uid, nlink: uint64(stat.Nlink), size: stat.Size, mtimeSec: stat.Mtim.Sec, mtimeNsec: stat.Mtim.Nsec, ctimeSec: stat.Ctim.Sec, ctimeNsec: stat.Ctim.Nsec}
}

func sameMutationStat(before, after *unix.Stat_t) bool {
	return mutationIdentityFromStat(before) == mutationIdentityFromStat(after)
}

func resolvedFromMutationStat(rel string, kind Kind, stat *unix.Stat_t) Resolved {
	return Resolved{Rel: rel, Exists: true, Kind: kind, Size: stat.Size, Modified: time.Unix(stat.Mtim.Sec, stat.Mtim.Nsec).UTC()}
}

func mutationFingerprint(rel string, kind Kind, identity mutationTargetIdentity) SourceFingerprint {
	if kind == KindFile {
		return fingerprintFile(rel, fileIdentity{dev: identity.dev, ino: identity.ino, mode: identity.mode & unix.S_IFMT, size: identity.size, mtimeSec: identity.mtimeSec, mtimeNsec: identity.mtimeNsec, ctimeSec: identity.ctimeSec, ctimeNsec: identity.ctimeNsec})
	}
	framed := append([]byte(nil), mutationDirectoryDomain...)
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

func (identity mutationTargetIdentity) protocolStamp() mutationRawStamp {
	return mutationRawStamp{Device: identity.dev, Inode: identity.ino, Mode: identity.mode, UID: identity.uid, NLink: identity.nlink, Size: identity.size, MtimeSec: identity.mtimeSec, MtimeNsec: identity.mtimeNsec, CtimeSec: identity.ctimeSec, CtimeNsec: identity.ctimeNsec}
}
