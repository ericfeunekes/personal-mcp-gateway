package fsx

import (
	"crypto/hmac"
	"crypto/sha256"

	"golang.org/x/sys/unix"
)

var opaqueBindingAuthorityDomain = []byte("personal-mcp-gateway/fsx-opaque-root-authority/v1\x00")

type opaqueBindingAuthority [sha256.Size]byte

// OpaqueBinding is a root-bound authentication value. It exposes no key or
// filesystem identity; callers can only ask a Vault to bind or verify bytes.
type OpaqueBinding [sha256.Size]byte

func deriveOpaqueBindingAuthority(root string) (opaqueBindingAuthority, error) {
	fd, err := unix.Open(root, directoryOpenFlags, 0)
	if err != nil {
		return opaqueBindingAuthority{}, mapOpenDirError(err)
	}
	defer unix.Close(fd)

	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return opaqueBindingAuthority{}, mapPathError(err)
	}
	if kindFromUnixMode(stat.Mode) != KindDir {
		return opaqueBindingAuthority{}, &Error{Code: CodeNotDirectory}
	}

	framed := make([]byte, 0, len(opaqueBindingAuthorityDomain)+len(root)+5*8)
	framed = append(framed, opaqueBindingAuthorityDomain...)
	framed = appendUint64(framed, uint64(len(root)))
	framed = append(framed, root...)
	framed = appendUint64(framed, uint64(uint32(stat.Dev)))
	framed = appendUint64(framed, stat.Ino)
	framed = appendUint64(framed, uint64(uint32(stat.Mode)&unix.S_IFMT))
	return sha256.Sum256(framed), nil
}

// BindOpaque authenticates domain-separated bytes with the configured root's
// canonical identity. The authority is deterministic across Vault instances
// over the same root and requires no persistent secret or per-value state.
func (v *Vault) BindOpaque(domain string, value []byte) OpaqueBinding {
	if v == nil {
		return OpaqueBinding{}
	}
	mac := hmac.New(sha256.New, v.opaqueBindingAuthority[:])
	_, _ = mac.Write([]byte(domain))
	var framed [8]byte
	encoded := appendUint64(framed[:0], uint64(len(value)))
	_, _ = mac.Write(encoded)
	_, _ = mac.Write(value)
	var binding OpaqueBinding
	copy(binding[:], mac.Sum(nil))
	return binding
}

// VerifyOpaque checks a binding without exposing or reconstructing the root
// authority outside fsx.
func (v *Vault) VerifyOpaque(domain string, value []byte, binding OpaqueBinding) bool {
	if v == nil {
		return false
	}
	expected := v.BindOpaque(domain, value)
	return hmac.Equal(expected[:], binding[:])
}
