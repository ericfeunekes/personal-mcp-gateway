package fsx

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	mutationProtocolVersion      = 1
	mutationMaxControlFrameBytes = 4 << 10
	mutationMaxPayloadBytes      = 8 << 20
)

var errMutationProtocol = errors.New("invalid mutation helper protocol")

type mutationOperation string

const (
	mutationOperationCreate  mutationOperation = "create"
	mutationOperationReplace mutationOperation = "replace"
)

type mutationPhase string

const (
	mutationPhaseRequest         mutationPhase = "request"
	mutationPhaseStageCreated    mutationPhase = "stage_created"
	mutationPhaseStageAck        mutationPhase = "stage_ack"
	mutationPhaseStageAnnounced  mutationPhase = "stage_announced"
	mutationPhasePayloadComplete mutationPhase = "payload_complete"
	mutationPhaseCommit          mutationPhase = "commit"
	mutationPhaseTerminal        mutationPhase = "terminal"
)

type mutationTerminal string

const (
	mutationTerminalCommitted mutationTerminal = "committed"
	mutationTerminalRefused   mutationTerminal = "refused"
	mutationTerminalError     mutationTerminal = "error"
	mutationTerminalUncertain mutationTerminal = "uncertain"
)

type mutationTerminalCode string

const (
	mutationTerminalCodeCommitted         mutationTerminalCode = "committed"
	mutationTerminalCodeSourceChanged     mutationTerminalCode = "source_changed"
	mutationTerminalCodeDestinationExists mutationTerminalCode = "destination_exists"
	mutationTerminalCodeUnsupported       mutationTerminalCode = "unsupported"
	mutationTerminalCodeCanceled          mutationTerminalCode = "canceled"
	mutationTerminalCodeIO                mutationTerminalCode = "io_error"
	mutationTerminalCodeUncertain         mutationTerminalCode = "uncertain"
)

type mutationFDRoles uint8

const (
	mutationFDControl mutationFDRoles = 1 << iota
	mutationFDRoot
	mutationFDParent
	mutationFDSource
)

const mutationFDRequired = mutationFDControl | mutationFDRoot | mutationFDParent

// mutationRawStamp is private protocol evidence for cleanup, revalidation, and
// effect-derived results. It is deliberately not a host path.
type mutationRawStamp struct {
	Device    uint64 `json:"device"`
	Inode     uint64 `json:"inode"`
	Mode      uint32 `json:"mode"`
	UID       uint32 `json:"uid"`
	NLink     uint64 `json:"nlink"`
	Size      int64  `json:"size"`
	MtimeSec  int64  `json:"mtime_sec"`
	MtimeNsec int64  `json:"mtime_nsec"`
	CtimeSec  int64  `json:"ctime_sec"`
	CtimeNsec int64  `json:"ctime_nsec"`
}

// mutationControl is a closed-world control record. Fields are accepted only
// in the phase where they are meaningful; JSON object maps are intentionally
// avoided so the wire contract stays explicit.
type mutationControl struct {
	Version            int                  `json:"version"`
	Operation          mutationOperation    `json:"operation"`
	Phase              mutationPhase        `json:"phase"`
	FDRoles            mutationFDRoles      `json:"fd_roles,omitempty"`
	PayloadBytes       uint64               `json:"payload_bytes,omitempty"`
	TargetLeaf         string               `json:"target_leaf,omitempty"`
	StageLeaf          string               `json:"stage_leaf,omitempty"`
	ExpectedSource     mutationRawStamp     `json:"expected_source,omitempty"`
	ExecutableIdentity mutationRawStamp     `json:"executable_identity,omitempty"`
	StageIdentity      mutationRawStamp     `json:"stage_identity,omitempty"`
	CommitToken        string               `json:"commit_token,omitempty"`
	Terminal           mutationTerminal     `json:"terminal,omitempty"`
	TerminalCode       mutationTerminalCode `json:"terminal_code,omitempty"`
}

type mutationProtocolSide uint8

const (
	mutationProtocolParent mutationProtocolSide = iota
	mutationProtocolHelper
)

// mutationProtocol validates one side of one helper invocation. It is not
// concurrent: callers serialize every read and write on the inherited stream.
type mutationProtocol struct {
	r io.Reader
	w io.Writer

	side      mutationProtocolSide
	operation mutationOperation
	roles     mutationFDRoles
	phase     mutationPhase
	payload   uint64
}

func newMutationProtocolParent(r io.Reader, w io.Writer, operation mutationOperation, roles mutationFDRoles) (*mutationProtocol, error) {
	if !validMutationOperation(operation) || !validMutationFDRoles(operation, roles) {
		return nil, errMutationProtocol
	}
	return &mutationProtocol{r: r, w: w, side: mutationProtocolParent, operation: operation, roles: roles}, nil
}

func newMutationProtocolHelper(r io.Reader, w io.Writer) *mutationProtocol {
	return &mutationProtocol{r: r, w: w, side: mutationProtocolHelper}
}

func (p *mutationProtocol) SendControl(control mutationControl) error {
	if p == nil || !p.canSend(control.Phase) || !p.validateOutgoing(control) {
		return errMutationProtocol
	}
	if err := writeMutationControl(p.w, control); err != nil {
		return err
	}
	p.advance(control)
	return nil
}

func (p *mutationProtocol) ReceiveControl() (mutationControl, error) {
	if p == nil {
		return mutationControl{}, errMutationProtocol
	}
	control, err := readMutationControl(p.r)
	if err != nil {
		return mutationControl{}, err
	}
	if !p.canReceive(control.Phase) || !p.validateIncoming(control) {
		return mutationControl{}, errMutationProtocol
	}
	p.advance(control)
	return control, nil
}

// SendPayload emits the one raw segment declared by the request. It may only
// occur after stage identity announcement and before payload_complete.
func (p *mutationProtocol) SendPayload(payload []byte) error {
	if p == nil || p.side != mutationProtocolParent || p.phase != mutationPhaseStageAnnounced || uint64(len(payload)) != p.payload {
		return errMutationProtocol
	}
	if err := writeAll(p.w, payload); err != nil {
		return err
	}
	p.phase = mutationPhasePayloadComplete
	return nil
}

// ReceivePayload consumes exactly the declared raw segment. The following
// control-frame read rejects any trailing payload bytes as an invalid frame.
func (p *mutationProtocol) ReceivePayload(dst []byte) error {
	if p == nil || p.side != mutationProtocolHelper || p.phase != mutationPhaseStageAnnounced || uint64(len(dst)) != p.payload {
		return errMutationProtocol
	}
	if _, err := io.ReadFull(p.r, dst); err != nil {
		return err
	}
	p.phase = mutationPhasePayloadComplete
	return nil
}

func (p *mutationProtocol) canSend(phase mutationPhase) bool {
	if p.side == mutationProtocolParent {
		switch p.phase {
		case "":
			return phase == mutationPhaseRequest
		case mutationPhaseStageCreated:
			return phase == mutationPhaseStageAck
		case mutationPhasePayloadComplete:
			return phase == mutationPhaseCommit
		}
		return false
	}
	switch p.phase {
	case mutationPhaseRequest:
		return phase == mutationPhaseStageCreated
	case mutationPhaseStageAck:
		return phase == mutationPhaseStageAnnounced
	case mutationPhasePayloadComplete:
		return phase == mutationPhasePayloadComplete
	case mutationPhaseCommit:
		return phase == mutationPhaseTerminal
	}
	return false
}

func (p *mutationProtocol) canReceive(phase mutationPhase) bool {
	if p.side == mutationProtocolParent {
		switch p.phase {
		case mutationPhaseRequest:
			return phase == mutationPhaseStageCreated
		case mutationPhaseStageAck:
			return phase == mutationPhaseStageAnnounced
		case mutationPhasePayloadComplete:
			return phase == mutationPhasePayloadComplete
		case mutationPhaseCommit:
			return phase == mutationPhaseTerminal
		}
		return false
	}
	switch p.phase {
	case "":
		return phase == mutationPhaseRequest
	case mutationPhaseStageCreated:
		return phase == mutationPhaseStageAck
	case mutationPhaseStageAnnounced:
		return false // payload is raw, not JSON
	case mutationPhasePayloadComplete:
		return phase == mutationPhaseCommit
	}
	return false
}

func (p *mutationProtocol) validateOutgoing(c mutationControl) bool {
	if p.side == mutationProtocolHelper && p.phase == "" {
		return false
	}
	return p.validateBound(c)
}

func (p *mutationProtocol) validateIncoming(c mutationControl) bool {
	if p.side == mutationProtocolHelper && p.phase == "" {
		if !validMutationOperation(c.Operation) || !validMutationFDRoles(c.Operation, c.FDRoles) {
			return false
		}
		p.operation, p.roles = c.Operation, c.FDRoles
	}
	return p.validateBound(c)
}

func (p *mutationProtocol) validateBound(c mutationControl) bool {
	if c.Version != mutationProtocolVersion || c.Operation != p.operation || !validMutationPhase(c.Phase) {
		return false
	}
	switch c.Phase {
	case mutationPhaseRequest:
		return c.FDRoles == p.roles && c.PayloadBytes <= mutationMaxPayloadBytes && validMutationTargetLeaf(c.TargetLeaf) && validMutationStageLeaf(c.StageLeaf) && validExpectedSource(c.Operation, c.ExpectedSource) && c.ExecutableIdentity == (mutationRawStamp{}) && c.StageIdentity == (mutationRawStamp{}) && c.CommitToken == "" && c.Terminal == "" && c.TerminalCode == ""
	case mutationPhaseStageCreated:
		return c.FDRoles == 0 && c.PayloadBytes == 0 && c.TargetLeaf == "" && c.StageLeaf == "" && c.ExpectedSource == (mutationRawStamp{}) && validMutationRawStamp(c.ExecutableIdentity) && c.StageIdentity == (mutationRawStamp{}) && c.CommitToken == "" && c.Terminal == "" && c.TerminalCode == ""
	case mutationPhaseStageAck:
		return c.FDRoles == 0 && c.PayloadBytes == 0 && c.TargetLeaf == "" && c.StageLeaf == "" && c.ExpectedSource == (mutationRawStamp{}) && c.ExecutableIdentity == (mutationRawStamp{}) && c.StageIdentity == (mutationRawStamp{}) && c.CommitToken == "" && c.Terminal == "" && c.TerminalCode == ""
	case mutationPhaseStageAnnounced:
		return c.FDRoles == 0 && c.PayloadBytes == 0 && c.TargetLeaf == "" && c.StageLeaf == "" && c.ExpectedSource == (mutationRawStamp{}) && c.ExecutableIdentity == (mutationRawStamp{}) && validMutationRawStamp(c.StageIdentity) && c.CommitToken == "" && c.Terminal == "" && c.TerminalCode == ""
	case mutationPhasePayloadComplete:
		return c.FDRoles == 0 && c.PayloadBytes == 0 && c.TargetLeaf == "" && c.StageLeaf == "" && c.ExpectedSource == (mutationRawStamp{}) && c.ExecutableIdentity == (mutationRawStamp{}) && validMutationRawStamp(c.StageIdentity) && c.CommitToken == "" && c.Terminal == "" && c.TerminalCode == ""
	case mutationPhaseCommit:
		return c.FDRoles == 0 && c.PayloadBytes == 0 && c.TargetLeaf == "" && c.StageLeaf == "" && c.ExpectedSource == (mutationRawStamp{}) && c.ExecutableIdentity == (mutationRawStamp{}) && c.StageIdentity == (mutationRawStamp{}) && validMutationToken(c.CommitToken) && c.Terminal == "" && c.TerminalCode == ""
	case mutationPhaseTerminal:
		identityValid := c.StageIdentity == (mutationRawStamp{})
		if c.Terminal == mutationTerminalCommitted {
			identityValid = validMutationRawStamp(c.StageIdentity)
		}
		return c.FDRoles == 0 && c.PayloadBytes == 0 && c.TargetLeaf == "" && c.StageLeaf == "" && c.ExpectedSource == (mutationRawStamp{}) && c.ExecutableIdentity == (mutationRawStamp{}) && identityValid && c.CommitToken == "" && validMutationTerminal(c.Terminal) && validMutationTerminalCode(c.Terminal, c.TerminalCode)
	}
	return false
}

func (p *mutationProtocol) advance(c mutationControl) {
	p.phase = c.Phase
	if c.Phase == mutationPhaseRequest {
		p.payload = c.PayloadBytes
	}
}

func writeMutationControl(w io.Writer, control mutationControl) error {
	if w == nil {
		return errMutationProtocol
	}
	encoded, err := json.Marshal(control)
	if err != nil || len(encoded) == 0 || len(encoded) > mutationMaxControlFrameBytes {
		return errMutationProtocol
	}
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(encoded)))
	if err := writeAll(w, length[:]); err != nil {
		return err
	}
	return writeAll(w, encoded)
}

func readMutationControl(r io.Reader) (mutationControl, error) {
	if r == nil {
		return mutationControl{}, errMutationProtocol
	}
	var length [4]byte
	if _, err := io.ReadFull(r, length[:]); err != nil {
		return mutationControl{}, err
	}
	n := binary.BigEndian.Uint32(length[:])
	if n == 0 || n > mutationMaxControlFrameBytes {
		return mutationControl{}, errMutationProtocol
	}
	encoded := make([]byte, n)
	if _, err := io.ReadFull(r, encoded); err != nil {
		return mutationControl{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var control mutationControl
	if err := decoder.Decode(&control); err != nil {
		return mutationControl{}, errMutationProtocol
	}
	if err := decoder.Decode(new(struct{})); !errors.Is(err, io.EOF) {
		return mutationControl{}, errMutationProtocol
	}
	return control, nil
}

func writeAll(w io.Writer, value []byte) error {
	for len(value) != 0 {
		n, err := w.Write(value)
		if n > 0 {
			value = value[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func validMutationOperation(operation mutationOperation) bool {
	return operation == mutationOperationCreate || operation == mutationOperationReplace
}
func validMutationPhase(phase mutationPhase) bool {
	switch phase {
	case mutationPhaseRequest, mutationPhaseStageCreated, mutationPhaseStageAck, mutationPhaseStageAnnounced, mutationPhasePayloadComplete, mutationPhaseCommit, mutationPhaseTerminal:
		return true
	}
	return false
}
func validMutationTerminal(terminal mutationTerminal) bool {
	return terminal == mutationTerminalCommitted || terminal == mutationTerminalRefused || terminal == mutationTerminalError || terminal == mutationTerminalUncertain
}

func validMutationTerminalCode(terminal mutationTerminal, code mutationTerminalCode) bool {
	switch terminal {
	case mutationTerminalCommitted:
		return code == mutationTerminalCodeCommitted
	case mutationTerminalRefused:
		return code == mutationTerminalCodeSourceChanged || code == mutationTerminalCodeDestinationExists || code == mutationTerminalCodeCanceled
	case mutationTerminalError:
		return code == mutationTerminalCodeIO || code == mutationTerminalCodeUnsupported
	case mutationTerminalUncertain:
		return code == mutationTerminalCodeUncertain
	}
	return false
}

func validMutationFDRoles(operation mutationOperation, roles mutationFDRoles) bool {
	if roles&^(mutationFDControl|mutationFDRoot|mutationFDParent|mutationFDSource) != 0 || roles&mutationFDRequired != mutationFDRequired {
		return false
	}
	return (operation == mutationOperationReplace) == (roles&mutationFDSource != 0)
}

func validMutationStageLeaf(leaf string) bool {
	const prefix = ".pmcg-stage-"
	if !strings.HasPrefix(leaf, prefix) || len(leaf) != len(prefix)+32 {
		return false
	}
	for _, char := range leaf[len(prefix):] {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func validMutationTargetLeaf(leaf string) bool {
	return leaf != "" && !strings.HasPrefix(leaf, ".") && !strings.ContainsAny(leaf, "/\\\x00") && leaf != "." && leaf != ".."
}

func validExpectedSource(operation mutationOperation, stamp mutationRawStamp) bool {
	if operation == mutationOperationCreate {
		return stamp == (mutationRawStamp{})
	}
	return validMutationRawStamp(stamp)
}

func validMutationRawStamp(stamp mutationRawStamp) bool {
	return stamp.Device != 0 && stamp.Inode != 0 && stamp.Mode != 0 && stamp.NLink != 0 && stamp.Size >= 0 && stamp.MtimeNsec >= 0 && stamp.MtimeNsec < 1_000_000_000 && stamp.CtimeNsec >= 0 && stamp.CtimeNsec < 1_000_000_000
}

func validMutationToken(token string) bool {
	if len(token) != 32 {
		return false
	}
	for _, char := range token {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func (c mutationControl) String() string { return fmt.Sprintf("%s/%s", c.Operation, c.Phase) }
