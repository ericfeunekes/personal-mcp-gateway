//go:build darwin

package fsx

import (
	"context"
	"errors"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// mutationHelperArgument selects the private same-binary helper mode before
// ordinary gateway startup. cmd/gateway dispatches it before parsing config.
const mutationHelperArgument = "--pmcg-mutation-helper-v1"

type mutationHelperHooks struct {
	afterStageCreation        func()
	executableIdentity        func() (mutationRawStamp, error)
	writeStage                func(*os.File, []byte) error
	afterCommitAccepted       func()
	beforeFinalRevalidation   func()
	afterFinalRevalidation    func()
	afterEffectStamp          func(int) (mutationRawStamp, error)
	afterEffectBeforeTerminal func()
	renameReplace             func(int, string, int, string) error
}

var mutationHelperTestHooks *mutationHelperHooks

var mutationRenameExclusive = func(fromFD int, from string, toFD int, to string) error {
	return unix.RenameatxNp(fromFD, from, toFD, to, unix.RENAME_EXCL)
}

// RunMutationHelper runs the private helper mode using only its fixed inherited
// descriptor table. It deliberately accepts neither paths nor configuration.
func RunMutationHelper() int {
	if err := runMutationHelper(); err != nil {
		return 1
	}
	return 0
}

func RunMutationHelperMode(args []string) (handled bool, code int) {
	if len(args) != 1 || args[0] != mutationHelperArgument {
		return false, 0
	}
	return true, RunMutationHelper()
}

func runMutationHelper() error {
	if err := mutationCloseUnexpectedInheritedFDs(); err != nil {
		return err
	}
	control := os.NewFile(uintptr(3), "mutation-control")
	root := os.NewFile(uintptr(4), "mutation-root")
	parent := os.NewFile(uintptr(5), "mutation-parent")
	if control == nil || root == nil || parent == nil {
		return errMutationProtocol
	}
	defer control.Close()
	defer root.Close()
	defer parent.Close()
	var source *os.File
	if _, err := unix.FcntlInt(uintptr(6), unix.F_GETFD, 0); err == nil {
		source = os.NewFile(uintptr(6), "mutation-source")
	}
	return runMutationHelperFiles(control, root, parent, source)
}

func mutationCloseUnexpectedInheritedFDs() error {
	// ExtraFiles assigns the authority descriptors to 3..6, but exec cannot
	// protect against an ambient non-CLOEXEC descriptor opened by foreign code.
	// Remove only descriptors that survived exec; runtime-created CLOEXEC
	// descriptors are implementation plumbing rather than inherited authority.
	dir, err := os.Open("/dev/fd")
	if err != nil {
		return errMutationProtocol
	}
	names, readErr := dir.Readdirnames(-1)
	closeErr := dir.Close()
	if readErr != nil || closeErr != nil {
		return errMutationProtocol
	}
	for _, name := range names {
		fd, err := strconv.Atoi(name)
		if err != nil || fd <= 6 {
			continue
		}
		flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		if err == nil && flags&unix.FD_CLOEXEC == 0 {
			_ = unix.Close(fd)
		}
	}
	return nil
}

func runMutationHelperFiles(control, root, parent, source *os.File) error {
	if err := mutationHelperFstatDirectory(int(root.Fd())); err != nil {
		return err
	}
	if err := mutationHelperFstatDirectory(int(parent.Fd())); err != nil {
		return err
	}
	protocol := newMutationProtocolHelper(control, control)
	request, err := protocol.ReceiveControl()
	if err != nil {
		return err
	}
	if request.Operation == mutationOperationReplace {
		if source == nil {
			return errMutationProtocol
		}
		defer source.Close()
		if err := mutationHelperFstatRegular(int(source.Fd()), request.ExpectedSource); err != nil {
			return err
		}
	} else if source != nil {
		_ = source.Close()
		return errMutationProtocol
	}

	stageFD, err := unix.Openat(int(parent.Fd()), request.StageLeaf, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	stage := os.NewFile(uintptr(stageFD), "mutation-stage")
	defer stage.Close()
	if err := unix.Fchmod(stageFD, 0o600); err != nil {
		return err
	}
	stageStamp, err := mutationRawStampFromFD(stageFD)
	if err != nil || !validMutationStageForCleanup(stageStamp, 0) {
		return errMutationProtocol
	}
	if hooks := mutationHelperTestHooks; hooks != nil && hooks.afterStageCreation != nil {
		hooks.afterStageCreation()
	}
	completed := false
	cleanup := func() bool {
		if !completed {
			return mutationParentCleanupStage(int(parent.Fd()), request.StageLeaf, stageStamp)
		}
		return true
	}
	defer func() { _ = cleanup() }()
	executableIdentity, err := mutationHelperExecutableIdentity()
	if err != nil {
		return err
	}
	if err := protocol.SendControl(mutationControl{Version: mutationProtocolVersion, Operation: request.Operation, Phase: mutationPhaseStageCreated, ExecutableIdentity: executableIdentity}); err != nil {
		return err
	}
	if _, err := protocol.ReceiveControl(); err != nil {
		return err
	}
	if err := protocol.SendControl(mutationControl{Version: mutationProtocolVersion, Operation: request.Operation, Phase: mutationPhaseStageAnnounced, StageIdentity: stageStamp}); err != nil {
		return err
	}
	payload := make([]byte, request.PayloadBytes)
	if err := protocol.ReceivePayload(payload); err != nil {
		return err
	}
	if err := mutationHelperWriteStage(stage, payload); err != nil {
		return err
	}
	finalStamp, err := mutationRawStampFromFD(stageFD)
	if err != nil {
		return err
	}
	if !validMutationStageForCleanup(finalStamp, int64(request.PayloadBytes)) {
		return errMutationProtocol
	}
	stageStamp = finalStamp
	if err := protocol.SendControl(mutationControl{Version: mutationProtocolVersion, Operation: request.Operation, Phase: mutationPhasePayloadComplete, StageIdentity: finalStamp}); err != nil {
		return err
	}
	if _, err := protocol.ReceiveControl(); err != nil {
		return err
	}
	if hooks := mutationHelperTestHooks; hooks != nil && hooks.afterCommitAccepted != nil {
		hooks.afterCommitAccepted()
	}

	var terminal mutationControl
	if hooks := mutationHelperTestHooks; hooks != nil && hooks.beforeFinalRevalidation != nil {
		hooks.beforeFinalRevalidation()
	}
	if request.Operation == mutationOperationReplace {
		if err := mutationHelperFstatRegular(int(source.Fd()), request.ExpectedSource); err != nil {
			terminal = mutationRefusal(request.Operation, mutationTerminalCodeSourceChanged)
		} else if err := mutationHelperNameMatches(int(parent.Fd()), request.TargetLeaf, request.ExpectedSource); err != nil {
			terminal = mutationRefusal(request.Operation, mutationTerminalCodeSourceChanged)
		} else if hooks := mutationHelperTestHooks; hooks != nil && hooks.afterFinalRevalidation != nil {
			hooks.afterFinalRevalidation()
			if err := mutationHelperRenameReplace(int(parent.Fd()), request.StageLeaf, int(parent.Fd()), request.TargetLeaf); err != nil {
				terminal = mutationError(request.Operation)
			} else if committedStamp, err := mutationHelperEffectStamp(stageFD); err != nil {
				completed = true
				terminal = mutationUncertain(request.Operation)
			} else {
				completed = true
				terminal = mutationCommitted(request.Operation, committedStamp)
			}
		} else if err := mutationHelperRenameReplace(int(parent.Fd()), request.StageLeaf, int(parent.Fd()), request.TargetLeaf); err != nil {
			terminal = mutationError(request.Operation)
		} else if committedStamp, err := mutationHelperEffectStamp(stageFD); err != nil {
			completed = true
			terminal = mutationUncertain(request.Operation)
		} else {
			completed = true
			terminal = mutationCommitted(request.Operation, committedStamp)
		}
	} else if err := mutationHelperDestinationAbsent(int(parent.Fd()), request.TargetLeaf); err != nil {
		terminal = mutationRefusal(request.Operation, mutationTerminalCodeDestinationExists)
	} else if hooks := mutationHelperTestHooks; hooks != nil && hooks.afterFinalRevalidation != nil {
		hooks.afterFinalRevalidation()
		if err := mutationRenameExclusive(int(parent.Fd()), request.StageLeaf, int(parent.Fd()), request.TargetLeaf); err != nil {
			if errors.Is(err, unix.EEXIST) {
				terminal = mutationRefusal(request.Operation, mutationTerminalCodeDestinationExists)
			} else if errors.Is(err, unix.ENOTSUP) {
				terminal = mutationControl{Version: mutationProtocolVersion, Operation: request.Operation, Phase: mutationPhaseTerminal, Terminal: mutationTerminalError, TerminalCode: mutationTerminalCodeUnsupported}
			} else {
				terminal = mutationError(request.Operation)
			}
		} else if committedStamp, err := mutationHelperEffectStamp(stageFD); err != nil {
			completed = true
			terminal = mutationUncertain(request.Operation)
		} else {
			completed = true
			terminal = mutationCommitted(request.Operation, committedStamp)
		}
	} else if err := mutationRenameExclusive(int(parent.Fd()), request.StageLeaf, int(parent.Fd()), request.TargetLeaf); err != nil {
		if errors.Is(err, unix.EEXIST) {
			terminal = mutationRefusal(request.Operation, mutationTerminalCodeDestinationExists)
		} else if errors.Is(err, unix.ENOTSUP) {
			terminal = mutationControl{Version: mutationProtocolVersion, Operation: request.Operation, Phase: mutationPhaseTerminal, Terminal: mutationTerminalError, TerminalCode: mutationTerminalCodeUnsupported}
		} else {
			terminal = mutationError(request.Operation)
		}
	} else if committedStamp, err := mutationHelperEffectStamp(stageFD); err != nil {
		completed = true
		terminal = mutationUncertain(request.Operation)
	} else {
		completed = true
		terminal = mutationCommitted(request.Operation, committedStamp)
	}
	if !completed {
		if !cleanup() {
			return errMutationProtocol
		}
	} else if hooks := mutationHelperTestHooks; hooks != nil && hooks.afterEffectBeforeTerminal != nil {
		hooks.afterEffectBeforeTerminal()
	}
	return protocol.SendControl(terminal)
}

func mutationCommitted(operation mutationOperation, identity mutationRawStamp) mutationControl {
	return mutationControl{Version: mutationProtocolVersion, Operation: operation, Phase: mutationPhaseTerminal, StageIdentity: identity, Terminal: mutationTerminalCommitted, TerminalCode: mutationTerminalCodeCommitted}
}
func mutationRefusal(operation mutationOperation, code mutationTerminalCode) mutationControl {
	return mutationControl{Version: mutationProtocolVersion, Operation: operation, Phase: mutationPhaseTerminal, Terminal: mutationTerminalRefused, TerminalCode: code}
}
func mutationError(operation mutationOperation) mutationControl {
	return mutationControl{Version: mutationProtocolVersion, Operation: operation, Phase: mutationPhaseTerminal, Terminal: mutationTerminalError, TerminalCode: mutationTerminalCodeIO}
}
func mutationUncertain(operation mutationOperation) mutationControl {
	return mutationControl{Version: mutationProtocolVersion, Operation: operation, Phase: mutationPhaseTerminal, Terminal: mutationTerminalUncertain, TerminalCode: mutationTerminalCodeUncertain}
}

func mutationHelperEffectStamp(fd int) (mutationRawStamp, error) {
	if hooks := mutationHelperTestHooks; hooks != nil && hooks.afterEffectStamp != nil {
		return hooks.afterEffectStamp(fd)
	}
	return mutationRawStampFromFD(fd)
}

func mutationHelperExecutableIdentity() (mutationRawStamp, error) {
	if hooks := mutationHelperTestHooks; hooks != nil && hooks.executableIdentity != nil {
		return hooks.executableIdentity()
	}
	return mutationRunningExecutableIdentity()
}

func mutationHelperWriteStage(stage *os.File, payload []byte) error {
	if hooks := mutationHelperTestHooks; hooks != nil && hooks.writeStage != nil {
		return hooks.writeStage(stage, payload)
	}
	return writeAll(stage, payload)
}

func mutationHelperRenameReplace(fromFD int, from string, toFD int, to string) error {
	if hooks := mutationHelperTestHooks; hooks != nil && hooks.renameReplace != nil {
		return hooks.renameReplace(fromFD, from, toFD, to)
	}
	return unix.Renameat(fromFD, from, toFD, to)
}

func mutationRunningExecutableIdentity() (mutationRawStamp, error) {
	path, err := os.Executable()
	if err != nil {
		return mutationRawStamp{}, err
	}
	var stat unix.Stat_t
	if err := unix.Stat(path, &stat); err != nil {
		return mutationRawStamp{}, err
	}
	return mutationRawStampFromStat(&stat), nil
}

func mutationHelperFstatDirectory(fd int) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || uint32(stat.Mode)&unix.S_IFMT != unix.S_IFDIR {
		return errMutationProtocol
	}
	return nil
}
func mutationHelperFstatRegular(fd int, expected mutationRawStamp) error {
	stamp, err := mutationRawStampFromFD(fd)
	if err != nil || stamp != expected || stamp.Mode&unix.S_IFMT != unix.S_IFREG {
		return errMutationProtocol
	}
	return nil
}
func mutationHelperNameMatches(parent int, leaf string, expected mutationRawStamp) error {
	var stat unix.Stat_t
	if err := unix.Fstatat(parent, leaf, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	stamp := mutationRawStampFromStat(&stat)
	if stamp != expected {
		return errMutationProtocol
	}
	return nil
}
func mutationHelperDestinationAbsent(parent int, leaf string) error {
	match, err := matchMutationName(context.Background(), parent, leaf)
	if err != nil {
		return err
	}
	if match.state == mutationNameAbsent {
		return nil
	}
	return unix.EEXIST
}
func mutationRawStampFromFD(fd int) (mutationRawStamp, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return mutationRawStamp{}, err
	}
	return mutationRawStampFromStat(&stat), nil
}
func mutationRawStampFromStat(stat *unix.Stat_t) mutationRawStamp {
	return mutationRawStamp{Device: uint64(uint32(stat.Dev)), Inode: stat.Ino, Mode: uint32(stat.Mode), UID: stat.Uid, NLink: uint64(stat.Nlink), Size: stat.Size, MtimeSec: stat.Mtim.Sec, MtimeNsec: stat.Mtim.Nsec, CtimeSec: stat.Ctim.Sec, CtimeNsec: stat.Ctim.Nsec}
}

func validMutationStageForCleanup(stamp mutationRawStamp, expectedSize int64) bool {
	return stamp.Mode&unix.S_IFMT == unix.S_IFREG && stamp.UID == uint32(os.Geteuid()) && stamp.NLink == 1 && stamp.Mode&0o777 == 0o600 && stamp.Size == expectedSize
}
