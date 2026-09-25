package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/retail-cortex/code_puppy/pkg/config"
	"github.com/retail-cortex/code_puppy/pkg/i18n"
	"github.com/retail-cortex/code_puppy/pkg/tui"
	"github.com/spf13/cobra"
)

var version = "2.0.0-go"

// rootOptions are the flags of the main (prompt/REPL) command.
type rootOptions struct {
	global       globalFlags
	prompt       string
	interactive  bool
	version      bool
	resume       string
	cont         bool
	outputFormat string
	maxTurns     int
	plan         bool
	images       []string
}

func main() {
	// Until startObservability installs the log file, slog's default would
	// print to stderr and duplicate the terminal warnings.
	slog.SetDefault(slog.New(slog.DiscardHandler))
	root := newRootCommand()
	err := root.Execute()
	if err != nil {
		fmt.Fprintln(os.Stderr, i18n.T("repl.error", "error", err))
	}
	os.Exit(exitCodeFor(err))
}

func newRootCommand() *cobra.Command {
	o := &rootOptions{}
	root := &cobra.Command{
		Use:   "code-puppy [flags] [prompt...]",
		Short: "🐶 Code Puppy - autonomous AI coding agent built on Google ADK",
		Long: `Code Puppy is an AI coding agent. Run it with no arguments for an interactive
session, or pass a prompt to run once and exit.

Exit codes: 0 success, 1 error, 2 usage, 3 --max-turns reached,
4 prompt blocked by a hook, 130 interrupted.`,
		Example: `  code-puppy                          # interactive
  code-puppy -d ~/src/app "fix the failing test"
  git diff | code-puppy -p - --output-format json
  code-puppy --continue "now add docs"`,
		Args:          cobra.ArbitraryArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRoot(cmd, o, args)
		},
	}
	root.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		return withCode(exitUsage, fmt.Errorf("%w\nRun '%s --help' for usage.", err, c.CommandPath()))
	})

	pf := root.PersistentFlags()
	pf.StringVarP(&o.global.config, "config", "c", "", "Directory containing .env.toml config (default ~/.code_puppy)")
	pf.StringVarP(&o.global.dir, "dir", "d", "", "Workspace directory to start in (default: current directory)")

	f := root.Flags()
	f.StringVarP(&o.prompt, "prompt", "p", "", `One-shot prompt; "-" reads it from stdin`)
	f.BoolVarP(&o.interactive, "interactive", "i", false, "Start the interactive REPL even when a prompt is given")
	f.StringVarP(&o.global.agent, "agent", "a", "", "Agent persona to activate (code-puppy, helios, qa-kitten, ...)")
	f.StringVarP(&o.global.model, "model", "m", "", "Model identifier to use")
	f.StringVar(&o.global.agency, "agency", "", "Agency level (low, medium, high, extreme)")
	f.BoolVar(&o.global.trustWorkspace, "trust-workspace", false, "Load agents and skills from the workspace (./agents, ./skills, .agents/skills)")
	f.BoolVarP(&o.version, "version", "v", false, "Print Code Puppy version")
	f.StringVarP(&o.resume, "resume", "r", "", "Resume a saved session by ID (no ID: the most recent)")
	f.Lookup("resume").NoOptDefVal = "latest"
	f.BoolVarP(&o.cont, "continue", "C", false, "Continue the most recent session")
	f.StringVar(&o.outputFormat, "output-format", formatText, "Output for one-shot runs: text, json, or stream-json")
	f.IntVar(&o.maxTurns, "max-turns", 0, "Stop after this many model calls in a one-shot run (0 = unlimited)")
	f.BoolVar(&o.plan, "plan", false, "One-shot plan: the agent may read and search but not edit or run commands")
	f.StringArrayVar(&o.images, "image", nil, "Attach an image to the first prompt (repeatable); @file.png in a prompt also works")

	root.AddCommand(newDoctorCommand(&o.global), newConfigCommand(&o.global))
	return root
}

func runRoot(cmd *cobra.Command, o *rootOptions, args []string) (err error) {
	if o.version {
		fmt.Printf("Code Puppy Go (Google ADK) version %s\n", version)
		return nil
	}
	switch o.outputFormat {
	case formatText, formatJSON, formatStreamJSON:
	default:
		return withCode(exitUsage, fmt.Errorf("invalid --output-format %q (use text, json, or stream-json)", o.outputFormat))
	}
	if o.maxTurns < 0 {
		return withCode(exitUsage, errors.New("--max-turns must be >= 0"))
	}

	stdinTTY := tui.StdinIsTerminal()
	prompt, stdinUsed, err := resolvePrompt(o.prompt, args, stdinTTY, o.interactive, os.Stdin)
	if err != nil {
		return err
	}
	oneShot := prompt != "" && !o.interactive
	if !oneShot && o.plan {
		return withCode(exitUsage, errors.New("--plan requires a prompt (in a session, use /plan <goal>)"))
	}
	if !oneShot && o.outputFormat != formatText {
		return withCode(exitUsage, errors.New("--output-format json/stream-json requires a prompt"))
	}

	// SIGTERM (and Ctrl+C in one-shot mode) cancels in-flight model calls and
	// tool processes so deferred cleanup runs. The REPL handles Ctrl+C itself.
	sigs := []os.Signal{syscall.SIGTERM}
	if oneShot {
		sigs = append(sigs, os.Interrupt)
	}
	ctx, stop := signal.NotifyContext(context.Background(), sigs...)
	defer stop()

	cfg, err := loadConfig(&o.global)
	if err != nil {
		return err
	}

	textMode := o.outputFormat == formatText
	pretty := textMode && stdoutIsTerminal()
	warnOut := os.Stderr
	warnFn := func(msg string) {
		slog.Warn(msg)
		fmt.Fprintf(warnOut, "%s⚠️  %s%s\n", tui.Yellow, msg, tui.Reset)
	}
	defer startObservability(ctx, cfg, warnFn)()
	defer func() { // runs before the log closes
		if err != nil {
			slog.Error("exit", "code", exitCodeFor(err), "error", err)
		}
	}()

	e, err := buildEnv(ctx, cfg, envOptions{streaming: pretty, warn: warnFn})
	if err != nil {
		return err
	}
	defer func() {
		if cerr := e.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	if e.modelErr != nil {
		msg := i18n.T("startup.model_failed", "error", modelErrorSummary(e.modelErr, cfg))
		if oneShot {
			// A placeholder model would "succeed" silently; scripts need a real failure.
			return withCode(exitFailure, fmt.Errorf("%s (run 'code-puppy doctor')", msg))
		}
		warnFn(msg)
		warnFn(i18n.T("startup.placeholder_model"))
	}

	// One input source for everything read from the terminal.
	var input tui.Input
	switch {
	case stdinUsed:
		// stdin carried the prompt; nothing can be answered interactively.
	case stdinTTY && !oneShot:
		ti, terr := tui.NewTerminalInput(tui.TerminalOptions{
			HistoryFile: config.ExpandHome(cfg.UI.HistoryFile),
			HistorySize: cfg.UI.HistorySize,
			Completer:   newCompleter(e),
		})
		if terr != nil {
			warnFn(i18n.T("startup.line_editor", "error", terr.Error()))
			input = tui.NewLineReader(os.Stdin, os.Stdout)
		} else {
			defer ti.Close()
			input = ti
		}
	default:
		promptOut := io.Writer(os.Stdout)
		if !textMode {
			promptOut = os.Stderr // keep stdout pure JSON
		}
		input = tui.NewLineReader(os.Stdin, promptOut)
	}
	if input != nil {
		e.tools.Hooks().SetApprover(tui.NewApprover(input, cfg.UI.DiffLines))
		e.tools.Hooks().SetUserPrompter(tui.NewUserPrompter(input))
	}

	attachPrompt := ""
	if oneShot {
		attachPrompt = prompt
	}
	attached, err := loadAttachments(e, o.images, attachPrompt, warnFn)
	if err != nil {
		return withCode(exitUsage, err)
	}

	title := i18n.T("session.interactive_title")
	if oneShot {
		title = firstLine(prompt, 60)
	}
	sess, resumed, err := selectSession(e.storage, o.resume, o.cont, title, e.engine.ActiveAgent())
	if err != nil {
		return err
	}
	e.audit.SetContext(sess.ID, e.tools.Workspace().Dir())

	if oneShot {
		return runOneShot(ctx, e, oneShotOptions{
			prompt: prompt, sessionID: sess.ID, format: o.outputFormat, maxTurns: o.maxTurns, plan: o.plan,
			input: input, stdinTTY: stdinTTY && !stdinUsed, stdout: os.Stdout,
			markdown: pretty && cfg.UI.Markdown, spinner: pretty && cfg.UI.Spinner, width: terminalWidth(),
			usageLines: pretty, images: attached,
		})
	}

	if resumed && sess.Workspace != "" && sess.Workspace != e.storage.Workspace() {
		warnFn(i18n.T("resume.other_workspace_id", "id", sess.ID, "workspace", sess.Workspace))
	}
	if resumed {
		fmt.Printf("%s▶️  %s%s\n", tui.Green, i18n.T("resume.starting", "id", sess.ID, "messages", i18n.N("session.messages", sess.MessageCount)), tui.Reset)
		tui.PrintRecap(sess.Messages, 3)
		fmt.Println()
	}
	return tui.RunREPL(ctx, &tui.App{
		Version:        version,
		Cfg:            cfg,
		Engine:         e.engine,
		Agents:         e.agents,
		Skills:         e.skills,
		Storage:        e.storage,
		Input:          input,
		Tools:          e.tools,
		Processes:      e.tools.Processes(),
		SandboxSummary: e.tools.SandboxSummary(),
		NewModel:       e.newModel,
		ReloadMemory:   e.reloadMemory,
		Attachments:    attached,
		Locales:        e.locales,
		SetLocale:      e.setLocale,
		SaveAgentModel: e.saveAgentModel,
		Printer: tui.PrinterOptions{
			Out: os.Stdout, Markdown: pretty && cfg.UI.Markdown, Theme: cfg.UI.Theme,
			Width: terminalWidth(), Spinner: pretty && cfg.UI.Spinner,
		},
	})
}

// resolvePrompt returns the prompt from -p, arguments, or stdin. stdin is
// read when -p is "-" or when it is piped and no prompt was given.
func resolvePrompt(flag string, args []string, stdinTTY, interactive bool, stdin io.Reader) (prompt string, stdinUsed bool, err error) {
	switch {
	case flag == "-" || (flag == "" && !stdinTTY && !interactive):
		data, err := io.ReadAll(io.LimitReader(stdin, 10<<20))
		if err != nil {
			return "", false, fmt.Errorf("read prompt from stdin: %w", err)
		}
		piped := strings.TrimSpace(string(data))
		// Arguments frame the piped content: `git diff | code-puppy review this`.
		switch {
		case len(args) > 0 && piped != "":
			prompt = strings.Join(args, " ") + "\n\n" + piped
		case len(args) > 0:
			prompt = strings.Join(args, " ")
		default:
			prompt = piped
		}
		if prompt == "" {
			// stdin is consumed, so there is nothing left to drive a REPL.
			return "", true, withCode(exitUsage, errors.New("no prompt: stdin was empty (use -i for an interactive session)"))
		}
		return prompt, true, nil
	case flag != "":
		if len(args) > 0 {
			return "", false, withCode(exitUsage, errors.New("give the prompt either with -p or as arguments, not both"))
		}
		return flag, false, nil
	default:
		return strings.Join(args, " "), false, nil
	}
}

func firstLine(s string, n int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > n {
		s = s[:n] + "…"
	}
	return s
}

// newCompleter registers slash commands and dynamic argument sources.
func newCompleter(e *env) *tui.Completer {
	c := tui.NewCompleter(e.tools.Workspace().Dir())
	for _, cmd := range []string{"help", "agents", "model", "skills", "session", "set", "clear", "sandbox", "exit", "quit",
		"undo", "checkpoints", "diff", "cost", "context", "compact", "memory", "approvals", "mcp", "resume", "locale", "attach", "paste",
		"tools", "plan", "show", "pin_model", "unpin"} {
		c.Command(cmd)
	}
	c.Command("skills", "list", "search")
	c.Command("session", "list", "new", "load")
	c.Command("memory", "show", "reload", "add")
	c.Command("approvals", "revoke", "clear")
	c.Command("diff", "git")
	c.Command("attach", "clear")
	c.Command("undo", "--force")
	c.Command("compact")
	c.Command("set", "agency=", "puppy_name=", "owner_name=")
	c.Dynamic("agent", func() []string {
		var names []string
		for _, a := range e.agents.List() {
			names = append(names, a.Name)
		}
		return names
	})
	agentNames := func() []string {
		var names []string
		for _, a := range e.agents.List() {
			names = append(names, a.Name)
		}
		return names
	}
	c.Dynamic("pin_model", agentNames)
	c.Dynamic("unpin", func() []string {
		var names []string
		for _, n := range agentNames() {
			if _, pinned := e.engine.AgentModel(n); pinned {
				names = append(names, n)
			}
		}
		return names
	})
	c.Dynamic("locale", func() []string {
		var tags []string
		for _, m := range e.locales.Available() {
			tags = append(tags, m.Locale)
		}
		return tags
	})
	c.Dynamic("resume", func() []string {
		var ids []string
		if list, err := e.storage.ListWorkspace(e.storage.Workspace()); err == nil {
			for i, s := range list {
				if i == 20 {
					break
				}
				ids = append(ids, s.ID)
			}
		}
		return ids
	})
	return c
}
