package fsx

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

const (
	testStageLeaf = ".pmcg-stage-0123456789abcdef0123456789abcdef"
	testToken     = "0123456789abcdef0123456789abcdef"
)

var testRawStamp = mutationRawStamp{Device: 1, Inode: 2, Mode: 0o100600, NLink: 1}

func TestMutationProtocolRoundTrip(t *testing.T) {
	var wire bytes.Buffer
	parent, err := newMutationProtocolParent(&wire, &wire, mutationOperationReplace, mutationFDRequired|mutationFDSource)
	if err != nil {
		t.Fatal(err)
	}
	helper := newMutationProtocolHelper(&wire, &wire)

	requireProtocolSend(t, parent, mutationControl{Version: mutationProtocolVersion, Operation: mutationOperationReplace, Phase: mutationPhaseRequest, FDRoles: mutationFDRequired | mutationFDSource, PayloadBytes: 3, TargetLeaf: "note.md", StageLeaf: testStageLeaf, ExpectedSource: testRawStamp})
	requireProtocolReceive(t, helper, mutationPhaseRequest)
	requireProtocolSend(t, helper, controlFor(mutationOperationReplace, mutationPhaseStageCreated))
	requireProtocolReceive(t, parent, mutationPhaseStageCreated)
	requireProtocolSend(t, parent, controlFor(mutationOperationReplace, mutationPhaseStageAck))
	requireProtocolReceive(t, helper, mutationPhaseStageAck)
	requireProtocolSend(t, helper, mutationControl{Version: mutationProtocolVersion, Operation: mutationOperationReplace, Phase: mutationPhaseStageAnnounced, StageIdentity: testRawStamp})
	requireProtocolReceive(t, parent, mutationPhaseStageAnnounced)
	if err := parent.SendPayload([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 3)
	if err := helper.ReceivePayload(payload); err != nil {
		t.Fatal(err)
	}
	if got := string(payload); got != "abc" {
		t.Fatalf("payload = %q", got)
	}
	requireProtocolSend(t, helper, mutationControl{Version: mutationProtocolVersion, Operation: mutationOperationReplace, Phase: mutationPhasePayloadComplete, StageIdentity: testRawStamp})
	requireProtocolReceive(t, parent, mutationPhasePayloadComplete)
	requireProtocolSend(t, parent, mutationControl{Version: mutationProtocolVersion, Operation: mutationOperationReplace, Phase: mutationPhaseCommit, CommitToken: testToken})
	requireProtocolReceive(t, helper, mutationPhaseCommit)
	requireProtocolSend(t, helper, mutationControl{Version: mutationProtocolVersion, Operation: mutationOperationReplace, Phase: mutationPhaseTerminal, StageIdentity: testRawStamp, Terminal: mutationTerminalCommitted, TerminalCode: mutationTerminalCodeCommitted})
	requireProtocolReceive(t, parent, mutationPhaseTerminal)
}

func TestMutationProtocolRejectsMalformedFrames(t *testing.T) {
	tests := []struct {
		name  string
		frame []byte
	}{
		{name: "truncated length", frame: []byte{0, 0}},
		{name: "truncated body", frame: framedRaw([]byte(`{"version":1`), 20)},
		{name: "empty", frame: framedRaw(nil, 0)},
		{name: "oversized", frame: framedRaw(nil, mutationMaxControlFrameBytes+1)},
		{name: "unknown field", frame: framedJSON([]byte(`{"version":1,"operation":"create","phase":"request","fd_roles":7,"payload_bytes":0,"target_leaf":"note.md","stage_leaf":".pmcg-stage-0123456789abcdef0123456789abcdef","unexpected":true}`))},
		{name: "trailing JSON", frame: framedJSON([]byte(`{"version":1,"operation":"create","phase":"request","fd_roles":7,"payload_bytes":0,"target_leaf":"note.md","stage_leaf":".pmcg-stage-0123456789abcdef0123456789abcdef"} null`))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := readMutationControl(bytes.NewReader(tt.frame))
			if err == nil {
				t.Fatal("accepted malformed frame")
			}
		})
	}
}

func TestMutationProtocolRejectsVersionPhaseAndRoleViolations(t *testing.T) {
	tests := []struct {
		name    string
		control mutationControl
	}{
		{name: "version", control: mutationControl{Version: 2, Operation: mutationOperationCreate, Phase: mutationPhaseRequest, FDRoles: mutationFDRequired, TargetLeaf: "note.md", StageLeaf: testStageLeaf}},
		{name: "unknown operation", control: mutationControl{Version: 1, Operation: "move", Phase: mutationPhaseRequest, FDRoles: mutationFDRequired, TargetLeaf: "note.md", StageLeaf: testStageLeaf}},
		{name: "wrong phase", control: control(mutationPhaseCommit)},
		{name: "create source fd", control: mutationControl{Version: 1, Operation: mutationOperationCreate, Phase: mutationPhaseRequest, FDRoles: mutationFDRequired | mutationFDSource, TargetLeaf: "note.md", StageLeaf: testStageLeaf}},
		{name: "replace lacks source fd", control: mutationControl{Version: 1, Operation: mutationOperationReplace, Phase: mutationPhaseRequest, FDRoles: mutationFDRequired, TargetLeaf: "note.md", StageLeaf: testStageLeaf, ExpectedSource: testRawStamp}},
		{name: "unknown role bit", control: mutationControl{Version: 1, Operation: mutationOperationCreate, Phase: mutationPhaseRequest, FDRoles: mutationFDRequired | 1<<7, TargetLeaf: "note.md", StageLeaf: testStageLeaf}},
		{name: "unsafe stage leaf", control: mutationControl{Version: 1, Operation: mutationOperationCreate, Phase: mutationPhaseRequest, FDRoles: mutationFDRequired, TargetLeaf: "note.md", StageLeaf: "../stage"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var wire bytes.Buffer
			if err := writeMutationControl(&wire, tt.control); err != nil {
				t.Fatal(err)
			}
			helper := newMutationProtocolHelper(&wire, &wire)
			if _, err := helper.ReceiveControl(); !errors.Is(err, errMutationProtocol) {
				t.Fatalf("err = %v, want protocol refusal", err)
			}
		})
	}
}

func TestMutationProtocolPayloadBoundsAndTrailingBytes(t *testing.T) {
	t.Run("declaration boundaries", func(t *testing.T) {
		for _, size := range []uint64{mutationMaxPayloadBytes, mutationMaxPayloadBytes + 1} {
			var wire bytes.Buffer
			helper := newMutationProtocolHelper(&wire, &wire)
			request := mutationControl{Version: 1, Operation: mutationOperationCreate, Phase: mutationPhaseRequest, FDRoles: mutationFDRequired, PayloadBytes: size, TargetLeaf: "note.md", StageLeaf: testStageLeaf}
			if err := writeMutationControl(&wire, request); err != nil {
				t.Fatal(err)
			}
			_, err := helper.ReceiveControl()
			if (size <= mutationMaxPayloadBytes) != (err == nil) {
				t.Fatalf("size %d: err = %v", size, err)
			}
		}
	})
	t.Run("truncated payload", func(t *testing.T) {
		parent, helper, wire := protocolAtPayload(t, 3)
		_ = parent
		wire.WriteString("ab")
		if err := helper.ReceivePayload(make([]byte, 3)); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("trailing payload", func(t *testing.T) {
		parent, helper, wire := protocolAtPayload(t, 3)
		if err := parent.SendPayload([]byte("abc")); err != nil {
			t.Fatal(err)
		}
		wire.WriteByte('x')
		if err := helper.ReceivePayload(make([]byte, 3)); err != nil {
			t.Fatal(err)
		}
		if _, err := helper.ReceiveControl(); err == nil {
			t.Fatal("accepted trailing payload bytes")
		}
	})
}

func TestMutationProtocolRejectsInvalidOrder(t *testing.T) {
	var wire bytes.Buffer
	parent, err := newMutationProtocolParent(&wire, &wire, mutationOperationCreate, mutationFDRequired)
	if err != nil {
		t.Fatal(err)
	}
	if err := parent.SendControl(control(mutationPhaseStageAck)); !errors.Is(err, errMutationProtocol) {
		t.Fatalf("err = %v", err)
	}
	if err := parent.SendPayload(nil); !errors.Is(err, errMutationProtocol) {
		t.Fatalf("err = %v", err)
	}
}

func TestMutationProtocolRequiresHelperExecutableIdentity(t *testing.T) {
	var wire bytes.Buffer
	parent, err := newMutationProtocolParent(&wire, &wire, mutationOperationCreate, mutationFDRequired)
	if err != nil {
		t.Fatal(err)
	}
	helper := newMutationProtocolHelper(&wire, &wire)
	requireProtocolSend(t, parent, mutationControl{Version: 1, Operation: mutationOperationCreate, Phase: mutationPhaseRequest, FDRoles: mutationFDRequired, TargetLeaf: "note.md", StageLeaf: testStageLeaf})
	requireProtocolReceive(t, helper, mutationPhaseRequest)
	if err := helper.SendControl(mutationControl{Version: 1, Operation: mutationOperationCreate, Phase: mutationPhaseStageCreated}); !errors.Is(err, errMutationProtocol) {
		t.Fatalf("missing executable identity err = %v", err)
	}
}

func protocolAtPayload(t *testing.T, size uint64) (*mutationProtocol, *mutationProtocol, *bytes.Buffer) {
	t.Helper()
	var wire bytes.Buffer
	parent, err := newMutationProtocolParent(&wire, &wire, mutationOperationCreate, mutationFDRequired)
	if err != nil {
		t.Fatal(err)
	}
	helper := newMutationProtocolHelper(&wire, &wire)
	requireProtocolSend(t, parent, mutationControl{Version: 1, Operation: mutationOperationCreate, Phase: mutationPhaseRequest, FDRoles: mutationFDRequired, PayloadBytes: size, TargetLeaf: "note.md", StageLeaf: testStageLeaf})
	requireProtocolReceive(t, helper, mutationPhaseRequest)
	requireProtocolSend(t, helper, controlFor(mutationOperationCreate, mutationPhaseStageCreated))
	requireProtocolReceive(t, parent, mutationPhaseStageCreated)
	requireProtocolSend(t, parent, controlFor(mutationOperationCreate, mutationPhaseStageAck))
	requireProtocolReceive(t, helper, mutationPhaseStageAck)
	requireProtocolSend(t, helper, mutationControl{Version: 1, Operation: mutationOperationCreate, Phase: mutationPhaseStageAnnounced, StageIdentity: testRawStamp})
	requireProtocolReceive(t, parent, mutationPhaseStageAnnounced)
	return parent, helper, &wire
}

func control(phase mutationPhase) mutationControl {
	return controlFor(mutationOperationReplace, phase)
}

func controlFor(operation mutationOperation, phase mutationPhase) mutationControl {
	control := mutationControl{Version: 1, Operation: operation, Phase: phase}
	if phase == mutationPhaseStageCreated {
		control.ExecutableIdentity = testRawStamp
	}
	return control
}
func requireProtocolSend(t *testing.T, p *mutationProtocol, c mutationControl) {
	t.Helper()
	if err := p.SendControl(c); err != nil {
		t.Fatal(err)
	}
}
func requireProtocolReceive(t *testing.T, p *mutationProtocol, phase mutationPhase) mutationControl {
	t.Helper()
	c, err := p.ReceiveControl()
	if err != nil {
		t.Fatal(err)
	}
	if c.Phase != phase {
		t.Fatalf("phase = %q, want %q", c.Phase, phase)
	}
	return c
}

func framedRaw(body []byte, length uint32) []byte {
	var frame [4]byte
	binary.BigEndian.PutUint32(frame[:], length)
	return append(frame[:], body...)
}
func framedJSON(body []byte) []byte { return framedRaw(body, uint32(len(body))) }
