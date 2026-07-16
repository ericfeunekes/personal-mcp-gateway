package fsx

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

// File is one confined, read-only regular-file descriptor. Its absolute host
// path and raw filesystem identity are intentionally not exposed. Callers must
// close it.
type File struct {
	mu          sync.Mutex
	file        *os.File
	resolved    Resolved
	identity    fileIdentity
	fingerprint SourceFingerprint
	testHooks   *vaultTestHooks
	endActivity func()
}

type fileIdentity struct {
	dev       uint64
	ino       uint64
	mode      uint32
	size      int64
	mtimeSec  int64
	mtimeNsec int64
	ctimeSec  int64
	ctimeNsec int64
}

// Resolved returns the canonical stored-spelling/NFC file metadata observed
// from the opened descriptor.
func (f *File) Resolved() Resolved {
	if f == nil {
		return Resolved{}
	}
	return f.resolved
}

// Fingerprint returns the opaque source-version fingerprint for the opened
// descriptor. It does not expose raw host filesystem identity.
func (f *File) Fingerprint() SourceFingerprint {
	if f == nil {
		return SourceFingerprint{}
	}
	return f.fingerprint
}

// Read performs one cancellation-aware read and verifies that the opened
// source stamp is unchanged both before and after the read. Evidence from a
// changed source is discarded by returning n == 0 with CodeSourceChanged.
func (f *File) Read(ctx context.Context, p []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, contextError(err)
	}
	if f == nil {
		return 0, &Error{Code: CodeNotFile}
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.file == nil {
		return 0, &Error{Code: CodeNotFile}
	}
	if err := f.revalidateLocked(); err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, contextError(err)
	}
	n, readErr := f.file.Read(p)
	if hook := f.testHooks; hook != nil && hook.afterFileRead != nil {
		hook.afterFileRead()
	}
	if err := ctx.Err(); err != nil {
		return 0, contextError(err)
	}
	if err := f.revalidateLocked(); err != nil {
		return 0, err
	}
	if errors.Is(readErr, io.EOF) {
		return n, io.EOF
	}
	if errors.Is(readErr, os.ErrClosed) {
		return 0, &Error{Code: CodeNotFile}
	}
	if readErr != nil {
		return 0, &Error{Code: CodePathDenied}
	}
	return n, nil
}

// Seek moves to an absolute byte offset after validating the source stamp.
// Only absolute seeks are exposed so continuation state remains explicit.
func (f *File) Seek(ctx context.Context, offset int64) error {
	if err := ctx.Err(); err != nil {
		return contextError(err)
	}
	if offset < 0 {
		return &Error{Code: CodeLimitExceeded}
	}
	if f == nil {
		return &Error{Code: CodeNotFile}
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.file == nil {
		return &Error{Code: CodeNotFile}
	}
	if err := f.revalidateLocked(); err != nil {
		return err
	}
	if offset > f.identity.size {
		return &Error{Code: CodeLimitExceeded}
	}
	position, err := f.file.Seek(offset, 0)
	if err != nil || position != offset {
		return &Error{Code: CodeLimitExceeded}
	}
	if err := ctx.Err(); err != nil {
		return contextError(err)
	}
	return f.revalidateLocked()
}

// Revalidate compares the current descriptor stamp with the stamp captured at
// open time. It never rereads content.
func (f *File) Revalidate(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return contextError(err)
	}
	if f == nil {
		return &Error{Code: CodeNotFile}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.file == nil {
		return &Error{Code: CodeNotFile}
	}
	return f.revalidateLocked()
}

func (f *File) revalidateLocked() error {
	var stat unix.Stat_t
	if err := unix.Fstat(int(f.file.Fd()), &stat); err != nil {
		return &Error{Code: CodeNotFile}
	}
	if identityFromStat(&stat) != f.identity {
		return &Error{Code: CodeSourceChanged}
	}
	return nil
}

// Close releases the descriptor and completes the aggregate activity record.
// It is idempotent.
func (f *File) Close() error {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	file := f.file
	f.file = nil
	end := f.endActivity
	f.endActivity = nil
	f.mu.Unlock()

	if end != nil {
		end()
	}
	if file == nil {
		return nil
	}
	err := file.Close()
	if errors.Is(err, os.ErrClosed) {
		return nil
	}
	if err != nil {
		return &Error{Code: CodePathDenied}
	}
	return nil
}
