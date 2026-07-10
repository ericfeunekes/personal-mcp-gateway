package config

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
)

type Mode string

const (
	ModeStdio Mode = "stdio"
	ModeHTTP  Mode = "http"

	DefaultHTTPAddr = "127.0.0.1:8765"

	TelemetrySQLite = "sqlite"
	TelemetryStderr = "stderr"
	TelemetryOff    = "off"
)

type Config struct {
	Mode         Mode
	ObsidianRoot string
	Addr         string
	Telemetry    string
	TelemetryDB  string
}

func Parse(args []string) (Config, error) {
	if len(args) == 0 {
		return Config{}, errors.New("mode is required")
	}

	mode := Mode(args[0])
	if mode != ModeStdio && mode != ModeHTTP {
		return Config{}, errors.New("mode must be stdio or http")
	}

	fs := flag.NewFlagSet(string(mode), flag.ContinueOnError)
	fs.SetOutput(nil)

	cfg := Config{
		Mode:      mode,
		Addr:      DefaultHTTPAddr,
		Telemetry: TelemetrySQLite,
	}
	fs.StringVar(&cfg.ObsidianRoot, "obsidian-root", "", "absolute path to the Obsidian vault root")
	fs.StringVar(&cfg.Telemetry, "telemetry", TelemetrySQLite, "structured event sink: sqlite, stderr, or off")
	fs.StringVar(&cfg.TelemetryDB, "telemetry-db", "", "absolute path to structured telemetry SQLite database")
	if mode == ModeHTTP {
		fs.StringVar(&cfg.Addr, "addr", DefaultHTTPAddr, "loopback HTTP listen address")
	}

	if err := fs.Parse(args[1:]); err != nil {
		return Config{}, errors.New("invalid command line flags")
	}
	if fs.NArg() != 0 {
		return Config{}, errors.New("unexpected positional arguments")
	}

	return Validate(cfg)
}

func Validate(cfg Config) (Config, error) {
	if cfg.Mode != ModeStdio && cfg.Mode != ModeHTTP {
		return Config{}, errors.New("mode must be stdio or http")
	}
	if cfg.Telemetry == "" {
		cfg.Telemetry = TelemetrySQLite
	}
	if cfg.Telemetry != TelemetrySQLite && cfg.Telemetry != TelemetryStderr && cfg.Telemetry != TelemetryOff {
		return Config{}, errors.New("telemetry must be sqlite, stderr, or off")
	}
	if cfg.Telemetry == TelemetrySQLite {
		if cfg.TelemetryDB == "" {
			dbPath, err := DefaultTelemetryDBPath()
			if err != nil {
				return Config{}, err
			}
			cfg.TelemetryDB = dbPath
		}
		if !filepath.IsAbs(cfg.TelemetryDB) {
			return Config{}, errors.New("telemetry db path must be absolute")
		}
		cfg.TelemetryDB = filepath.Clean(cfg.TelemetryDB)
	}
	if cfg.ObsidianRoot == "" {
		return Config{}, errors.New("obsidian root is required")
	}
	if !filepath.IsAbs(cfg.ObsidianRoot) {
		return Config{}, errors.New("obsidian root must be an absolute path")
	}

	cleanRoot := filepath.Clean(cfg.ObsidianRoot)
	info, err := os.Stat(cleanRoot)
	if err != nil {
		return Config{}, errors.New("obsidian root is not an accessible directory")
	}
	if !info.IsDir() {
		return Config{}, errors.New("obsidian root is not an accessible directory")
	}
	cfg.ObsidianRoot = cleanRoot

	if cfg.Mode == ModeHTTP {
		if cfg.Addr == "" {
			cfg.Addr = DefaultHTTPAddr
		}
		if err := ValidateLoopbackAddr(cfg.Addr); err != nil {
			return Config{}, err
		}
	}

	return cfg, nil
}

func DefaultTelemetryDBPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", errors.New("telemetry db path is required")
	}
	return filepath.Join(dir, "personal-mcp-gateway", "telemetry.sqlite"), nil
}

func ValidateLoopbackAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return errors.New("http address must include host and port")
	}
	if host == "" {
		return errors.New("http address must bind an explicit loopback host")
	}
	if port == "" {
		return errors.New("http address must include a port")
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("http address host must be loopback")
	}
	if !ip.IsLoopback() || ip.IsUnspecified() {
		return errors.New("http address host must be loopback")
	}
	return nil
}

func RootAccessible(root string) bool {
	info, err := os.Stat(root)
	return err == nil && info.IsDir()
}
