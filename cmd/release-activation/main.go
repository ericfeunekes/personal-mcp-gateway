package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"personal-mcp-gateway/internal/releaseactivation"
)

type lifecycleManager interface {
	Status(context.Context) (*releaseactivation.Manifest, error)
	Prepare(context.Context, releaseactivation.PrepareRequest) (*releaseactivation.Manifest, error)
	Resume(context.Context, releaseactivation.ReleaseID) (*releaseactivation.Manifest, error)
	Accept(context.Context, releaseactivation.ReleaseID) (*releaseactivation.Manifest, error)
	Rollback(context.Context, releaseactivation.ReleaseID) (*releaseactivation.Manifest, error)
	WithClear(context.Context, func(context.Context, releaseactivation.Runtime) error) error
}

type dependencies struct {
	manager lifecycleManager
	uid     int
	home    string
	gitWait time.Duration
}

type usageError struct{ message string }

const (
	updateOverallTimeout = 30 * time.Second
	gitChildTimeout      = 5 * time.Second
)

func (e *usageError) Error() string { return e.message }

// guidanceError is the one public failure that deliberately carries bounded
// recovery records. The records are derived only from a validated manifest;
// child errors and caller-controlled paths never enter the public stream.
type guidanceError struct {
	manifest *releaseactivation.Manifest
	err      error
}

func (e *guidanceError) Error() string { return e.err.Error() }
func (e *guidanceError) Unwrap() error { return e.err }

func main() {
	_ = os.Unsetenv("CONTROL_PLANE_API_KEY")
	_ = os.Unsetenv("OPENAI_API_KEY")
	os.Exit(runMain(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

func runMain(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	deps, err := productionDependencies()
	if err != nil {
		writeFailure(stderr, releaseactivation.SanitizedError(err))
		return 1
	}
	return runWithDependencies(ctx, args, stdout, stderr, deps)
}

func runWithDependencies(ctx context.Context, args []string, stdout, stderr io.Writer, deps dependencies) int {
	records, err := execute(ctx, args, deps)
	if err == nil {
		for _, record := range records {
			_, _ = fmt.Fprintln(stdout, record)
		}
		return 0
	}
	var usage *usageError
	if errors.As(err, &usage) {
		_, _ = fmt.Fprintln(stderr, "error=usage message=invalid release command")
		return 2
	}
	writeFailure(stderr, releaseactivation.SanitizedError(err))
	var guidance *guidanceError
	if errors.As(err, &guidance) {
		for _, record := range manifestRecords(guidance.manifest) {
			_, _ = fmt.Fprintln(stderr, record)
		}
	}
	return 1
}

func productionDependencies() (dependencies, error) {
	executable, err := os.Executable()
	if err != nil {
		return dependencies{}, err
	}
	executable = filepath.Clean(executable)
	uid := os.Geteuid()
	account, err := user.LookupId(strconv.Itoa(uid))
	if err != nil || !filepath.IsAbs(account.HomeDir) {
		return dependencies{}, errors.New("effective user lookup failed")
	}
	var store *releaseactivation.Store
	if filepath.Base(executable) == "authority" && filepath.Base(filepath.Dir(executable)) == "active" {
		store, err = releaseactivation.NewStoreAt(filepath.Dir(filepath.Dir(executable)), uid)
	} else {
		store, err = releaseactivation.NewStore()
	}
	if err != nil {
		return dependencies{}, err
	}
	runtime := releaseactivation.NewOSRuntime()
	manager := &releaseactivation.Manager{Store: store, Runtime: runtime, ControllerPath: executable}
	return dependencies{manager: manager, uid: uid, home: filepath.Clean(account.HomeDir)}, nil
}

func execute(ctx context.Context, args []string, deps dependencies) ([]string, error) {
	if deps.manager == nil || len(args) == 0 {
		return nil, &usageError{}
	}
	switch args[0] {
	case "status":
		if len(args) != 1 {
			return nil, &usageError{}
		}
		manifest, err := deps.manager.Status(ctx)
		return manifestRecords(manifest), err
	case "prepare":
		request, err := parsePrepare(args[1:], deps)
		if err != nil {
			return nil, err
		}
		manifest, err := deps.manager.Prepare(ctx, request)
		return manifestRecords(manifest), err
	case "release":
		return releaseOrGuide(ctx, args[1:], deps)
	case "resume":
		if len(args) != 1 {
			return nil, &usageError{}
		}
		manifest, err := deps.manager.Resume(ctx, "")
		return manifestRecords(manifest), err
	case "accept":
		id, err := parseReleaseID(args[1:])
		if err != nil {
			return nil, err
		}
		manifest, err := deps.manager.Accept(ctx, id)
		return manifestRecords(manifest), err
	case "rollback":
		id, err := parseReleaseID(args[1:])
		if err != nil {
			return nil, err
		}
		manifest, err := deps.manager.Rollback(ctx, id)
		return manifestRecords(manifest), err
	case "update-after-fetch":
		request, err := parseUpdate(args[1:])
		if err != nil {
			return nil, err
		}
		updateCtx, cancel := context.WithTimeout(ctx, updateOverallTimeout)
		defer cancel()
		err = deps.manager.WithClear(updateCtx, func(ctx context.Context, _ releaseactivation.Runtime) error {
			return updateAfterFetch(ctx, request, deps.gitWait)
		})
		if err != nil {
			return nil, err
		}
		return []string{"state=clear action=source_updated"}, nil
	case "restart", "install-launchagent", "uninstall-launchagent":
		request, err := parseAdmin(args[0], args[1:], deps)
		if err != nil {
			return nil, err
		}
		err = deps.manager.WithClear(ctx, func(ctx context.Context, runtime releaseactivation.Runtime) error {
			return runAdmin(ctx, runtime, request)
		})
		if err != nil {
			return nil, err
		}
		return []string{"state=clear action=" + args[0]}, nil
	default:
		return nil, &usageError{}
	}
}

// releaseOrGuide is intentionally private. It gives the stable dispatcher one
// idempotent operation for both an interrupted prepared release and the final
// post-gate publication attempt. Prepare output is suppressed; only the final
// pending identity is returned on success.
func releaseOrGuide(ctx context.Context, prepareArgs []string, deps dependencies) ([]string, error) {
	manifest, err := deps.manager.Status(ctx)
	if err != nil {
		return nil, err
	}
	if manifest == nil {
		request, parseErr := parsePrepare(prepareArgs, deps)
		if parseErr != nil {
			return nil, parseErr
		}
		manifest, err = deps.manager.Prepare(ctx, request)
		if err != nil {
			// A compatible authority may have published while Status was clear.
			// Re-read once so the same operation can resume or give exact guidance.
			if releaseactivation.SanitizedError(err).Code != releaseactivation.ErrorStateConflict {
				return nil, err
			}
			manifest, err = deps.manager.Status(ctx)
			if err != nil {
				return nil, err
			}
			if manifest == nil {
				return nil, releaseactivation.ErrStateConflict
			}
		}
	}
	if manifest.State == releaseactivation.StatePrepared {
		resumed, resumeErr := deps.manager.Resume(ctx, "")
		if resumeErr != nil && resumed != nil && resumed.State != releaseactivation.StatePrepared &&
			releaseactivation.SanitizedError(resumeErr).Code == releaseactivation.ErrorStateConflict {
			return nil, &guidanceError{manifest: resumed, err: releaseactivation.ErrStateConflict}
		}
		return manifestRecords(resumed), resumeErr
	}
	return nil, &guidanceError{manifest: manifest, err: releaseactivation.ErrStateConflict}
}

func parsePrepare(args []string, deps dependencies) (releaseactivation.PrepareRequest, error) {
	set := flag.NewFlagSet("prepare", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	var request releaseactivation.PrepareRequest
	var repoRoot string
	set.StringVar(&request.Commit, "commit", "", "")
	set.StringVar(&request.CandidatePath, "candidate", "", "")
	set.StringVar(&request.AuthorityPath, "authority", "", "")
	set.StringVar(&request.TargetPath, "target", "", "")
	set.StringVar(&request.LaunchAgentLabel, "label", "", "")
	set.StringVar(&repoRoot, "repo-root", "", "")
	set.StringVar(&request.EnvironmentPath, "environment", "", "")
	set.StringVar(&request.HealthURLFile, "health-url-file", "", "")
	set.IntVar(&request.ReadyTimeoutSeconds, "ready-timeout-seconds", 45, "")
	set.IntVar(&request.ReadyPollMilliseconds, "ready-poll-milliseconds", 1000, "")
	if err := set.Parse(args); err != nil || set.NArg() != 0 {
		return request, &usageError{}
	}
	if !allNonempty(request.Commit, request.CandidatePath, request.AuthorityPath, request.TargetPath,
		request.LaunchAgentLabel, repoRoot, request.EnvironmentPath, request.HealthURLFile) {
		return request, &usageError{}
	}
	if !releaseactivation.ValidLaunchAgentLabel(request.LaunchAgentLabel) {
		return request, &usageError{}
	}
	for _, path := range []string{request.CandidatePath, request.AuthorityPath, request.TargetPath, repoRoot, request.EnvironmentPath, request.HealthURLFile} {
		if !filepath.IsAbs(path) {
			return request, &usageError{}
		}
	}
	repoRoot = filepath.Clean(repoRoot)
	request.EffectiveUID = deps.uid
	request.PlistPath = filepath.Join(deps.home, "Library", "LaunchAgents", request.LaunchAgentLabel+".plist")
	request.WrapperPath = filepath.Join(repoRoot, "scripts", "run-obsidian-tunnel.sh")
	request.MCPWrapperPath = filepath.Join(repoRoot, "scripts", "run-obsidian-mcp-stdio.sh")
	request.StdoutPath = filepath.Join(deps.home, "Library", "Logs", "personal-mcp-gateway", "obsidian-tunnel.out.log")
	request.StderrPath = filepath.Join(deps.home, "Library", "Logs", "personal-mcp-gateway", "obsidian-tunnel.err.log")
	return request, nil
}

func parseReleaseID(args []string) (releaseactivation.ReleaseID, error) {
	set := flag.NewFlagSet("release-id", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	var raw string
	set.StringVar(&raw, "release-id", "", "")
	if err := set.Parse(args); err != nil || set.NArg() != 0 || raw == "" {
		return "", &usageError{}
	}
	return releaseactivation.ReleaseID(raw), nil
}

type updateRequest struct {
	repo, expectedHead, expectedRemoteOID string
}

func parseUpdate(args []string) (updateRequest, error) {
	set := flag.NewFlagSet("update", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	var request updateRequest
	set.StringVar(&request.repo, "repo", "", "")
	set.StringVar(&request.expectedHead, "expected-head", "", "")
	set.StringVar(&request.expectedRemoteOID, "expected-remote-oid", "", "")
	if err := set.Parse(args); err != nil || set.NArg() != 0 || !filepath.IsAbs(request.repo) ||
		!validGitOID(request.expectedHead) || !validGitOID(request.expectedRemoteOID) {
		return request, &usageError{}
	}
	return request, nil
}

type adminRequest struct {
	command, repoRoot, label, home, healthURLFile string
	uid                                           int
}

func parseAdmin(command string, args []string, deps dependencies) (adminRequest, error) {
	set := flag.NewFlagSet(command, flag.ContinueOnError)
	set.SetOutput(io.Discard)
	request := adminRequest{command: command, home: deps.home, uid: deps.uid}
	set.StringVar(&request.repoRoot, "repo-root", "", "")
	set.StringVar(&request.label, "label", "", "")
	set.StringVar(&request.healthURLFile, "health-url-file", "", "")
	if err := set.Parse(args); err != nil || set.NArg() != 0 || !filepath.IsAbs(request.repoRoot) || request.label == "" {
		return request, &usageError{}
	}
	if !releaseactivation.ValidLaunchAgentLabel(request.label) {
		return request, &usageError{}
	}
	if command == "restart" && !filepath.IsAbs(request.healthURLFile) {
		return request, &usageError{}
	}
	return request, nil
}

func runAdmin(ctx context.Context, runtime releaseactivation.Runtime, request adminRequest) error {
	manifest := releaseactivation.Manifest{EffectiveUID: request.uid, LaunchAgentLabel: request.label, HealthURLFile: request.healthURLFile}
	switch request.command {
	case "restart":
		return runtime.Restart(ctx, manifest)
	case "install-launchagent":
		adapter := filepath.Join(request.repoRoot, "scripts", "internal", "install-obsidian-tunnel-launchagent.sh")
		return runtime.InvokeInstallAdapter(ctx, adapter, request.repoRoot, request.home, strconv.Itoa(request.uid), request.label)
	case "uninstall-launchagent":
		adapter := filepath.Join(request.repoRoot, "scripts", "internal", "uninstall-obsidian-tunnel-launchagent.sh")
		return runtime.InvokeUninstallAdapter(ctx, adapter, request.home, strconv.Itoa(request.uid), request.label)
	default:
		return &usageError{}
	}
}

func updateAfterFetch(ctx context.Context, request updateRequest, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = gitChildTimeout
	}
	branch, err := gitOutputWithTimeout(ctx, timeout, request.repo, "branch", "--show-current")
	if err != nil || branch != "main" {
		return errors.New("branch check failed")
	}
	status, err := gitOutputWithTimeout(ctx, timeout, request.repo, "status", "--porcelain", "--untracked-files=all")
	if err != nil || status != "" {
		return errors.New("tree check failed")
	}
	head, err := gitOutputWithTimeout(ctx, timeout, request.repo, "rev-parse", "HEAD")
	if err != nil || head != request.expectedHead {
		return errors.New("head changed after fetch")
	}
	if _, err := gitOutputWithTimeout(ctx, timeout, request.repo, "cat-file", "-e", request.expectedRemoteOID+"^{commit}"); err != nil {
		return errors.New("remote object check failed")
	}
	if _, err := gitOutputWithTimeout(ctx, timeout, request.repo, "merge", "--ff-only", request.expectedRemoteOID); err != nil {
		return errors.New("fast-forward failed")
	}
	finalHead, err := gitOutputWithTimeout(ctx, timeout, request.repo, "rev-parse", "HEAD")
	if err != nil || finalHead != request.expectedRemoteOID {
		return errors.New("updated head mismatch")
	}
	return nil
}

func gitOutput(ctx context.Context, repo string, args ...string) (string, error) {
	return gitOutputWithTimeout(ctx, gitChildTimeout, repo, args...)
}

func gitOutputWithTimeout(ctx context.Context, timeout time.Duration, repo string, args ...string) (string, error) {
	childCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	commandArgs := append([]string{"-C", repo}, args...)
	command := exec.CommandContext(childCtx, "git", commandArgs...)
	command.WaitDelay = time.Second
	var stdout, stderr boundedBuffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	runErr := command.Run()
	if stdout.truncated || stderr.truncated {
		return "", errors.New("git output exceeded limit")
	}
	if runErr != nil {
		return "", runErr
	}
	return strings.TrimSpace(stdout.String()), nil
}

type boundedBuffer struct {
	data      []byte
	truncated bool
}

func (b *boundedBuffer) Write(data []byte) (int, error) {
	original := len(data)
	remaining := (32 << 10) - len(b.data)
	if remaining <= 0 {
		b.truncated = b.truncated || original > 0
		return original, nil
	}
	if len(data) > remaining {
		data = data[:remaining]
		b.truncated = true
	}
	b.data = append(b.data, data...)
	return original, nil
}

func (b *boundedBuffer) String() string { return string(b.data) }

func validGitOID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func manifestRecords(manifest *releaseactivation.Manifest) []string {
	if manifest == nil {
		return []string{"state=clear"}
	}
	identity := fmt.Sprintf("state=%s id=%s commit=%s sha256=%s", manifest.State, manifest.ID, short(manifest.Commit), short(manifest.CandidateSHA256))
	switch manifest.State {
	case releaseactivation.StatePrepared:
		return []string{identity, "resume=make release", "rollback=make release-rollback RELEASE_ID=" + string(manifest.ID)}
	case releaseactivation.StatePending:
		return []string{identity, "accept=make release-accept RELEASE_ID=" + string(manifest.ID), "rollback=make release-rollback RELEASE_ID=" + string(manifest.ID)}
	case releaseactivation.StateAccepting:
		return []string{identity, "resume=make release-accept RELEASE_ID=" + string(manifest.ID)}
	case releaseactivation.StateRollingBack:
		return []string{identity, "resume=make release-rollback RELEASE_ID=" + string(manifest.ID)}
	default:
		return nil
	}
}

func writeFailure(stderr io.Writer, failure *releaseactivation.Error) {
	if failure == nil {
		failure = releaseactivation.SanitizedError(errors.New("release operation failed"))
	}
	_, _ = fmt.Fprintf(stderr, "error=%s message=%s\n", failure.Code, failure.Message)
}

func short(value string) string {
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}

func allNonempty(values ...string) bool {
	for _, value := range values {
		if value == "" {
			return false
		}
	}
	return true
}
