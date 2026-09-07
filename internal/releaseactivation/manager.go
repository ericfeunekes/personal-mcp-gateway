package releaseactivation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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
	return SanitizedError(cause)
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

type serviceLoadedRuntime interface {
	ServiceLoaded(context.Context, Manifest) (bool, error)
}

// captureService keeps service discovery inside Prepare's lifecycle lock. A
// loaded LaunchAgent is included even if its child has crashed; a missing job
// is deliberately not installed as a side effect of release.
func (m *Manager) captureService(ctx context.Context, base Manifest, candidate ServiceCandidate) (ServiceDescriptor, bool, error) {
	if candidate.Server != "ynab" || !ValidLaunchAgentLabel(candidate.LaunchAgentLabel) ||
		!absolutePaths(candidate.PlistPath, candidate.WrapperPath, candidate.MCPWrapperPath, candidate.StdoutPath, candidate.StderrPath, candidate.EnvironmentPath, candidate.HealthURLFile) {
		return ServiceDescriptor{}, false, lifecycleError(ErrorStateMalformed)
	}
	probe := base
	probe.LaunchAgentLabel, probe.PlistPath, probe.WrapperPath, probe.MCPWrapperPath = candidate.LaunchAgentLabel, candidate.PlistPath, candidate.WrapperPath, candidate.MCPWrapperPath
	loadedRuntime, ok := m.Runtime.(serviceLoadedRuntime)
	if !ok {
		// Isolated lifecycle fakes that predate the optional second service have
		// no launchd surface; they model the service as absent.
		return ServiceDescriptor{}, false, nil
	}
	loaded, err := loadedRuntime.ServiceLoaded(ctx, probe)
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
	if descriptor.MCPWrapperSHA256, hashErr = HashRegular(descriptor.MCPWrapperPath); hashErr != nil {
		return ServiceDescriptor{}, false, hashErr
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
		if len(rawValue) >= 2 && ((rawValue[0] == '"' && rawValue[len(rawValue)-1] == '"') || (rawValue[0] == '\'' && rawValue[len(rawValue)-1] == '\'')) {
			rawValue = rawValue[1 : len(rawValue)-1]
		}
		if strings.ContainsAny(rawValue, "`\\\"'") {
			return lifecycleError(ErrorStateMalformed)
		}
		rawValue = strings.Replace(rawValue, "$HOME", home, 1)
		if strings.Contains(rawValue, "$") || !filepath.IsAbs(rawValue) {
			return lifecycleError(ErrorStateMalformed)
		}
		value = filepath.Clean(rawValue)
	}
	if value == "" || value != filepath.Clean(targetPath) {
		return lifecycleError(ErrorStateMalformed)
	}
	return nil
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
	case errors.As(err, &topology):
		return lifecycleError(ErrorStateMalformed)
	default:
		return lifecycleError(ErrorRecoveryUnconfirmed)
	}
}
