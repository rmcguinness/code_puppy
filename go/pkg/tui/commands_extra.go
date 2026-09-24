package tui

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/retail-cortex/code_puppy/pkg/audit"
	"github.com/retail-cortex/code_puppy/pkg/memory"
	"github.com/retail-cortex/code_puppy/pkg/session"
	"github.com/retail-cortex/code_puppy/pkg/textutil"
	"github.com/retail-cortex/code_puppy/pkg/tools"
)

// handleExtraCommand processes commands added for checkpoints, cost,
// memory, approvals and MCP. It reports whether cmd was recognised.
func handleExtraCommand(ctx context.Context, cmd string, args []string, app *App) bool {
	switch cmd {
	case "undo":
		cmdUndo(args, app)
	case "checkpoints":
		cmdCheckpoints(app)
	case "diff":
		cmdDiff(ctx, args, app)
	case "cost", "usage":
		cmdCost(app)
	case "context":
		cmdContext(app)
	case "memory":
		cmdMemory(ctx, args, app)
	case "approvals":
		cmdApprovals(args, app)
	case "mcp":
		cmdMCP(app)
	case "resume":
		cmdSessionLoad(args, app)
	default:
		return false
	}
	return true
}

func needTools(app *App) bool {
	if app.Tools == nil {
		fmt.Println("Not available in this session.")
		return false
	}
	return true
}

func cmdUndo(args []string, app *App) {
	if !needTools(app) {
		return
	}
	force := len(args) > 0 && (args[0] == "--force" || args[0] == "-f")
	res, err := app.Tools.Checkpoints().Undo(force)
	if errors.Is(err, tools.ErrUndoConflict) {
		fmt.Printf("%s⚠️  %v%s\n", Yellow, err, Reset)
		return
	}
	if len(res.Restored) > 0 {
		fmt.Printf("%s↩️  Undid %q: restored %s%s\n", Green, safe(res.Turn.Label), safe(strings.Join(res.Restored, ", ")), Reset)
		app.Tools.Hooks().Audit().Log(audit.Entry{Kind: audit.KindUndo, Detail: strings.Join(res.Restored, ", ")})
	}
	if err != nil {
		fmt.Printf("%s❌ %v%s\n", Red, err, Reset)
	}
}

func cmdCheckpoints(app *App) {
	if !needTools(app) {
		return
	}
	list := app.Tools.Checkpoints().List()
	if len(list) == 0 {
		fmt.Println("No file changes recorded in this session.")
		return
	}
	fmt.Printf("\n%s📌 Checkpoints (newest first; /undo reverts the top one):%s\n", Bold, Reset)
	for _, c := range list {
		fmt.Printf("  %s#%d%s %s %s%s%s\n      %s\n", Bold, c.ID, Reset, c.Time.Format("15:04:05"), Dim, safe(c.Label), Reset, safe(strings.Join(c.Files, ", ")))
	}
	fmt.Println()
}

func cmdDiff(ctx context.Context, args []string, app *App) {
	if !needTools(app) {
		return
	}
	if len(args) > 0 && args[0] == "git" {
		cmd := exec.CommandContext(ctx, "git", "-c", "color.ui=always", "diff", "--stat", "--patch")
		cmd.Dir = app.Tools.Workspace().Dir()
		out, err := cmd.CombinedOutput()
		if err != nil {
			fmt.Printf("%s❌ git diff failed: %v%s\n%s", Red, err, Reset, safe(string(out)))
			return
		}
		if len(out) == 0 {
			fmt.Println("No uncommitted changes.")
		}
		fmt.Print(string(out))
		return
	}
	d := app.Tools.Checkpoints().SessionDiff()
	if strings.TrimSpace(d) == "" {
		fmt.Println("No changes made by tools in this session. (Use /diff git for the full working tree.)")
		return
	}
	out, _ := RenderDiff(d, 0)
	fmt.Print("\n" + out + "\n")
}

func cmdCost(app *App) {
	active := app.Storage.Active()
	if active == nil {
		fmt.Println("No active session.")
		return
	}
	u := app.Engine.Usage(active.ID)
	fmt.Printf("\n%s💰 Session usage (%s):%s\n", Bold, safe(active.ID), Reset)
	fmt.Printf("  Model calls:   %d\n", u.Calls)
	fmt.Printf("  Input tokens:  %s (%s cached)\n", humanTokens(u.Input), humanTokens(u.Cached))
	fmt.Printf("  Output tokens: %s\n", humanTokens(u.Output))
	if u.Priced {
		fmt.Printf("  Estimated cost: $%.4f\n", u.CostUSD)
	} else {
		fmt.Printf("  Estimated cost: unknown (add [pricing.%q] to your config)\n", app.Engine.ModelName())
	}
	fmt.Println()
}

func cmdContext(app *App) {
	active := app.Storage.Active()
	if active == nil {
		fmt.Println("No active session.")
		return
	}
	u := app.Engine.Usage(active.ID)
	c := app.Cfg.Context
	fmt.Printf("\n%s🧠 Context:%s %s tokens in the last prompt\n", Bold, Reset, humanTokens(u.LastPrompt))
	if c.Compaction && c.TokenThreshold > 0 {
		pct := float64(u.LastPrompt) / float64(c.TokenThreshold) * 100
		fmt.Printf("  Compaction at %s tokens (%.0f%% used); the %d newest events are kept verbatim.\n\n", humanTokens(int64(c.TokenThreshold)), pct, c.RetainEvents)
	} else {
		fmt.Println("  Compaction is disabled ([context] compaction = false).")
		fmt.Println()
	}
}

func cmdMemory(ctx context.Context, args []string, app *App) {
	sub := "show"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "show", "reload":
		if app.ReloadMemory == nil {
			fmt.Println("Project memory is not available.")
			return
		}
		paths, err := app.ReloadMemory(ctx)
		if err != nil {
			fmt.Printf("%s❌ %v%s\n", Red, err, Reset)
			return
		}
		if len(paths) == 0 {
			fmt.Printf("No instruction files found (looked for %s). Use /memory add <note>.\n", strings.Join(app.Cfg.Memory.Files, ", "))
			return
		}
		fmt.Printf("\n%s📝 Loaded instructions:%s\n", Bold, Reset)
		for _, p := range paths {
			fmt.Printf("  • %s\n", safe(p))
		}
		fmt.Println()
	case "add":
		if !needTools(app) {
			return
		}
		file := "PUPPY.md"
		if len(app.Cfg.Memory.Files) > 0 {
			file = app.Cfg.Memory.Files[len(app.Cfg.Memory.Files)-1]
		}
		path, err := memory.Append(app.Tools.Workspace().Dir(), file, strings.Join(args[1:], " "))
		if err != nil {
			fmt.Printf("%s❌ %v%s\n", Red, err, Reset)
			return
		}
		if app.ReloadMemory != nil {
			app.ReloadMemory(ctx)
		}
		fmt.Printf("%s✅ Remembered in %s%s\n", Green, safe(path), Reset)
	default:
		fmt.Println("Usage: /memory [show|reload|add <note>]")
	}
}

func cmdApprovals(args []string, app *App) {
	if !needTools(app) {
		return
	}
	hooks := app.Tools.Hooks()
	store := hooks.Store()
	session := hooks.SessionRules()
	saved := store.Rules()

	if len(args) >= 1 && (args[0] == "revoke" || args[0] == "clear") {
		var targets []string
		if args[0] == "clear" {
			targets = append(targets, session...)
			for _, r := range saved {
				targets = append(targets, r.Key)
			}
		} else {
			n, err := strconv.Atoi(strings.TrimPrefix(strings.Join(args[1:], ""), "#"))
			all := append(append([]string{}, session...), keysOf(saved)...)
			if err != nil || n < 1 || n > len(all) {
				fmt.Println("Usage: /approvals revoke <number>  (see /approvals)")
				return
			}
			targets = []string{all[n-1]}
		}
		for _, k := range targets {
			hooks.RevokeSession(k)
			if store != nil {
				store.Remove(k)
			}
		}
		fmt.Printf("%s✅ Revoked %d rule(s).%s\n", Green, len(targets), Reset)
		return
	}

	if len(session)+len(saved) == 0 {
		fmt.Println("No remembered approvals. Answer [s] or [a] at an approval prompt to add one.")
		return
	}
	fmt.Printf("\n%s🔑 Remembered approvals:%s\n", Bold, Reset)
	n := 0
	for _, k := range session {
		n++
		fmt.Printf("  %d. %s %s(this session)%s\n", n, safe(describeKey(k)), Dim, Reset)
	}
	for _, r := range saved {
		n++
		fmt.Printf("  %d. %s %s(always, since %s)%s\n", n, safe(describeKey(r.Key)), Dim, r.Added.Format("2006-01-02"), Reset)
	}
	fmt.Printf("  %sRevoke with /approvals revoke <number> or /approvals clear%s\n\n", Dim, Reset)
}

func keysOf(rules []tools.ApprovalRule) []string {
	out := make([]string, len(rules))
	for i, r := range rules {
		out[i] = r.Key
	}
	return out
}

// describeKey renders an approval key for people.
func describeKey(k string) string {
	kind, rest, _ := strings.Cut(k, ":")
	switch kind {
	case "cmd":
		if dir, cmd, ok := strings.Cut(rest, "\x00"); ok {
			return "command in " + dir + ": " + cmd
		}
		return "command: " + rest
	case "write":
		return "file edits in " + rest
	case "delete":
		return "file deletions in " + rest
	case "web":
		return "web requests to " + rest
	case "mcp":
		return "MCP tool " + rest
	case "uc-run":
		return "forged tool " + strings.ReplaceAll(rest, "\x00", " ")
	default:
		return k
	}
}

func cmdMCP(app *App) {
	if !needTools(app) {
		return
	}
	servers := app.Tools.MCP().Servers()
	if len(servers) == 0 {
		fmt.Println("No MCP servers configured. Add [[mcp.servers]] entries to your config.")
		return
	}
	fmt.Printf("\n%s🔌 MCP servers:%s\n", Bold, Reset)
	for _, s := range app.Cfg.MCP.Servers {
		target := s.URL
		if target == "" {
			target = strings.Join(append([]string{s.Command}, s.Args...), " ")
		}
		approval := "approval required"
		if s.AutoApprove {
			approval = "auto-approved"
		}
		fmt.Printf("  • %s%s%s: %s %s(%s)%s\n", Bold, safe(s.Name), Reset, safe(target), Dim, approval, Reset)
	}
	fmt.Println()
}

func cmdSessionLoad(args []string, app *App) {
	if len(args) == 0 {
		fmt.Println("Usage: /resume <session-id>   (see /session list)")
		return
	}
	rec, err := app.Storage.Load(args[0])
	if err != nil {
		fmt.Printf("%s❌ %v%s\n", Red, err, Reset)
		return
	}
	fmt.Printf("%s▶️  Resumed session %s (%s, %d messages)%s\n", Green, safe(rec.ID), safe(rec.Title), rec.MessageCount, Reset)
	PrintRecap(rec.Messages, 3)
}

// PrintRecap shows the last n messages of a resumed session.
func PrintRecap(msgs []session.Message, n int) {
	if len(msgs) > n {
		msgs = msgs[len(msgs)-n:]
	}
	for _, m := range msgs {
		who := "you"
		if m.Role != "user" {
			who = "puppy"
		}
		fmt.Printf("  %s%s:%s %s\n", Dim, who, Reset, safe(textutil.Ellipsize(strings.Join(strings.Fields(m.Content), " "), 160)))
	}
}
