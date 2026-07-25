//go:build !darwin

package fsx

import "context"

func (m *Mutator) StatMutationTarget(context.Context, string, string) (MutationTarget, error) {
	return MutationTarget{}, &Error{Code: CodeUnsupported}
}

func (m *Mutator) CreateFile(context.Context, string, string, []byte) (MutationResult, error) {
	return MutationResult{}, &Error{Code: CodeUnsupported}
}

func (m *Mutator) ReplaceFile(context.Context, string, string, SourceFingerprint, []byte) (MutationResult, error) {
	return MutationResult{}, &Error{Code: CodeUnsupported}
}

func (m *Mutator) Move(context.Context, string, string, string, SourceFingerprint) (MutationResult, error) {
	return MutationResult{}, &Error{Code: CodeUnsupported}
}

func (m *Mutator) Delete(context.Context, string, string, SourceFingerprint) (DeleteResult, error) {
	return DeleteResult{}, &Error{Code: CodeUnsupported}
}
