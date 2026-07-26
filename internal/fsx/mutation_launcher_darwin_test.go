//go:build darwin

package fsx

import (
	"context"
	"errors"
	"testing"
)

func TestRunStagedMutationRefusesInvalidRequestBeforeSpawn(t *testing.T) {
	_, err := runStagedMutation(context.Background(), stagedMutation{Operation: mutationOperationCreate})
	if !errors.Is(err, errMutationProtocol) {
		t.Fatalf("error = %v, want protocol refusal", err)
	}
}

func TestMutationStageLeafIsProtocolValid(t *testing.T) {
	leaf, err := mutationStageLeaf()
	if err != nil {
		t.Fatal(err)
	}
	if !validMutationStageLeaf(leaf) {
		t.Fatalf("invalid stage leaf %q", leaf)
	}
}
