package fsx

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpaqueBindingIsRestartStableAndRootBound(t *testing.T) {
	root := t.TempDir()
	first, err := NewVault(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewVault(filepath.Join(root, "."))
	if err != nil {
		t.Fatal(err)
	}

	value := []byte("bounded cursor envelope")
	binding := first.BindOpaque("cursor/v2", value)
	if binding == (OpaqueBinding{}) || !second.VerifyOpaque("cursor/v2", value, binding) {
		t.Fatal("same-root Vault instance did not verify restart-stable binding")
	}
	if second.VerifyOpaque("cursor/v2-other", value, binding) || second.VerifyOpaque("cursor/v2", []byte("changed"), binding) {
		t.Fatal("binding did not cover its domain and value")
	}

	otherRoot := t.TempDir()
	other, err := NewVault(otherRoot)
	if err != nil {
		t.Fatal(err)
	}
	if other.VerifyOpaque("cursor/v2", value, binding) {
		t.Fatal("different root accepted binding")
	}

	if err := os.WriteFile(filepath.Join(root, "later.md"), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	afterMutation, err := NewVault(root)
	if err != nil {
		t.Fatal(err)
	}
	if !afterMutation.VerifyOpaque("cursor/v2", value, binding) {
		t.Fatal("ordinary root membership mutation changed stable authority")
	}
}
