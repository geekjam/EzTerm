// Package daemon hosts the ezterm HTTP API.
package daemon

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ezterm/internal/config"
	"ezterm/internal/message"
	"ezterm/internal/session"
	"ezterm/internal/sshconfig"
	"ezterm/internal/storage"
)

// Run parses daemon flags, initializes state, and serves until interrupted.
// It returns a process exit code.
func Run(args []string) int {
	fs := flag.NewFlagSet("ezterm daemon", flag.ContinueOnError)
	host := fs.String("host", config.DefaultHost, "HTTP bind address")
	port := fs.Int("port", config.DefaultPort, "HTTP port")
	dataDir := fs.String("data-dir", "", "data directory (default ~/.ezterm)")
	logLevel := fs.String("log-level", "info", "log verbosity: debug, info, warn, error")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg := config.Config{Host: *host, Port: *port, LogLevel: *logLevel}
	if *dataDir != "" {
		expanded, err := config.ExpandDataDir(*dataDir)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		cfg.DataDir = expanded
	} else {
		defaultDir, err := config.DefaultDataDir()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		cfg.DataDir = defaultDir
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "invalid configuration: %v\n", err)
		return 1
	}
	setupLogger(cfg.LogLevel)

	store := storage.New(cfg.DataDir)
	if err := store.Init(); err != nil {
		slog.Error("initialize data directory", "error", err)
		return 1
	}
	msgMgr := message.NewManager(store)
	sshStore := sshconfig.NewStore(cfg.DataDir)
	mgr := session.NewManager(store, msgMgr, sshStore)
	if err := mgr.Restore(); err != nil {
		slog.Warn("restore sessions", "error", err)
	}

	handler := NewHandlerWithAddress(mgr, sshStore, cfg.Host, cfg.Port)
	addr := net.JoinHostPort(cfg.Host, fmt.Sprintf("%d", cfg.Port))
	server := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot listen on %s: %v\n", addr, err)
		slog.Error("listen failed", "addr", addr, "error", err)
		return 1
	}
	slog.Info("ezterm daemon listening", "addr", addr, "data_dir", cfg.DataDir)

	shutdownCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.Serve(listener)
	}()

	select {
	case err := <-serveErr:
		if err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			return 1
		}
		return 0
	case <-shutdownCtx.Done():
		slog.Info("shutting down")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			slog.Error("graceful shutdown failed", "error", err)
			_ = server.Close()
			return 1
		}
		return 0
	}
}

func setupLogger(level string) {
	var slogLevel slog.Level
	switch level {
	case "debug":
		slogLevel = slog.LevelDebug
	case "warn":
		slogLevel = slog.LevelWarn
	case "error":
		slogLevel = slog.LevelError
	default:
		slogLevel = slog.LevelInfo
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slogLevel})))
}
