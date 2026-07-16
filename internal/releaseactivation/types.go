// Package releaseactivation owns the local release transaction lifecycle.
package releaseactivation

import "fmt"

const ManifestVersion = 2

// State is the durable state of the single release slot. Clear is represented
// on disk by the absence of an active manifest.
type State string

const (
	StateClear       State = "clear"
	StatePrepared    State = "prepared"
	StatePending     State = "pending"
	StateAccepting   State = "accepting"
	StateRollingBack State = "rolling_back"
)

// Event is the lifecycle vocabulary. EventDeploymentReady is internal to
// Manager: public callers cannot use a passive observation to advance a
// prepared transaction.
type Event string

const (
	EventPrepare         Event = "prepare"
	EventResume          Event = "resume"
	EventDeploymentReady Event = "deployment_ready"
	EventAccept          Event = "accept"
	EventRollback        Event = "rollback"
)

// ReleaseID is an opaque, printable identifier. Authorization always compares
// the complete value; prefixes are display-only and never accepted.
type ReleaseID string

// Manifest is the private, versioned durable transaction record. It contains
// paths and fingerprints needed by the pinned controller, but never raw
// environment values or credentials.
type Manifest struct {
	Version          int       `json:"version"`
	State            State     `json:"state"`
	ID               ReleaseID `json:"id"`
	Commit           string    `json:"commit"`
	DependencySHA256 string    `json:"dependency_sha256"`

	CandidateFile   string `json:"candidate_file"`
	CandidateSHA256 string `json:"candidate_sha256"`
	AuthorityFile   string `json:"authority_file"`
	AuthoritySHA256 string `json:"authority_sha256"`
	PreviousPresent bool   `json:"previous_present"`
	PreviousFile    string `json:"previous_file,omitempty"`
	PreviousSHA256  string `json:"previous_sha256,omitempty"`

	TargetPath            string `json:"target_path"`
	EffectiveUID          int    `json:"effective_uid"`
	LaunchAgentLabel      string `json:"launch_agent_label"`
	PlistPath             string `json:"plist_path"`
	PlistSHA256           string `json:"plist_sha256"`
	WrapperPath           string `json:"wrapper_path"`
	WrapperSHA256         string `json:"wrapper_sha256"`
	MCPWrapperPath        string `json:"mcp_wrapper_path"`
	MCPWrapperSHA256      string `json:"mcp_wrapper_sha256"`
	StdoutPath            string `json:"stdout_path"`
	StderrPath            string `json:"stderr_path"`
	EnvironmentPath       string `json:"environment_path"`
	EnvironmentSHA256     string `json:"environment_sha256"`
	HealthURLFile         string `json:"health_url_file"`
	ReadyTimeoutSeconds   int    `json:"ready_timeout_seconds"`
	ReadyPollMilliseconds int    `json:"ready_poll_milliseconds"`
}

// Snapshot is the complete input state for a decision. Manifest is nil exactly
// when the slot is clear.
type Snapshot struct {
	Manifest *Manifest
}

// State returns the snapshot's effective durable state.
func (s Snapshot) State() State {
	if s.Manifest == nil {
		return StateClear
	}
	return s.Manifest.State
}

// Observed contains facts gathered by the Manager before calling Decide. The
// pure model deliberately cannot discover any of these facts itself.
type Observed struct {
	CandidateSHA256         string
	AuthorityArtifactSHA256 string
	ControllerSHA256        string
	PreviousSHA256          string
	InstalledPresent        bool
	InstalledSHA256         string
	PlistSHA256             string
	WrapperSHA256           string
	MCPWrapperSHA256        string
	EnvironmentSHA256       string
	RuntimeReady            bool
	SupervisorUnloaded      bool
}

// Context carries the requested identity, a proposed manifest for prepare, and
// all observations on which the decision may rely.
type Context struct {
	ReleaseID ReleaseID
	Prepared  *Manifest
	Observed  Observed

	// rollbackReady is an attempt-local receipt. Manager sets it only after it
	// restarted the previous runtime and WaitReady returned successfully while
	// holding the lifecycle lock. It is never serialized or accepted from a
	// public caller, so a crash safely loses it and repeats reconciliation.
	rollbackReady bool
}

// CommandKind describes one ordered action for the Manager. Persist commands
// always precede effects, making direction choices durable before mutation.
type CommandKind string

const (
	CommandPublishPrepared  CommandKind = "publish_prepared"
	CommandPersistState     CommandKind = "persist_state"
	CommandResumeDeployment CommandKind = "resume_deployment"
	CommandResumeRollback   CommandKind = "resume_rollback"
	CommandClearTransaction CommandKind = "clear_transaction"
)

// Command is intentionally declarative. The Manager owns all I/O and interprets
// the command against Decision.Next.
type Command struct {
	Kind CommandKind
}

// Decision is the complete, deterministic result of one state/event pair.
// Err decisions retain Current and never contain commands.
type Decision struct {
	Current  Snapshot
	Next     Snapshot
	Commands []Command
	Applied  bool
	Err      *Error
}

// ErrorCode is the stable machine-facing failure taxonomy.
type ErrorCode string

const (
	ErrorNoPending           ErrorCode = "no_pending"
	ErrorBusy                ErrorCode = "busy"
	ErrorIdentityMismatch    ErrorCode = "identity_mismatch"
	ErrorAuthorityMismatch   ErrorCode = "authority_mismatch"
	ErrorInstalledMismatch   ErrorCode = "installed_mismatch"
	ErrorStateMalformed      ErrorCode = "state_malformed"
	ErrorArtifactMismatch    ErrorCode = "artifact_mismatch"
	ErrorRuntimeDrift        ErrorCode = "runtime_drift"
	ErrorStateConflict       ErrorCode = "state_conflict"
	ErrorRecoveryUnconfirmed ErrorCode = "recovery_unconfirmed"
)

// Error is a sanitized lifecycle error. Message is fixed per code and must not
// contain paths, identifiers, child output, or environment data.
type Error struct {
	Code    ErrorCode
	Message string
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func lifecycleError(code ErrorCode) *Error {
	return &Error{Code: code, Message: errorMessage(code)}
}

func errorMessage(code ErrorCode) string {
	switch code {
	case ErrorNoPending:
		return "no unresolved release exists"
	case ErrorBusy:
		return "release state is busy"
	case ErrorIdentityMismatch:
		return "release identity does not match"
	case ErrorAuthorityMismatch:
		return "release controller identity does not match"
	case ErrorInstalledMismatch:
		return "installed target does not match release state"
	case ErrorStateMalformed:
		return "release state is malformed"
	case ErrorArtifactMismatch:
		return "release artifact does not match"
	case ErrorRuntimeDrift:
		return "supervised runtime configuration changed"
	case ErrorStateConflict:
		return "event conflicts with release state"
	case ErrorRecoveryUnconfirmed:
		return "recovery could not be confirmed"
	default:
		return "release operation failed"
	}
}
