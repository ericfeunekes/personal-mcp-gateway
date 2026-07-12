package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLaunchAgentAdaptersEscapeValidateAndRoundTrip(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("LaunchAgent adapters require macOS plutil")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo & <gateway>")
	home := filepath.Join(root, "home & gateway")
	if err := os.MkdirAll(filepath.Join(repo, "scripts"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "scripts", "run-obsidian-tunnel.sh"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	canonicalRepo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	canonicalHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	launchctl := filepath.Join(bin, "launchctl")
	fake := `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$LAUNCHCTL_LOG"
case "$1" in
  print) test -f "$LAUNCHCTL_STATE" ;;
  bootstrap) : > "$LAUNCHCTL_STATE" ;;
  bootout) rm -f "$LAUNCHCTL_STATE" ;;
esac
`
	if err := os.WriteFile(launchctl, []byte(fake), 0o700); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "loaded")
	log := filepath.Join(root, "launchctl.log")
	env := append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "LAUNCHCTL_STATE="+state, "LAUNCHCTL_LOG="+log)
	label := "com.example.gateway"

	install := exec.Command("bash", filepath.Join(repoRoot(t), "scripts/internal/install-obsidian-tunnel-launchagent.sh"), repo, home, "501", label)
	install.Env = env
	if output, err := install.CombinedOutput(); err != nil {
		t.Fatalf("install failed: %v\n%s", err, output)
	}
	plist := filepath.Join(canonicalHome, "Library", "LaunchAgents", label+".plist")
	assertPlistRaw(t, plist, "Label", label)
	assertPlistRaw(t, plist, "ProgramArguments.0", filepath.Join(canonicalRepo, "scripts", "run-obsidian-tunnel.sh"))
	assertPlistRaw(t, plist, "WorkingDirectory", canonicalRepo)
	data, err := os.ReadFile(plist)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "&amp;") || !strings.Contains(string(data), "&lt;") || strings.Contains(string(data), "<gateway>") {
		t.Fatalf("plist values were not XML escaped:\n%s", data)
	}

	uninstall := exec.Command("bash", filepath.Join(repoRoot(t), "scripts/internal/uninstall-obsidian-tunnel-launchagent.sh"), home, "501", label)
	uninstall.Env = env
	if output, err := uninstall.CombinedOutput(); err != nil {
		t.Fatalf("uninstall failed: %v\n%s", err, output)
	}
	if _, err := os.Lstat(plist); !os.IsNotExist(err) {
		t.Fatalf("plist still exists after uninstall: %v", err)
	}
}

func TestLaunchAgentAdaptersRejectTraversalAndSymlinkedParent(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("LaunchAgent adapters require macOS plutil")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(filepath.Join(repo, "scripts"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "scripts", "run-obsidian-tunnel.sh"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "Library"), 0o700); err != nil {
		t.Fatal(err)
	}
	installPath := filepath.Join(repoRoot(t), "scripts/internal/install-obsidian-tunnel-launchagent.sh")

	cmd := exec.Command("bash", installPath, repo, home, "501", "../../owned")
	if err := cmd.Run(); err == nil {
		t.Fatal("install accepted traversal label")
	}

	outside := filepath.Join(root, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, "Library", "LaunchAgents")); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("bash", installPath, repo, home, "501", "com.example.gateway")
	if err := cmd.Run(); err == nil {
		t.Fatal("install accepted symlinked LaunchAgents directory")
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("adapter wrote through symlinked parent: %v", entries)
	}
}

func assertPlistRaw(t *testing.T, plist, key, want string) {
	t.Helper()
	output, err := exec.Command("/usr/bin/plutil", "-extract", key, "raw", plist).Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSuffix(string(output), "\n"); got != want {
		t.Fatalf("plist %s = %q, want %q", key, got, want)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(wd)
}
