package tui

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/retail-cortex/code_puppy/pkg/audit"
	"github.com/retail-cortex/code_puppy/pkg/i18n"
	"github.com/retail-cortex/code_puppy/pkg/memory"
	"github.com/retail-cortex/code_puppy/pkg/runtime"
	"github.com/retail-cortex/code_puppy/pkg/session"
	"github.com/retail-cortex/code_puppy/pkg/textutil"
	"github.com/retail-cortex/code_puppy/pkg/tools"
	"google.golang.org/adk/v2/model"
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
	case "compact":
		cmdCompact(ctx, args, app)
	case "memory":
		cmdMemory(ctx, args, app)
	case "approvals":
		cmdApprovals(args, app)
	case "mcp":
		cmdMCP(app)
	case "resume":
		cmdSessionLoad(args, app)
	case "attach":
		cmdAttach(args, app)
	case "paste":
		cmdPaste(ctx, app)
	case "locale", "lang", "language":
		cmdLocale(ctx, args, app)
	case "tools":
		cmdTools(app)
	case "pin_model", "pin":
		cmdPinModel(ctx, args, app)
	case "unpin":
		cmdUnpin(ctx, args, app)
	case "model_settings":
		cmdModelSettings(args, app)
	default:
		return false
	}
	return true
}

func needTools(app *App) bool {
	if app.Tools == nil {
		fmt.Println(i18n.T("common.not_available"))
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
		fmt.Printf("%s↩️  %s%s\n", Green, i18n.T("undo.done", "label", strconv.Quote(safe(res.Turn.Label)), "files", safe(strings.Join(res.Restored, ", "))), Reset)
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
		fmt.Println(i18n.T("checkpoints.none"))
		return
	}
	fmt.Printf("\n%s📌 %s%s\n", Bold, i18n.T("checkpoints.title"), Reset)
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
			fmt.Printf("%s❌ %s%s\n%s", Red, i18n.T("diff.git_failed", "error", err), Reset, safe(string(out)))
			return
		}
		if len(out) == 0 {
			fmt.Println(i18n.T("diff.git_clean"))
		}
		fmt.Print(string(out))
		return
	}
	d := app.Tools.Checkpoints().SessionDiff()
	if strings.TrimSpace(d) == "" {
		fmt.Println(i18n.T("diff.none"))
		return
	}
	out, _ := RenderDiff(d, 0)
	fmt.Print("\n" + out + "\n")
}

func cmdCost(app *App) {
	active := app.Storage.Active()
	if active == nil {
		fmt.Println(i18n.T("session.none_active"))
		return
	}
	u := app.Engine.Usage(active.ID)
	fmt.Printf("\n%s💰 %s%s\n", Bold, i18n.T("cost.title", "id", safe(active.ID)), Reset)
	fmt.Printf("  %s\n", i18n.T("cost.calls", "count", u.Calls))
	fmt.Printf("  %s\n", i18n.T("cost.input", "input", humanTokens(u.Input), "cached", humanTokens(u.Cached), "written", humanTokens(u.CacheWrite)))
	fmt.Printf("  %s\n", i18n.T("cost.output", "output", humanTokens(u.Output)))
	if u.Priced {
		fmt.Printf("  %s\n", i18n.T("cost.estimate", "cost", fmt.Sprintf("$%.4f", u.CostUSD)))
	} else {
		fmt.Printf("  %s\n", i18n.T("cost.unknown", "model", strconv.Quote(app.Engine.ModelName())))
	}
	fmt.Println()
}

func cmdContext(app *App) {
	active := app.Storage.Active()
	if active == nil {
		fmt.Println(i18n.T("session.none_active"))
		return
	}
	u := app.Engine.Usage(active.ID)
	c := app.Cfg.Context
	fmt.Printf("\n%s🧠 %s%s %s\n", Bold, i18n.T("context.title"), Reset, i18n.T("context.size", "tokens", humanTokens(u.LastPrompt)))
	if c.Compaction && c.TokenThreshold > 0 {
		pct := float64(u.LastPrompt) / float64(c.TokenThreshold) * 100
		fmt.Printf("  %s\n\n", i18n.T("context.threshold", "threshold", humanTokens(int64(c.TokenThreshold)), "percent", fmt.Sprintf("%.0f", pct), "keep", c.RetainEvents))
	} else {
		fmt.Println("  " + i18n.T("context.auto_off"))
		fmt.Println()
	}
}

func cmdCompact(ctx context.Context, args []string, app *App) {
	active := app.Storage.Active()
	if active == nil {
		fmt.Println(i18n.T("session.none_active"))
		return
	}
	before := app.Engine.Usage(active.ID)
	fmt.Printf("%s🗜️  %s%s\n", Dim, i18n.T("compact.running"), Reset)
	res, err := app.Engine.Compact(ctx, active.ID, strings.Join(args, " "), 1)
	if err != nil {
		if errors.Is(err, runtime.ErrNothingToCompact) {
			fmt.Printf("%s%v%s\n", Yellow, err, Reset)
			return
		}
		fmt.Printf("%s❌ %s%s\n", Red, i18n.T("compact.failed", "error", safe(err.Error())), Reset)
		return
	}
	fmt.Printf("%s✅ %s%s\n", Green, i18n.T("compact.done", "events", res.EventsCompacted, "chars", res.SummaryChars), Reset)
	if line := UsageLine(before, app.Engine.Usage(active.ID)); line != "" {
		fmt.Printf("%s%s%s\n", Dim, line, Reset)
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
			fmt.Println(i18n.T("memory.unavailable"))
			return
		}
		paths, err := app.ReloadMemory(ctx)
		if err != nil {
			fmt.Printf("%s❌ %v%s\n", Red, err, Reset)
			return
		}
		if len(paths) == 0 {
			fmt.Println(i18n.T("memory.none", "files", strings.Join(app.Cfg.Memory.Files, ", ")))
			return
		}
		fmt.Printf("\n%s📝 %s%s\n", Bold, i18n.T("memory.loaded"), Reset)
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
		fmt.Printf("%s✅ %s%s\n", Green, i18n.T("memory.added", "path", safe(path)), Reset)
	default:
		fmt.Println(i18n.T("memory.usage"))
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
				fmt.Println(i18n.T("approvals.revoke_usage"))
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
		fmt.Printf("%s✅ %s%s\n", Green, i18n.N("approvals.revoked", len(targets)), Reset)
		return
	}

	if len(session)+len(saved) == 0 {
		fmt.Println(i18n.T("approvals.none"))
		return
	}
	fmt.Printf("\n%s🔑 %s%s\n", Bold, i18n.T("approvals.title"), Reset)
	n := 0
	for _, k := range session {
		n++
		fmt.Printf("  %d. %s %s(%s)%s\n", n, safe(describeKey(k)), Dim, i18n.T("approvals.scope_session"), Reset)
	}
	for _, r := range saved {
		n++
		fmt.Printf("  %d. %s %s(%s)%s\n", n, safe(describeKey(r.Key)), Dim, i18n.T("approvals.scope_always", "date", r.Added.Format("2006-01-02")), Reset)
	}
	fmt.Printf("  %s%s%s\n\n", Dim, i18n.T("approvals.revoke_hint"), Reset)
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
			return i18n.T("approval_key.command_in", "dir", dir, "command", cmd)
		}
		return i18n.T("approval_key.command", "command", rest)
	case "write":
		return i18n.T("approval_key.write", "path", rest)
	case "delete":
		return i18n.T("approval_key.delete", "path", rest)
	case "web":
		return i18n.T("approval_key.web", "host", rest)
	case "mcp":
		return i18n.T("approval_key.mcp", "tool", rest)
	case "uc-run":
		return i18n.T("approval_key.uc", "tool", strings.ReplaceAll(rest, "\x00", " "))
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
		fmt.Println(i18n.T("mcp.none"))
		return
	}
	fmt.Printf("\n%s🔌 %s%s\n", Bold, i18n.T("mcp.title"), Reset)
	for _, s := range app.Cfg.MCP.Servers {
		target := s.URL
		if target == "" {
			target = strings.Join(append([]string{s.Command}, s.Args...), " ")
		}
		approval := i18n.T("mcp.approval_required")
		if s.AutoApprove {
			approval = i18n.T("mcp.auto_approved")
		}
		fmt.Printf("  • %s%s%s: %s %s(%s)%s\n", Bold, safe(s.Name), Reset, safe(target), Dim, approval, Reset)
	}
	fmt.Println()
}

// cmdPinModel pins an agent to a model ("provider/model") for this session
// and in the config file; without arguments it lists the pins.
func cmdPinModel(ctx context.Context, args []string, app *App) {
	if len(args) == 0 {
		listed := false
		for _, a := range app.Agents.List() {
			if m, pinned := app.Engine.AgentModel(a.Name); pinned {
				if !listed {
					fmt.Printf("\n%s📌 %s%s\n", Bold, i18n.T("pin.title"), Reset)
					listed = true
				}
				fmt.Printf("  %s%-18s%s %s\n", Bold, safe(a.Name), Reset, safe(m))
			}
		}
		if !listed {
			fmt.Println(i18n.T("pin.none"))
		}
		fmt.Println()
		return
	}
	if len(args) != 2 {
		fmt.Printf("%s%s%s\n", Yellow, i18n.T("pin.usage"), Reset)
		return
	}
	agent, ref := args[0], args[1]
	if _, ok := app.Agents.Get(agent); !ok {
		fmt.Printf("%s❌ %s%s\n", Red, i18n.T("pin.unknown_agent", "agent", safe(agent)), Reset)
		return
	}
	if app.NewModel == nil {
		fmt.Printf("%s%s%s\n", Yellow, i18n.T("model.switch_unavailable"), Reset)
		return
	}
	llm, err := app.NewModel(ctx, app.Cfg, ref)
	if err == nil {
		err = app.Engine.PinModel(ctx, agent, llm)
	}
	if err != nil {
		fmt.Printf("%s❌ %s%s\n", Red, i18n.T("pin.failed", "error", safe(err.Error())), Reset)
		return
	}
	fmt.Printf("%s📌 %s%s\n", Green, i18n.T("pin.done", "agent", safe(agent), "model", safe(llm.Name())), Reset)
	saveAgentModel(app, agent, ref)
}

// cmdUnpin returns an agent to the configured model, or to its own
// default_model if it declares one.
func cmdUnpin(ctx context.Context, args []string, app *App) {
	if len(args) != 1 {
		fmt.Printf("%s%s%s\n", Yellow, i18n.T("pin.unpin_usage"), Reset)
		return
	}
	agent := args[0]
	spec, ok := app.Agents.Get(agent)
	if !ok {
		fmt.Printf("%s❌ %s%s\n", Red, i18n.T("pin.unknown_agent", "agent", safe(agent)), Reset)
		return
	}
	err := app.Engine.Unpin(ctx, agent)
	if err == nil && spec.DefaultModel != "" && app.NewModel != nil {
		var llm model.LLM
		if llm, err = app.NewModel(ctx, app.Cfg, spec.DefaultModel); err == nil {
			err = app.Engine.PinModel(ctx, agent, llm)
		}
	}
	if err != nil {
		fmt.Printf("%s❌ %s%s\n", Red, i18n.T("pin.failed", "error", safe(err.Error())), Reset)
		return
	}
	m, _ := app.Engine.AgentModel(agent)
	fmt.Printf("%s✅ %s%s\n", Green, i18n.T("pin.unpinned", "agent", safe(agent), "model", safe(m)), Reset)
	saveAgentModel(app, agent, "")
}

func saveAgentModel(app *App, agent, ref string) {
	if app.SaveAgentModel == nil {
		return
	}
	if path, err := app.SaveAgentModel(agent, ref); err != nil {
		fmt.Printf("%s⚠️  %s%s\n", Yellow, i18n.T("pin.save_failed", "error", safe(err.Error())), Reset)
	} else {
		fmt.Printf("%s%s%s\n", Dim, i18n.T("pin.saved", "path", safe(path)), Reset)
	}
}

// cmdTools lists what the active agent can use: its built-in tools and the
// MCP servers offered to it. ● marks tools that stay available in /plan.
func cmdTools(app *App) {
	if !needTools(app) {
		return
	}
	active := app.Engine.ActiveAgent()
	spec, ok := app.Agents.Get(active)
	if !ok {
		return
	}
	list := app.Tools.GetToolsForAgent(spec.Tools)
	sort.Slice(list, func(i, j int) bool { return list[i].Name() < list[j].Name() })
	fmt.Printf("\n%s🧰 %s%s\n", Bold, i18n.T("tools.title", "agent", safe(active)), Reset)
	for _, t := range list {
		mark := " "
		if runtime.PlanAllows(t.Name()) {
			mark = Green + "●" + Reset
		}
		fmt.Printf("  %s %s%-26s%s %s\n", mark, Bold, safe(t.Name()), Reset, safe(textutil.Ellipsize(firstSentence(t.Description()), 70)))
	}
	for _, s := range app.Cfg.MCP.Servers {
		if !mcpOfferedTo(s.Agents, active) {
			continue
		}
		detail := i18n.T("tools.mcp_all")
		if len(s.Tools) > 0 {
			detail = strings.Join(s.Tools, ", ")
		}
		if s.Prefix != "" {
			detail += " " + i18n.T("tools.mcp_prefix", "prefix", s.Prefix+"__")
		}
		fmt.Printf("  %s %s%-26s%s %s\n", " ", Bold, "mcp:"+safe(s.Name), Reset, safe(detail))
	}
	fmt.Printf("\n  %s%s%s\n\n", Dim, i18n.T("tools.legend"), Reset)
}

// mcpOfferedTo mirrors the MCP manager's rule for the primary agent: no
// agents list means the primary agent only; "*" means every agent.
func mcpOfferedTo(agents []string, active string) bool {
	if len(agents) == 0 {
		return true
	}
	for _, a := range agents {
		if a == "*" || a == active {
			return true
		}
	}
	return false
}

func firstSentence(s string) string {
	s = strings.TrimSpace(strings.SplitN(s, "\n", 2)[0])
	if i := strings.Index(s, ". "); i >= 0 {
		s = s[:i+1]
	}
	return s
}

func cmdSessionLoad(args []string, app *App) {
	if len(args) == 0 {
		fmt.Println(i18n.T("resume.usage"))
		return
	}
	rec, branched, err := app.Storage.Open(args[0])
	if err != nil {
		fmt.Printf("%s❌ %v%s\n", Red, err, Reset)
		return
	}
	if branched {
		fmt.Printf("%s▶️  %s%s\n", Green, i18n.T("snapshot.branched", "id", safe(rec.ID), "name", safe(args[0]), "messages", i18n.N("session.messages", rec.MessageCount)), Reset)
		PrintRecap(rec.Messages, 3)
		return
	}
	fmt.Printf("%s▶️  %s%s\n", Green, i18n.T("resume.done", "id", safe(rec.ID), "title", safe(rec.Title), "messages", i18n.N("session.messages", rec.MessageCount)), Reset)
	if rec.Workspace != "" && rec.Workspace != app.Storage.Workspace() {
		fmt.Printf("%s⚠️  %s%s\n", Yellow, i18n.T("resume.other_workspace", "workspace", safe(rec.Workspace)), Reset)
	}
	PrintRecap(rec.Messages, 3)
}

// cmdSessionSave saves the active session as a named snapshot, to return
// to later with /session load <name>. --force replaces a snapshot of that name.
func cmdSessionSave(args []string, app *App) {
	force := slices.Contains(args, "--force")
	args = slices.DeleteFunc(slices.Clone(args), func(a string) bool { return a == "--force" })
	if len(args) != 1 {
		fmt.Printf("%s%s%s\n", Yellow, i18n.T("snapshot.usage"), Reset)
		return
	}
	active := app.Storage.Active()
	if active == nil {
		fmt.Println(i18n.T("session.none_active"))
		return
	}
	name := args[0]
	rec, err := app.Storage.Snapshot(active.ID, name, force)
	switch {
	case errors.Is(err, session.ErrNameTaken):
		fmt.Printf("%s⚠️  %s%s\n", Yellow, i18n.T("snapshot.taken", "name", safe(name)), Reset)
	case err != nil:
		fmt.Printf("%s❌ %s%s\n", Red, i18n.T("snapshot.failed", "error", safe(err.Error())), Reset)
	default:
		fmt.Printf("%s📸 %s%s\n", Green, i18n.T("snapshot.saved", "name", safe(name), "messages", i18n.N("session.messages", rec.MessageCount)), Reset)
	}
}

// PrintRecap shows the last n messages of a resumed session.
func PrintRecap(msgs []session.Message, n int) {
	if len(msgs) > n {
		msgs = msgs[len(msgs)-n:]
	}
	for _, m := range msgs {
		who := i18n.T("recap.you")
		if m.Role != "user" {
			who = i18n.T("recap.puppy")
		}
		fmt.Printf("  %s%s:%s %s\n", Dim, who, Reset, safe(textutil.Ellipsize(strings.Join(strings.Fields(m.Content), " "), 160)))
	}
}

func cmdLocale(ctx context.Context, args []string, app *App) {
	b := app.Locales
	if b == nil {
		b = i18n.Default()
	}
	if len(args) == 0 {
		cur := i18n.Current()
		fmt.Printf("\n%s🌐 %s%s\n", Bold, i18n.T("locale.current", "name", cur.NativeName(), "tag", cur.Tag()), Reset)
		var names []string
		for _, m := range b.Available() {
			names = append(names, m.Locale+" ("+m.Name+")")
		}
		fmt.Println("  " + i18n.T("locale.available", "list", strings.Join(names, ", ")))
		if app.Cfg != nil && app.Cfg.UI.LocalesDir != "" {
			fmt.Printf("  %s%s%s\n", Dim, i18n.T("locale.custom_hint", "dir", app.Cfg.UI.LocalesDir), Reset)
		}
		fmt.Println()
		return
	}
	if app.SetLocale == nil {
		fmt.Println(i18n.T("locale.unavailable"))
		return
	}
	input := strings.Join(args, " ")
	tag, err := b.Resolve(input)
	if err != nil {
		fmt.Printf("%s❌ %s%s\n", Red, i18n.T("locale.unknown", "input", strconv.Quote(safe(input))), Reset)
		return
	}
	l := b.Localizer(tag)
	i18n.SetCurrent(l)
	path, err := app.SetLocale(ctx, l)
	fmt.Printf("%s✅ %s%s\n", Green, i18n.T("locale.changed", "name", l.NativeName(), "tag", tag), Reset)
	if !l.HasCatalog() {
		fmt.Printf("%s%s%s\n", Yellow, i18n.T("locale.no_catalog", "name", l.LanguageName()), Reset)
	}
	if err != nil {
		fmt.Printf("%s⚠️  %s%s\n", Yellow, i18n.T("locale.save_failed", "error", safe(err.Error())), Reset)
	} else if path != "" {
		fmt.Printf("%s%s%s\n", Dim, i18n.T("locale.saved", "path", safe(path)), Reset)
	}
}
