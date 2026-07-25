//go:build darwin

package fsx

import (
	"context"
	"errors"

	"golang.org/x/sys/unix"
)

func (m *Mutator) Move(ctx context.Context, base, source, destination string, expected SourceFingerprint) (MutationResult, error) {
	if m == nil || m.vault == nil {
		return MutationResult{}, &Error{Code: CodePathDenied}
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
	sourceRoot, err := cloneMutationRoot(root)
	if err != nil {
		return MutationResult{}, err
	}
	prepared, err := m.vault.prepareMutationTargetFromRoot(ctx, sourceRoot, base, source)
	if err != nil {
		return MutationResult{}, err
	}
	defer prepared.close()
	if prepared.fingerprint != expected {
		return MutationResult{}, &Error{Code: CodeSourceChanged}
	}
	destinationRoot, err := cloneMutationRoot(root)
	if err != nil {
		return MutationResult{}, err
	}
	destinationParent, requestedLeaf, parentRel, err := m.vault.prepareMutationParentFromRoot(ctx, destinationRoot, base, destination)
	if err != nil {
		return MutationResult{}, err
	}
	defer destinationParent.Close()
	destinationMatch, err := matchMutationName(ctx, int(destinationParent.file.Fd()), requestedLeaf)
	if err != nil {
		return MutationResult{}, err
	}
	if destinationMatch.state != mutationNameAbsent {
		return MutationResult{}, &Error{Code: CodeDestinationExists}
	}
	if err := revalidatePreparedMutationTarget(ctx, prepared); err != nil {
		return MutationResult{}, err
	}
	destinationMatch, err = matchMutationName(ctx, int(destinationParent.file.Fd()), requestedLeaf)
	if err != nil {
		return MutationResult{}, err
	}
	if destinationMatch.state != mutationNameAbsent {
		return MutationResult{}, &Error{Code: CodeDestinationExists}
	}
	if hooks := m.vault.testHooks; hooks != nil && hooks.beforeMutationEffect != nil {
		hooks.beforeMutationEffect()
	}
	if err := mutationRenameExclusive(int(prepared.parent.file.Fd()), prepared.leaf, int(destinationParent.file.Fd()), requestedLeaf); err != nil {
		switch {
		case errors.Is(err, unix.EEXIST):
			return MutationResult{}, &Error{Code: CodeDestinationExists}
		case errors.Is(err, unix.ENOTSUP):
			return MutationResult{}, &Error{Code: CodeUnsupported}
		default:
			return MutationResult{}, &Error{Code: CodePathDenied}
		}
	}
	var after unix.Stat_t
	if err := unix.Fstat(int(prepared.source.Fd()), &after); err != nil {
		return MutationResult{}, &Error{Code: CodeUncertain}
	}
	identity := mutationIdentityFromStat(&after)
	rel := joinRel(parentRel, requestedLeaf)
	resolved := resolvedFromMutationStat(rel, prepared.resolved.Kind, &after)
	return MutationResult{Resolved: resolved, Fingerprint: mutationFingerprint(rel, prepared.resolved.Kind, identity)}, nil
}

func (m *Mutator) Delete(ctx context.Context, base, input string, expected SourceFingerprint) (DeleteResult, error) {
	if m == nil || m.vault == nil {
		return DeleteResult{}, &Error{Code: CodePathDenied}
	}
	if end := m.vault.activity.Begin(); end != nil {
		defer end()
	}
	prepared, err := m.vault.prepareMutationTarget(ctx, base, input)
	if err != nil {
		return DeleteResult{}, err
	}
	defer prepared.close()
	if prepared.fingerprint != expected {
		return DeleteResult{}, &Error{Code: CodeSourceChanged}
	}
	if err := revalidatePreparedMutationTarget(ctx, prepared); err != nil {
		return DeleteResult{}, err
	}
	flags := 0
	if prepared.resolved.Kind == KindDir {
		flags = unix.AT_REMOVEDIR
	}
	if hooks := m.vault.testHooks; hooks != nil && hooks.beforeMutationEffect != nil {
		hooks.beforeMutationEffect()
	}
	if err := unix.Unlinkat(int(prepared.parent.file.Fd()), prepared.leaf, flags); err != nil {
		if errors.Is(err, unix.ENOTEMPTY) || errors.Is(err, unix.EEXIST) {
			return DeleteResult{}, &Error{Code: CodeNotEmpty}
		}
		return DeleteResult{}, &Error{Code: CodeSourceChanged}
	}
	removed := prepared.resolved
	removed.Exists = false
	return DeleteResult{Resolved: removed}, nil
}

func revalidatePreparedMutationTarget(ctx context.Context, prepared *preparedMutationTarget) error {
	if hooks := prepared.parent.testHooks; hooks != nil && hooks.beforeMutationFinal != nil {
		hooks.beforeMutationFinal()
	}
	if err := ctx.Err(); err != nil {
		return contextError(err)
	}
	var descriptor, named unix.Stat_t
	if err := unix.Fstat(int(prepared.source.Fd()), &descriptor); err != nil {
		return &Error{Code: CodeSourceChanged}
	}
	if mutationIdentityFromStat(&descriptor) != prepared.identity {
		return &Error{Code: CodeSourceChanged}
	}
	if err := unix.Fstatat(int(prepared.parent.file.Fd()), prepared.leaf, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil || mutationIdentityFromStat(&named) != prepared.identity {
		return &Error{Code: CodeSourceChanged}
	}
	if prepared.resolved.Kind == KindDir {
		empty, err := mutationDirectoryEmpty(ctx, int(prepared.source.Fd()))
		if err != nil {
			return err
		}
		if !empty {
			return &Error{Code: CodeNotEmpty}
		}
		var final unix.Stat_t
		if err := unix.Fstat(int(prepared.source.Fd()), &final); err != nil || mutationIdentityFromStat(&final) != prepared.identity {
			return &Error{Code: CodeSourceChanged}
		}
	}
	return nil
}
