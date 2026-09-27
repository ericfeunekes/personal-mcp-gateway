package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestParseRequiresAbsoluteRoot(t *testing.T) {
	_, err := Parse([]string{"stdio", "--obsidian-root", "relative"})
	if err == nil {
		t.Fatal("Parse() error = nil, want error")
	}
	if strings.Contains(err.Error(), "relative") {
		t.Fatalf("Parse() error leaked input path: %v", err)
	}
}

func TestParseValidModes(t *testing.T) {
	root := t.TempDir()

	stdio, err := Parse([]string{"stdio", "--obsidian-root", root})
	if err != nil {
		t.Fatal(err)
	}
	if stdio.Mode != ModeStdio {
		t.Fatalf("stdio mode = %q", stdio.Mode)
	}
	if stdio.ObsidianRoot != filepath.Clean(root) {
		t.Fatalf("root not cleaned")
	}

	http, err := Parse([]string{"http", "--obsidian-root", root})
	if err != nil {
		t.Fatal(err)
	}
	if http.Mode != ModeHTTP || http.Addr != DefaultHTTPAddr {
		t.Fatalf("http config = %#v", http)
	}
}

func TestValidateLoopbackAddr(t *testing.T) {
	valid := []string{
		"127.0.0.1:8765",
		"localhost:8765",
		"[::1]:8765",
	}
	for _, addr := range valid {
		if err := ValidateLoopbackAddr(addr); err != nil {
			t.Fatalf("ValidateLoopbackAddr(%q) = %v", addr, err)
		}
	}

	invalid := []string{
		":8765",
		"0.0.0.0:8765",
		"[::]:8765",
		"192.168.1.10:8765",
		"example.com:8765",
		"127.0.0.1",
	}
	for _, addr := range invalid {
		if err := ValidateLoopbackAddr(addr); err == nil {
			t.Fatalf("ValidateLoopbackAddr(%q) = nil, want error", addr)
		}
	}
}

func TestConfigErrorsDoNotLeakRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing")
	_, err := Parse([]string{"stdio", "--obsidian-root", root})
	if err == nil {
		t.Fatal("Parse() error = nil, want error")
	}
	if strings.Contains(err.Error(), root) || strings.Contains(err.Error(), filepath.Dir(root)) {
		t.Fatalf("Parse() error leaked host path: %v", err)
	}
}

func TestAllowedHostIsOneExactDNSNameInHTTPMode(t *testing.T) {
	root := t.TempDir()
	valid, err := Parse([]string{"http", "--obsidian-root", root, "--telemetry", "off", "--allowed-host", "Mac.Example.ts.net"})
	if err != nil {
		t.Fatal(err)
	}
	if valid.AllowedHost != "mac.example.ts.net" {
		t.Fatalf("allowed host = %q, want lower-cased exact name", valid.AllowedHost)
	}
	for _, host := range []string{"*.example.ts.net", "mac.example.ts.net:443", "100.64.0.1", "::1", "localhost", "mac.example.ts.net.", "a..b", "-bad.example", "bad_.example", "https://mac.example.ts.net"} {
		if _, err := Parse([]string{"http", "--obsidian-root", root, "--telemetry", "off", "--allowed-host", host}); err == nil {
			t.Fatalf("allowed host %q was accepted", host)
		}
	}
	if _, err := Parse([]string{"stdio", "--obsidian-root", root, "--telemetry", "off", "--allowed-host", "mac.example.ts.net"}); err == nil {
		t.Fatal("stdio mode accepted --allowed-host")
	}
	if _, err := Validate(Config{Mode: ModeStdio, ObsidianRoot: root, Telemetry: TelemetryOff, AllowedHost: "mac.example.ts.net"}); err == nil {
		t.Fatal("stdio config accepted an allowed host")
	}
}
