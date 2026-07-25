package fsx

const (
	CodeDestinationExists Code = "destination_exists"
	CodeNotEmpty          Code = "not_empty"
	CodeUnsupported       Code = "unsupported"
	CodeUncertain         Code = "uncertain"
)

// MutationTarget is the private mutation-ready identity of one allowed vault
// entry. The fingerprint is opaque outside fsx.
type MutationTarget struct {
	Resolved    Resolved
	Fingerprint SourceFingerprint
}

// MutationResult is metadata derived from the descriptor that owns a
// successful create, replacement, or move effect.
type MutationResult struct {
	Resolved    Resolved
	Fingerprint SourceFingerprint
}

// DeleteResult deliberately carries no recovery handle: deletion is permanent.
type DeleteResult struct {
	Resolved Resolved
}

// Mutator is bound to one confined Vault. helperExecutable is private test and
// launch configuration; an empty value resolves to the running executable.
type Mutator struct {
	vault            *Vault
	helperExecutable string
}

func NewMutator(vault *Vault) *Mutator {
	return &Mutator{vault: vault}
}
