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
	FilesScanned uint64
	BytesScanned uint64
}

func NewCompleteCoverage(work CoverageWork) Coverage {
	return Coverage{
		ResultComplete: true,
		ScopeComplete:  true,
		Consistency:    CoverageConsistencyStable,
		FilesScanned:   work.FilesScanned,
		BytesScanned:   work.BytesScanned,
		StoppedBy:      CoverageStopScope,
		Continuation:   CoverageContinuationComplete,
	}
}

func NewCursorCoverage(work CoverageWork, stoppedBy CursorStop, cursor string) (Coverage, error) {
	if cursor == "" || (stoppedBy != CursorStopResultLimit && stoppedBy != CursorStopResponseLimit) {
		return Coverage{}, ErrInvalidCoverage
	}
	return Coverage{
		ResultComplete: false,
		ScopeComplete:  true,
		Consistency:    CoverageConsistencyStable,
		FilesScanned:   work.FilesScanned,
		BytesScanned:   work.BytesScanned,
		StoppedBy:      string(stoppedBy),
		Continuation:   CoverageContinuationCursor,
		NextCursor:     cursor,
	}, nil
}

func NewRestartCoverage(work CoverageWork, stoppedBy RestartStop) (Coverage, error) {
	switch stoppedBy {
	case RestartStopTimeout, RestartStopCanceled, RestartStopSourceChange, RestartStopError:
	default:
		return Coverage{}, ErrInvalidCoverage
	}
	return Coverage{
		ResultComplete: false,
		ScopeComplete:  false,
		Consistency:    CoverageConsistencyBestEffort,
		FilesScanned:   work.FilesScanned,
		BytesScanned:   work.BytesScanned,
		StoppedBy:      string(stoppedBy),
		Continuation:   CoverageContinuationRestart,
	}, nil
}
