package fsx

// Position identifies an entry in canonical listing order. NFC is the
// normalized sort key and Stored is the exact stored spelling tie-breaker.
// Callers must treat positions as untrusted.
type Position struct {
	NFC    string
	Stored string
}

type SourceFingerprint [32]byte

// ListOptions bounds one shallow, stateless directory page.
type ListOptions struct {
	Limit int
	After *Position
}

// ListPage is the complete evidence returned by one full shallow scan while
// retaining at most Limit+1 candidates.
type ListPage struct {
	Dir                Resolved
	Entries            []Entry
	HasMore            bool
	Source             SourceFingerprint
	BoundaryFound      bool
	FilesScanned       uint64
	BytesScanned       uint64
	CandidatesRetained int
}
