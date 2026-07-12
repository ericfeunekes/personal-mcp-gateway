package releaseactivation

import (
	"encoding/hex"
	"path/filepath"
	"strings"
	"time"
)

// ValidateSnapshot checks lifecycle invariants without performing I/O.
func ValidateSnapshot(snapshot Snapshot) *Error {
	if snapshot.Manifest == nil {
		return nil
	}
	m := snapshot.Manifest
	if m.Version != ManifestVersion || !isActiveState(m.State) ||
		!validReleaseID(m.ID) || !validToken(m.Commit) ||
		m.CandidateFile == "" || !validSHA256(m.CandidateSHA256) ||
		m.AuthorityFile == "" || !validSHA256(m.AuthoritySHA256) ||
		!absolutePaths(m.TargetPath, m.PlistPath, m.WrapperPath, m.MCPWrapperPath, m.StdoutPath,
			m.StderrPath, m.EnvironmentPath, m.HealthURLFile) ||
		m.EffectiveUID < 0 || !ValidLaunchAgentLabel(m.LaunchAgentLabel) ||
		!validSHA256(m.PlistSHA256) || !validSHA256(m.WrapperSHA256) ||
		!validSHA256(m.MCPWrapperSHA256) || !validSHA256(m.EnvironmentSHA256) ||
		m.ReadyTimeoutSeconds <= 0 || m.ReadyTimeoutSeconds > int(maxReadyTimeout/time.Second) ||
		m.ReadyPollMilliseconds <= 0 || m.ReadyPollMilliseconds > int(maxReadyPoll/time.Millisecond) {
		return lifecycleError(ErrorStateMalformed)
	}
	if m.PreviousPresent {
		if m.PreviousFile == "" || !validSHA256(m.PreviousSHA256) {
			return lifecycleError(ErrorStateMalformed)
		}
	} else if m.PreviousFile != "" || m.PreviousSHA256 != "" {
		return lifecycleError(ErrorStateMalformed)
	}
	return nil
}

func absolutePaths(paths ...string) bool {
	for _, path := range paths {
		if !filepath.IsAbs(path) {
			return false
		}
	}
	return true
}

// Decide applies one event to one validated snapshot. It is pure: callers must
// inject every filesystem, runtime, and completion observation through Context.
func Decide(snapshot Snapshot, event Event, context Context) Decision {
	decision := Decision{Current: cloneSnapshot(snapshot), Next: cloneSnapshot(snapshot)}
	if err := ValidateSnapshot(snapshot); err != nil {
		return rejected(decision, err)
	}
	if !validEvent(event) {
		return rejected(decision, lifecycleError(ErrorStateConflict))
	}

	if snapshot.Manifest == nil {
		if event != EventPrepare {
			return rejected(decision, lifecycleError(ErrorNoPending))
		}
		return decidePrepare(decision, context)
	}

	m := snapshot.Manifest
	if event == EventPrepare {
		return rejected(decision, lifecycleError(ErrorStateConflict))
	}
	if context.ReleaseID != m.ID {
		return rejected(decision, lifecycleError(ErrorIdentityMismatch))
	}
	if !eventAllowed(m.State, event) {
		return rejected(decision, lifecycleError(ErrorStateConflict))
	}
	if err := validateActiveObservations(*m, context.Observed); err != nil {
		return rejected(decision, err)
	}

	switch m.State {
	case StatePrepared:
		return decidePrepared(decision, event, *m, context.Observed)
	case StatePending:
		return decidePending(decision, event, *m, context.Observed)
	case StateAccepting:
		return decideAccepting(decision, *m, context.Observed)
	case StateRollingBack:
		return decideRollingBack(decision, event, *m, context.Observed, context.rollbackReady)
	default:
		// ValidateSnapshot makes this unreachable, but retain an explicit
		// fail-closed branch if the state set changes.
		return rejected(decision, lifecycleError(ErrorStateMalformed))
	}
}

func decidePrepare(decision Decision, context Context) Decision {
	if context.Prepared == nil {
		return rejected(decision, lifecycleError(ErrorStateMalformed))
	}
	m := cloneManifest(*context.Prepared)
	if m.State != StatePrepared || context.ReleaseID != "" && context.ReleaseID != m.ID {
		return rejected(decision, lifecycleError(ErrorIdentityMismatch))
	}
	prepared := Snapshot{Manifest: &m}
	if err := ValidateSnapshot(prepared); err != nil {
		return rejected(decision, err)
	}
	if err := validatePrepareObservations(m, context.Observed); err != nil {
		return rejected(decision, err)
	}
	decision.Next = prepared
	decision.Commands = []Command{{Kind: CommandPublishPrepared}}
	decision.Applied = true
	return decision
}

func decidePrepared(decision Decision, event Event, m Manifest, observed Observed) Decision {
	switch event {
	case EventResume:
		if err := validateRuntime(m, observed); err != nil {
			return rejected(decision, err)
		}
		if !targetAllowedForPrepared(m, observed) {
			return rejected(decision, lifecycleError(ErrorInstalledMismatch))
		}
		decision.Commands = []Command{{Kind: CommandResumeDeployment}}
		decision.Applied = true
		return decision
	case EventDeploymentReady:
		if err := validateRuntime(m, observed); err != nil {
			return rejected(decision, err)
		}
		if !observed.InstalledPresent || observed.InstalledSHA256 != m.CandidateSHA256 {
			return rejected(decision, lifecycleError(ErrorInstalledMismatch))
		}
		if !observed.RuntimeReady {
			return rejected(decision, lifecycleError(ErrorRecoveryUnconfirmed))
		}
		return transition(decision, m, StatePending, CommandPersistState)
	case EventRollback:
		if err := validateRuntime(m, observed); err != nil {
			return rejected(decision, err)
		}
		if !targetAllowedForPrepared(m, observed) {
			return rejected(decision, lifecycleError(ErrorInstalledMismatch))
		}
		return transition(decision, m, StateRollingBack, CommandPersistState)
	case EventAccept:
		return rejected(decision, lifecycleError(ErrorStateConflict))
	default:
		return rejected(decision, lifecycleError(ErrorStateConflict))
	}
}

func decidePending(decision Decision, event Event, m Manifest, observed Observed) Decision {
	if !observed.InstalledPresent || observed.InstalledSHA256 != m.CandidateSHA256 {
		return rejected(decision, lifecycleError(ErrorInstalledMismatch))
	}
	switch event {
	case EventAccept:
		if err := validateAcceptanceReadiness(m, observed); err != nil {
			return rejected(decision, err)
		}
		return transition(decision, m, StateAccepting, CommandPersistState)
	case EventRollback:
		if err := validateRuntime(m, observed); err != nil {
			return rejected(decision, err)
		}
		return transition(decision, m, StateRollingBack, CommandPersistState)
	case EventResume:
		return rejected(decision, lifecycleError(ErrorStateConflict))
	default:
		return rejected(decision, lifecycleError(ErrorStateConflict))
	}
}

func decideAccepting(decision Decision, m Manifest, observed Observed) Decision {
	if err := validateAcceptanceReadiness(m, observed); err != nil {
		return rejected(decision, err)
	}
	decision.Next = Snapshot{}
	decision.Commands = []Command{{Kind: CommandClearTransaction}}
	decision.Applied = true
	return decision
}

func decideRollingBack(decision Decision, event Event, m Manifest, observed Observed, rollbackReady bool) Decision {
	if err := validateRuntime(m, observed); err != nil {
		return rejected(decision, err)
	}
	if !targetAllowedForRollback(m, observed) {
		return rejected(decision, lifecycleError(ErrorInstalledMismatch))
	}
	if recoveryConfirmed(m, observed, rollbackReady) {
		decision.Next = Snapshot{}
		decision.Commands = []Command{{Kind: CommandClearTransaction}}
		decision.Applied = true
		return decision
	}
	decision.Commands = []Command{{Kind: CommandResumeRollback}}
	decision.Applied = true
	return decision
}

func transition(decision Decision, m Manifest, state State, command CommandKind) Decision {
	next := cloneManifest(m)
	next.State = state
	decision.Next = Snapshot{Manifest: &next}
	decision.Commands = []Command{{Kind: command}}
	decision.Applied = true
	return decision
}

func rejected(decision Decision, err *Error) Decision {
	decision.Next = cloneSnapshot(decision.Current)
	decision.Commands = nil
	decision.Applied = false
	decision.Err = err
	return decision
}

func validatePrepareObservations(m Manifest, observed Observed) *Error {
	if err := validateActiveObservations(m, observed); err != nil {
		return err
	}
	if m.PreviousPresent {
		if !observed.InstalledPresent || observed.InstalledSHA256 != m.PreviousSHA256 {
			return lifecycleError(ErrorInstalledMismatch)
		}
	} else if observed.InstalledPresent {
		return lifecycleError(ErrorInstalledMismatch)
	}
	return validateRuntime(m, observed)
}

func validateActiveObservations(m Manifest, observed Observed) *Error {
	if observed.ControllerSHA256 != m.AuthoritySHA256 {
		return lifecycleError(ErrorAuthorityMismatch)
	}
	if observed.AuthorityArtifactSHA256 != m.AuthoritySHA256 ||
		observed.CandidateSHA256 != m.CandidateSHA256 ||
		m.PreviousPresent && observed.PreviousSHA256 != m.PreviousSHA256 {
		return lifecycleError(ErrorArtifactMismatch)
	}
	return nil
}

func validateRuntime(m Manifest, observed Observed) *Error {
	if observed.PlistSHA256 != m.PlistSHA256 ||
		observed.WrapperSHA256 != m.WrapperSHA256 ||
		observed.MCPWrapperSHA256 != m.MCPWrapperSHA256 ||
		observed.EnvironmentSHA256 != m.EnvironmentSHA256 {
		return lifecycleError(ErrorRuntimeDrift)
	}
	return nil
}

// validateAcceptanceReadiness proves that accepting the candidate is safe at
// both irreversible boundaries: before persisting accepting and immediately
// before clearing recovery state. RuntimeReady alone is insufficient when the
// supervisor reports the service unloaded.
func validateAcceptanceReadiness(m Manifest, observed Observed) *Error {
	if err := validateRuntime(m, observed); err != nil {
		return err
	}
	if !observed.InstalledPresent || observed.InstalledSHA256 != m.CandidateSHA256 {
		return lifecycleError(ErrorInstalledMismatch)
	}
	if observed.SupervisorUnloaded || !observed.RuntimeReady {
		return lifecycleError(ErrorRecoveryUnconfirmed)
	}
	return nil
}

func targetAllowedForPrepared(m Manifest, observed Observed) bool {
	if !observed.InstalledPresent {
		return !m.PreviousPresent
	}
	if observed.InstalledSHA256 == m.CandidateSHA256 {
		return true
	}
	return m.PreviousPresent && observed.InstalledSHA256 == m.PreviousSHA256
}

func targetAllowedForRollback(m Manifest, observed Observed) bool {
	if !observed.InstalledPresent {
		return !m.PreviousPresent
	}
	if observed.InstalledSHA256 == m.CandidateSHA256 {
		return true
	}
	return m.PreviousPresent && observed.InstalledSHA256 == m.PreviousSHA256
}

func recoveryConfirmed(m Manifest, observed Observed, rollbackReady bool) bool {
	if m.PreviousPresent {
		return rollbackReady && observed.InstalledPresent && observed.InstalledSHA256 == m.PreviousSHA256 && observed.RuntimeReady
	}
	return !observed.InstalledPresent && observed.SupervisorUnloaded
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	if snapshot.Manifest == nil {
		return Snapshot{}
	}
	m := cloneManifest(*snapshot.Manifest)
	return Snapshot{Manifest: &m}
}

func cloneManifest(manifest Manifest) Manifest { return manifest }

func isActiveState(state State) bool {
	switch state {
	case StatePrepared, StatePending, StateAccepting, StateRollingBack:
		return true
	default:
		return false
	}
}

func validEvent(event Event) bool {
	switch event {
	case EventPrepare, EventResume, EventDeploymentReady, EventAccept, EventRollback:
		return true
	default:
		return false
	}
}

func eventAllowed(state State, event Event) bool {
	switch state {
	case StatePrepared:
		return event == EventResume || event == EventDeploymentReady || event == EventRollback
	case StatePending:
		return event == EventAccept || event == EventRollback
	case StateAccepting:
		return event == EventAccept
	case StateRollingBack:
		return event == EventRollback
	default:
		return false
	}
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}

func validReleaseID(id ReleaseID) bool {
	value := string(id)
	return validSHA256(value)
}

func validToken(value string) bool {
	return value != "" && !strings.ContainsAny(value, " \t\r\n") && len(value) <= 128
}
