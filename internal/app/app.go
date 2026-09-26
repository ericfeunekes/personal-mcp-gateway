package app

import (
	"encoding/json"
	"net/http"
	"os"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"personal-mcp-gateway/internal/audit"
	"personal-mcp-gateway/internal/config"
	"personal-mcp-gateway/internal/fsx"
	localmcp "personal-mcp-gateway/internal/mcp"
	"personal-mcp-gateway/internal/tools/obsidian"
	"personal-mcp-gateway/internal/tools/ynab"
)

type App struct {
	cfg       config.Config
	vault     *fsx.Vault
	server    *sdk.Server
	documents *localmcp.NativeDocumentBridge
	ynab      *ynab.Tools
	log       *audit.Logger
}

func New(cfg config.Config, log *audit.Logger) (*App, error) {
	return NewWithActivities(cfg, log, nil, nil)
}

// NewWithVaultActivity is the private construction seam used by exact-candidate
// resource proof. Public tool behavior is identical to New.
func NewWithVaultActivity(cfg config.Config, log *audit.Logger, activity *fsx.ActivityCounter) (*App, error) {
	return NewWithActivities(cfg, log, activity, nil)
}

// NewWithActivities is the private exact-candidate construction seam. The
// second observer records aggregate concurrent grep scans only when the
// inherited resource probe is enabled.
func NewWithActivities(cfg config.Config, log *audit.Logger, activity *fsx.ActivityCounter, grepActivity *fsx.SchedulerActivity) (*App, error) {
	return newWithGrepTestHooks(cfg, log, activity, grepActivity, nil)
}

// NewWithGrepTestHooks is internal-package test plumbing for deterministic
// scheduler boundary tests; normal construction always supplies nil hooks.
func NewWithGrepTestHooks(cfg config.Config, log *audit.Logger, hooks *obsidian.GrepTestHooks) (*App, error) {
	return newWithGrepTestHooks(cfg, log, nil, nil, hooks)
}

func newWithGrepTestHooks(cfg config.Config, log *audit.Logger, activity *fsx.ActivityCounter, grepActivity *fsx.SchedulerActivity, hooks *obsidian.GrepTestHooks) (*App, error) {
	if cfg.Server == config.ServerYNAB {
		tools, err := ynab.New(ynab.Options{Token: cfg.YNABToken, ExportRoot: cfg.YNABExportRoot})
		if err != nil {
			return nil, err
		}
		descriptors, err := tools.Descriptors()
		if err != nil {
			_ = tools.Close()
			return nil, err
		}
		server, names, err := localmcp.NewNamedServer(sdk.Implementation{Name: config.ServerYNAB, Version: localmcp.ServerVersion}, log, string(cfg.Mode), descriptors)
		if err != nil {
			_ = tools.Close()
			return nil, err
		}
		if log != nil && log.Enabled() {
			log.Event("gateway.backend_ready", map[string]any{"transport": string(cfg.Mode), "server": config.ServerYNAB, "tools": names})
		}
		return &App{cfg: cfg, server: server, ynab: tools, log: log}, nil
	}
	vault, err := fsx.NewVaultWithActivity(cfg.ObsidianRoot, activity)
	if err != nil {
		return nil, err
	}
	var documents *localmcp.NativeDocumentBridge
	var descriptors []localmcp.ToolDescriptor
	if obsidian.DocumentReadingCandidateEnabled() {
		documents = localmcp.NewNativeDocumentBridge()
		executable, executableErr := os.Executable()
		if executableErr != nil {
			_ = documents.Close()
			return nil, executableErr
		}
		descriptors, err = obsidian.DescriptorsWithNativeDocuments(vault, documents, grepActivity, hooks, obsidian.NewPDFValidatorProcess(executable))
	} else {
		descriptors, err = obsidian.DescriptorsWithGrepTestHooks(vault, grepActivity, hooks)
	}
	if err != nil {
		if documents != nil {
			_ = documents.Close()
		}
		return nil, err
	}
	server, toolNames, err := localmcp.NewServer(log, string(cfg.Mode), descriptors)
	if err != nil {
		return nil, err
	}

	if log != nil && log.Enabled() {
		log.Event("gateway.backend_ready", map[string]any{
			"transport": string(cfg.Mode),
			"server":    localmcp.ServerName,
			"tools":     toolNames,
		})
	}

	return &App{
		cfg:       cfg,
		vault:     vault,
		server:    server,
		documents: documents,
		log:       log,
	}, nil
}

func (a *App) Server() *sdk.Server {
	return a.server
}

func (a *App) NativeDocuments() *localmcp.NativeDocumentBridge { return a.documents }

func (a *App) Close() error {
	if a.ynab != nil {
		return a.ynab.Close()
	}
	if a.documents == nil {
		return nil
	}
	return a.documents.Close()
}

func (a *App) HTTPHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/healthz", a.auditHTTP("healthz", http.HandlerFunc(a.health)))
	mux.Handle("/readyz", a.auditHTTP("readyz", http.HandlerFunc(a.ready)))
	var handler http.Handler
	if a.documents != nil {
		handler = localmcp.StreamableHTTPHandlerWithNativeDocuments(a.server, a.documents, a.cfg.AllowedHost)
	} else {
		handler = localmcp.StreamableHTTPHandlerWithNativeDocuments(a.server, nil, a.cfg.AllowedHost)
	}
	mux.Handle("/mcp", a.auditHTTP("mcp", handler))
	return mux
}

func (a *App) health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func (a *App) ready(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if a.log != nil && a.log.Degraded() {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(readyResponse{
			OK: false,
			Error: &readyError{
				Code:    "telemetry_degraded",
				Message: "telemetry is degraded",
			},
		})
		return
	}
	ready := a.server != nil
	if a.cfg.Server == config.ServerYNAB {
		ready = ready && a.ynab != nil
	} else {
		ready = ready && a.vault != nil && config.RootAccessible(a.cfg.ObsidianRoot)
	}
	if !ready {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(readyResponse{
			OK: false,
			Error: &readyError{
				Code:    "not_ready",
				Message: "gateway is not ready",
			},
		})
		return
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(readyResponse{OK: true})
}

type readyResponse struct {
	OK    bool        `json:"ok"`
	Error *readyError `json:"error,omitempty"`
}

type readyError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (a *App) auditHTTP(route string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.log == nil || !a.log.Enabled() {
			next.ServeHTTP(w, r)
			return
		}

		start := time.Now()
		method, methodMeta := audit.SafeIdentifier(r.Method, a.log.RunID(), "other",
			http.MethodGet,
			http.MethodPost,
			http.MethodHead,
			http.MethodOptions,
			http.MethodDelete,
		)
		recorder := &statusRecorder{
			ResponseWriter: w,
			status:         http.StatusOK,
		}
		next.ServeHTTP(recorder, r)
		a.log.Event("http.request", map[string]any{
			"transport":   string(a.cfg.Mode),
			"route":       route,
			"method":      method,
			"method_meta": methodMeta,
			"status":      recorder.status,
			"duration_ms": time.Since(start).Milliseconds(),
		})
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (r *statusRecorder) WriteHeader(status int) {
	if r.wrote {
		return
	}
	r.wrote = true
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}
