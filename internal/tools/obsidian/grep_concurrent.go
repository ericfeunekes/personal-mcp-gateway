package obsidian

import (
	"bytes"
	"context"
	"errors"
	"io"
	"regexp"
	"sync"
	"unicode/utf8"

	"personal-mcp-gateway/internal/fsx"
)

// grepWorkerCeiling is intentionally an internal fixed ceiling. It is not a
// caller option: all public ordering, cursor, and coverage semantics remain
// independent of when any individual worker completes.
const grepWorkerCeiling = 16

type grepCheckpointCoords struct {
	resumeOffset  int64
	resumeLine    int
	contextOffset int64
	contextLine   int
}

type grepScannedCandidate struct {
	match            GrepMatch
	occurrencesReady bool
	remaining        int
	emission         grepCheckpointCoords
	retry            grepCheckpointCoords
}

type grepScanEventKind uint8

const (
	grepScanCandidateEvent grepScanEventKind = iota
	grepScanFullEvent
	grepScanPartialEvent
	grepScanUnfitEvent
	grepScanErrorEvent
)

// grepScanEvent is intentionally file-local. bytes is cumulative from the
// job's starting offset; only the ordered reducer turns it into public work.
type grepScanEvent struct {
	kind       grepScanEventKind
	bytes      int64
	candidate  *grepScannedCandidate
	checkpoint grepCheckpointCoords
	advanced   bool
	err        error
}

type grepScanJob struct {
	sequence int
	entry    fsx.WalkFile
	file     *fsx.File

	fingerprint fsx.SourceFingerprint
	partial     *grepPartialCursor
	allowance   int64

	events chan grepScanEvent

	acceptedBytes int64
	started       bool
	reserved      bool
}

type grepConcurrentState struct {
	run *grepRun

	ctx    context.Context
	cancel context.CancelFunc
	jobs   chan *grepScanJob
	wg     sync.WaitGroup

	pending       []*grepScanJob
	reservedFiles int
	reservedBytes int64
	nextSequence  int
	workers       int
	terminalErr   error
	hooks         *grepConcurrentHooks
	mu            sync.Mutex
	active        int
	inFlight      int
}

// grepConcurrentHooks is deliberately request-scoped and unexported. Tests
// may gate a sequence and observe aggregate activity; normal calls pass nil.
type grepConcurrentHooks struct {
	gate          func(context.Context, int) error
	terminalEvent func(int)
	observe       func(grepConcurrentSnapshot)
}

// GrepTestHooks is an internal-package test seam. It is never exposed through
// MCP and normal descriptor construction passes nil.
type GrepTestHooks struct {
	Gate          func(context.Context, int) error
	TerminalEvent func(sequence int)
}

func (h *GrepTestHooks) concurrentHooks() *grepConcurrentHooks {
	if h == nil {
		return nil
	}
	return &grepConcurrentHooks{gate: h.Gate, terminalEvent: h.TerminalEvent}
}

type grepConcurrentSnapshot struct {
	Active   int
	InFlight int
}

func (g *grepRun) walkConcurrent(ctx context.Context) error {
	return g.walkConcurrentWithHooks(ctx, nil)
}

func (g *grepRun) walkConcurrentWithHooks(ctx context.Context, hooks *grepConcurrentHooks) error {
	workerCtx, cancel := context.WithCancel(ctx)
	s := &grepConcurrentState{
		run:    g,
		ctx:    workerCtx,
		cancel: cancel,
		jobs:   make(chan *grepScanJob, grepWorkerCeiling),
		hooks:  hooks,
	}
	defer func() {
		cancel()
		close(s.jobs)
		s.wg.Wait()
		s.releasePending()
	}()

	_, walkErr := g.tools.vault.WalkFiles(ctx, "", g.canonical, s.visit)
	if walkErr != nil {
		s.terminalErr = walkErr
	}
	if err := s.drain(); err != nil {
		return err
	}
	if g.stop != nil {
		return nil
	}
	if s.terminalErr != nil {
		return s.terminalErr
	}
	return nil
}

func (s *grepConcurrentState) releasePending() {
	for _, job := range s.pending {
		if job.file != nil {
			_ = job.file.Close()
			job.file = nil
		}
		if job.reserved {
			s.run.tools.grepActivity.Release()
			s.observeInFlight(-1)
			job.reserved = false
		}
	}
}

// ensureWorkers starts only the workers the ordered window can currently use.
// Broad scans still reach the fixed sixteen-worker ceiling; a one-file request
// does not retain sixteen short-lived worker stacks after every call.
func (s *grepConcurrentState) ensureWorkers() {
	for s.workers < len(s.pending) && s.workers < grepWorkerCeiling {
		s.workers++
		s.wg.Add(1)
		go s.worker()
	}
}

func (s *grepConcurrentState) worker() {
	defer s.wg.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case job, ok := <-s.jobs:
			if !ok {
				return
			}
			s.scan(job)
		}
	}
}

func (s *grepConcurrentState) visit(ctx context.Context, entry fsx.WalkFile) (fsx.WalkAction, error) {
	if !isMarkdownPath(entry.Resolved.Rel) {
		return fsx.WalkContinue, nil
	}
	if err := ctx.Err(); err != nil {
		return fsx.WalkStop, grepContextError(err)
	}
	if s.run.resume != nil && !s.run.boundarySeen {
		return s.visitResume(ctx, entry)
	}
	return s.schedule(ctx, entry, nil)
}

func (s *grepConcurrentState) visitResume(ctx context.Context, entry fsx.WalkFile) (fsx.WalkAction, error) {
	position := entry.Position
	order := compareGrepPosition(position, cursorPositionToFSX(s.run.resume.Boundary))
	if order > 0 {
		return fsx.WalkStop, ErrCursorStale
	}
	file, err := entry.Open(ctx)
	if err != nil {
		return fsx.WalkStop, err
	}
	fingerprint := file.Fingerprint()
	if err := file.Revalidate(ctx); err != nil {
		_ = file.Close()
		return fsx.WalkStop, err
	}
	_ = file.Close()
	s.run.work.SourceEntriesValidated++
	if order < 0 {
		s.run.prefix = extendGrepPrefix(s.run.prefix, position, fingerprint)
		return fsx.WalkContinue, nil
	}
	s.run.boundarySeen = true
	expectedPrefix, _ := decodeDigest(s.run.resume.Prefix)
	if s.run.resume.Partial == nil {
		s.run.prefix = extendGrepPrefix(s.run.prefix, position, fingerprint)
		if !equalDigest(s.run.prefix[:], expectedPrefix) {
			return fsx.WalkStop, ErrCursorStale
		}
		copyPosition := position
		s.run.lastFull = &copyPosition
		return fsx.WalkContinue, nil
	}
	if !equalDigest(s.run.prefix[:], expectedPrefix) || encodeDigest(fingerprint[:]) != s.run.resume.Partial.Fingerprint {
		return fsx.WalkStop, ErrCursorStale
	}
	return s.schedule(ctx, entry, s.run.resume.Partial)
}

func (s *grepConcurrentState) schedule(ctx context.Context, entry fsx.WalkFile, partial *grepPartialCursor) (fsx.WalkAction, error) {
	if len(s.pending) == grepWorkerCeiling {
		if err := s.reduceOldest(); err != nil {
			return fsx.WalkStop, err
		}
		if s.run.stop != nil {
			return fsx.WalkStop, nil
		}
	}
	if s.reservedFiles >= s.run.query.MaxFiles {
		if err := s.drain(); err != nil {
			return fsx.WalkStop, err
		}
		if s.run.stop != nil {
			return fsx.WalkStop, nil
		}
		return s.limitStop(CursorStopFileLimit)
	}
	if s.reservedBytes >= s.run.query.MaxBytes {
		if err := s.drain(); err != nil {
			return fsx.WalkStop, err
		}
		if s.run.stop != nil {
			return fsx.WalkStop, nil
		}
		return s.limitStop(CursorStopByteLimit)
	}

	file, err := entry.Open(ctx)
	if err != nil {
		if partial != nil && fsx.IsCode(err, fsx.CodeSourceChanged) {
			err = ErrCursorStale
		}
		job := s.errorJob(entry, err)
		s.pending = append(s.pending, job)
		s.terminalErr = nil
		return fsx.WalkStop, nil
	}
	startOffset := int64(0)
	if partial != nil {
		startOffset = partial.ContextOffset
	}
	if startOffset > entry.Resolved.Size {
		_ = file.Close()
		job := s.errorJob(entry, ErrCursorStale)
		s.pending = append(s.pending, job)
		return fsx.WalkStop, nil
	}
	remaining := s.run.query.MaxBytes - s.reservedBytes
	allowance := min(entry.Resolved.Size-startOffset, remaining)
	if allowance < 0 || (allowance == 0 && partial == nil) {
		_ = file.Close()
		return s.limitStop(CursorStopByteLimit)
	}
	job := &grepScanJob{
		sequence:    s.nextSequence,
		entry:       entry,
		file:        file,
		fingerprint: file.Fingerprint(),
		partial:     partial,
		allowance:   allowance,
		events:      make(chan grepScanEvent, 1),
		reserved:    true,
	}
	s.nextSequence++
	s.reservedFiles++
	s.reservedBytes += allowance
	s.pending = append(s.pending, job)
	s.run.tools.grepActivity.Reserve()
	s.observeInFlight(1)
	s.ensureWorkers()
	select {
	case <-s.ctx.Done():
		_ = file.Close()
		job.file = nil
		job.events <- grepScanEvent{kind: grepScanErrorEvent, err: grepContextError(s.ctx.Err())}
		close(job.events)
		return fsx.WalkStop, grepContextError(s.ctx.Err())
	case s.jobs <- job:
		return fsx.WalkContinue, nil
	}
}

func (s *grepConcurrentState) errorJob(entry fsx.WalkFile, err error) *grepScanJob {
	job := &grepScanJob{sequence: s.nextSequence, entry: entry, events: make(chan grepScanEvent, 1)}
	s.nextSequence++
	job.events <- grepScanEvent{kind: grepScanErrorEvent, err: err}
	close(job.events)
	return job
}

func (s *grepConcurrentState) limitStop(reason CursorStop) (fsx.WalkAction, error) {
	if s.run.lastFull == nil {
		return fsx.WalkStop, &fsx.Error{Code: fsx.CodeLimitExceeded}
	}
	s.run.stop = &grepStop{reason: reason, state: s.run.fullBoundaryState(*s.run.lastFull)}
	return fsx.WalkStop, nil
}

func (s *grepConcurrentState) drain() error {
	for len(s.pending) > 0 {
		// A canonical boundary may have been accepted by the visitor's final
		// reduction. Its speculative suffix was canceled deliberately and must
		// never be reduced into a public cancellation error.
		if s.run.stop != nil {
			return nil
		}
		if err := s.reduceOldest(); err != nil {
			return err
		}
		if s.run.stop != nil {
			return nil
		}
	}
	return nil
}

func (s *grepConcurrentState) reduceOldest() error {
	job := s.pending[0]
	defer func() {
		s.pending = s.pending[1:]
		if job.reserved {
			s.run.tools.grepActivity.Release()
			s.observeInFlight(-1)
			job.reserved = false
		}
	}()
	for event := range job.events {
		if !job.started {
			job.started = true
			if job.file != nil {
				s.run.work.FilesScanned++
			}
		}
		if event.bytes < job.acceptedBytes {
			return &fsx.Error{Code: fsx.CodeSourceChanged}
		}
		delta := event.bytes - job.acceptedBytes
		job.acceptedBytes = event.bytes
		s.run.pageBytes += delta
		s.run.work.BytesScanned = uint64(s.run.pageBytes)

		switch event.kind {
		case grepScanCandidateEvent:
			candidate := pendingGrepMatch{
				match:            event.candidate.match,
				occurrencesReady: event.candidate.occurrencesReady,
				emission: s.run.partialCheckpoint(job.entry.Position, job.fingerprint,
					event.candidate.emission.resumeOffset, event.candidate.emission.resumeLine,
					event.candidate.emission.contextOffset, event.candidate.emission.contextLine),
				retry: s.run.partialCheckpoint(job.entry.Position, job.fingerprint,
					event.candidate.retry.resumeOffset, event.candidate.retry.resumeLine,
					event.candidate.retry.contextOffset, event.candidate.retry.contextLine),
			}
			stop, err := s.run.emit(s.ctx, candidate)
			if err != nil {
				s.cancel()
				return err
			}
			if stop {
				s.cancel()
				return nil
			}
		case grepScanFullEvent:
			s.run.prefix = extendGrepPrefix(s.run.prefix, job.entry.Position, job.fingerprint)
			copyPosition := job.entry.Position
			s.run.lastFull = &copyPosition
			return nil
		case grepScanPartialEvent:
			checkpoint := s.run.partialCheckpoint(job.entry.Position, job.fingerprint,
				event.checkpoint.resumeOffset, event.checkpoint.resumeLine,
				event.checkpoint.contextOffset, event.checkpoint.contextLine)
			if !event.advanced {
				if s.run.lastFull != nil && s.run.fullBoundaryAdvances(*s.run.lastFull) {
					s.run.stop = &grepStop{reason: CursorStopByteLimit, state: s.run.fullBoundaryState(*s.run.lastFull)}
					return nil
				}
				return &fsx.Error{Code: fsx.CodeLimitExceeded}
			}
			if !s.run.checkpointAdvances(checkpoint) {
				return &fsx.Error{Code: fsx.CodeLimitExceeded}
			}
			s.run.stop = &grepStop{reason: CursorStopByteLimit, state: checkpoint.state()}
			return nil
		case grepScanUnfitEvent:
			stop, err := s.run.stopForUnfitGrepCandidate()
			if err != nil {
				s.cancel()
				return err
			}
			if stop {
				s.cancel()
			}
			return nil
		case grepScanErrorEvent:
			s.cancel()
			if errors.Is(event.err, context.Canceled) || errors.Is(event.err, context.DeadlineExceeded) {
				return grepContextError(event.err)
			}
			return event.err
		}
	}
	if err := s.ctx.Err(); err != nil {
		return grepContextError(err)
	}
	return &fsx.Error{Code: fsx.CodeSourceChanged}
}

func (s *grepConcurrentState) scan(job *grepScanJob) {
	defer close(job.events)
	grepDone := s.run.tools.grepActivity.BeginScan()
	if grepDone != nil {
		defer grepDone()
	}
	s.observeActive(1)
	defer s.observeActive(-1)
	if s.hooks != nil && s.hooks.gate != nil {
		if err := s.hooks.gate(s.ctx, job.sequence); err != nil {
			s.send(job, grepScanEvent{kind: grepScanErrorEvent, err: err})
			return
		}
	}
	if job.file == nil {
		return
	}
	defer job.file.Close()
	if job.partial != nil && encodeDigest(job.fingerprint[:]) != job.partial.Fingerprint {
		s.send(job, grepScanEvent{kind: grepScanErrorEvent, err: ErrCursorStale})
		return
	}
	startOffset, startLine, resumeOffset := int64(0), 1, int64(0)
	if job.partial != nil {
		startOffset, startLine = job.partial.ContextOffset, job.partial.ContextLine
		resumeOffset = job.partial.ResumeOffset
		if resumeOffset > job.entry.Resolved.Size || job.file.Seek(s.ctx, startOffset) != nil {
			s.send(job, grepScanEvent{kind: grepScanErrorEvent, err: ErrCursorStale})
			return
		}
	}
	reader, fastMiss, consumed, err := newGrepLineReaderFor(s.ctx, job.file, job.entry.Resolved.Size, startOffset, startLine, job.allowance, s.run.query, s.run.re)
	if err != nil {
		s.send(job, grepScanEvent{kind: grepScanErrorEvent, bytes: consumed, err: err})
		return
	}
	if fastMiss {
		s.send(job, grepScanEvent{kind: grepScanFullEvent, bytes: consumed})
		return
	}
	defer reader.close()
	before := make([]grepLine, 0, s.run.query.ContextLines)
	pending := make([]grepScannedCandidate, 0, s.run.query.ContextLines+1)
	checkpoint := grepCheckpointCoords{resumeOffset: startOffset, resumeLine: startLine, contextOffset: startOffset, contextLine: startLine}
	advanced := false
	for {
		line, readErr := reader.next(s.ctx)
		consumed += reader.takeNewBytes()
		if errors.Is(readErr, io.EOF) {
			for len(pending) > 0 {
				if !s.send(job, grepScanEvent{kind: grepScanCandidateEvent, bytes: consumed, candidate: &pending[0]}) {
					return
				}
				pending = pending[1:]
			}
			s.send(job, grepScanEvent{kind: grepScanFullEvent, bytes: consumed})
			return
		}
		if errors.Is(readErr, errLineTooLarge) {
			s.send(job, grepScanEvent{kind: grepScanErrorEvent, bytes: consumed, err: &fsx.Error{Code: fsx.CodeInputTooLarge}})
			return
		}
		if errors.Is(readErr, errLineBudget) {
			if len(pending) > 0 {
				checkpoint = pending[0].retry
			}
			s.send(job, grepScanEvent{kind: grepScanPartialEvent, bytes: consumed, checkpoint: checkpoint, advanced: advanced})
			return
		}
		if readErr != nil {
			s.send(job, grepScanEvent{kind: grepScanErrorEvent, bytes: consumed, err: readErr})
			return
		}
		if (line.large != nil && !utf8.Valid(line.large)) || (line.large == nil && !utf8.ValidString(line.text)) {
			s.send(job, grepScanEvent{kind: grepScanErrorEvent, bytes: consumed, err: errInvalidUTF8})
			return
		}
		if line.start < resumeOffset {
			before = appendGrepRing(before, line, s.run.query.ContextLines)
			continue
		}
		if line.start != resumeOffset && !advanced {
			s.send(job, grepScanEvent{kind: grepScanErrorEvent, bytes: consumed, err: ErrCursorStale})
			return
		}
		advanced = true
		if s.run.query.Regex && line.large != nil && len(pending) > 0 {
			s.send(job, grepScanEvent{kind: grepScanUnfitEvent, bytes: consumed})
			return
		}
		for i := range pending {
			pending[i].match.After = append(pending[i].match.After, grepContextEvidence(line))
			pending[i].remaining--
		}
		for len(pending) > 0 && pending[0].remaining == 0 {
			if !s.send(job, grepScanEvent{kind: grepScanCandidateEvent, bytes: consumed, candidate: &pending[0]}) {
				return
			}
			pending = pending[1:]
		}
		match, matched := s.run.matchEvidence(line)
		if matched {
			if s.run.query.Regex && (line.large != nil || grepRingHasLargeLine(before)) {
				s.send(job, grepScanEvent{kind: grepScanUnfitEvent, bytes: consumed})
				return
			}
			beforeContext := make([]GrepContextLine, len(before))
			for i := range before {
				beforeContext[i] = grepContextEvidence(before[i])
			}
			retryOffset, retryLine := line.start, line.number
			if len(before) > 0 {
				retryOffset, retryLine = before[0].start, before[0].number
			}
			emissionOffset, emissionLine := grepResumeContext(before, line, s.run.query.ContextLines)
			pending = append(pending, grepScannedCandidate{
				match: GrepMatch{Path: job.entry.Resolved.Rel, Line: line.number, Column: match.Column, Occurrences: match.Occurrences,
					Text: match.Text, TextTruncated: match.TextTruncated, TextStartColumn: match.TextStartColumn,
					TextEndColumn: match.TextEndColumn, LineBytes: match.LineBytes, Before: beforeContext,
					After: []GrepContextLine{}, Fingerprint: encodeDigest(job.fingerprint[:])},
				occurrencesReady: !s.run.query.Regex,
				remaining:        s.run.query.ContextLines,
				emission:         grepCheckpointCoords{resumeOffset: line.end, resumeLine: line.number + 1, contextOffset: emissionOffset, contextLine: emissionLine},
				retry:            grepCheckpointCoords{resumeOffset: line.start, resumeLine: line.number, contextOffset: retryOffset, contextLine: retryLine},
			})
		}
		before = appendGrepRing(before, line, s.run.query.ContextLines)
		contextOffset, contextLine := grepResumeContext(before[:max(len(before)-1, 0)], line, s.run.query.ContextLines)
		checkpoint = grepCheckpointCoords{resumeOffset: line.end, resumeLine: line.number + 1, contextOffset: contextOffset, contextLine: contextLine}
		if s.run.query.ContextLines == 0 {
			for len(pending) > 0 && pending[0].remaining == 0 {
				if !s.send(job, grepScanEvent{kind: grepScanCandidateEvent, bytes: consumed, candidate: &pending[0]}) {
					return
				}
				pending = pending[1:]
			}
		}
	}
}

func (s *grepConcurrentState) observeActive(delta int) {
	if s.hooks == nil || s.hooks.observe == nil {
		return
	}
	s.mu.Lock()
	s.active += delta
	snapshot := grepConcurrentSnapshot{Active: s.active, InFlight: s.inFlight}
	s.mu.Unlock()
	s.hooks.observe(snapshot)
}

func (s *grepConcurrentState) observeInFlight(delta int) {
	if s.hooks == nil || s.hooks.observe == nil {
		return
	}
	s.mu.Lock()
	s.inFlight += delta
	snapshot := grepConcurrentSnapshot{Active: s.active, InFlight: s.inFlight}
	s.mu.Unlock()
	s.hooks.observe(snapshot)
}

func (s *grepConcurrentState) send(job *grepScanJob, event grepScanEvent) bool {
	select {
	case <-s.ctx.Done():
		return false
	case job.events <- event:
		if s.hooks != nil && s.hooks.terminalEvent != nil && event.kind != grepScanCandidateEvent {
			s.hooks.terminalEvent(job.sequence)
		}
		return true
	}
}

// newGrepLineReaderFor is the worker-local form of the existing fast-miss
// reader setup. consumed is non-zero only when the small-file fast path read
// bytes before returning an error or full-file miss.
func newGrepLineReaderFor(ctx context.Context, file *fsx.File, size, offset int64, line int, remaining int64, query normalizedGrepQuery, re *regexp.Regexp) (*grepLineReader, bool, int64, error) {
	if offset != 0 || query.Regex || !query.CaseSensitive || size > remaining || size > grepFastFileBytes {
		var oversizedLiteral *regexp.Regexp
		if !query.Regex {
			oversizedLiteral = re
		}
		return newGrepLineReader(file, size, offset, line, remaining, oversizedLiteral), false, 0, nil
	}
	data := make([]byte, int(size))
	read := 0
	for read < len(data) {
		n, err := file.Read(ctx, data[read:])
		read += n
		if err != nil && !(errors.Is(err, io.EOF) && read == len(data)) {
			return nil, false, int64(read), err
		}
		if n == 0 && read < len(data) {
			return nil, false, int64(read), &fsx.Error{Code: fsx.CodeSourceChanged}
		}
	}
	if !bytes.Contains(data, []byte(query.Pattern)) {
		if !utf8.Valid(data) {
			return nil, false, int64(read), errInvalidUTF8
		}
		if err := fitContextError(ctx); err != nil {
			return nil, false, int64(read), err
		}
		return nil, true, int64(read), nil
	}
	return newBufferedGrepLineReader(file, size, line, remaining, data), false, 0, nil
}
