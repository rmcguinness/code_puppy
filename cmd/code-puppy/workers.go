package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/retail-cortex/code_puppy/internal/app"
	"github.com/retail-cortex/code_puppy/internal/client"
	"github.com/retail-cortex/code_puppy/internal/config"
	"github.com/retail-cortex/code_puppy/internal/server"
	"github.com/retail-cortex/code_puppy/internal/tui"
	"github.com/retail-cortex/code_puppy/internal/workers"
	"github.com/spf13/cobra"
)

// workerOps are a workspace's workers, in the service or in this process.
type workerOps interface {
	ListWorkers() ([]app.WorkerInfo, error)
	EnableWorker(name, hash string) (app.WorkerInfo, error)
	DisableWorker(name string) (app.WorkerInfo, error)
	RunWorker(ctx context.Context, name string, on func(app.Event)) (workers.Run, error)
	WorkerRuns(name string, limit int) ([]workers.Run, error)
}

// localWorkers runs workers in this process, when the service isn't running.
type localWorkers struct{ *app.Workspace }

func (l localWorkers) RunWorker(ctx context.Context, name string, on func(app.Event)) (workers.Run, error) {
	return l.Workspace.RunWorker(ctx, name, app.RunOptions{Manual: true, OnEvent: on})
}

// openWorkers reaches the workspace's workers through the service when it
// is running (it owns them then), and otherwise in this process. close
// releases what it opened.
func openWorkers(ctx context.Context, g *globalFlags) (ops workerOps, close func(), err error) {
	cfg, err := loadConfig(g)
	if err != nil {
		return nil, nil, err
	}
	if socket := server.DefaultSocket(); server.Running(socket) {
		dir, err := filepath.Abs(config.ExpandHome(cfg.Tools.WorkspaceDir))
		if err != nil {
			return nil, nil, err
		}
		return client.AttachWorkers(socket, dir), func() {}, nil
	}
	w, err := app.Open(ctx, cfg, app.Options{})
	if err != nil {
		return nil, nil, err
	}
	return localWorkers{w}, func() { w.Close() }, nil
}

func newWorkersCommand(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workers",
		Short: "List, enable and run the workspace's workers (workers/<name>/WORKER.md)",
		Long: `Workers are workflows a workspace defines in workers/<name>/WORKER.md and the
Code Puppy service runs on their schedules, unattended. A worker runs only once
enabled, pinned to the content you reviewed: editing it disables it until it is
enabled again. It may do only what its permissions allow; anything else is
refused and recorded.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return workersList(cmd, g) },
	}
	var yes bool
	enable := &cobra.Command{
		Use:   "enable <name>",
		Short: "Show a worker and enable it as shown",
		Args:  cobra.ExactArgs(1),
		RunE:  func(cmd *cobra.Command, args []string) error { return workersEnable(cmd, g, args[0], yes) },
	}
	enable.Flags().BoolVarP(&yes, "yes", "y", false, "Enable without asking")
	var limit int
	runs := &cobra.Command{
		Use:   "runs <name>",
		Short: "Show a worker's recent runs",
		Args:  cobra.ExactArgs(1),
		RunE:  func(cmd *cobra.Command, args []string) error { return workersRuns(cmd, g, args[0], limit) },
	}
	runs.Flags().IntVarP(&limit, "limit", "n", 10, "How many runs to show")
	cmd.AddCommand(
		&cobra.Command{Use: "list", Short: "List the workspace's workers", Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error { return workersList(cmd, g) }},
		enable,
		&cobra.Command{Use: "disable <name>", Short: "Stop a worker from running", Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error { return workersDisable(cmd, g, args[0]) }},
		&cobra.Command{Use: "run <name>", Short: "Run an enabled worker now and show what it does", Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error { return workersRun(cmd, g, args[0]) }},
		runs,
	)
	return cmd
}

func workersList(cmd *cobra.Command, g *globalFlags) error {
	ops, done, err := openWorkers(cmd.Context(), g)
	if err != nil {
		return err
	}
	defer done()
	list, err := ops.ListWorkers()
	if err != nil {
		return workerErr(err)
	}
	out := cmd.OutOrStdout()
	if len(list) == 0 {
		fmt.Fprintln(out, "No workers: add workers/<name>/WORKER.md to the workspace.")
		return nil
	}
	for _, w := range list {
		next := ""
		if !w.Next.IsZero() {
			next = "  next " + w.Next.Local().Format("Mon Jan 2 15:04")
		}
		fmt.Fprintf(out, "%-20s %-9s %s (%s)%s\n", w.Name, w.State, w.Schedule, w.Cron, next)
		if w.Description != "" {
			fmt.Fprintf(out, "    %s\n", w.Description)
		}
		for _, p := range w.Problems {
			fmt.Fprintf(out, "    ! %s\n", p)
		}
	}
	return nil
}

// describeWorker prints what enabling a worker approves.
func describeWorker(out io.Writer, w app.WorkerInfo) {
	fmt.Fprintf(out, "%s  (%s)\n", w.Name, w.Path)
	if w.Description != "" {
		fmt.Fprintf(out, "  %s\n", w.Description)
	}
	fmt.Fprintf(out, "  schedule:    %s = %s (%s)\n", w.Schedule, w.Cron, w.Timezone)
	perms := "none (it can read, and write nothing)"
	if len(w.Permissions) > 0 {
		perms = strings.Join(w.Permissions, ", ")
	}
	fmt.Fprintf(out, "  may:         %s\n", perms)
	fmt.Fprintf(out, "  limits:      %d model calls, $%.2f, %s\n", w.Limits.MaxTurns, w.Limits.MaxCostUSD, w.Limits.Timeout)
	for _, p := range w.Problems {
		fmt.Fprintf(out, "  ! %s\n", p)
	}
	fmt.Fprintf(out, "  content:     %s\n", w.Hash)
}

func findWorker(list []app.WorkerInfo, name string) (app.WorkerInfo, bool) {
	for _, w := range list {
		if w.Name == name {
			return w, true
		}
	}
	return app.WorkerInfo{}, false
}

func workersEnable(cmd *cobra.Command, g *globalFlags, name string, yes bool) error {
	ops, done, err := openWorkers(cmd.Context(), g)
	if err != nil {
		return err
	}
	defer done()
	list, err := ops.ListWorkers()
	if err != nil {
		return workerErr(err)
	}
	w, found := findWorker(list, name)
	if !found {
		return withCode(exitUsage, fmt.Errorf("no worker %q (see 'code-puppy workers')", name))
	}
	if w.State == workers.StateInvalid {
		describeWorker(cmd.OutOrStdout(), w)
		return withCode(exitUsage, fmt.Errorf("worker %q can't be enabled until WORKER.md is fixed", name))
	}
	out := cmd.OutOrStdout()
	describeWorker(out, w)
	fmt.Fprintf(out, "\nRead %s before enabling: it runs unattended with the permissions above.\n", w.Path)
	if !yes {
		fmt.Fprint(out, "Enable it as shown? [y/N] ")
		answer, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
			return errors.New("not enabled")
		}
	}
	// The hash shown is the one enabled: an edit since fails.
	on, err := ops.EnableWorker(name, w.Hash)
	if err != nil {
		return workerErr(err)
	}
	next := "when the service runs"
	if !on.Next.IsZero() {
		next = on.Next.Local().Format("Mon Jan 2 15:04")
	}
	fmt.Fprintf(out, "✅ %s enabled; next run %s.\n", name, next)
	if !server.Running(server.DefaultSocket()) {
		fmt.Fprintln(out, "Workers run in the Code Puppy service: start it with 'code-puppy serve'.")
	}
	return nil
}

func workersDisable(cmd *cobra.Command, g *globalFlags, name string) error {
	ops, done, err := openWorkers(cmd.Context(), g)
	if err != nil {
		return err
	}
	defer done()
	if _, err := ops.DisableWorker(name); err != nil {
		return workerErr(err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s disabled.\n", name)
	return nil
}

func workersRun(cmd *cobra.Command, g *globalFlags, name string) error {
	ops, done, err := openWorkers(cmd.Context(), g)
	if err != nil {
		return err
	}
	defer done()
	out := cmd.OutOrStdout()
	printer := tui.NewPrinter(tui.PrinterOptions{Out: out})
	run, err := ops.RunWorker(cmd.Context(), name, printer.Handle)
	if err != nil {
		return workerErr(err)
	}
	fmt.Fprintln(out)
	printRun(out, run)
	if run.Status != workers.RunSucceeded {
		return withCode(exitFailure, fmt.Errorf("the run %s", run.Status))
	}
	return nil
}

func workersRuns(cmd *cobra.Command, g *globalFlags, name string, limit int) error {
	ops, done, err := openWorkers(cmd.Context(), g)
	if err != nil {
		return err
	}
	defer done()
	runs, err := ops.WorkerRuns(name, limit)
	if err != nil {
		return workerErr(err)
	}
	if len(runs) == 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "%s hasn't run yet.\n", name)
	}
	for _, r := range runs {
		printRun(cmd.OutOrStdout(), r)
	}
	return nil
}

func printRun(out io.Writer, r workers.Run) {
	how := "scheduled"
	if r.Manual {
		how = "manual"
	}
	fmt.Fprintf(out, "%s  %-9s %s, %s, $%.4f, %d model calls  session %s\n",
		r.Started.Local().Format("2006-01-02 15:04"), r.Status, how, r.Duration.Round(time.Second), r.CostUSD, r.Calls, r.SessionID)
	for _, f := range r.Refusals {
		fmt.Fprintf(out, "    refused: %s (%s)\n", f.Detail, f.Tool)
	}
	if r.Error != "" {
		fmt.Fprintf(out, "    %s\n", r.Error)
	}
}

// workerErr gives worker errors the usage exit code where the request,
// not the system, was at fault.
func workerErr(err error) error {
	for _, e := range []error{app.ErrUnknownWorker, app.ErrWorkerNotEnabled, app.ErrRunInProgress, app.ErrWorkersDisabled, workers.ErrHashMismatch} {
		if errors.Is(err, e) {
			return withCode(exitUsage, err)
		}
	}
	return err
}
