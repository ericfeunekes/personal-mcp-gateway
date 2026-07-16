package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"personal-mcp-gateway/internal/releaseactivation"
)

const updateRaceHelperEnv = "PERSONAL_MCP_GATEWAY_UPDATE_RACE_HELPER"

// TestUpdateRaceHelperProcess is an OS-process entrypoint. The parent gives it
// only temporary roots; it never discovers or mutates the real per-user store.
func TestUpdateRaceHelperProcess(t *testing.T) {
	if os.Getenv(updateRaceHelperEnv) != "1" {
		return
	}
	args := helperArgs(os.Args)
	if len(args) == 0 {
		_, _ = fmt.Fprintln(os.Stderr, "missing helper mode")
		os.Exit(2)
	}
	if err := runUpdateRaceHelper(args); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}

func TestUpdateAndPrepareSerializeAcrossProcesses(t *testing.T) {
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("update owns lock before prepare", func(t *testing.T) {
		fixture := newUpdateRaceFixture(t, realGit)
		barrier := newFIFOBarrier(t, fixture.root)
		shimDir := installMergeBarrierShim(t, fixture.root, realGit)
		update := startUpdateRaceHelper(t, []string{
			"update", fixture.storeRoot, strconv.Itoa(os.Geteuid()), fixture.repo,
			fixture.initialHead, fixture.remoteHead, fixture.log,
		}, "PATH="+shimDir+":"+os.Getenv("PATH"), "MERGE_READY_FIFO="+barrier.ready,
			"MERGE_RELEASE_FIFO="+barrier.release, "RACE_LOG="+fixture.log)

		barrier.waitReady(t, update)
		assertLogOrder(t, fixture.log, "update_before", "merge_before")
		assertStoreBusyAndState(t, fixture.storeRoot, nil)
		assertGitState(t, realGit, fixture.repo, fixture.initialHead, "initial\n")
		// FETCH_HEAD is intentionally mutable and outside the lifecycle lock.
		// Rewriting it while merge is blocked must not retarget this update,
		// which is bound to the exact object ID passed to the controller.
		if err := os.WriteFile(filepath.Join(fixture.repo, ".git", "FETCH_HEAD"), []byte(fixture.initialHead+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		contender := startUpdateRaceHelper(t, []string{
			"prepare", fixture.storeRoot, strconv.Itoa(os.Geteuid()), fixture.candidate,
			fixture.authority, fixture.log,
		})
		if err := contender.Wait(); err == nil {
			t.Fatal("prepare contender unexpectedly acquired update-owned lock")
		}
		assertLogOrder(t, fixture.log, "prepare_before", "prepare_error=busy")
		assertStoreBusyAndState(t, fixture.storeRoot, nil)
		assertGitState(t, realGit, fixture.repo, fixture.initialHead, "initial\n")

		barrier.releaseChild(t)
		if err := update.Wait(); err != nil {
			t.Fatalf("update helper: %v\n%s", err, update.stderr.String())
		}
		assertLogOrder(t, fixture.log, "merge_before", "merge_after", "update_after")
		assertGitState(t, realGit, fixture.repo, fixture.remoteHead, "remote\n")
		store, _ := releaseactivation.NewStoreAt(fixture.storeRoot, os.Geteuid())
		if active, inspectErr := store.Inspect(); inspectErr != nil || active != nil {
			t.Fatalf("active after update = %#v, %v; want clear", active, inspectErr)
		}
	})

	t.Run("prepare owns lock before update", func(t *testing.T) {
		fixture := newUpdateRaceFixture(t, realGit)
		barrier := newFIFOBarrier(t, fixture.root)
		prepare := startUpdateRaceHelper(t, []string{
			"prepare-hold", fixture.storeRoot, strconv.Itoa(os.Geteuid()), fixture.candidate,
			fixture.authority, fixture.log, barrier.ready, barrier.release,
		})
		barrier.waitReady(t, prepare)
		assertLogOrder(t, fixture.log, "prepare_before", "prepare_published")
		assertStoreBusyAndState(t, fixture.storeRoot, ptrState(releaseactivation.StatePrepared))
		assertGitState(t, realGit, fixture.repo, fixture.initialHead, "initial\n")

		update := startUpdateRaceHelper(t, []string{
			"update", fixture.storeRoot, strconv.Itoa(os.Geteuid()), fixture.repo,
			fixture.initialHead, fixture.remoteHead, fixture.log,
		})
		if err := update.Wait(); err == nil {
			t.Fatal("update contender unexpectedly acquired prepare-owned lock")
		}
		assertLogOrder(t, fixture.log, "update_before", "update_error=busy")
		assertStoreBusyAndState(t, fixture.storeRoot, ptrState(releaseactivation.StatePrepared))
		assertGitState(t, realGit, fixture.repo, fixture.initialHead, "initial\n")

		barrier.releaseChild(t)
		if err := prepare.Wait(); err != nil {
			t.Fatalf("prepare helper: %v\n%s", err, prepare.stderr.String())
		}
		assertLogOrder(t, fixture.log, "prepare_published", "prepare_after")
		store, _ := releaseactivation.NewStoreAt(fixture.storeRoot, os.Geteuid())
		active, inspectErr := store.Inspect()
		if inspectErr != nil || active == nil || active.State != releaseactivation.StatePrepared {
			t.Fatalf("active after prepare = %#v, %v", active, inspectErr)
		}
		assertGitState(t, realGit, fixture.repo, fixture.initialHead, "initial\n")
	})
}

type updateRaceFixture struct {
	root, storeRoot, repo, log, candidate, authority, initialHead, remoteHead string
}

func newUpdateRaceFixture(t *testing.T, git string) updateRaceFixture {
	t.Helper()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	repo := filepath.Join(root, "repo")
	updater := filepath.Join(root, "updater")
	runGit(t, git, "init", "--bare", remote)
	runGit(t, git, "init", "-b", "main", repo)
	runGit(t, git, "-C", repo, "config", "user.email", "test@example.invalid")
	runGit(t, git, "-C", repo, "config", "user.name", "Release Test")
	writeRaceFile(t, filepath.Join(repo, "payload.txt"), "initial\n", 0o600)
	runGit(t, git, "-C", repo, "add", "payload.txt")
	runGit(t, git, "-C", repo, "commit", "-m", "initial")
	runGit(t, git, "-C", repo, "remote", "add", "origin", remote)
	runGit(t, git, "-C", repo, "push", "-u", "origin", "main")
	initial := strings.TrimSpace(runGit(t, git, "-C", repo, "rev-parse", "HEAD"))
	runGit(t, git, "clone", "--branch", "main", remote, updater)
	runGit(t, git, "-C", updater, "config", "user.email", "test@example.invalid")
	runGit(t, git, "-C", updater, "config", "user.name", "Release Test")
	writeRaceFile(t, filepath.Join(updater, "payload.txt"), "remote\n", 0o600)
	runGit(t, git, "-C", updater, "add", "payload.txt")
	runGit(t, git, "-C", updater, "commit", "-m", "remote")
	runGit(t, git, "-C", updater, "push", "origin", "main")
	runGit(t, git, "-C", repo, "fetch", "origin", "main")
	remoteHead := strings.TrimSpace(runGit(t, git, "-C", repo, "rev-parse", "origin/main"))
	candidate := filepath.Join(root, "candidate")
	authority := filepath.Join(root, "authority")
	writeRaceFile(t, candidate, "candidate", 0o700)
	writeRaceFile(t, authority, "authority", 0o700)
	return updateRaceFixture{root: root, storeRoot: filepath.Join(root, "state"), repo: repo,
		log: filepath.Join(root, "race.log"), candidate: candidate, authority: authority,
		initialHead: initial, remoteHead: remoteHead}
}

func runUpdateRaceHelper(args []string) error {
	uid, err := strconv.Atoi(args[2])
	if err != nil {
		return err
	}
	switch args[0] {
	case "update":
		if len(args) != 7 {
			return errors.New("bad update helper arguments")
		}
		appendRaceLog(args[6], "update_before")
		store, err := releaseactivation.NewStoreAt(args[1], uid)
		if err != nil {
			return err
		}
		manager := &releaseactivation.Manager{Store: store, Runtime: updateRaceRuntime{}, ControllerPath: "test-update-controller"}
		_, err = execute(context.Background(), []string{
			"update-after-fetch", "--repo", args[3], "--expected-head", args[4], "--expected-remote-oid", args[5],
		}, dependencies{
			manager: manager,
			uid:     uid,
			home:    filepath.Join(filepath.Dir(args[3]), "unused-home"),
			// This helper proves process-lock ordering, not the production Git
			// latency budget. All-package test contention can otherwise consume
			// the five-second production child deadline before the FIFO barrier.
			gitWait: 15 * time.Second,
		})
		if err != nil {
			appendRaceLog(args[6], "update_error="+string(releaseactivation.SanitizedError(err).Code))
			return err
		}
		appendRaceLog(args[6], "update_after")
		return nil
	case "prepare", "prepare-hold":
		minimum := 6
		if len(args) < minimum {
			return errors.New("bad prepare helper arguments")
		}
		log := args[5]
		appendRaceLog(log, "prepare_before")
		var store *releaseactivation.Store
		if args[0] == "prepare-hold" {
			if len(args) != 8 {
				return errors.New("bad prepare hold arguments")
			}
			store, err = releaseactivation.NewStoreAtWithHook(args[1], uid, func(point releaseactivation.StoreHookPoint) error {
				if point == releaseactivation.StoreAfterActivePublish {
					appendRaceLog(log, "prepare_published")
					return signalAndWaitFIFO(args[6], args[7])
				}
				return nil
			})
		} else {
			store, err = releaseactivation.NewStoreAt(args[1], uid)
		}
		if err != nil {
			return err
		}
		manager := &releaseactivation.Manager{Store: store, Runtime: prepareRaceRuntime{}, ControllerPath: args[4]}
		prepareArgs, deps, err := prepareRaceCLI(filepath.Dir(args[3]), uid, args[3], args[4], manager)
		if err != nil {
			return err
		}
		if _, err := execute(context.Background(), prepareArgs, deps); err != nil {
			appendRaceLog(log, "prepare_error="+string(releaseactivation.SanitizedError(err).Code))
			return err
		}
		if args[0] == "prepare" {
			appendRaceLog(log, "prepare_published")
		}
		appendRaceLog(log, "prepare_after")
		return nil
	default:
		return errors.New("unknown helper mode")
	}
}

// updateRaceRuntime satisfies Manager's complete dependency contract. The
// update path exercises only WithClear, so any lifecycle effect is a test bug.
type updateRaceRuntime struct{}

func (updateRaceRuntime) Observe(context.Context, releaseactivation.Manifest, string, releaseactivation.RuntimeArtifacts) (releaseactivation.Observed, error) {
	return releaseactivation.Observed{}, errors.New("unexpected observe")
}
func (updateRaceRuntime) InstallCandidate(context.Context, releaseactivation.Manifest, releaseactivation.RuntimeArtifacts) error {
	return errors.New("unexpected install")
}
func (updateRaceRuntime) Restart(context.Context, releaseactivation.Manifest) error {
	return errors.New("unexpected restart")
}
func (updateRaceRuntime) WaitReady(context.Context, releaseactivation.Manifest) error {
	return errors.New("unexpected readiness")
}
func (updateRaceRuntime) RestorePrevious(context.Context, releaseactivation.Manifest, releaseactivation.RuntimeArtifacts) error {
	return errors.New("unexpected restore")
}
func (updateRaceRuntime) Bootout(context.Context, releaseactivation.Manifest) error {
	return errors.New("unexpected bootout")
}
func (updateRaceRuntime) ConfirmUnloaded(context.Context, releaseactivation.Manifest) (bool, error) {
	return false, errors.New("unexpected unload confirmation")
}
func (updateRaceRuntime) RemoveTarget(context.Context, releaseactivation.Manifest) error {
	return errors.New("unexpected target removal")
}
func (updateRaceRuntime) InvokeInstallAdapter(context.Context, string, ...string) error {
	return errors.New("unexpected install adapter")
}
func (updateRaceRuntime) InvokeUninstallAdapter(context.Context, string, ...string) error {
	return errors.New("unexpected uninstall adapter")
}

type prepareRaceRuntime struct{ updateRaceRuntime }

func (prepareRaceRuntime) Observe(_ context.Context, manifest releaseactivation.Manifest, controller string, artifacts releaseactivation.RuntimeArtifacts) (releaseactivation.Observed, error) {
	candidate, err := releaseactivation.HashRegular(artifacts.Candidate)
	if err != nil {
		return releaseactivation.Observed{}, err
	}
	authority, err := releaseactivation.HashRegular(artifacts.Authority)
	if err != nil {
		return releaseactivation.Observed{}, err
	}
	controllerHash, err := releaseactivation.HashRegular(controller)
	if err != nil {
		return releaseactivation.Observed{}, err
	}
	return releaseactivation.Observed{
		CandidateSHA256: candidate, AuthorityArtifactSHA256: authority, ControllerSHA256: controllerHash,
		PlistSHA256: manifest.PlistSHA256, WrapperSHA256: manifest.WrapperSHA256,
		MCPWrapperSHA256: manifest.MCPWrapperSHA256, EnvironmentSHA256: manifest.EnvironmentSHA256,
		SupervisorUnloaded: true,
	}, nil
}

func prepareRaceCLI(root string, uid int, candidate, authority string, manager *releaseactivation.Manager) ([]string, dependencies, error) {
	home := filepath.Join(root, "prepare-home")
	repo := filepath.Join(root, "prepare-repo")
	label := "com.example.release-race"
	paths := map[string]struct {
		content string
		mode    os.FileMode
	}{
		filepath.Join(home, "Library", "LaunchAgents", label+".plist"): {"<plist/>\n", 0o600},
		filepath.Join(repo, "scripts", "run-obsidian-tunnel.sh"):       {"#!/bin/sh\n", 0o700},
		filepath.Join(repo, "scripts", "run-obsidian-mcp-stdio.sh"):    {"#!/bin/sh\n", 0o700},
		filepath.Join(root, "prepare.env"):                             {"BOUND=1\n", 0o600},
	}
	for path, file := range paths {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, dependencies{}, err
		}
		if err := os.WriteFile(path, []byte(file.content), file.mode); err != nil {
			return nil, dependencies{}, err
		}
	}
	candidateSHA256, err := releaseactivation.HashRegular(candidate)
	if err != nil {
		return nil, dependencies{}, err
	}
	args := []string{"prepare", "--commit", strings.Repeat("b", 40), "--candidate-sha256", candidateSHA256, "--dependency-sha256", strings.Repeat("9", 64),
		"--candidate", candidate, "--authority", authority,
		"--target", filepath.Join(root, "target"), "--label", label, "--repo-root", repo,
		"--environment", filepath.Join(root, "prepare.env"), "--health-url-file", filepath.Join(root, "health-url"),
		"--ready-timeout-seconds", "5", "--ready-poll-milliseconds", "50"}
	return args, dependencies{manager: manager, uid: uid, home: home}, nil
}

type fifoBarrier struct{ ready, release string }

func newFIFOBarrier(t *testing.T, root string) fifoBarrier {
	t.Helper()
	barrier := fifoBarrier{filepath.Join(root, "ready.fifo"), filepath.Join(root, "release.fifo")}
	for _, path := range []string{barrier.ready, barrier.release} {
		if err := unix.Mkfifo(path, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return barrier
}

func (b fifoBarrier) waitReady(t *testing.T, process *updateRaceProcess) {
	t.Helper()
	file, err := os.OpenFile(b.ready, os.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		one := []byte{0}
		read, readErr := file.Read(one)
		if read == 1 {
			if one[0] != '1' {
				t.Fatalf("barrier ready byte = %q", one)
			}
			return
		}
		if readErr != nil && !errors.Is(readErr, unix.EAGAIN) && !errors.Is(readErr, io.EOF) {
			t.Fatalf("barrier ready: %v", readErr)
		}
		select {
		case <-process.done:
			t.Fatalf("helper exited before barrier: %v\n%s", process.waitErr, process.stderr.String())
		case <-deadline.C:
			_ = process.cmd.Process.Kill()
			<-process.done
			t.Fatalf("helper did not reach barrier\n%s", process.stderr.String())
		case <-poll.C:
		}
	}
}

func (b fifoBarrier) releaseChild(t *testing.T) {
	t.Helper()
	file, err := os.OpenFile(b.release, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.Write([]byte("1\n")); err != nil {
		t.Fatal(err)
	}
}

func signalAndWaitFIFO(ready, release string) error {
	w, err := os.OpenFile(ready, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_, err = w.Write([]byte("1"))
	_ = w.Close()
	if err != nil {
		return err
	}
	r, err := os.OpenFile(release, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer r.Close()
	var one [1]byte
	_, err = r.Read(one[:])
	return err
}

func installMergeBarrierShim(t *testing.T, root, realGit string) string {
	t.Helper()
	dir := filepath.Join(root, "shim")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
set -eu
case " $* " in
  *" merge --ff-only "*)
    printf '%s\n' merge_before >>"$RACE_LOG"
    printf 1 >"$MERGE_READY_FIFO"
    IFS= read -r _ <"$MERGE_RELEASE_FIFO"
    set +e
    "` + realGit + `" "$@"
    status=$?
    set -e
    printf '%s\n' merge_after >>"$RACE_LOG"
    exit "$status"
    ;;
esac
exec "` + realGit + `" "$@"
`
	writeRaceFile(t, filepath.Join(dir, "git"), script, 0o700)
	return dir
}

type updateRaceProcess struct {
	cmd     *exec.Cmd
	stderr  strings.Builder
	done    chan struct{}
	waitErr error
}

func (p *updateRaceProcess) Wait() error {
	<-p.done
	return p.waitErr
}

func startUpdateRaceHelper(t *testing.T, args []string, extraEnv ...string) *updateRaceProcess {
	t.Helper()
	commandArgs := []string{"-test.run=^TestUpdateRaceHelperProcess$", "--"}
	commandArgs = append(commandArgs, args...)
	process := &updateRaceProcess{cmd: exec.Command(os.Args[0], commandArgs...), done: make(chan struct{})}
	process.cmd.Env = append(os.Environ(), append([]string{updateRaceHelperEnv + "=1"}, extraEnv...)...)
	process.cmd.Stderr = &process.stderr
	if err := process.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		process.waitErr = process.cmd.Wait()
		close(process.done)
	}()
	t.Cleanup(func() {
		select {
		case <-process.done:
		default:
			_ = process.cmd.Process.Kill()
			<-process.done
		}
	})
	return process
}

func helperArgs(args []string) []string {
	for index, arg := range args {
		if arg == "--" {
			return args[index+1:]
		}
	}
	return nil
}

func appendRaceLog(path, line string) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err == nil {
		_, _ = fmt.Fprintln(file, line)
		_ = file.Close()
	}
}

func runGit(t *testing.T, git string, args ...string) string {
	t.Helper()
	output, err := exec.Command(git, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}
func writeRaceFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}
func ptrState(state releaseactivation.State) *releaseactivation.State { return &state }

func assertStoreBusyAndState(t *testing.T, root string, want *releaseactivation.State) {
	t.Helper()
	store, _ := releaseactivation.NewStoreAt(root, os.Geteuid())
	if locked, err := store.Acquire(); !errors.Is(err, releaseactivation.ErrBusy) {
		if locked != nil {
			_ = locked.Close()
		}
		t.Fatalf("Acquire while owner blocked = %v, want busy", err)
	}
	active, err := store.Inspect()
	if err != nil {
		t.Fatal(err)
	}
	if want == nil && active != nil {
		t.Fatalf("active = %#v, want clear", active)
	}
	if want != nil && (active == nil || active.State != *want) {
		t.Fatalf("active = %#v, want %s", active, *want)
	}
}

func assertGitState(t *testing.T, git, repo, wantHead, wantTree string) {
	t.Helper()
	head := strings.TrimSpace(runGit(t, git, "-C", repo, "rev-parse", "HEAD"))
	data, err := os.ReadFile(filepath.Join(repo, "payload.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if head != wantHead || string(data) != wantTree {
		t.Fatalf("HEAD/tree = %s/%q, want %s/%q", head, data, wantHead, wantTree)
	}
}

func assertLogOrder(t *testing.T, path string, entries ...string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cursor := 0
	for _, entry := range entries {
		index := strings.Index(string(data[cursor:]), entry)
		if index < 0 {
			t.Fatalf("log %q missing ordered %q", data, entries)
		}
		cursor += index + len(entry)
	}
}
