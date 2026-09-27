package releaseactivation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// Manager is the sole persistence-facing lifecycle interpreter. ControllerPath
// is the executable handling the current request; every active operation binds
// it to the immutable authority recorded by the transaction.
type Manager struct {
	Store            *Store
	Runtime          Runtime
	ControllerPath   string
	ControllerSHA256 string
}

// PrepareRequest contains resolved, non-secret release bindings. Manager owns
// release identity allocation, hashing, prior-target discovery, and manifest
// construction. CandidateSHA256 is the previously validated report-set
// identity; Manager re-observes the bytes under the lifecycle lock before it
// publishes that identity.
type PrepareRequest struct {
	Commit                string
	CandidateSHA256       string
	AuthoritySHA256       string
	DependencySHA256      string
	CandidatePath         string
	AuthorityPath         string
	TargetPath            string
	EffectiveUID          int
	LaunchAgentLabel      string
	PlistPath             string
	WrapperPath           string
	MCPWrapperPath        string
	StdoutPath            string
	StderrPath            string
	EnvironmentPath       string
	HealthURLFile         string
	ReadyTimeoutSeconds   int
	ReadyPollMilliseconds int
	ServiceCandidates     []ServiceCandidate
}

// Status returns the active identity after validating the durable store and
// pinned authority. Runtime/configuration drift deliberately does not hide the
// actionable release identity; terminal mutations enforce those fingerprints.
func (m *Manager) Status(_ context.Context) (*Manifest, error) {
	locked, err := m.acquire()
	if err != nil {
		return nil, err
	}
	defer locked.Close()

	manifest, err := locked.Load()
	if err != nil || manifest == nil {
		return manifest, err
	}
	if err := m.validateCurrentAuthority(*manifest); err != nil {
		return manifest, err
	}
	return manifest, nil
}

// Prepare atomically publishes immutable recovery authority before any target
// mutation. It does not install or restart the candidate; Resume owns that
// reconciliation.
func (m *Manager) Prepare(ctx context.Context, request PrepareRequest) (*Manifest, error) {
	locked, err := m.acquire()
	if err != nil {
		return nil, err
	}
	defer locked.Close()

	current, err := locked.Load()
	if err != nil {
		return nil, err
	}
	if current != nil {
		return nil, lifecycleError(ErrorStateConflict)
	}
	if request.EffectiveUID != m.Store.effectiveUID || !ValidLaunchAgentLabel(request.LaunchAgentLabel) {
		return nil, lifecycleError(ErrorStateMalformed)
	}
	if !validCommit(request.Commit) || !validSHA256(request.CandidateSHA256) || !validSHA256(request.AuthoritySHA256) || !validSHA256(request.DependencySHA256) {
		return nil, lifecycleError(ErrorStateMalformed)
	}
	if m.ControllerSHA256 != request.AuthoritySHA256 {
		return nil, lifecycleError(ErrorAuthorityMismatch)
	}
	sources := ArtifactSources{Candidate: request.CandidatePath, Authority: request.AuthorityPath}
	previousPresent, err := pathExists(request.TargetPath)
	if err != nil {
		return nil, err
	}
	if previousPresent {
		sources.Previous = request.TargetPath
	}
	if err := ValidateSourceTopology(m.Store, sources); err != nil {
		return nil, err
	}

	id, err := NewReleaseID()
	if err != nil {
		return nil, err
	}
	candidateHash, err := HashRegular(request.CandidatePath)
	if err != nil {
		return nil, err
	}
	if candidateHash != request.CandidateSHA256 {
		return nil, lifecycleError(ErrorArtifactMismatch)
	}
	authorityHash, err := HashRegular(request.AuthorityPath)
	if err != nil {
		return nil, err
	}
	if authorityHash != request.AuthoritySHA256 {
		return nil, lifecycleError(ErrorAuthorityMismatch)
	}
	plistHash, err := HashRegular(request.PlistPath)
	if err != nil {
		return nil, err
	}
	wrapperHash, err := HashRegular(request.WrapperPath)
	if err != nil {
		return nil, err
	}
	mcpWrapperHash, err := HashRegular(request.MCPWrapperPath)
	if err != nil {
		return nil, err
	}
	environmentHash, err := HashRegular(request.EnvironmentPath)
	if err != nil {
		return nil, err
	}
	previousHash := ""
	if previousPresent {
		previousHash, err = HashRegular(request.TargetPath)
		if err != nil {
			return nil, err
		}
	}
	manifest := Manifest{
		Version: ManifestVersion, State: StatePrepared, ID: id, Commit: request.Commit,
		DependencySHA256: request.DependencySHA256,
		CandidateFile:    candidateFileName, CandidateSHA256: candidateHash,
		AuthorityFile: authorityFileName, AuthoritySHA256: request.AuthoritySHA256,
		PreviousPresent: previousPresent, PreviousSHA256: previousHash,
		TargetPath: request.TargetPath, EffectiveUID: request.EffectiveUID,
		LaunchAgentLabel: request.LaunchAgentLabel,
		PlistPath:        request.PlistPath, PlistSHA256: plistHash,
		WrapperPath: request.WrapperPath, WrapperSHA256: wrapperHash,
		MCPWrapperPath: request.MCPWrapperPath, MCPWrapperSHA256: mcpWrapperHash,
		StdoutPath: request.StdoutPath, StderrPath: request.StderrPath,
		EnvironmentPath: request.EnvironmentPath, EnvironmentSHA256: environmentHash,
		HealthURLFile:         request.HealthURLFile,
		ReadyTimeoutSeconds:   request.ReadyTimeoutSeconds,
		ReadyPollMilliseconds: request.ReadyPollMilliseconds,
	}
	// New clear-state transactions always record the exact supervised set. The
	// legacy request fields remain the Obsidian descriptor so pinned v2
	// controllers and their manifests can finish unchanged.
	manifest.Services = []ServiceDescriptor{{
		Server: "obsidian", LaunchAgentLabel: manifest.LaunchAgentLabel,
		PlistPath: manifest.PlistPath, PlistSHA256: manifest.PlistSHA256,
		WrapperPath: manifest.WrapperPath, WrapperSHA256: manifest.WrapperSHA256,
		MCPWrapperPath: manifest.MCPWrapperPath, MCPWrapperSHA256: manifest.MCPWrapperSHA256,
		StdoutPath: manifest.StdoutPath, StderrPath: manifest.StderrPath,
		EnvironmentPath: manifest.EnvironmentPath, EnvironmentSHA256: manifest.EnvironmentSHA256,
		HealthURLFile: manifest.HealthURLFile,
	}}
	for _, candidate := range request.ServiceCandidates {
		descriptor, include, serviceErr := m.captureService(ctx, manifest, candidate)
		if serviceErr != nil {
			return nil, serviceErr
		}
		if include {
			manifest.Services = append(manifest.Services, descriptor)
		}
	}
	if previousPresent {
		manifest.PreviousFile = previousFileName
	}
	if err := ValidatePrepareTopology(m.Store, sources, &manifest); err != nil {
		return nil, err
	}

	artifacts := RuntimeArtifacts{Candidate: request.CandidatePath, Authority: request.AuthorityPath}
	if previousPresent {
		artifacts.Previous = request.TargetPath
	}
	if err := locked.CleanupOrphans(); err != nil {
		return nil, err
	}
	observed, err := m.Runtime.Observe(ctx, manifest, m.ControllerPath, artifacts)
	if err != nil {
		return nil, err
	}
	decision := Decide(Snapshot{}, EventPrepare, Context{Prepared: &manifest, Observed: observed})
	if decision.Err != nil {
		return nil, decision.Err
	}
	prepared, err := locked.Prepare(manifest, sources)
	if err != nil {
		return nil, err
	}
	// Store.Prepare compares the source bytes with the Manager-selected hashes
	// before publishing. Keep this defensive check at the ownership boundary so
	// a future store implementation cannot silently weaken that contract.
	if prepared.CandidateSHA256 != candidateHash || prepared.AuthoritySHA256 != request.AuthoritySHA256 ||
		prepared.PreviousSHA256 != previousHash {
		return nil, lifecycleError(ErrorArtifactMismatch)
	}
	return prepared, nil
}

// Resume reconciles the current prepared deployment. An empty ID means the
// identity loaded under the lock; this is reserved for the public make release
// prepared-resume path.
func (m *Manager) Resume(ctx context.Context, id ReleaseID) (*Manifest, error) {
	return m.run(ctx, EventResume, id, true)
}

// Accept irreversibly chooses the exact pending candidate and then clears the
// recovery transaction. A full, non-empty release ID is mandatory.
func (m *Manager) Accept(ctx context.Context, id ReleaseID) (*Manifest, error) {
	if id == "" {
		return nil, lifecycleError(ErrorIdentityMismatch)
	}
	return m.run(ctx, EventAccept, id, false)
}

// Rollback irreversibly chooses recovery for the exact release, proves the
// recovered runtime (or first-install removal), and only then clears state.
func (m *Manager) Rollback(ctx context.Context, id ReleaseID) (*Manifest, error) {
	if id == "" {
		return nil, lifecycleError(ErrorIdentityMismatch)
	}
	return m.run(ctx, EventRollback, id, false)
}

// WithClear serializes an administrative effect with release transitions and
// runs it only while no recovery transaction exists.
func (m *Manager) WithClear(ctx context.Context, effect func(context.Context, Runtime) error) error {
	locked, err := m.acquire()
	if err != nil {
		return err
	}
	defer locked.Close()
	manifest, err := locked.Load()
	if err != nil {
		return err
	}
	if manifest != nil {
		return lifecycleError(ErrorStateConflict)
	}
	if effect == nil {
		return lifecycleError(ErrorStateConflict)
	}
	if err := locked.CleanupOrphans(); err != nil {
		return err
	}
	return effect(ctx, m.Runtime)
}

// Probe runs a read-only administrative effect under the same advisory lock
// as every other transition, but without WithClear's clear-state requirement
// or orphan cleanup. verify-live must succeed while a release is pending
// (docs/runbooks/local-release.md requires checking liveness before accept),
// and it never mutates recovery bookkeeping, so it does not need either
// WithClear guard.
func (m *Manager) Probe(ctx context.Context, effect func(context.Context, Runtime) error) error {
	locked, err := m.acquire()
	if err != nil {
		return err
	}
	defer locked.Close()
	if effect == nil {
		return lifecycleError(ErrorStateConflict)
	}
	return effect(ctx, m.Runtime)
}

func (m *Manager) run(ctx context.Context, event Event, id ReleaseID, allowCurrentID bool) (*Manifest, error) {
	locked, err := m.acquire()
	if err != nil {
		return nil, err
	}
	defer locked.Close()

	manifest, err := locked.Load()
	if err != nil {
		return nil, err
	}
	if manifest == nil {
		return nil, lifecycleError(ErrorNoPending)
	}
	if err := m.validateCurrentAuthority(*manifest); err != nil {
		return manifest, err
	}
	if allowCurrentID && id == "" && event == EventResume && manifest.State == StatePrepared {
		id = manifest.ID
	}
	if err := validateRequestedEvent(*manifest, event, id); err != nil {
		return manifest, err
	}
	if err := locked.CleanupOrphans(); err != nil {
		return manifest, err
	}

	observed, err := m.observe(ctx, *manifest)
	if err != nil {
		return manifest, err
	}
	decision := Decide(Snapshot{Manifest: manifest}, event, Context{ReleaseID: id, Observed: observed})
	if decision.Err != nil || len(decision.Commands) != 1 {
		if decision.Err != nil {
			return manifest, decision.Err
		}
		return manifest, lifecycleError(ErrorStateConflict)
	}

	switch decision.Commands[0].Kind {
	case CommandResumeDeployment:
		if err := m.resumeDeployment(ctx, locked, manifest, observed); err != nil {
			return manifest, err
		}
		fresh, err := m.observe(ctx, *manifest)
		if err != nil {
			return manifest, m.rollbackDeploymentFailure(ctx, locked, manifest, err)
		}
		ready := Decide(Snapshot{Manifest: manifest}, EventDeploymentReady, Context{ReleaseID: id, Observed: fresh})
		if ready.Err != nil || len(ready.Commands) != 1 || ready.Commands[0].Kind != CommandPersistState || ready.Next.Manifest == nil {
			cause := error(lifecycleError(ErrorRecoveryUnconfirmed))
			if ready.Err != nil {
				cause = ready.Err
			}
			return manifest, m.rollbackDeploymentFailure(ctx, locked, manifest, cause)
		}
		if err := locked.Rewrite(*ready.Next.Manifest); err != nil {
			return manifest, err
		}
		return ready.Next.Manifest, nil

	case CommandPersistState:
		if decision.Next.Manifest == nil {
			return manifest, lifecycleError(ErrorStateMalformed)
		}
		if err := locked.Rewrite(*decision.Next.Manifest); err != nil {
			return manifest, err
		}
		manifest = decision.Next.Manifest
		if manifest.State == StateRollingBack {
			return m.finishRollback(ctx, locked, manifest, observed)
		}
		if manifest.State == StateAccepting {
			return m.finishAccept(ctx, locked, manifest, id)
		}
		return manifest, lifecycleError(ErrorStateConflict)

	case CommandResumeRollback:
		return m.finishRollback(ctx, locked, manifest, observed)

	case CommandClearTransaction:
		if _, err := locked.Clear(manifest.ID); err != nil {
			return manifest, err
		}
		return nil, nil

	default:
		return manifest, lifecycleError(ErrorStateConflict)
	}
}

func (m *Manager) resumeDeployment(ctx context.Context, locked *LockedStore, manifest *Manifest, observed Observed) error {
	if !observed.InstalledPresent || observed.InstalledSHA256 != manifest.CandidateSHA256 {
		if err := m.Runtime.InstallCandidate(ctx, *manifest, m.artifacts()); err != nil {
			return m.rollbackDeploymentFailure(ctx, locked, manifest, err)
		}
		var err error
		observed, err = m.observe(ctx, *manifest)
		if err != nil || !observed.InstalledPresent || observed.InstalledSHA256 != manifest.CandidateSHA256 {
			if err == nil {
				err = lifecycleError(ErrorInstalledMismatch)
			}
			return m.rollbackDeploymentFailure(ctx, locked, manifest, err)
		}
	}
	for _, service := range serviceManifests(*manifest) {
		if err := m.Runtime.Restart(ctx, service); err != nil {
			return m.rollbackDeploymentFailure(ctx, locked, manifest, err)
		}
		if err := m.Runtime.WaitReady(ctx, service); err != nil {
			return m.rollbackDeploymentFailure(ctx, locked, manifest, err)
		}
	}
	return nil
}

func (m *Manager) rollbackDeploymentFailure(ctx context.Context, locked *LockedStore, manifest *Manifest, cause error) error {
	observed, err := m.observe(ctx, *manifest)
	if err != nil {
		return lifecycleError(ErrorRecoveryUnconfirmed)
	}
	decision := Decide(Snapshot{Manifest: manifest}, EventRollback, Context{ReleaseID: manifest.ID, Observed: observed})
	if decision.Err != nil || decision.Next.Manifest == nil {
		return lifecycleError(ErrorRecoveryUnconfirmed)
	}
	if err := locked.Rewrite(*decision.Next.Manifest); err != nil {
		return err
	}
	if _, err := m.finishRollback(ctx, locked, decision.Next.Manifest, observed); err != nil {
		return lifecycleError(ErrorRecoveryUnconfirmed)
	}
	return rolledBackFailure(causeOperation(cause))
}

func (m *Manager) finishAccept(ctx context.Context, locked *LockedStore, manifest *Manifest, id ReleaseID) (*Manifest, error) {
	fresh, err := m.observe(ctx, *manifest)
	if err != nil {
		return manifest, err
	}
	decision := Decide(Snapshot{Manifest: manifest}, EventAccept, Context{ReleaseID: id, Observed: fresh})
	if decision.Err != nil || len(decision.Commands) != 1 || decision.Commands[0].Kind != CommandClearTransaction {
		if decision.Err != nil {
			return manifest, decision.Err
		}
		return manifest, lifecycleError(ErrorRecoveryUnconfirmed)
	}
	if _, err := locked.Clear(manifest.ID); err != nil {
		return manifest, err
	}
	return nil, nil
}

func (m *Manager) finishRollback(ctx context.Context, locked *LockedStore, manifest *Manifest, observed Observed) (*Manifest, error) {
	decision := Decide(Snapshot{Manifest: manifest}, EventRollback, Context{ReleaseID: manifest.ID, Observed: observed})
	if decision.Err != nil || len(decision.Commands) != 1 {
		return manifest, lifecycleError(ErrorRecoveryUnconfirmed)
	}
	if decision.Commands[0].Kind == CommandClearTransaction {
		if _, err := locked.Clear(manifest.ID); err != nil {
			return manifest, err
		}
		return nil, nil
	}
	if decision.Commands[0].Kind != CommandResumeRollback {
		return manifest, lifecycleError(ErrorRecoveryUnconfirmed)
	}

	rollbackReady, err := m.resumeRollback(ctx, *manifest, observed)
	if err != nil {
		return manifest, lifecycleError(ErrorRecoveryUnconfirmed)
	}
	fresh, err := m.observe(ctx, *manifest)
	if err != nil {
		return manifest, lifecycleError(ErrorRecoveryUnconfirmed)
	}
	decision = Decide(Snapshot{Manifest: manifest}, EventRollback, Context{
		ReleaseID: manifest.ID, Observed: fresh, rollbackReady: rollbackReady,
	})
	if decision.Err != nil || len(decision.Commands) != 1 || decision.Commands[0].Kind != CommandClearTransaction {
		return manifest, lifecycleError(ErrorRecoveryUnconfirmed)
	}
	if _, err := locked.Clear(manifest.ID); err != nil {
		return manifest, err
	}
	return nil, nil
}

func (m *Manager) resumeRollback(ctx context.Context, manifest Manifest, observed Observed) (bool, error) {
	if manifest.PreviousPresent {
		if !observed.InstalledPresent || observed.InstalledSHA256 != manifest.PreviousSHA256 {
			if err := m.Runtime.RestorePrevious(ctx, manifest, m.artifacts()); err != nil {
				return false, err
			}
		}
		for _, service := range serviceManifests(manifest) {
			if err := m.Runtime.Restart(ctx, service); err != nil {
				return false, err
			}
			if err := m.Runtime.WaitReady(ctx, service); err != nil {
				return false, err
			}
		}
		return true, nil
	}

	for _, service := range serviceManifests(manifest) {
		if err := m.Runtime.Bootout(ctx, service); err != nil {
			return false, err
		}
		unloaded, err := m.Runtime.ConfirmUnloaded(ctx, service)
		if err != nil || !unloaded {
			return false, lifecycleError(ErrorRecoveryUnconfirmed)
		}
	}
	if observed.InstalledPresent {
		if err := m.Runtime.RemoveTarget(ctx, manifest); err != nil {
			return false, err
		}
	}
	return false, nil
}

func (m *Manager) validateCurrentAuthority(manifest Manifest) error {
	if m.ControllerSHA256 != manifest.AuthoritySHA256 {
		return lifecycleError(ErrorAuthorityMismatch)
	}
	controllerHash, err := hashRuntimeFile(m.ControllerPath, true)
	if err != nil || controllerHash != m.ControllerSHA256 {
		return lifecycleError(ErrorAuthorityMismatch)
	}
	return nil
}

func validateRequestedEvent(manifest Manifest, event Event, id ReleaseID) error {
	if !eventAllowed(manifest.State, event) || event == EventDeploymentReady || event == EventPrepare {
		return lifecycleError(ErrorStateConflict)
	}
	if id != manifest.ID {
		return lifecycleError(ErrorIdentityMismatch)
	}
	return nil
}

func (m *Manager) observe(ctx context.Context, manifest Manifest) (Observed, error) {
	observed, err := m.Runtime.Observe(ctx, manifest, m.ControllerPath, m.artifacts())
	if err != nil || len(manifest.Services) < 2 {
		return observed, err
	}
	for _, service := range serviceManifests(manifest)[1:] {
		serviceObserved, serviceErr := m.Runtime.Observe(ctx, service, m.ControllerPath, m.artifacts())
		if serviceErr != nil {
			return Observed{}, serviceErr
		}
		if serviceObserved.PlistSHA256 != service.PlistSHA256 || serviceObserved.WrapperSHA256 != service.WrapperSHA256 ||
			serviceObserved.MCPWrapperSHA256 != service.MCPWrapperSHA256 || serviceObserved.EnvironmentSHA256 != service.EnvironmentSHA256 {
			return Observed{}, lifecycleError(ErrorRuntimeDrift)
		}
		observed.RuntimeReady = observed.RuntimeReady && serviceObserved.RuntimeReady
		observed.SupervisorUnloaded = observed.SupervisorUnloaded && serviceObserved.SupervisorUnloaded
	}
	return observed, nil
}

func serviceManifests(manifest Manifest) []Manifest {
	if len(manifest.Services) == 0 {
		return []Manifest{manifest}
	}
	services := make([]Manifest, 0, len(manifest.Services))
	for _, descriptor := range manifest.Services {
		service := manifest
		service.LaunchAgentLabel, service.PlistPath, service.PlistSHA256 = descriptor.LaunchAgentLabel, descriptor.PlistPath, descriptor.PlistSHA256
		service.WrapperPath, service.WrapperSHA256 = descriptor.WrapperPath, descriptor.WrapperSHA256
		service.MCPWrapperPath, service.MCPWrapperSHA256 = descriptor.MCPWrapperPath, descriptor.MCPWrapperSHA256
		service.StdoutPath, service.StderrPath = descriptor.StdoutPath, descriptor.StderrPath
		service.EnvironmentPath, service.EnvironmentSHA256, service.HealthURLFile = descriptor.EnvironmentPath, descriptor.EnvironmentSHA256, descriptor.HealthURLFile
		services = append(services, service)
	}
	return services
}

func (m *Manager) artifacts() RuntimeArtifacts {
	return RuntimeArtifacts{
		Candidate: m.Store.ActiveCandidatePath(),
		Authority: m.Store.ActiveAuthorityPath(),
		Previous:  m.Store.ActivePreviousPath(),
	}
}

// captureService keeps service discovery inside Prepare's lifecycle lock. A
// loaded LaunchAgent is included even if its child has crashed; a missing job
// is deliberately not installed as a side effect of release.
func (m *Manager) captureService(ctx context.Context, base Manifest, candidate ServiceCandidate) (ServiceDescriptor, bool, error) {
	// ynab is a tunnel service: it runs behind a second-level MCP stdio wrapper
	// and must record it. obsidian-http/ynab-http exec the gateway binary
	// directly over loopback HTTP with no such wrapper, so their candidate must
	// leave MCPWrapperPath empty rather than alias the process wrapper.
	tunnel := candidate.Server == "ynab"
	http := candidate.Server == "obsidian-http" || candidate.Server == "ynab-http"
	if (!tunnel && !http) || !ValidLaunchAgentLabel(candidate.LaunchAgentLabel) ||
		!absolutePaths(candidate.PlistPath, candidate.WrapperPath, candidate.StdoutPath, candidate.StderrPath, candidate.EnvironmentPath, candidate.HealthURLFile) ||
		(tunnel && !filepath.IsAbs(candidate.MCPWrapperPath)) || (http && candidate.MCPWrapperPath != "") {
		return ServiceDescriptor{}, false, lifecycleError(ErrorStateMalformed)
	}
	probe := base
	probe.LaunchAgentLabel, probe.PlistPath, probe.WrapperPath, probe.MCPWrapperPath = candidate.LaunchAgentLabel, candidate.PlistPath, candidate.WrapperPath, candidate.MCPWrapperPath
	loaded, err := m.Runtime.ServiceLoaded(ctx, probe)
	if err != nil {
		return ServiceDescriptor{}, false, err
	}
	if !loaded {
		return ServiceDescriptor{}, false, nil
	}
	if err := validateServiceGatewayBinding(candidate.EnvironmentPath, candidate.PlistPath, base.TargetPath); err != nil {
		return ServiceDescriptor{}, false, err
	}
	descriptor := ServiceDescriptor{Server: candidate.Server, LaunchAgentLabel: candidate.LaunchAgentLabel,
		PlistPath: candidate.PlistPath, WrapperPath: candidate.WrapperPath, MCPWrapperPath: candidate.MCPWrapperPath,
		StdoutPath: candidate.StdoutPath, StderrPath: candidate.StderrPath, EnvironmentPath: candidate.EnvironmentPath, HealthURLFile: candidate.HealthURLFile}
	var hashErr error
	if descriptor.PlistSHA256, hashErr = HashRegular(descriptor.PlistPath); hashErr != nil {
		return ServiceDescriptor{}, false, hashErr
	}
	if descriptor.WrapperSHA256, hashErr = HashRegular(descriptor.WrapperPath); hashErr != nil {
		return ServiceDescriptor{}, false, hashErr
	}
	if tunnel {
		if descriptor.MCPWrapperSHA256, hashErr = HashRegular(descriptor.MCPWrapperPath); hashErr != nil {
			return ServiceDescriptor{}, false, hashErr
		}
	}
	if descriptor.EnvironmentSHA256, hashErr = HashRegular(descriptor.EnvironmentPath); hashErr != nil {
		return ServiceDescriptor{}, false, hashErr
	}
	return descriptor, true, nil
}

func validateServiceGatewayBinding(environmentPath, plistPath, targetPath string) error {
	data, err := os.ReadFile(environmentPath)
	if err != nil || len(data) > 65536 {
		return lifecycleError(ErrorStateMalformed)
	}
	home := strings.TrimSuffix(filepath.Dir(filepath.Dir(plistPath)), "/Library")
	if home == "" || !filepath.IsAbs(home) {
		return lifecycleError(ErrorStateMalformed)
	}
	value := ""
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSuffix(raw, "\r")
		if len(line) > 4096 || line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, rawValue, ok := strings.Cut(line, "=")
		if !ok || key == "" {
			return lifecycleError(ErrorStateMalformed)
		}
		if key != "GATEWAY_BIN" {
			continue
		}
		if value != "" {
			return lifecycleError(ErrorStateMalformed)
		}
		expanded, ok := parseGatewayBinValue(rawValue, home)
		if !ok {
			return lifecycleError(ErrorStateMalformed)
		}
		value = expanded
	}
	if value == "" || value != filepath.Clean(targetPath) {
		return lifecycleError(ErrorStateMalformed)
	}
	return nil
}

// parseGatewayBinValue mirrors scripts/internal/release-config.sh's
// load_release_config handling of one already-cut-at-"=" GATEWAY_BIN value:
// it strips one matching pair of quotes and expands a leading $HOME/${HOME}
// for a double-quoted or bare value only. A single-quoted value is bash
// literal text with no expansion at all (release_config_expand_home_prefix is
// never called for it in the shell loader), so it must stay literal here too.
// The shell loader's disallowed-metacharacter set differs by quoting mode
// (see load_release_config), so each mode is checked against exactly the
// shell's set for that mode rather than one shared set for all three:
//   - single-quoted: only an embedded "'" is disallowed (backtick, backslash,
//     and '"' are ordinary characters inside single quotes in the shell).
//   - double-quoted: an embedded "`", "\", or '"' is disallowed; an embedded
//     "'" is allowed.
//   - bare (unquoted): an embedded "`", "\", '"', "'", "#", or whitespace is
//     disallowed.
func parseGatewayBinValue(rawValue, home string) (string, bool) {
	singleQuoted := len(rawValue) >= 2 && rawValue[0] == '\'' && rawValue[len(rawValue)-1] == '\''
	doubleQuoted := len(rawValue) >= 2 && rawValue[0] == '"' && rawValue[len(rawValue)-1] == '"'
	if singleQuoted || doubleQuoted {
		rawValue = rawValue[1 : len(rawValue)-1]
	}
	switch {
	case singleQuoted:
		if strings.Contains(rawValue, "'") {
			return "", false
		}
	case doubleQuoted:
		if strings.ContainsAny(rawValue, "`\\\"") {
			return "", false
		}
	default:
		if strings.ContainsAny(rawValue, "`\\\"'#") || strings.ContainsFunc(rawValue, unicode.IsSpace) {
			return "", false
		}
	}
	expanded := rawValue
	if !singleQuoted {
		var ok bool
		expanded, ok = expandHomePrefix(rawValue, home)
		if !ok {
			return "", false
		}
	}
	if !filepath.IsAbs(expanded) {
		return "", false
	}
	return filepath.Clean(expanded), true
}

// expandHomePrefix mirrors scripts/internal/release-config.sh's
// release_config_expand_home_prefix exactly: only a leading $HOME or ${HOME}
// expands, and any other "$" anywhere in the value is rejected rather than
// substituted. The shell loader and this parser must accept and reject the
// same values for GATEWAY_BIN.
func expandHomePrefix(value, home string) (string, bool) {
	switch {
	case value == "$HOME":
		return home, true
	case strings.HasPrefix(value, "$HOME/"):
		return home + "/" + strings.TrimPrefix(value, "$HOME/"), true
	case value == "${HOME}":
		return home, true
	case strings.HasPrefix(value, "${HOME}/"):
		return home + "/" + strings.TrimPrefix(value, "${HOME}/"), true
	case strings.Contains(value, "$"):
		return "", false
	default:
		return value, true
	}
}

func (m *Manager) acquire() (*LockedStore, error) {
	if m == nil || m.Store == nil || m.Runtime == nil || m.ControllerPath == "" || !validSHA256(m.ControllerSHA256) {
		return nil, lifecycleError(ErrorStateMalformed)
	}
	locked, err := m.Store.Acquire()
	if errors.Is(err, ErrBusy) {
		return nil, lifecycleError(ErrorBusy)
	}
	return locked, err
}

func pathExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// rollbackRecoveredError marks a deployment failure whose rollback was
// confirmed. operation is the fixed, non-sensitive step that failed; it never
// carries the underlying cause so SanitizedError cannot leak it.
type rollbackRecoveredError struct {
	operation string
}

func (e *rollbackRecoveredError) Error() string {
	return "release " + e.operation + " failed; rollback confirmed"
}

func rolledBackFailure(operation string) error {
	return &rollbackRecoveredError{operation: operation}
}

// causeOperation extracts the fixed operation string a runtimeError carries,
// falling back to a generic step name when cause is not one (for example a
// lifecycle decision rejection observed while rolling back).
func causeOperation(cause error) string {
	var re *runtimeError
	if errors.As(cause, &re) {
		return re.operation
	}
	return "deployment"
}

// causeClass reduces a host-effect cause to one of a fixed set of coarse,
// non-sensitive classes. It never returns child stdout/stderr, paths, or
// environment data.
func causeClass(cause error) string {
	if errors.Is(cause, context.DeadlineExceeded) {
		return "timeout"
	}
	var exit *exitStatusError
	if errors.As(cause, &exit) {
		return fmt.Sprintf("exit_status=%d", exit.code)
	}
	return "failed"
}

// SanitizedError maps internal/store/runtime failures to the stable public
// taxonomy without preserving causes, paths, child output, or environment data.
func SanitizedError(err error) *Error {
	if err == nil {
		return nil
	}
	var lifecycle *Error
	if errors.As(err, &lifecycle) {
		return lifecycleError(lifecycle.Code)
	}
	var recovered *rollbackRecoveredError
	if errors.As(err, &recovered) {
		return &Error{Code: ErrorRolledBack, Message: fmt.Sprintf("candidate failed at %s; previous runtime restored", recovered.operation)}
	}
	var hostEffect *runtimeError
	if errors.As(err, &hostEffect) {
		return &Error{Code: ErrorHostEffectFailed, Message: fmt.Sprintf("%s: %s", hostEffect.operation, causeClass(hostEffect.cause))}
	}
	var topology *PathTopologyError
	switch {
	case errors.Is(err, ErrBusy):
		return lifecycleError(ErrorBusy)
	case errors.Is(err, ErrArtifactMismatch):
		return lifecycleError(ErrorArtifactMismatch)
	case errors.Is(err, ErrStateConflict):
		return lifecycleError(ErrorStateConflict)
	case errors.Is(err, ErrStateMalformed):
		return lifecycleError(ErrorStateMalformed)
	case errors.Is(err, ErrUpdateCheckFailed):
		return lifecycleError(ErrorUpdateFailed)
	case errors.As(err, &topology):
		return lifecycleError(ErrorStateMalformed)
	default:
		return lifecycleError(ErrorRecoveryUnconfirmed)
	}
}

// HostEffectFailure sanitizes a private host-effect adapter's cause into the
// stable host_effect_failed record. operation must be a fixed, non-sensitive
// string; cause classification never surfaces child output, paths, or
// environment data. Command adapters outside this package (for example
// cmd/release-activation) use this to report launchctl/adapter failures
// instead of collapsing them into recovery_unconfirmed.
func HostEffectFailure(operation string, cause error) error {
	return runtimeFailure(operation, cause)
}
