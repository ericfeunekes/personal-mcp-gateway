//go:build darwin

package fsx

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"golang.org/x/sys/unix"
)

var mutationHelperSlots = make(chan struct{}, 4)

// mutationLauncherTestHooks are deterministic, package-local process seams.
// They are nil in production and are not configurable through the gateway.
type mutationLauncherHooks struct {
	afterStageCreated    func(*exec.Cmd)
	afterStageAnnounced  func(*exec.Cmd)
	afterPayloadComplete func(*exec.Cmd)
	afterCommitSent      func(*exec.Cmd)
	beforeCleanupCheck   func(int, string)
	cleanupUnlink        func(int, string) error
}

var mutationLauncherTestHooks *mutationLauncherHooks

var errMutationUncertain = errors.New("mutation helper outcome is uncertain")
var errMutationCleanup = errors.New("mutation helper cleanup is uncertain")

// stagedMutation is intentionally raw and private until Mutator integration
// exists. Descriptors are already confined/opened; no host paths are accepted.
type stagedMutation struct {
	Executable string
	Operation  mutationOperation
	Root       *os.File
	Parent     *os.File
	Source     *os.File
	TargetLeaf string
	Expected   mutationRawStamp
	Payload    []byte
}

type stagedMutationResult struct {
	Terminal      mutationControl
	StageIdentity mutationRawStamp
}

func runStagedMutation(ctx context.Context, request stagedMutation) (result stagedMutationResult, resultErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	hardDeadline := time.Now().Add(2 * time.Second)
	if callerDeadline, ok := ctx.Deadline(); ok && callerDeadline.Before(hardDeadline) {
		hardDeadline = callerDeadline
	}
	var cancel context.CancelFunc
	ctx, cancel = context.WithDeadline(ctx, hardDeadline)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return stagedMutationResult{}, err
	}
	if len(request.Payload) > mutationMaxPayloadBytes || !validMutationOperation(request.Operation) || request.Executable == "" || request.Root == nil || request.Parent == nil || !validMutationTargetLeaf(request.TargetLeaf) || !validExpectedSource(request.Operation, request.Expected) || (request.Operation == mutationOperationReplace && request.Source == nil) || (request.Operation == mutationOperationCreate && request.Source != nil) {
		return stagedMutationResult{}, errMutationProtocol
	}
	select {
	case mutationHelperSlots <- struct{}{}:
		defer func() { <-mutationHelperSlots }()
	case <-ctx.Done():
		return stagedMutationResult{}, ctx.Err()
	}
	stageLeaf, err := mutationStageLeaf()
	if err != nil {
		return stagedMutationResult{}, err
	}
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return stagedMutationResult{}, err
	}
	unix.CloseOnExec(fds[0])
	unix.CloseOnExec(fds[1])
	parentControl := os.NewFile(uintptr(fds[0]), "mutation-parent-control")
	childControl := os.NewFile(uintptr(fds[1]), "mutation-child-control")
	defer parentControl.Close()
	defer childControl.Close()
	interrupt := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = parentControl.SetDeadline(time.Now())
		case <-interrupt:
		}
	}()
	defer close(interrupt)
	cmd := exec.Command(request.Executable, mutationHelperArgument)
	devNull, err := os.OpenFile("/dev/null", os.O_RDWR, 0)
	if err != nil {
		return stagedMutationResult{}, err
	}
	defer devNull.Close()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devNull, devNull, devNull
	cmd.ExtraFiles = []*os.File{childControl, request.Root, request.Parent}
	if request.Operation == mutationOperationReplace {
		cmd.ExtraFiles = append(cmd.ExtraFiles, request.Source)
	}
	var executableBefore unix.Stat_t
	if err := unix.Stat(request.Executable, &executableBefore); err != nil {
		return stagedMutationResult{}, err
	}
	if err := cmd.Start(); err != nil {
		return stagedMutationResult{}, err
	}
	_ = childControl.Close()
	var executableAfter unix.Stat_t
	if err := unix.Stat(request.Executable, &executableAfter); err != nil || mutationRawStampFromStat(&executableBefore) != mutationRawStampFromStat(&executableAfter) {
		_ = parentControl.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return stagedMutationResult{}, errMutationProtocol
	}
	protocol, err := newMutationProtocolParent(parentControl, parentControl, request.Operation, mutationFDControl|mutationFDRoot|mutationFDParent)
	if request.Operation == mutationOperationReplace {
		protocol, err = newMutationProtocolParent(parentControl, parentControl, request.Operation, mutationFDControl|mutationFDRoot|mutationFDParent|mutationFDSource)
	}
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return stagedMutationResult{}, err
	}
	commitSent := false
	var stageIdentity mutationRawStamp
	cleanupAndWait := func() bool {
		_ = parentControl.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
		return mutationParentCleanupStage(int(request.Parent.Fd()), stageLeaf, stageIdentity)
	}
	defer func() {
		if !commitSent {
			if !cleanupAndWait() {
				result = stagedMutationResult{}
				resultErr = errMutationCleanup
			}
		}
	}()
	control := mutationControl{Version: mutationProtocolVersion, Operation: request.Operation, Phase: mutationPhaseRequest, FDRoles: mutationFDControl | mutationFDRoot | mutationFDParent, PayloadBytes: uint64(len(request.Payload)), TargetLeaf: request.TargetLeaf, StageLeaf: stageLeaf, ExpectedSource: request.Expected}
	if request.Operation == mutationOperationReplace {
		control.FDRoles |= mutationFDSource
	}
	if err := protocol.SendControl(control); err != nil {
		return stagedMutationResult{}, mutationPrecommitError(ctx, err)
	}
	started, err := protocol.ReceiveControl()
	if err != nil {
		return stagedMutationResult{}, mutationPrecommitError(ctx, err)
	}
	if started.ExecutableIdentity != mutationRawStampFromStat(&executableBefore) {
		return stagedMutationResult{}, errMutationProtocol
	}
	if hooks := mutationLauncherTestHooks; hooks != nil && hooks.afterStageCreated != nil {
		hooks.afterStageCreated(cmd)
	}
	if err := protocol.SendControl(mutationControl{Version: mutationProtocolVersion, Operation: request.Operation, Phase: mutationPhaseStageAck}); err != nil {
		return stagedMutationResult{}, mutationPrecommitError(ctx, err)
	}
	announced, err := protocol.ReceiveControl()
	if err != nil {
		return stagedMutationResult{}, mutationPrecommitError(ctx, err)
	}
	stageIdentity = announced.StageIdentity
	if hooks := mutationLauncherTestHooks; hooks != nil && hooks.afterStageAnnounced != nil {
		hooks.afterStageAnnounced(cmd)
	}
	if err := protocol.SendPayload(request.Payload); err != nil {
		return stagedMutationResult{}, mutationPrecommitError(ctx, err)
	}
	completed, err := protocol.ReceiveControl()
	if err != nil {
		return stagedMutationResult{}, mutationPrecommitError(ctx, err)
	}
	stageIdentity = completed.StageIdentity
	if hooks := mutationLauncherTestHooks; hooks != nil && hooks.afterPayloadComplete != nil {
		hooks.afterPayloadComplete(cmd)
	}
	if err := ctx.Err(); err != nil {
		return stagedMutationResult{}, err
	}
	token, err := mutationCommitToken()
	if err != nil {
		return stagedMutationResult{}, err
	}
	if err := protocol.SendControl(mutationControl{Version: mutationProtocolVersion, Operation: request.Operation, Phase: mutationPhaseCommit, CommitToken: token}); err != nil {
		return stagedMutationResult{}, err
	}
	commitSent = true
	if hooks := mutationLauncherTestHooks; hooks != nil && hooks.afterCommitSent != nil {
		hooks.afterCommitSent(cmd)
	}
	terminal, receiveErr := protocol.ReceiveControl()
	waitErr := mutationWaitHelper(ctx, cmd)
	if receiveErr != nil || waitErr != nil {
		_ = mutationParentCleanupStage(int(request.Parent.Fd()), stageLeaf, stageIdentity)
		return stagedMutationResult{}, errMutationUncertain
	}
	if terminal.Terminal != mutationTerminalCommitted && terminal.Terminal != mutationTerminalUncertain {
		if !mutationParentCleanupStage(int(request.Parent.Fd()), stageLeaf, stageIdentity) {
			return stagedMutationResult{}, errMutationCleanup
		}
	}
	return stagedMutationResult{Terminal: terminal, StageIdentity: terminal.StageIdentity}, nil
}

func mutationPrecommitError(ctx context.Context, err error) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	return err
}

func mutationWaitHelper(ctx context.Context, cmd *exec.Cmd) error {
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		return <-done
	}
}

// mutationParentCleanupStage only unlinks the exact implementation-private
// regular stage identity last announced by the helper. It intentionally leaves
// a mismatched name untouched rather than risking another writer's entry.
func mutationParentCleanupStage(parentFD int, leaf string, expected mutationRawStamp) bool {
	if hooks := mutationLauncherTestHooks; hooks != nil && hooks.beforeCleanupCheck != nil {
		hooks.beforeCleanupCheck(parentFD, leaf)
	}
	var stat unix.Stat_t
	if err := unix.Fstatat(parentFD, leaf, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return errors.Is(err, unix.ENOENT)
	}
	stamp := mutationRawStampFromStat(&stat)
	if !validMutationStageForCleanup(stamp, stamp.Size) || stamp.Size > mutationMaxPayloadBytes {
		return false
	}
	if expected != (mutationRawStamp{}) && !mutationSameStageIdentity(stamp, expected) {
		return true
	}
	unlink := func(parentFD int, leaf string) error { return unix.Unlinkat(parentFD, leaf, 0) }
	if hooks := mutationLauncherTestHooks; hooks != nil && hooks.cleanupUnlink != nil {
		unlink = hooks.cleanupUnlink
	}
	if err := unlink(parentFD, leaf); err != nil && !errors.Is(err, unix.ENOENT) {
		return false
	}
	return true
}

func mutationSameStageIdentity(actual, expected mutationRawStamp) bool {
	return actual.Device == expected.Device && actual.Inode == expected.Inode && actual.Mode == expected.Mode && actual.UID == expected.UID && actual.NLink == expected.NLink
}

func mutationStageLeaf() (string, error)   { return mutationHex(".pmcg-stage-", 16) }
func mutationCommitToken() (string, error) { return mutationHex("", 16) }
func mutationHex(prefix string, bytes int) (string, error) {
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s%x", prefix, b), nil
}
