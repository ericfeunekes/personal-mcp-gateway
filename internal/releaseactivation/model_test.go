package releaseactivation

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	testCandidate  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testAuthority  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testPrevious   = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	testPlist      = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	testWrapper    = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	testEnv        = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	testMCPWrapper = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testID         = ReleaseID("123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0")
)

func TestDecideExhaustiveStateEventMatrix(t *testing.T) {
	t.Parallel()

	type expected struct {
		state   State
		command CommandKind
		code    ErrorCode
	}
	states := []State{StateClear, StatePrepared, StatePending, StateAccepting, StateRollingBack}
	events := []Event{EventPrepare, EventResume, EventDeploymentReady, EventAccept, EventRollback}
	want := map[State]map[Event]expected{
		StateClear: {
			EventPrepare:         {state: StatePrepared, command: CommandPublishPrepared},
			EventResume:          {state: StateClear, code: ErrorNoPending},
			EventDeploymentReady: {state: StateClear, code: ErrorNoPending},
			EventAccept:          {state: StateClear, code: ErrorNoPending},
			EventRollback:        {state: StateClear, code: ErrorNoPending},
		},
		StatePrepared: {
			EventPrepare:         {state: StatePrepared, code: ErrorStateConflict},
			EventResume:          {state: StatePrepared, command: CommandResumeDeployment},
			EventDeploymentReady: {state: StatePending, command: CommandPersistState},
			EventAccept:          {state: StatePrepared, code: ErrorStateConflict},
			EventRollback:        {state: StateRollingBack, command: CommandPersistState},
		},
		StatePending: {
			EventPrepare:         {state: StatePending, code: ErrorStateConflict},
			EventResume:          {state: StatePending, code: ErrorStateConflict},
			EventDeploymentReady: {state: StatePending, code: ErrorStateConflict},
			EventAccept:          {state: StateAccepting, command: CommandPersistState},
			EventRollback:        {state: StateRollingBack, command: CommandPersistState},
		},
		StateAccepting: {
			EventPrepare:         {state: StateAccepting, code: ErrorStateConflict},
			EventResume:          {state: StateAccepting, code: ErrorStateConflict},
			EventDeploymentReady: {state: StateAccepting, code: ErrorStateConflict},
			EventAccept:          {state: StateClear, command: CommandClearTransaction},
			EventRollback:        {state: StateAccepting, code: ErrorStateConflict},
		},
		StateRollingBack: {
			EventPrepare:         {state: StateRollingBack, code: ErrorStateConflict},
			EventResume:          {state: StateRollingBack, code: ErrorStateConflict},
			EventDeploymentReady: {state: StateRollingBack, code: ErrorStateConflict},
			EventAccept:          {state: StateRollingBack, code: ErrorStateConflict},
			EventRollback:        {state: StateClear, command: CommandClearTransaction},
		},
	}

	seen := 0
	for _, state := range states {
		for _, event := range events {
			state, event := state, event
			t.Run(string(state)+"/"+string(event), func(t *testing.T) {
				snapshot, context := matrixInputs(state)
				if state == StateRollingBack && event == EventRollback {
					context.rollbackReady = true
				}
				decision := Decide(snapshot, event, context)
				expect := want[state][event]
				if got := decision.Next.State(); got != expect.state {
					t.Fatalf("next state = %q, want %q", got, expect.state)
				}
				if expect.code != "" {
					assertRejected(t, decision, expect.code)
					return
				}
				if decision.Err != nil || !decision.Applied {
					t.Fatalf("successful decision = %#v", decision)
				}
				assertCommands(t, decision, expect.command)
			})
			seen++
		}
	}
	if seen != len(states)*len(events) {
		t.Fatalf("covered %d state/event pairs, want %d", seen, len(states)*len(events))
	}
}

func TestDecidePreparedResumeReconcilesWithoutSecondDeploymentEvent(t *testing.T) {
	t.Parallel()
	m := validManifest(StatePrepared, true)

	tests := []struct {
		name      string
		observed  Observed
		wantState State
		want      CommandKind
	}{
		{
			name:      "previous target still installed",
			observed:  validObserved(m, testPrevious, false),
			wantState: StatePrepared,
			want:      CommandResumeDeployment,
		},
		{
			name:      "candidate installed but runtime not ready",
			observed:  validObserved(m, testCandidate, false),
			wantState: StatePrepared,
			want:      CommandResumeDeployment,
		},
		{
			name:      "candidate installed and runtime ready still requires restart",
			observed:  validObserved(m, testCandidate, true),
			wantState: StatePrepared,
			want:      CommandResumeDeployment,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decision := Decide(Snapshot{Manifest: &m}, EventResume, Context{ReleaseID: m.ID, Observed: tt.observed})
			if decision.Err != nil || decision.Next.State() != tt.wantState {
				t.Fatalf("decision = %#v, want state %q", decision, tt.wantState)
			}
			assertCommands(t, decision, tt.want)
		})
	}

	ready := validObserved(m, testCandidate, true)
	decision := Decide(Snapshot{Manifest: &m}, EventDeploymentReady, Context{ReleaseID: m.ID, Observed: ready})
	if decision.Err != nil || decision.Next.State() != StatePending {
		t.Fatalf("deployment-ready decision = %#v", decision)
	}
	assertCommands(t, decision, CommandPersistState)

	notReady := ready
	notReady.RuntimeReady = false
	decision = Decide(Snapshot{Manifest: &m}, EventDeploymentReady, Context{ReleaseID: m.ID, Observed: notReady})
	assertRejected(t, decision, ErrorRecoveryUnconfirmed)
}

func TestDecideAcceptRequiresCurrentReadyLoadedCandidateAtBothBoundaries(t *testing.T) {
	t.Parallel()

	for _, state := range []State{StatePending, StateAccepting} {
		m := validManifest(state, true)
		valid := validObservedForState(m)
		for _, tt := range []struct {
			name   string
			mutate func(*Observed)
			code   ErrorCode
		}{
			{name: "candidate absent", mutate: func(o *Observed) { o.InstalledPresent = false; o.InstalledSHA256 = "" }, code: ErrorInstalledMismatch},
			{name: "candidate changed", mutate: func(o *Observed) { o.InstalledSHA256 = testPrevious }, code: ErrorInstalledMismatch},
			{name: "runtime drift", mutate: func(o *Observed) { o.PlistSHA256 = testPrevious }, code: ErrorRuntimeDrift},
			{name: "runtime not ready", mutate: func(o *Observed) { o.RuntimeReady = false }, code: ErrorRecoveryUnconfirmed},
			{name: "supervisor unloaded despite ready signal", mutate: func(o *Observed) { o.SupervisorUnloaded = true }, code: ErrorRecoveryUnconfirmed},
		} {
			t.Run(string(state)+"/"+tt.name, func(t *testing.T) {
				observed := valid
				tt.mutate(&observed)
				decision := Decide(Snapshot{Manifest: &m}, EventAccept, Context{ReleaseID: m.ID, Observed: observed})
				assertRejected(t, decision, tt.code)
			})
		}
	}
}

func TestDecideFirstInstallRollbackRequiresAbsentTargetAndUnloadedSupervisor(t *testing.T) {
	t.Parallel()
	m := validManifest(StateRollingBack, false)

	for _, tt := range []struct {
		name        string
		present     bool
		installed   string
		unloaded    bool
		wantState   State
		wantCommand CommandKind
	}{
		{name: "candidate still installed", present: true, installed: testCandidate, unloaded: true, wantState: StateRollingBack, wantCommand: CommandResumeRollback},
		{name: "target absent but supervisor loaded", unloaded: false, wantState: StateRollingBack, wantCommand: CommandResumeRollback},
		{name: "target absent and supervisor unloaded", unloaded: true, wantState: StateClear, wantCommand: CommandClearTransaction},
	} {
		t.Run(tt.name, func(t *testing.T) {
			observed := validObserved(m, tt.installed, false)
			observed.InstalledPresent = tt.present
			observed.SupervisorUnloaded = tt.unloaded
			decision := Decide(Snapshot{Manifest: &m}, EventRollback, Context{ReleaseID: m.ID, Observed: observed})
			if decision.Err != nil || decision.Next.State() != tt.wantState {
				t.Fatalf("decision = %#v, want state %q", decision, tt.wantState)
			}
			assertCommands(t, decision, tt.wantCommand)
		})
	}
}

func TestDecidePreviousRollbackRequiresExactRuntimeProof(t *testing.T) {
	t.Parallel()
	m := validManifest(StateRollingBack, true)
	observed := validObserved(m, testPrevious, false)

	decision := Decide(Snapshot{Manifest: &m}, EventRollback, Context{ReleaseID: m.ID, Observed: observed})
	if decision.Err != nil || decision.Next.State() != StateRollingBack {
		t.Fatalf("receipt-absent recovery decision = %#v", decision)
	}
	assertCommands(t, decision, CommandResumeRollback)

	observed.RuntimeReady = true
	decision = Decide(Snapshot{Manifest: &m}, EventRollback, Context{ReleaseID: m.ID, Observed: observed})
	if decision.Err != nil || decision.Next.State() != StateRollingBack {
		t.Fatalf("passive-ready recovery decision = %#v", decision)
	}
	assertCommands(t, decision, CommandResumeRollback)

	decision = Decide(Snapshot{Manifest: &m}, EventRollback, Context{ReleaseID: m.ID, Observed: observed, rollbackReady: true})
	if decision.Err != nil || decision.Next.State() != StateClear {
		t.Fatalf("proven recovery decision = %#v", decision)
	}
	assertCommands(t, decision, CommandClearTransaction)
}

func TestDecideExactIdentityAndIrreversibleDirection(t *testing.T) {
	t.Parallel()

	for _, state := range []State{StatePrepared, StatePending, StateAccepting, StateRollingBack} {
		m := validManifest(state, true)
		event := map[State]Event{
			StatePrepared: EventResume, StatePending: EventAccept,
			StateAccepting: EventAccept, StateRollingBack: EventRollback,
		}[state]
		decision := Decide(Snapshot{Manifest: &m}, event, Context{
			ReleaseID: ReleaseID(string(m.ID) + "x"),
			Observed:  validObservedForState(m),
		})
		assertRejected(t, decision, ErrorIdentityMismatch)
	}

	accepting := validManifest(StateAccepting, true)
	decision := Decide(Snapshot{Manifest: &accepting}, EventRollback, Context{ReleaseID: accepting.ID, Observed: validObservedForState(accepting)})
	assertRejected(t, decision, ErrorStateConflict)

	rollingBack := validManifest(StateRollingBack, true)
	decision = Decide(Snapshot{Manifest: &rollingBack}, EventAccept, Context{ReleaseID: rollingBack.ID, Observed: validObservedForState(rollingBack)})
	assertRejected(t, decision, ErrorStateConflict)
}

func TestDecideInvalidFactsNeverEmitMutatingCommands(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		state  State
		event  Event
		mutate func(*Observed)
		code   ErrorCode
	}{
		{name: "candidate artifact", state: StatePending, event: EventAccept, mutate: func(o *Observed) { o.CandidateSHA256 = testPrevious }, code: ErrorArtifactMismatch},
		{name: "authority artifact", state: StatePending, event: EventAccept, mutate: func(o *Observed) { o.AuthorityArtifactSHA256 = testPrevious }, code: ErrorArtifactMismatch},
		{name: "controller", state: StatePending, event: EventAccept, mutate: func(o *Observed) { o.ControllerSHA256 = testPrevious }, code: ErrorAuthorityMismatch},
		{name: "installed candidate", state: StatePending, event: EventAccept, mutate: func(o *Observed) { o.InstalledSHA256 = testPrevious }, code: ErrorInstalledMismatch},
		{name: "wrapper drift", state: StatePrepared, event: EventResume, mutate: func(o *Observed) { o.WrapperSHA256 = testPrevious }, code: ErrorRuntimeDrift},
		{name: "mcp wrapper drift", state: StatePrepared, event: EventResume, mutate: func(o *Observed) { o.MCPWrapperSHA256 = testPrevious }, code: ErrorRuntimeDrift},
		{name: "environment drift", state: StatePending, event: EventRollback, mutate: func(o *Observed) { o.EnvironmentSHA256 = testPrevious }, code: ErrorRuntimeDrift},
		{name: "accept environment drift", state: StatePending, event: EventAccept, mutate: func(o *Observed) { o.EnvironmentSHA256 = testPrevious }, code: ErrorRuntimeDrift},
		{name: "accepting environment drift", state: StateAccepting, event: EventAccept, mutate: func(o *Observed) { o.EnvironmentSHA256 = testPrevious }, code: ErrorRuntimeDrift},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := validManifest(tt.state, true)
			observed := validObservedForState(m)
			tt.mutate(&observed)
			decision := Decide(Snapshot{Manifest: &m}, tt.event, Context{ReleaseID: m.ID, Observed: observed})
			assertRejected(t, decision, tt.code)
		})
	}
}

func TestValidateSnapshotRejectsMalformedAndPartialManifests(t *testing.T) {
	t.Parallel()

	valid := validManifest(StatePrepared, true)
	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{name: "version", mutate: func(m *Manifest) { m.Version++ }},
		{name: "clear manifest", mutate: func(m *Manifest) { m.State = StateClear }},
		{name: "unknown state", mutate: func(m *Manifest) { m.State = State("unknown") }},
		{name: "short id", mutate: func(m *Manifest) { m.ID = "abcdef" }},
		{name: "uppercase id", mutate: func(m *Manifest) { m.ID = ReleaseID(strings.ToUpper(string(m.ID))) }},
		{name: "commit", mutate: func(m *Manifest) { m.Commit = "" }},
		{name: "candidate file", mutate: func(m *Manifest) { m.CandidateFile = "" }},
		{name: "candidate hash", mutate: func(m *Manifest) { m.CandidateSHA256 = "short" }},
		{name: "authority file", mutate: func(m *Manifest) { m.AuthorityFile = "" }},
		{name: "authority hash", mutate: func(m *Manifest) { m.AuthoritySHA256 = "" }},
		{name: "target", mutate: func(m *Manifest) { m.TargetPath = "" }},
		{name: "uid", mutate: func(m *Manifest) { m.EffectiveUID = -1 }},
		{name: "label", mutate: func(m *Manifest) { m.LaunchAgentLabel = "" }},
		{name: "label traversal", mutate: func(m *Manifest) { m.LaunchAgentLabel = "../agent" }},
		{name: "label empty segment", mutate: func(m *Manifest) { m.LaunchAgentLabel = "local..agent" }},
		{name: "plist path", mutate: func(m *Manifest) { m.PlistPath = "" }},
		{name: "plist hash", mutate: func(m *Manifest) { m.PlistSHA256 = "" }},
		{name: "wrapper path", mutate: func(m *Manifest) { m.WrapperPath = "" }},
		{name: "wrapper hash", mutate: func(m *Manifest) { m.WrapperSHA256 = "" }},
		{name: "mcp wrapper path", mutate: func(m *Manifest) { m.MCPWrapperPath = "" }},
		{name: "mcp wrapper hash", mutate: func(m *Manifest) { m.MCPWrapperSHA256 = "" }},
		{name: "stdout", mutate: func(m *Manifest) { m.StdoutPath = "" }},
		{name: "stderr", mutate: func(m *Manifest) { m.StderrPath = "" }},
		{name: "environment path", mutate: func(m *Manifest) { m.EnvironmentPath = "" }},
		{name: "environment hash", mutate: func(m *Manifest) { m.EnvironmentSHA256 = "" }},
		{name: "health", mutate: func(m *Manifest) { m.HealthURLFile = "" }},
		{name: "relative target", mutate: func(m *Manifest) { m.TargetPath = "gateway" }},
		{name: "timeout too large", mutate: func(m *Manifest) { m.ReadyTimeoutSeconds = int(maxReadyTimeout/time.Second) + 1 }},
		{name: "poll too large", mutate: func(m *Manifest) { m.ReadyPollMilliseconds = int(maxReadyPoll/time.Millisecond) + 1 }},
		{name: "ready timeout", mutate: func(m *Manifest) { m.ReadyTimeoutSeconds = 0 }},
		{name: "ready poll", mutate: func(m *Manifest) { m.ReadyPollMilliseconds = 0 }},
		{name: "previous file", mutate: func(m *Manifest) { m.PreviousFile = "" }},
		{name: "previous hash", mutate: func(m *Manifest) { m.PreviousSHA256 = "" }},
		{name: "unexpected previous", mutate: func(m *Manifest) { m.PreviousPresent = false }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := valid
			tt.mutate(&m)
			if err := ValidateSnapshot(Snapshot{Manifest: &m}); err == nil || err.Code != ErrorStateMalformed {
				t.Fatalf("ValidateSnapshot error = %#v, want %q", err, ErrorStateMalformed)
			}
			decision := Decide(Snapshot{Manifest: &m}, EventResume, Context{})
			assertRejected(t, decision, ErrorStateMalformed)
		})
	}
}

func TestPrepareValidatesCandidateAndCurrentTargetBeforePublication(t *testing.T) {
	t.Parallel()
	m := validManifest(StatePrepared, true)
	observed := validObserved(m, testPrevious, false)

	decision := Decide(Snapshot{}, EventPrepare, Context{Prepared: &m, Observed: observed})
	if decision.Err != nil || decision.Next.State() != StatePrepared {
		t.Fatalf("valid prepare = %#v", decision)
	}
	assertCommands(t, decision, CommandPublishPrepared)

	for _, tt := range []struct {
		name   string
		mutate func(*Observed)
		code   ErrorCode
	}{
		{name: "candidate artifact", mutate: func(o *Observed) { o.CandidateSHA256 = testPrevious }, code: ErrorArtifactMismatch},
		{name: "authority artifact", mutate: func(o *Observed) { o.AuthorityArtifactSHA256 = testPrevious }, code: ErrorArtifactMismatch},
		{name: "wrong installed target", mutate: func(o *Observed) { o.InstalledSHA256 = testCandidate }, code: ErrorInstalledMismatch},
		{name: "runtime fingerprint", mutate: func(o *Observed) { o.MCPWrapperSHA256 = testPrevious }, code: ErrorRuntimeDrift},
	} {
		t.Run(tt.name, func(t *testing.T) {
			bad := observed
			tt.mutate(&bad)
			got := Decide(Snapshot{}, EventPrepare, Context{Prepared: &m, Observed: bad})
			assertRejected(t, got, tt.code)
		})
	}
}

func TestDecisionDoesNotAliasInputManifest(t *testing.T) {
	t.Parallel()
	m := validManifest(StatePending, true)
	original := m
	decision := Decide(Snapshot{Manifest: &m}, EventAccept, Context{ReleaseID: m.ID, Observed: validObservedForState(m)})
	if decision.Err != nil {
		t.Fatal(decision.Err)
	}
	decision.Next.Manifest.Commit = "changed"
	if !reflect.DeepEqual(m, original) {
		t.Fatalf("input manifest mutated: got %#v want %#v", m, original)
	}
}

func TestLifecycleErrorsAreFixedAndSanitized(t *testing.T) {
	t.Parallel()
	for _, code := range []ErrorCode{
		ErrorNoPending, ErrorBusy, ErrorIdentityMismatch, ErrorAuthorityMismatch,
		ErrorInstalledMismatch, ErrorStateMalformed, ErrorArtifactMismatch,
		ErrorRuntimeDrift, ErrorStateConflict, ErrorRecoveryUnconfirmed,
	} {
		err := lifecycleError(code)
		if err.Code != code || err.Message == "" {
			t.Fatalf("error for %q = %#v", code, err)
		}
		if strings.ContainsAny(err.Message, "\r\n=") {
			t.Fatalf("message for %q is not a fixed record-safe phrase: %q", code, err.Message)
		}
	}
}

func matrixInputs(state State) (Snapshot, Context) {
	if state == StateClear {
		m := validManifest(StatePrepared, true)
		return Snapshot{}, Context{Prepared: &m, Observed: validObserved(m, testPrevious, false)}
	}
	m := validManifest(state, true)
	return Snapshot{Manifest: &m}, Context{ReleaseID: m.ID, Observed: validObservedForState(m)}
}

func validManifest(state State, previous bool) Manifest {
	m := Manifest{
		Version:               ManifestVersion,
		State:                 state,
		ID:                    testID,
		Commit:                "0123456789abcdef0123456789abcdef01234567",
		CandidateFile:         "candidate",
		CandidateSHA256:       testCandidate,
		AuthorityFile:         "authority",
		AuthoritySHA256:       testAuthority,
		PreviousPresent:       previous,
		TargetPath:            "/private/test/gateway",
		EffectiveUID:          501,
		LaunchAgentLabel:      "dev.personal-mcp-gateway.obsidian",
		PlistPath:             "/private/test/agent.plist",
		PlistSHA256:           testPlist,
		WrapperPath:           "/private/test/run.sh",
		WrapperSHA256:         testWrapper,
		MCPWrapperPath:        "/private/test/run-mcp.sh",
		MCPWrapperSHA256:      testMCPWrapper,
		StdoutPath:            "/private/test/stdout.log",
		StderrPath:            "/private/test/stderr.log",
		EnvironmentPath:       "/private/test/.env.local",
		EnvironmentSHA256:     testEnv,
		HealthURLFile:         "/private/test/health.url",
		ReadyTimeoutSeconds:   30,
		ReadyPollMilliseconds: 250,
	}
	if previous {
		m.PreviousFile = "previous"
		m.PreviousSHA256 = testPrevious
	}
	return m
}

func validObservedForState(m Manifest) Observed {
	switch m.State {
	case StateRollingBack:
		if m.PreviousPresent {
			return validObserved(m, m.PreviousSHA256, true)
		}
		o := validObserved(m, "", false)
		o.InstalledPresent = false
		o.SupervisorUnloaded = true
		return o
	default:
		return validObserved(m, m.CandidateSHA256, true)
	}
}

func validObserved(m Manifest, installed string, ready bool) Observed {
	return Observed{
		CandidateSHA256:         m.CandidateSHA256,
		AuthorityArtifactSHA256: m.AuthoritySHA256,
		ControllerSHA256:        m.AuthoritySHA256,
		PreviousSHA256:          m.PreviousSHA256,
		InstalledPresent:        installed != "",
		InstalledSHA256:         installed,
		PlistSHA256:             m.PlistSHA256,
		WrapperSHA256:           m.WrapperSHA256,
		MCPWrapperSHA256:        m.MCPWrapperSHA256,
		EnvironmentSHA256:       m.EnvironmentSHA256,
		RuntimeReady:            ready,
	}
}

func assertRejected(t *testing.T, decision Decision, code ErrorCode) {
	t.Helper()
	if decision.Err == nil || decision.Err.Code != code {
		t.Fatalf("error = %#v, want code %q (decision %#v)", decision.Err, code, decision)
	}
	if decision.Applied || len(decision.Commands) != 0 {
		t.Fatalf("rejected decision emitted mutation: %#v", decision)
	}
	if !reflect.DeepEqual(decision.Current, decision.Next) {
		t.Fatalf("rejected decision changed snapshot: current=%#v next=%#v", decision.Current, decision.Next)
	}
}

func assertCommands(t *testing.T, decision Decision, kinds ...CommandKind) {
	t.Helper()
	if len(decision.Commands) != len(kinds) {
		t.Fatalf("commands = %#v, want %v", decision.Commands, kinds)
	}
	for i, kind := range kinds {
		if decision.Commands[i].Kind != kind {
			t.Fatalf("command[%d] = %q, want %q", i, decision.Commands[i].Kind, kind)
		}
	}
}
