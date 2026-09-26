package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/retail-cortex/code_puppy/internal/app"
	"github.com/retail-cortex/code_puppy/internal/config"
	"github.com/retail-cortex/code_puppy/internal/server"
	"github.com/retail-cortex/code_puppy/internal/workers"
	"github.com/spf13/cobra"
)

// serveGrace is how long turns in progress may finish when the service stops.
const serveGrace = 10 * time.Second

func newServeCommand(g *globalFlags) *cobra.Command {
	var socket string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the Code Puppy service: every workspace, over a Unix socket",
		Long: `Runs the per-user Code Puppy service. Clients (the desktop app, and the CLI
when it attaches) reach it over a Unix socket only this user can open. It
opens a workspace when a client first names it; a workspace open here can't
also be opened by a separate CLI.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if socket == "" {
				socket = server.DefaultSocket()
			}
			return runServe(cmd.Context(), g, socket)
		},
	}
	cmd.Flags().StringVar(&socket, "socket", "", "Unix socket to listen on (default ~/.code_puppy/run/code-puppy.sock)")
	return cmd
}

func runServe(ctx context.Context, g *globalFlags, socket string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	if g.dir != "" {
		return withCode(exitUsage, errors.New("serve holds every workspace; clients name theirs (--dir doesn't apply)"))
	}
	cfg, err := loadConfig(g)
	if err != nil {
		return err
	}
	warn := func(msg string) { slog.Warn(msg) }
	defer startObservability(ctx, cfg, warn)()

	// One record of enabled workers and their runs, shared by every
	// workspace the service opens.
	store, err := workers.OpenStore(config.ExpandHome("~/.code_puppy/workers.json"))
	if err != nil {
		return err
	}
	runs := workers.OpenRunLog(config.ExpandHome("~/.code_puppy/worker-runs"))

	// Each workspace gets its own configuration: a workspace owns and
	// changes it.
	s := server.New(func(ctx context.Context, dir string) (*app.Workspace, error) {
		cfg, err := loadConfig(g)
		if err != nil {
			return nil, err
		}
		cfg.Tools.WorkspaceDir = dir
		w, err := app.Open(ctx, cfg, app.Options{
			Streaming: true,
			Warn:      func(msg string) { slog.Warn(msg, "workspace", dir) },
			Workers:   store,
		})
		if err != nil {
			return nil, err
		}
		if merr := w.ModelErr(); merr != nil {
			slog.Warn("model unavailable", "workspace", dir, "error", app.ModelErrorSummary(merr, cfg))
		}
		slog.Info("workspace opened", "workspace", dir)
		return w, nil
	}, server.WithScheduler(server.SchedulerConfig{Store: store, Runs: runs, MaxConcurrent: cfg.Workers.Policy.MaxConcurrent}))
	defer s.Close()

	l, err := server.Listen(socket)
	if err != nil {
		if errors.Is(err, server.ErrRunning) {
			return withCode(exitUsage, err)
		}
		return err
	}
	defer os.Remove(socket)
	if cfg.Workers.Enabled {
		s.StartScheduler(ctx)
	}
	fmt.Fprintf(os.Stderr, "🐶 Code Puppy service listening on %s\n", socket)
	slog.Info("serve", "socket", socket)
	return server.Serve(ctx, l, s.Handler(), serveGrace)
}
