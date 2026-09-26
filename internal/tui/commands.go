package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/retail-cortex/code_puppy/internal/i18n"
	"github.com/retail-cortex/code_puppy/internal/session"
)

// ErrExit is returned by HandleCommand when the user asks to quit.
var ErrExit = errors.New("exit")

// HandleCommand processes slash commands. Returns true if input was a slash command.
func HandleCommand(ctx context.Context, input string, app *App) (bool, error) {
	trimmed := strings.TrimSpace(input)
	if !strings.HasPrefix(trimmed, "/") {
		return false, nil
	}

	parts := strings.Fields(trimmed[1:])
	if len(parts) == 0 {
		return true, nil
	}

	cmd := strings.ToLower(parts[0])
	args := parts[1:]
	if cmd == "show" { // Python's /show: the settings, like /set with no arguments
		cmd, args = "set", nil
	}
	cfg, eng := app.Cfg, app.Engine

	switch cmd {
	case "help":
		printHelp()

	case "agents":
		fmt.Printf("\n%s🤖 %s:%s\n", Bold, i18n.T("agents.title"), Reset)
		active := eng.ActiveAgent()
		for _, a := range app.Agents.List() {
			marker := "  "
			if a.Name == active {
				marker = "👉"
			}
			pin := ""
			if m, pinned := eng.AgentModel(a.Name); pinned {
				pin = fmt.Sprintf(" %s[📌 %s]%s", Cyan, safe(m), Reset)
			}
			fmt.Printf("%s %s%s%s (%s)%s: %s\n", marker, Bold, safe(a.DisplayName), Reset, safe(a.Name), pin, safe(a.Description))
		}
		fmt.Println()

	case "agent":
		if len(args) == 0 {
			if spec, ok := app.Agents.Get(eng.ActiveAgent()); ok {
				fmt.Println(i18n.T("agent.current", "name", Bold+safe(spec.DisplayName)+Reset, "id", safe(spec.Name)))
			}
			return true, nil
		}
		target := args[0]
		if err := eng.SetActiveAgent(ctx, target); err != nil {
			fmt.Printf("%s❌ %s%s\n", Red, i18n.T("agent.switch_failed", "error", safe(err.Error())), Reset)
		} else if spec, ok := app.Agents.Get(target); ok {
			fmt.Printf("%s %s%s\n", Green, i18n.T("agent.switched", "name", Bold+safe(spec.DisplayName)), Reset)
		}

	case "model":
		if len(args) == 0 {
			fmt.Println(i18n.T("model.current", "model", Cyan+safe(eng.ModelName())+Reset, "provider", cfg.LLM.Provider))
			return true, nil
		}
		if app.NewModel == nil {
			fmt.Printf("%s%s%s\n", Yellow, i18n.T("model.switch_unavailable"), Reset)
			return true, nil
		}
		llm, err := app.NewModel(ctx, cfg, args[0])
		if err == nil {
			err = eng.SetModel(ctx, llm)
		}
		if err != nil {
			fmt.Printf("%s❌ %s%s\n", Red, i18n.T("model.switch_failed", "error", safe(err.Error())), Reset)
			return true, nil
		}
		cfg.CodePuppy.DefaultModel = args[0]
		fmt.Printf("%s %s%s\n", Green, i18n.T("model.set", "model", Cyan+safe(args[0])), Reset)
		if m, pinned := eng.AgentModel(eng.ActiveAgent()); pinned {
			fmt.Printf("%s%s%s\n", Dim, i18n.T("pin.active_pinned", "agent", eng.ActiveAgent(), "model", safe(m)), Reset)
		}

	case "skills":
		handleSkillsCommand(args, app)

	case "session":
		handleSessionCommand(args, app)

	case "set":
		if len(args) == 0 {
			fmt.Printf("\n%s⚙️ %s:%s\n", Bold, i18n.T("settings.title"), Reset)
			fmt.Printf("  %-14s %s\n", i18n.T("settings.puppy_name")+":", cfg.CodePuppy.PuppyName)
			fmt.Printf("  %-14s %s\n", i18n.T("settings.owner_name")+":", cfg.CodePuppy.OwnerName)
			fmt.Printf("  %-14s %s\n", i18n.T("settings.agency")+":", cfg.CodePuppy.AgencyLevel)
			fmt.Printf("  %-14s %s\n", i18n.T("settings.model")+":", i18n.T("settings.model_value", "model", eng.ModelName(), "provider", cfg.LLM.Provider))
			fmt.Printf("  %-14s %s\n", i18n.T("settings.agent")+":", eng.ActiveAgent())
			fmt.Printf("  %-14s %s\n\n", i18n.T("settings.locale")+":", i18n.Current().Tag())
			return true, nil
		}
		kv := strings.SplitN(strings.Join(args, " "), "=", 2)
		if len(kv) != 2 {
			fmt.Printf("%s%s%s\n", Yellow, i18n.T("set.usage"), Reset)
			return true, nil
		}
		k, v := strings.ToLower(strings.TrimSpace(kv[0])), strings.TrimSpace(kv[1])
		switch k {
		case "agency", "agency_level":
			switch strings.ToLower(v) {
			case "low", "medium", "high", "extreme":
			default:
				fmt.Printf("%s%s%s\n", Yellow, i18n.T("set.agency_invalid"), Reset)
				return true, nil
			}
			cfg.CodePuppy.AgencyLevel = strings.ToLower(v)
		case "puppy_name":
			cfg.CodePuppy.PuppyName = v
		case "owner_name":
			cfg.CodePuppy.OwnerName = v
		default:
			fmt.Printf("%s%s%s\n", Yellow, i18n.T("set.unknown", "key", safe(k)), Reset)
			return true, nil
		}
		// Instructions embed these values, so rebuild the agent tree.
		if err := eng.Rebuild(ctx); err != nil {
			fmt.Printf("%s❌ %s%s\n", Red, i18n.T("set.failed", "error", err), Reset)
			return true, nil
		}
		fmt.Printf("%s %s%s\n", Green, i18n.T("set.updated", "key", k, "value", safe(v)), Reset)

	case "clear":
		fmt.Print("\033[H\033[2J")

	case "sandbox":
		fmt.Printf("\n%s🛡️  %s:%s\n", Bold, i18n.T("sandbox.title"), Reset)
		for _, line := range app.SandboxSummary {
			fmt.Printf("  %s\n", safe(line))
		}
		fmt.Println()

	case "exit", "quit":
		return true, ErrExit

	default:
		if handleExtraCommand(ctx, cmd, args, app) {
			return true, nil
		}
		fmt.Printf("%s%s%s\n", Yellow, i18n.T("command.unknown", "command", safe(cmd)), Reset)
	}

	return true, nil
}

func printHelp() {
	rows := [][2]string{
		{"/agents", "help.agents"},
		{"/agent [name]", "help.agent"},
		{"/model [name]", "help.model"},
		{"/pin_model [<agent> <model>]", "help.pin"},
		{"/unpin <agent>", "help.unpin"},
		{"/model_settings [<model> [key=value…|reset]]", "help.model_settings"},
		{"/skills list|show <name>|search <q>", "help.skills"},
		{"/session list [--all]|new|load <id|name>|save <name>", "help.session"},
		{"/undo [--force]", "help.undo"},
		{"/checkpoints", "help.checkpoints"},
		{"/diff [git]", "help.diff"},
		{"/cost", "help.cost"},
		{"/context", "help.context"},
		{"/compact [focus]", "help.compact"},
		{"/memory [reload|add <note>]", "help.memory"},
		{"/approvals [revoke <n>|clear]", "help.approvals"},
		{"/mcp", "help.mcp"},
		{"/tools", "help.tools"},
		{"/plan <goal>", "help.plan"},
		{"/search web|session <terms>", "help.search"},
		{"/btw <question>", "help.btw"},
		{"/rename <name>", "help.rename"},
		{"/envs [prune|remove <key>]", "help.envs"},
		{"!<command>", "help.shell"},
		{"/sandbox", "help.sandbox"},
		{"/attach [path|clear]", "help.attach"},
		{"/paste", "help.paste"},
		{"/locale [code]", "help.locale"},
		{"/set [key=value], /show", "help.set"},
		{"/clear", "help.clear"},
		{"/exit, /quit", "help.exit"},
	}
	fmt.Printf("\n%s🐾 %s:%s\n", Bold, i18n.T("help.title"), Reset)
	for _, r := range rows {
		fmt.Printf("  %s%-36s%s %s\n", Bold, r[0], Reset, i18n.T(r[1]))
	}
	fmt.Printf("\n  %s%s%s\n\n", Dim, i18n.T("help.input"), Reset)
}

func handleSkillsCommand(args []string, app *App) {
	prov := app.Skills
	if prov == nil {
		fmt.Println(i18n.T("skills.disabled"))
		return
	}

	sub := "list"
	if len(args) > 0 {
		sub = strings.ToLower(args[0])
	}

	switch sub {
	case "list":
		all := prov.List()
		fmt.Printf("\n%s📦 %s:%s\n", Bold, i18n.T("skills.discovered", "count", len(all)), Reset)
		for _, s := range all {
			tags := ""
			if len(s.Tags) > 0 {
				tags = fmt.Sprintf("[%s]", strings.Join(s.Tags, ", "))
			}
			fmt.Printf("  • %s%s%s %s: %s\n", Bold, safe(s.Name), Reset, Dim+safe(tags)+Reset, safe(s.Description))
			if line := scriptSummary(s, skillPolicy(app)); line != "" {
				fmt.Printf("      %s%s%s\n", Dim, safe(line), Reset)
			}
		}
		fmt.Println()

	case "show":
		if len(args) < 2 {
			fmt.Printf("%s%s%s\n", Yellow, i18n.T("skills.show_usage"), Reset)
			return
		}
		showSkill(args[1], prov, skillPolicy(app))

	case "search":
		query := ""
		if len(args) > 1 {
			query = strings.Join(args[1:], " ")
		}
		matched := prov.Search(query)
		fmt.Printf("\n%s🔍 %s:%s\n", Bold, i18n.T("skills.search_results", "query", safe(query), "count", len(matched)), Reset)
		for _, s := range matched {
			fmt.Printf("  • %s%s%s: %s\n", Bold, safe(s.Name), Reset, safe(s.Description))
		}
		fmt.Println()
	}
}

func handleSessionCommand(args []string, app *App) {
	storage := app.Storage
	if storage == nil {
		fmt.Println(i18n.T("session.disabled"))
		return
	}

	sub := "list"
	if len(args) > 0 {
		sub = strings.ToLower(args[0])
	}

	switch sub {
	case "list":
		all := len(args) > 1 && (args[1] == "--all" || args[1] == "-a")
		var list []*session.SessionRecord
		var err error
		if all {
			list, err = storage.List()
		} else {
			list, err = storage.ListWorkspace(storage.Workspace())
		}
		if err != nil {
			fmt.Println(i18n.T("session.list_failed", "error", err))
			return
		}
		title := i18n.T("session.list_title_workspace", "count", len(list))
		if all {
			title = i18n.T("session.list_title_all", "count", len(list))
		}
		fmt.Printf("\n%s📁 %s:%s\n", Bold, title, Reset)
		for _, s := range list {
			snapshot := ""
			if s.Name != "" {
				snapshot = " " + Cyan + "📸 " + safe(s.Name) + Reset
			}
			fmt.Printf("  • %s%s%s (%s): %s [%s]%s\n", Bold, safe(s.ID), Reset, safe(s.Agent), safe(sessionTitle(s)), i18n.N("session.messages", s.MessageCount), snapshot)
			if all {
				ws := s.Workspace
				if ws == "" {
					ws = i18n.T("session.workspace_unknown")
				}
				fmt.Printf("      %s%s%s\n", Dim, safe(ws), Reset)
			}
		}
		if !all {
			fmt.Printf("  %s%s%s\n", Dim, i18n.T("session.list_all_hint"), Reset)
		}
		fmt.Println()

	case "load", "resume":
		cmdSessionLoad(args[1:], app)

	case "save":
		cmdSessionSave(args[1:], app)

	case "new":
		rec, err := storage.CreateSession(session.NewSessionID(), "", app.Engine.ActiveAgent())
		if err != nil {
			fmt.Printf("%s❌ %s%s\n", Red, i18n.T("session.create_failed", "error", err), Reset)
			return
		}
		fmt.Printf("%s %s%s\n", Green, i18n.T("session.started", "id", rec.ID), Reset)
	}
}
