package fsx

import "context"

// WalkAction tells WalkFiles whether to continue or return successfully after
// the current regular file.
type WalkAction uint8

const (
	WalkContinue WalkAction = iota
	WalkStop
)

// WalkFile is the generic metadata for one canonical regular-file entry. Open
// performs a fresh confined fd-anchored open; callers own and must close a
// returned File.
type WalkFile struct {
	Resolved Resolved
	Position Position
	Open     func(context.Context) (*File, error)
}

// WalkVisitor consumes regular files in strict canonical full-path order. It
// may filter by generic metadata before opening content.
type WalkVisitor func(context.Context, WalkFile) (WalkAction, error)

// WalkStats reports generic filesystem work only. Content-file and byte
// accounting remains with the domain that elects to open an entry.
type WalkStats struct {
	EntriesScanned          uint64
	DirectoriesScanned      uint64
	FilesVisited            uint64
	PeakCandidatesRetained  int
	PeakDirectoriesDeferred int
}
