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
