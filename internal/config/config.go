package config

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

type Mode string

const (
	ModeStdio Mode = "stdio"
	ModeHTTP  Mode = "http"

	DefaultHTTPAddr     = "127.0.0.1:8765"
	DefaultYNABHTTPAddr = "127.0.0.1:8768"
	ServerObsidian      = "obsidian"
	ServerYNAB          = "ynab"

	TelemetrySQLite = "sqlite"
	TelemetryStderr = "stderr"
	TelemetryOff    = "off"
)

type Config struct {
	Mode           Mode
	Server         string
	YNABToken      string
	YNABExportRoot string
	ObsidianRoot   string
	Addr           string
	AllowedHost    string
	Telemetry      string
	TelemetryDB    string
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
		Server:    ServerObsidian,
		Telemetry: TelemetrySQLite,
	}
	fs.StringVar(&cfg.Server, "server", ServerObsidian, "MCP server: obsidian or ynab")
	fs.StringVar(&cfg.ObsidianRoot, "obsidian-root", "", "absolute path to the Obsidian vault root")
	fs.StringVar(&cfg.Telemetry, "telemetry", TelemetrySQLite, "structured event sink: sqlite, stderr, or off")
	fs.StringVar(&cfg.TelemetryDB, "telemetry-db", "", "absolute path to structured telemetry SQLite database")
	if mode == ModeHTTP {
		fs.StringVar(&cfg.Addr, "addr", "", "loopback HTTP listen address")
		fs.StringVar(&cfg.AllowedHost, "allowed-host", "", "one exact non-loopback Host name accepted on the loopback listener")
	}

	if err := fs.Parse(args[1:]); err != nil {
		return Config{}, errors.New("invalid command line flags")
	}
	if fs.NArg() != 0 {
		return Config{}, errors.New("unexpected positional arguments")
	}
	if cfg.Server == ServerYNAB {
		cfg.YNABToken = os.Getenv("YNAB_TOKEN")
		cfg.YNABExportRoot = os.Getenv("YNAB_EXPORT_ROOT")
	}

	return Validate(cfg)
}

func Validate(cfg Config) (Config, error) {
	if cfg.Server == "" {
		cfg.Server = ServerObsidian
	}
	if cfg.Server != ServerObsidian && cfg.Server != ServerYNAB {
		return Config{}, errors.New("server must be obsidian or ynab")
	}
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
			if cfg.Server == ServerYNAB {
				cfg.TelemetryDB = filepath.Join(filepath.Dir(dbPath), "ynab", "telemetry.sqlite")
			}
		}
		if !filepath.IsAbs(cfg.TelemetryDB) {
			return Config{}, errors.New("telemetry db path must be absolute")
		}
		cfg.TelemetryDB = filepath.Clean(cfg.TelemetryDB)
	}
	if cfg.Server == ServerYNAB {
		if strings.TrimSpace(cfg.YNABToken) == "" || strings.ContainsAny(cfg.YNABToken, "\r\n") {
			return Config{}, errors.New("YNAB token is missing or invalid")
		}
		if cfg.ObsidianRoot != "" {
			return Config{}, errors.New("obsidian root is not valid for YNAB")
		}
		if cfg.YNABExportRoot == "" {
			dir, err := os.UserConfigDir()
			if err != nil {
				return Config{}, errors.New("YNAB export root is required")
			}
			cfg.YNABExportRoot = filepath.Join(dir, "personal-mcp-gateway", "ynab", "exports")
		}
		if !filepath.IsAbs(cfg.YNABExportRoot) {
			return Config{}, errors.New("YNAB export root must be absolute")
		}
		cfg.YNABExportRoot = filepath.Clean(cfg.YNABExportRoot)
	} else {
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
	}

	if cfg.Mode == ModeHTTP {
		if cfg.Addr == "" {
			cfg.Addr = DefaultHTTPAddr
			if cfg.Server == ServerYNAB {
				cfg.Addr = DefaultYNABHTTPAddr
			}
		}
		if err := ValidateLoopbackAddr(cfg.Addr); err != nil {
			return Config{}, err
		}
		if cfg.AllowedHost != "" {
			if !validDNSName(cfg.AllowedHost) {
				return Config{}, errors.New("allowed host must be one exact DNS name")
			}
			cfg.AllowedHost = strings.ToLower(cfg.AllowedHost)
		}
	} else if cfg.AllowedHost != "" {
		return Config{}, errors.New("allowed host is valid only in http mode")
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

// validDNSName accepts one exact multi-label DNS name: no port, wildcard, IP
// literal, trailing dot, or empty label.
func validDNSName(name string) bool {
	if len(name) > 253 || net.ParseIP(name) != nil {
		return false
	}
	labels := strings.Split(name, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}

func RootAccessible(root string) bool {
	info, err := os.Stat(root)
	return err == nil && info.IsDir()
}
