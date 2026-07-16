package releaseactivation

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagerPrepareRejectsPostObserveArtifactRaceWithOSRuntime(t *testing.T) {
	for _, role := range []string{"candidate", "authority", "previous"} {
		t.Run(role, func(t *testing.T) {
			root := t.TempDir()
			candidate := writeRuntimeFile(t, filepath.Join(root, "sources", "candidate"), "candidate-before", 0o755)
			authority := writeRuntimeFile(t, filepath.Join(root, "sources", "authority"), "authority-before", 0o755)
			target := writeRuntimeFile(t, filepath.Join(root, "installed", "gateway"), "previous-before", 0o755)
			plist := writeRuntimeFile(t, filepath.Join(root, "bindings", "agent.plist"), "plist", 0o600)
			wrapper := writeRuntimeFile(t, filepath.Join(root, "bindings", "run-tunnel"), "wrapper", 0o755)
			mcpWrapper := writeRuntimeFile(t, filepath.Join(root, "bindings", "run-mcp"), "mcp-wrapper", 0o755)
			environment := writeRuntimeFile(t, filepath.Join(root, "bindings", "runtime.env"), "BOUND=1\n", 0o600)
			mutatedPath := map[string]string{"candidate": candidate, "authority": authority, "previous": target}[role]
			mutated := false
			store, err := NewStoreAt(filepath.Join(root, "state"), os.Geteuid())
			if err != nil {
				t.Fatal(err)
			}
			runner := &recordingRunner{fallback: CommandResult{ExitCode: launchctlNotFound}}
			osRuntime := &OSRuntime{Runner: runner}
			runtime := &postObserveRuntime{Runtime: osRuntime, after: func() error {
				mutated = true
				return os.WriteFile(mutatedPath, []byte("changed-after-os-observe"), 0o755)
			}}
			authoritySHA256, err := HashRegular(authority)
			if err != nil {
				t.Fatal(err)
			}
			manager := &Manager{Store: store, Runtime: runtime, ControllerPath: authority, ControllerSHA256: authoritySHA256}
			candidateSHA256, err := HashRegular(candidate)
			if err != nil {
				t.Fatal(err)
			}
			request := PrepareRequest{
				Commit: strings.Repeat("1", 40), CandidateSHA256: candidateSHA256, AuthoritySHA256: authoritySHA256, DependencySHA256: strings.Repeat("9", 64),
				CandidatePath: candidate, AuthorityPath: authority, TargetPath: target,
				EffectiveUID: os.Geteuid(), LaunchAgentLabel: "com.example.os-runtime-race",
				PlistPath: plist, WrapperPath: wrapper, MCPWrapperPath: mcpWrapper,
				StdoutPath: filepath.Join(root, "logs", "stdout.log"), StderrPath: filepath.Join(root, "logs", "stderr.log"),
				EnvironmentPath: environment, HealthURLFile: filepath.Join(root, "runtime", "health-url"),
				ReadyTimeoutSeconds: 5, ReadyPollMilliseconds: 25,
			}
			prepared, err := manager.Prepare(context.Background(), request)
			if failure := SanitizedError(err); prepared != nil || failure == nil || failure.Code != ErrorArtifactMismatch {
				t.Fatalf("Prepare = %#v, %v; want artifact_mismatch", prepared, err)
			}
			if !mutated || len(runner.calls()) != 1 {
				t.Fatalf("race ordering mutated=%v runtime calls=%v", mutated, runner.calls())
			}
			if active, inspectErr := store.Inspect(); inspectErr != nil || active != nil {
				t.Fatalf("post-observe race published state: %#v, %v", active, inspectErr)
			}
			wantTarget := "previous-before"
			if role == "previous" {
				wantTarget = "changed-after-os-observe"
			}
			data, readErr := os.ReadFile(target)
			if readErr != nil || string(data) != wantTarget {
				t.Fatalf("target after rejected race = %q, %v; want %q", data, readErr, wantTarget)
			}
		})
	}
}

type postObserveRuntime struct {
	Runtime
	after func() error
}

func (r *postObserveRuntime) Observe(ctx context.Context, manifest Manifest, controller string, artifacts RuntimeArtifacts) (Observed, error) {
	observed, err := r.Runtime.Observe(ctx, manifest, controller, artifacts)
	if err != nil {
		return Observed{}, err
	}
	if err := r.after(); err != nil {
		return Observed{}, err
	}
	return observed, nil
}
