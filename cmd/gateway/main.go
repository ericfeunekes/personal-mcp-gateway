package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"personal-mcp-gateway/internal/app"
	"personal-mcp-gateway/internal/audit"
	"personal-mcp-gateway/internal/config"
	"personal-mcp-gateway/internal/fsx"
	localmcp "personal-mcp-gateway/internal/mcp"
	"personal-mcp-gateway/internal/resourceprobe"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

func run(args []string, stderr io.Writer) (code int) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runWithContext(ctx, args, stderr, newAuditLogger)
}

type auditLoggerFactory func(config.Config, io.Writer) (*audit.Logger, func() error, error)

func runWithContext(ctx context.Context, args []string, stderr io.Writer, auditFactory auditLoggerFactory) (code int) {
	cfg, err := config.Parse(args)
	if err != nil {
		writeErr(stderr, "configuration error: %v\n", err)
		return 2
	}
	probe, err := resourceprobe.FromEnvironment()
	if err != nil {
		writeErr(stderr, "configuration error: resource probe is invalid\n")
		return 2
	}
	if probe != nil {
		defer probe.Close()
	}

	log, closeLog, err := auditFactory(cfg, stderr)
	if err != nil {
		writeErr(stderr, "configuration error: telemetry store is not writable\n")
		return 2
	}
	defer func() {
		if err := closeLog(); err != nil {
			writeErr(stderr, "runtime error: telemetry close failed\n")
			if code == 0 {
				code = 1
			}
		}
	}()

	log.Event("gateway.start", map[string]any{
		"transport": string(cfg.Mode),
		"telemetry": cfg.Telemetry,
	})

	var activity *fsx.ActivityCounter
	var grepActivity *fsx.SchedulerActivity
	if probe != nil {
		activity = probe.Activity()
		grepActivity = probe.GrepActivity()
	}
	application, err := app.NewWithActivities(cfg, log, activity, grepActivity)
	if err != nil {
		log.Event("gateway.start_failed", map[string]any{
			"transport":  string(cfg.Mode),
			"error_code": "not_ready",
		})
		writeErr(stderr, "startup error: gateway is not ready\n")
		return 1
	}
	probeContext := ctx
	var probeErrors <-chan error
	if probe != nil {
		controlled, cancelProbe := context.WithCancel(ctx)
		defer cancelProbe()
		errors := make(chan error, 1)
		go func() {
			errors <- probe.Run(controlled)
			cancelProbe()
		}()
		probeContext = controlled
		probeErrors = errors
	}
	probeFailed := func() bool {
		if probeErrors == nil {
			return false
		}
		select {
		case probeErr := <-probeErrors:
			return probeErr != nil && ctx.Err() == nil
		default:
			return false
		}
	}

	switch cfg.Mode {
	case config.ModeStdio:
		if err := localmcp.RunStdio(probeContext, application.Server()); err != nil && !errors.Is(err, context.Canceled) {
			log.Event("gateway.runtime_error", map[string]any{
				"transport":  string(cfg.Mode),
				"error_code": "stdio_stopped",
			})
			writeErr(stderr, "runtime error: stdio server stopped\n")
			return 1
		}
		if probeFailed() {
			writeErr(stderr, "runtime error: resource probe stopped\n")
			return 1
		}
		log.Event("gateway.stop", map[string]any{"transport": string(cfg.Mode)})
		return 0
	case config.ModeHTTP:
		if err := runHTTP(probeContext, cfg.Addr, application.HTTPHandler(), stderr); err != nil {
			log.Event("gateway.runtime_error", map[string]any{
				"transport":  string(cfg.Mode),
				"error_code": "http_stopped",
			})
			writeErr(stderr, "runtime error: http server stopped\n")
			return 1
		}
		if probeFailed() {
			writeErr(stderr, "runtime error: resource probe stopped\n")
			return 1
		}
		log.Event("gateway.stop", map[string]any{"transport": string(cfg.Mode)})
		return 0
	default:
		writeErr(stderr, "configuration error: mode must be stdio or http\n")
		return 2
	}
}

func newAuditLogger(cfg config.Config, stderr io.Writer) (*audit.Logger, func() error, error) {
	runID := audit.NewRunID()
	setHandler := func(logger *audit.Logger) *audit.Logger {
		logger.SetDegradationHandler(func(audit.Degradation) {
			writeErr(stderr, "runtime warning: telemetry degraded\n")
		})
		return logger
	}
	switch cfg.Telemetry {
	case config.TelemetrySQLite:
		logger, err := audit.NewSQLite(cfg.TelemetryDB, runID)
		if err != nil {
			return audit.Disabled(), func() error { return nil }, err
		}
		return setHandler(logger), logger.Close, nil
	case config.TelemetryStderr:
		logger := audit.NewJSONL(stderr, runID)
		return setHandler(logger), logger.Close, nil
	case config.TelemetryOff:
		return audit.Disabled(), func() error { return nil }, nil
	default:
		return audit.Disabled(), func() error { return nil }, nil
	}
}

func runHTTP(ctx context.Context, addr string, handler http.Handler, stderr io.Writer) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	defer listener.Close()

	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
	}

	done := make(chan error, 1)
	go func() {
		done <- server.Serve(listener)
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return err
		}
		err := <-done
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func writeErr(w io.Writer, format string, args ...any) {
	if w == nil {
		return
	}
	_, _ = fmt.Fprintf(w, format, args...)
}
