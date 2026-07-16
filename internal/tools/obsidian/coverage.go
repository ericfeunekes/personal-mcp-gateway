package obsidian

import "errors"

const (
	CoverageConsistencyStable     = "stable"
	CoverageConsistencyBestEffort = "best_effort"
	CoverageStopScope             = "scope"
	CoverageContinuationComplete  = "complete"
	CoverageContinuationCursor    = "cursor"
	CoverageContinuationRestart   = "restart"
)

type CursorStop string

const (
	CursorStopResultLimit   CursorStop = "result_limit"
	CursorStopResponseLimit CursorStop = "response_limit"
	CursorStopFileLimit     CursorStop = "file_limit"
	CursorStopByteLimit     CursorStop = "byte_limit"
)

type RestartStop string

const (
	RestartStopTimeout      RestartStop = "timeout"
	RestartStopCanceled     RestartStop = "canceled"
	RestartStopSourceChange RestartStop = "source_change"
	RestartStopError        RestartStop = "error"
)

var ErrInvalidCoverage = errors.New("invalid coverage")

type CoverageWork struct {
	FilesScanned           uint64
	BytesScanned           uint64
	SourceEntriesValidated uint64
}

func NewCompleteCoverage(work CoverageWork) Coverage {
	return Coverage{
		ResultComplete:         true,
		ScopeComplete:          true,
		Consistency:            CoverageConsistencyStable,
		FilesScanned:           work.FilesScanned,
		BytesScanned:           work.BytesScanned,
		SourceEntriesValidated: work.SourceEntriesValidated,
		StoppedBy:              CoverageStopScope,
		Continuation:           CoverageContinuationComplete,
	}
}

func NewCursorCoverage(work CoverageWork, stoppedBy CursorStop, cursor string) (Coverage, error) {
	return NewCursorCoverageWithScope(work, stoppedBy, cursor, true)
}

// NewCursorCoverageWithScope constructs a deterministic, advancing partial.
// Shallow ls pages pass scopeComplete=true because the directory snapshot is
// fully materialized before fitting; streaming retrieval pages pass false.
func NewCursorCoverageWithScope(work CoverageWork, stoppedBy CursorStop, cursor string, scopeComplete bool) (Coverage, error) {
	if cursor == "" || !validCursorStop(stoppedBy) {
		return Coverage{}, ErrInvalidCoverage
	}
	return Coverage{
		ResultComplete:         false,
		ScopeComplete:          scopeComplete,
		Consistency:            CoverageConsistencyStable,
		FilesScanned:           work.FilesScanned,
		BytesScanned:           work.BytesScanned,
		SourceEntriesValidated: work.SourceEntriesValidated,
		StoppedBy:              string(stoppedBy),
		Continuation:           CoverageContinuationCursor,
		NextCursor:             cursor,
	}, nil
}

func NewRestartCoverage(work CoverageWork, stoppedBy RestartStop) (Coverage, error) {
	switch stoppedBy {
	case RestartStopTimeout, RestartStopCanceled, RestartStopSourceChange, RestartStopError:
	default:
		return Coverage{}, ErrInvalidCoverage
	}
	return Coverage{
		ResultComplete:         false,
		ScopeComplete:          false,
		Consistency:            CoverageConsistencyBestEffort,
		FilesScanned:           work.FilesScanned,
		BytesScanned:           work.BytesScanned,
		SourceEntriesValidated: work.SourceEntriesValidated,
		StoppedBy:              string(stoppedBy),
		Continuation:           CoverageContinuationRestart,
	}, nil
}

func validCursorStop(stoppedBy CursorStop) bool {
	switch stoppedBy {
	case CursorStopResultLimit, CursorStopResponseLimit, CursorStopFileLimit, CursorStopByteLimit:
		return true
	default:
		return false
	}
}
