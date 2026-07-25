//go:build darwin

package fsx

import (
	"context"
	"errors"
	"os"
	"time"
)

func (m *Mutator) CreateFile(ctx context.Context, base, input string, payload []byte) (MutationResult, error) {
	if m == nil || m.vault == nil {
		return MutationResult{}, &Error{Code: CodePathDenied}
	}
	if len(payload) > mutationMaxPayloadBytes {
		return MutationResult{}, &Error{Code: CodeInputTooLarge}
	}
	if end := m.vault.activity.Begin(); end != nil {
		defer end()
	}
	root, err := m.vault.openRoot(ctx)
	if err != nil {
		return MutationResult{}, err
	}
	defer root.Close()
	if hooks := m.vault.testHooks; hooks != nil && hooks.afterMutationRoot != nil {
		hooks.afterMutationRoot()
	}
	rootForParent, err := cloneMutationRoot(root)
	if err != nil {
		return MutationResult{}, err
	}
	parent, leaf, parentRel, err := m.vault.prepareMutationParentFromRoot(ctx, rootForParent, base, input)
	if err != nil {
		return MutationResult{}, err
	}
	defer parent.Close()
	match, err := matchMutationName(ctx, int(parent.file.Fd()), leaf)
	if err != nil {
		return MutationResult{}, err
	}
	if match.state != mutationNameAbsent {
		return MutationResult{}, &Error{Code: CodeDestinationExists}
	}
	executable, err := m.mutationExecutable()
	if err != nil {
		return MutationResult{}, err
	}
	result, err := runStagedMutation(ctx, stagedMutation{Executable: executable, Operation: mutationOperationCreate, Root: root.file, Parent: parent.file, TargetLeaf: leaf, Payload: payload})
	if err != nil {
		return MutationResult{}, mapStagedMutationError(err)
	}
	if err := mapStagedTerminal(result.Terminal); err != nil {
		return MutationResult{}, err
	}
	identity := mutationIdentityFromProtocol(result.StageIdentity)
	rel := joinRel(parentRel, leaf)
	resolved := Resolved{Rel: rel, Exists: true, Kind: KindFile, Size: identity.size, Modified: mutationTime(identity)}
	return MutationResult{Resolved: resolved, Fingerprint: mutationFingerprint(rel, KindFile, identity)}, nil
}

func (m *Mutator) ReplaceFile(ctx context.Context, base, input string, expected SourceFingerprint, payload []byte) (MutationResult, error) {
	if m == nil || m.vault == nil {
		return MutationResult{}, &Error{Code: CodePathDenied}
	}
	if len(payload) > mutationMaxPayloadBytes {
		return MutationResult{}, &Error{Code: CodeInputTooLarge}
	}
	if end := m.vault.activity.Begin(); end != nil {
		defer end()
	}
	root, err := m.vault.openRoot(ctx)
	if err != nil {
		return MutationResult{}, err
	}
	defer root.Close()
	if hooks := m.vault.testHooks; hooks != nil && hooks.afterMutationRoot != nil {
		hooks.afterMutationRoot()
	}
	rootForTarget, err := cloneMutationRoot(root)
	if err != nil {
		return MutationResult{}, err
	}
	prepared, err := m.vault.prepareMutationTargetFromRoot(ctx, rootForTarget, base, input)
	if err != nil {
		return MutationResult{}, err
	}
	defer prepared.close()
	if prepared.resolved.Kind != KindFile {
		return MutationResult{}, &Error{Code: CodeNotFile}
	}
	if prepared.fingerprint != expected {
		return MutationResult{}, &Error{Code: CodeSourceChanged}
	}
	executable, err := m.mutationExecutable()
	if err != nil {
		return MutationResult{}, err
	}
	result, err := runStagedMutation(ctx, stagedMutation{Executable: executable, Operation: mutationOperationReplace, Root: root.file, Parent: prepared.parent.file, Source: prepared.source, TargetLeaf: prepared.leaf, Expected: prepared.identity.protocolStamp(), Payload: payload})
	if err != nil {
		return MutationResult{}, mapStagedMutationError(err)
	}
	if err := mapStagedTerminal(result.Terminal); err != nil {
		return MutationResult{}, err
	}
	identity := mutationIdentityFromProtocol(result.StageIdentity)
	resolved := Resolved{Rel: prepared.rel, Exists: true, Kind: KindFile, Size: identity.size, Modified: mutationTime(identity)}
	return MutationResult{Resolved: resolved, Fingerprint: mutationFingerprint(prepared.rel, KindFile, identity)}, nil
}

func (m *Mutator) mutationExecutable() (string, error) {
	if m.helperExecutable != "" {
		return m.helperExecutable, nil
	}
	path, err := os.Executable()
	if err != nil {
		return "", &Error{Code: CodePathDenied}
	}
	return path, nil
}

func mutationIdentityFromProtocol(stamp mutationRawStamp) mutationTargetIdentity {
	return mutationTargetIdentity{dev: stamp.Device, ino: stamp.Inode, mode: stamp.Mode, uid: stamp.UID, nlink: stamp.NLink, size: stamp.Size, mtimeSec: stamp.MtimeSec, mtimeNsec: stamp.MtimeNsec, ctimeSec: stamp.CtimeSec, ctimeNsec: stamp.CtimeNsec}
}

func mutationTime(identity mutationTargetIdentity) time.Time {
	return time.Unix(identity.mtimeSec, identity.mtimeNsec).UTC()
}

func mapStagedMutationError(err error) error {
	switch {
	case errors.Is(err, errMutationUncertain), errors.Is(err, errMutationCleanup):
		return &Error{Code: CodeUncertain}
	case errors.Is(err, context.DeadlineExceeded):
		return &Error{Code: CodeTimeout}
	case errors.Is(err, context.Canceled):
		return &Error{Code: CodeCanceled}
	default:
		return &Error{Code: CodePathDenied}
	}
}

func mapStagedTerminal(terminal mutationControl) error {
	if terminal.Terminal == mutationTerminalCommitted && terminal.TerminalCode == mutationTerminalCodeCommitted {
		return nil
	}
	switch terminal.TerminalCode {
	case mutationTerminalCodeSourceChanged:
		return &Error{Code: CodeSourceChanged}
	case mutationTerminalCodeDestinationExists:
		return &Error{Code: CodeDestinationExists}
	case mutationTerminalCodeUnsupported:
		return &Error{Code: CodeUnsupported}
	case mutationTerminalCodeCanceled:
		return &Error{Code: CodeCanceled}
	case mutationTerminalCodeUncertain:
		return &Error{Code: CodeUncertain}
	default:
		return &Error{Code: CodePathDenied}
	}
}
