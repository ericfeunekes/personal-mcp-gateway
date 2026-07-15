package fsx

import (
	"container/heap"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"hash"
	"io"
	"sort"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/text/unicode/norm"

	"personal-mcp-gateway/internal/limits"
)

var (
	membershipDomain = []byte("personal-mcp-gateway/fsx-membership/v1\x00")
	sourceDomain     = []byte("personal-mcp-gateway/fsx-source/v1\x00")
)

type directoryIdentity struct {
	dev       uint64
	ino       uint64
	size      int64
	mtimeSec  int64
	mtimeNsec int64
}

type membershipAccumulator struct {
	count      uint64
	framedSize uint64
	xor        [sha256.Size]byte
	sum        [sha256.Size]byte
	hasher     hash.Hash
	frame      []byte
	item       [sha256.Size]byte
}

type listCandidate struct {
	name     string
	rel      string
	position Position
}

type candidateHeap []listCandidate

func (h candidateHeap) Len() int { return len(h) }
func (h candidateHeap) Less(i, j int) bool {
	return comparePosition(h[i].position, h[j].position) > 0
}
func (h candidateHeap) Swap(i, j int)   { h[i], h[j] = h[j], h[i] }
func (h *candidateHeap) Push(value any) { *h = append(*h, value.(listCandidate)) }
func (h *candidateHeap) Pop() any {
	old := *h
	n := len(old)
	value := old[n-1]
	*h = old[:n-1]
	return value
}

type selectedEntry struct {
	candidate listCandidate
	baseline  unix.Stat_t
	final     unix.Stat_t
}

func (d *Directory) ListPage(ctx context.Context, opts ListOptions) (ListPage, error) {
	page := ListPage{Dir: d.Resolved(), BoundaryFound: opts.After == nil}
	if err := ctx.Err(); err != nil {
		return page, contextError(err)
	}
	if d == nil || d.file == nil || d.listed {
		return page, &Error{Code: CodeNotDirectory}
	}
	d.listed = true

	limit := opts.Limit
	if limit == 0 {
		limit = DefaultLimit
	}
	if limit < 0 || limit > MaxLimit {
		return page, &Error{Code: CodeLimitExceeded}
	}
	if opts.After != nil {
		if len([]byte(opts.After.NFC)) > limits.PathMaxBytes || len([]byte(opts.After.Stored)) > limits.PathMaxBytes {
			return page, &Error{Code: CodeInputTooLarge}
		}
	}

	fd, err := d.fd()
	if err != nil {
		return page, err
	}
	before, err := statDirectory(fd)
	if err != nil {
		return page, mapPathError(err)
	}
	if hook := d.testHooks; hook != nil && hook.beforeListScan != nil {
		hook.beforeListScan()
	}

	capacity := limit + 1
	candidates := make(candidateHeap, 0, capacity)
	heap.Init(&candidates)
	var membership membershipAccumulator
	for {
		if err := ctx.Err(); err != nil {
			return page, contextError(err)
		}
		entries, readErr := d.file.ReadDir(64)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return page, mapReadDirError(readErr)
		}
		for _, dirEntry := range entries {
			page.FilesScanned++
			if err := ctx.Err(); err != nil {
				return page, contextError(err)
			}
			name := dirEntry.Name()
			if deniedSegment(name) {
				continue
			}
			rel := joinRel(d.resolved.Rel, name)
			position := Position{NFC: norm.NFC.String(rel), Stored: rel}
			membership.add(position)
			if opts.After != nil && comparePosition(position, *opts.After) == 0 {
				page.BoundaryFound = true
			}
			if opts.After != nil && comparePosition(position, *opts.After) <= 0 {
				continue
			}
			candidate := listCandidate{name: name, rel: rel, position: position}
			if candidates.Len() < capacity {
				heap.Push(&candidates, candidate)
				if candidates.Len() > page.CandidatesRetained {
					page.CandidatesRetained = candidates.Len()
				}
				continue
			}
			if comparePosition(candidate.position, candidates[0].position) < 0 {
				heap.Pop(&candidates)
				heap.Push(&candidates, candidate)
			}
		}
		if len(entries) > 0 {
			if hook := d.testHooks; hook != nil && hook.afterListBatch != nil {
				hook.afterListBatch(page.FilesScanned)
			}
			if err := ctx.Err(); err != nil {
				return withoutEntries(page), contextError(err)
			}
		}
		if errors.Is(readErr, io.EOF) || len(entries) == 0 {
			break
		}
	}

	afterScan, err := statDirectory(fd)
	if err != nil || before != afterScan {
		return page, &Error{Code: CodeSourceChanged}
	}
	page.Source = sourceFingerprint(before, membership)
	if !page.BoundaryFound {
		return page, nil
	}

	selected := make([]selectedEntry, candidates.Len())
	for i := len(selected) - 1; i >= 0; i-- {
		selected[i].candidate = heap.Pop(&candidates).(listCandidate)
	}
	sort.Slice(selected, func(i, j int) bool {
		return comparePosition(selected[i].candidate.position, selected[j].candidate.position) < 0
	})

	for i := range selected {
		if err := ctx.Err(); err != nil {
			return withoutEntries(page), contextError(err)
		}
		if err := unix.Fstatat(fd, selected[i].candidate.name, &selected[i].baseline, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return withoutEntries(page), &Error{Code: CodeSourceChanged}
		}
	}
	if hook := d.testHooks; hook != nil && hook.afterEntryBaseline != nil {
		hook.afterEntryBaseline()
	}
	for i := range selected {
		if err := ctx.Err(); err != nil {
			return withoutEntries(page), contextError(err)
		}
		if err := unix.Fstatat(fd, selected[i].candidate.name, &selected[i].final, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return withoutEntries(page), &Error{Code: CodeSourceChanged}
		}
		if !sameEntryIdentity(&selected[i].baseline, &selected[i].final) {
			return withoutEntries(page), &Error{Code: CodeSourceChanged}
		}
	}

	afterMetadata, err := statDirectory(fd)
	if err != nil || before != afterMetadata {
		return withoutEntries(page), &Error{Code: CodeSourceChanged}
	}
	page.HasMore = len(selected) > limit
	if page.HasMore {
		selected = selected[:limit]
	}
	page.Entries = make([]Entry, 0, len(selected))
	for _, item := range selected {
		page.Entries = append(page.Entries, Entry{
			Name:     item.candidate.name,
			Rel:      item.candidate.rel,
			Kind:     kindFromUnixMode(item.final.Mode),
			Size:     item.final.Size,
			Modified: time.Unix(item.final.Mtim.Sec, item.final.Mtim.Nsec).UTC(),
			Position: item.candidate.position,
		})
	}
	return page, nil
}

func withoutEntries(page ListPage) ListPage {
	page.Entries = nil
	page.HasMore = false
	page.Source = SourceFingerprint{}
	return page
}

func comparePosition(left, right Position) int {
	if left.NFC < right.NFC {
		return -1
	}
	if left.NFC > right.NFC {
		return 1
	}
	if left.Stored < right.Stored {
		return -1
	}
	if left.Stored > right.Stored {
		return 1
	}
	return 0
}

func statDirectory(fd int) (directoryIdentity, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return directoryIdentity{}, err
	}
	if kindFromUnixMode(stat.Mode) != KindDir {
		return directoryIdentity{}, unix.ENOTDIR
	}
	return directoryIdentity{
		dev:       uint64(uint32(stat.Dev)),
		ino:       stat.Ino,
		size:      stat.Size,
		mtimeSec:  stat.Mtim.Sec,
		mtimeNsec: stat.Mtim.Nsec,
	}, nil
}

func sameEntryIdentity(before, after *unix.Stat_t) bool {
	return before.Dev == after.Dev && before.Ino == after.Ino &&
		(uint32(before.Mode)&unix.S_IFMT) == (uint32(after.Mode)&unix.S_IFMT)
}

func (a *membershipAccumulator) add(position Position) {
	if a.hasher == nil {
		a.hasher = sha256.New()
	} else {
		a.hasher.Reset()
	}
	a.frame = a.frame[:0]
	a.frame = appendUint64(a.frame, uint64(len(position.NFC)))
	a.frame = append(a.frame, position.NFC...)
	a.frame = appendUint64(a.frame, uint64(len(position.Stored)))
	a.frame = append(a.frame, position.Stored...)
	_, _ = a.hasher.Write(membershipDomain)
	_, _ = a.hasher.Write(a.frame)
	item := a.hasher.Sum(a.item[:0])
	a.count++
	a.framedSize += uint64(len(a.frame))
	carry := uint16(0)
	for i := sha256.Size - 1; i >= 0; i-- {
		a.xor[i] ^= item[i]
		total := uint16(a.sum[i]) + uint16(item[i]) + carry
		a.sum[i] = byte(total)
		carry = total >> 8
	}
}

func sourceFingerprint(identity directoryIdentity, membership membershipAccumulator) SourceFingerprint {
	fixed := make([]byte, 0, len(sourceDomain)+7*8+2*sha256.Size)
	fixed = append(fixed, sourceDomain...)
	fixed = appendUint64(fixed, identity.dev)
	fixed = appendUint64(fixed, identity.ino)
	fixed = appendUint64(fixed, uint64(identity.size))
	fixed = appendUint64(fixed, uint64(identity.mtimeSec))
	fixed = appendUint64(fixed, uint64(identity.mtimeNsec))
	fixed = appendUint64(fixed, membership.count)
	fixed = appendUint64(fixed, membership.framedSize)
	fixed = append(fixed, membership.xor[:]...)
	fixed = append(fixed, membership.sum[:]...)
	return sha256.Sum256(fixed)
}

func appendUint64(dst []byte, value uint64) []byte {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	return append(dst, encoded[:]...)
}
