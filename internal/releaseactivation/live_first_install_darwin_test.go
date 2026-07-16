//go:build darwin

package releaseactivation

import (
	"context"
	"fmt"
	"html"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const (
	liveFirstInstallEnv    = "RUN_LIVE_RELEASE_FIRST_INSTALL"
	liveLaunchHelperEnv    = "PERSONAL_MCP_GATEWAY_LIVE_LAUNCH_HELPER"
	liveLaunchHealthEnv    = "PERSONAL_MCP_GATEWAY_LIVE_HEALTH_FILE"
	liveLaunchHelperTarget = "^TestLiveFirstInstallLaunchAgentHelper$"
)

// TestLiveFirstInstallLaunchAgentHelper is the candidate process launched by
// the isolated LaunchAgent drill. Normal test runs return immediately.
func TestLiveFirstInstallLaunchAgentHelper(t *testing.T) {
	if os.Getenv(liveLaunchHelperEnv) != "1" {
		return
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	healthFile := os.Getenv(liveLaunchHealthEnv)
	if !filepath.IsAbs(healthFile) {
		t.Fatal("live helper health path is not absolute")
	}
	if err := os.MkdirAll(filepath.Dir(healthFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(healthFile, []byte("http://"+listener.Addr().String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/healthz" && request.URL.Path != "/readyz" {
			http.NotFound(writer, request)
			return
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("ok"))
	})
	if err := http.Serve(listener, handler); err != nil {
		t.Fatal(err)
	}
}

// TestLiveFirstInstallLaunchAgentRollback is opt-in because it mutates the
// current user's launchd domain. It uses a randomized label and t.TempDir only.
func TestLiveFirstInstallLaunchAgentRollback(t *testing.T) {
	if os.Getenv(liveFirstInstallEnv) != "1" {
		t.Skip("set RUN_LIVE_RELEASE_FIRST_INSTALL=1 for the isolated launchd drill")
	}
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	authority := filepath.Join(repoRoot, ".build", "release-activation")
	if _, err := hashRuntimeFile(authority, true); err != nil {
		t.Fatalf("build release controller before live drill: %v", err)
	}
	candidate, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	candidate, err = filepath.EvalSymlinks(candidate)
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	id, err := NewReleaseID()
	if err != nil {
		t.Fatal(err)
	}
	label := "com.example.personal-mcp-release-proof-" + string(id)[:12]
	uid := os.Geteuid()
	target := filepath.Join(root, "installed", "gateway")
	healthFile := filepath.Join(root, "runtime", "health-url")
	wrapper := filepath.Join(root, "bindings", "run-live-helper.sh")
	mcpWrapper := filepath.Join(root, "bindings", "unused-mcp-wrapper.sh")
	environment := filepath.Join(root, "bindings", "runtime.env")
	plist := filepath.Join(root, "bindings", label+".plist")
	stdout := filepath.Join(root, "logs", "launch.out.log")
	stderr := filepath.Join(root, "logs", "launch.err.log")
	for _, dir := range []string{filepath.Dir(target), filepath.Dir(wrapper), filepath.Dir(healthFile), filepath.Dir(stdout)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeLiveFile(t, wrapper, "#!/bin/sh\nexec "+shellQuote(target)+" -test.run="+shellQuote(liveLaunchHelperTarget)+" -test.timeout=2m\n", 0o700)
	writeLiveFile(t, mcpWrapper, "#!/bin/sh\nexit 0\n", 0o700)
	writeLiveFile(t, environment, "LIVE_PROOF=1\n", 0o600)
	plistData := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>%s</string>
<key>ProgramArguments</key><array><string>%s</string></array>
<key>WorkingDirectory</key><string>%s</string>
<key>RunAtLoad</key><true/><key>KeepAlive</key><true/><key>ThrottleInterval</key><integer>1</integer>
<key>EnvironmentVariables</key><dict>
<key>%s</key><string>1</string><key>%s</key><string>%s</string>
</dict>
<key>StandardOutPath</key><string>%s</string><key>StandardErrorPath</key><string>%s</string>
</dict></plist>
`, html.EscapeString(label), html.EscapeString(wrapper), html.EscapeString(root),
		html.EscapeString(liveLaunchHelperEnv), html.EscapeString(liveLaunchHealthEnv), html.EscapeString(healthFile),
		html.EscapeString(stdout), html.EscapeString(stderr))
	writeLiveFile(t, plist, plistData, 0o644)

	domain := fmt.Sprintf("gui/%d", uid)
	service := domain + "/" + label
	t.Cleanup(func() { _ = exec.Command("launchctl", "bootout", domain, plist).Run() })
	if output, err := exec.Command("launchctl", "bootstrap", domain, plist).CombinedOutput(); err != nil {
		t.Fatalf("bootstrap isolated LaunchAgent: %v: %s", err, output)
	}
	if output, err := exec.Command("launchctl", "print", service).CombinedOutput(); err != nil {
		t.Fatalf("isolated LaunchAgent not loaded: %v: %s", err, output)
	}

	store, err := NewStoreAt(filepath.Join(root, "state"), uid)
	if err != nil {
		t.Fatal(err)
	}
	manager := &Manager{Store: store, Runtime: NewOSRuntime(), ControllerPath: authority}
	candidateSHA256, err := HashRegular(candidate)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := manager.Prepare(context.Background(), PrepareRequest{
		Commit: strings.Repeat("d", 40), CandidateSHA256: candidateSHA256, DependencySHA256: strings.Repeat("9", 64),
		CandidatePath: candidate, AuthorityPath: authority, TargetPath: target,
		EffectiveUID: uid, LaunchAgentLabel: label, PlistPath: plist, WrapperPath: wrapper, MCPWrapperPath: mcpWrapper,
		StdoutPath: stdout, StderrPath: stderr, EnvironmentPath: environment, HealthURLFile: healthFile,
		ReadyTimeoutSeconds: 30, ReadyPollMilliseconds: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	manager.ControllerPath = store.ActiveAuthorityPath()
	pending, err := manager.Resume(context.Background(), prepared.ID)
	if err != nil || pending == nil || pending.State != StatePending {
		t.Fatalf("first-install resume = %#v, %v; want pending", pending, err)
	}
	cleared, err := manager.Rollback(context.Background(), prepared.ID)
	if err != nil || cleared != nil {
		t.Fatalf("first-install rollback = %#v, %v; want clear", cleared, err)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("candidate target remains after rollback: %v", err)
	}
	if err := exec.Command("launchctl", "print", service).Run(); err == nil {
		t.Fatal("isolated supervisor remains loaded after rollback")
	}
	if active, err := store.Inspect(); err != nil || active != nil {
		t.Fatalf("store after first-install rollback = %#v, %v", active, err)
	}
}

func writeLiveFile(t *testing.T, path, contents string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
