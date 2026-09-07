package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestYNABConfigDoesNotRequireVault(t *testing.T) {
	t.Setenv("YNAB_TOKEN", "fixture-token")
	t.Setenv("YNAB_EXPORT_ROOT", filepath.Join(t.TempDir(), "exports"))
	cfg, err := Parse([]string{"http", "--server", "ynab", "--telemetry", "off"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server != ServerYNAB || cfg.Addr != DefaultYNABHTTPAddr || cfg.ObsidianRoot != "" {
		t.Fatal("wrong integration selection")
	}
	if cfg.YNABToken != "fixture-token" || !filepath.IsAbs(cfg.YNABExportRoot) {
		t.Fatal("YNAB settings not loaded")
	}
}

func TestYNABConfigRejectsMissingTokenWithoutLeakingInput(t *testing.T) {
	t.Setenv("YNAB_TOKEN", "")
	if _, err := Parse([]string{"stdio", "--server", "ynab"}); err == nil {
		t.Fatal("accepted missing token")
	}
	t.Setenv("YNAB_TOKEN", "private-token\ninvalid")
	if _, err := Parse([]string{"stdio", "--server", "ynab"}); err == nil || strings.Contains(err.Error(), "private-token") {
		t.Fatal("unsafe token error")
	}
}

func TestServerConfigurationIsolation(t *testing.T) {
	t.Setenv("YNAB_TOKEN", "private-token")
	t.Setenv("YNAB_EXPORT_ROOT", "relative-private-path")
	if _, err := Parse([]string{"stdio", "--server", "ynab"}); err == nil || strings.Contains(err.Error(), "relative-private-path") {
		t.Fatal("unsafe root validation")
	}
	cfg, err := Parse([]string{"stdio", "--obsidian-root", t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server != ServerObsidian || cfg.YNABToken != "" || cfg.YNABExportRoot != "" {
		t.Fatal("Obsidian loaded unrelated settings")
	}
	if _, err := Parse([]string{"stdio", "--server", "unexpected"}); err == nil {
		t.Fatal("accepted unknown server")
	}
}

func TestYNABConfigKeepsExplicitLoopbackAddress(t *testing.T) {
	t.Setenv("YNAB_TOKEN", "fixture-token")
	cfg, err := Parse([]string{"http", "--server", "ynab", "--addr", "127.0.0.1:9876"})
	if err != nil || cfg.Addr != "127.0.0.1:9876" {
		t.Fatal("explicit address lost")
	}
	if _, err := Parse([]string{"http", "--server", "ynab", "--addr", "0.0.0.0:9876"}); err == nil {
		t.Fatal("public listener accepted")
	}
}
