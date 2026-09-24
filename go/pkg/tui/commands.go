package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/retail-cortex/code_puppy/pkg/session"
	"github.com/retail-cortex/code_puppy/pkg/skills"
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
	cfg, eng := app.Cfg, app.Engine

	switch cmd {
	case "help":
		printHelp()

	case "agents":
		fmt.Printf("\n%s🤖 Available Agents:%s\n", Bold, Reset)
		active := eng.ActiveAgent()
		for _, a := range app.Agents.List() {
			marker := "  "
			if a.Name == active {
				marker = "👉"
			}
			fmt.Printf("%s %s%s%s (%s): %s\n", marker, Bold, safe(a.DisplayName), Reset, safe(a.Name), safe(a.Description))
		}
		fmt.Println()

	case "agent":
		if len(args) == 0 {
			if spec, ok := app.Agents.Get(eng.ActiveAgent()); ok {
				fmt.Printf("Current agent: %s%s%s (%s)\n", Bold, safe(spec.DisplayName), Reset, safe(spec.Name))
			}
			return true, nil
		}
		target := args[0]
		if err := eng.SetActiveAgent(ctx, target); err != nil {
			fmt.Printf("%s❌ Error switching agent: %v%s\n", Red, safe(err.Error()), Reset)
		} else if spec, ok := app.Agents.Get(target); ok {
			fmt.Printf("%s Switched active agent to: %s%s%s\n", Green, Bold, safe(spec.DisplayName), Reset)
		}

	case "model":
		if len(args) == 0 {
			fmt.Printf("Current model: %s%s%s (Provider: %s)\n", Cyan, safe(eng.ModelName()), Reset, cfg.LLM.Provider)
			return true, nil
		}
		if app.NewModel == nil {
			fmt.Printf("%sModel switching is not available%s\n", Yellow, Reset)
			return true, nil
		}
		llm, err := app.NewModel(ctx, cfg, args[0])
		if err == nil {
			err = eng.SetModel(ctx, llm)
		}
		if err != nil {
			fmt.Printf("%s❌ Could not switch model: %v%s\n", Red, safe(err.Error()), Reset)
			return true, nil
		}
		cfg.CodePuppy.DefaultModel = args[0]
		fmt.Printf("%s Model set to: %s%s%s\n", Green, Cyan, safe(args[0]), Reset)

	case "skills":
		handleSkillsCommand(args, app.Skills)

	case "session":
		handleSessionCommand(args, app)

	case "set":
		if len(args) == 0 {
			fmt.Printf("\n%s⚙️ Current Settings:%s\n", Bold, Reset)
			fmt.Printf("  Puppy Name:   %s\n", cfg.CodePuppy.PuppyName)
			fmt.Printf("  Owner Name:   %s\n", cfg.CodePuppy.OwnerName)
			fmt.Printf("  Agency Level: %s\n", cfg.CodePuppy.AgencyLevel)
			fmt.Printf("  Default Model: %s\n", cfg.CodePuppy.DefaultModel)
			fmt.Printf("  Active Agent: %s\n\n", eng.ActiveAgent())
			return true, nil
		}
		kv := strings.SplitN(strings.Join(args, " "), "=", 2)
		if len(kv) != 2 {
			fmt.Printf("%sUsage: /set key=value%s\n", Yellow, Reset)
			return true, nil
		}
		k, v := strings.ToLower(strings.TrimSpace(kv[0])), strings.TrimSpace(kv[1])
		switch k {
		case "agency", "agency_level":
			switch strings.ToLower(v) {
			case "low", "medium", "high", "extreme":
			default:
				fmt.Printf("%sAgency must be one of: low, medium, high, extreme%s\n", Yellow, Reset)
				return true, nil
			}
			cfg.CodePuppy.AgencyLevel = strings.ToLower(v)
		case "puppy_name":
			cfg.CodePuppy.PuppyName = v
		case "owner_name":
			cfg.CodePuppy.OwnerName = v
		default:
			fmt.Printf("%sUnknown configuration setting '%s'%s\n", Yellow, safe(k), Reset)
			return true, nil
		}
		// Instructions embed these values, so rebuild the agent tree.
		if err := eng.Rebuild(ctx); err != nil {
			fmt.Printf("%s❌ Failed to apply setting: %v%s\n", Red, err, Reset)
			return true, nil
		}
		fmt.Printf("%s %s updated to: %s%s\n", Green, k, safe(v), Reset)

	case "clear":
		fmt.Print("\033[H\033[2J")

	case "sandbox":
		fmt.Printf("\n%s🛡️  Sandbox Policy:%s\n", Bold, Reset)
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
		fmt.Printf("%sUnknown command '/%s'. Type /help for a list of commands.%s\n", Yellow, safe(cmd), Reset)
	}

	return true, nil
}

func printHelp() {
	rows := [][2]string{
		{"/agents", "List all available agent personas"},
		{"/agent [name]", "Switch or show the active agent"},
		{"/model [name]", "Switch or show the active LLM model"},
		{"/skills list|search <q>", "List or search Agent Skills"},
		{"/session list|new|load <id>", "Manage saved sessions (/resume <id> also works)"},
		{"/undo [--force]", "Revert the file changes from the last turn"},
		{"/checkpoints", "List turns with file changes"},
		{"/diff [git]", "Show this session's file changes (or git diff)"},
		{"/cost", "Token usage and estimated cost"},
		{"/context", "Context size and compaction threshold"},
		{"/memory [reload|add <note>]", "Show, reload, or add project instructions"},
		{"/approvals [revoke <n>|clear]", "Remembered approval rules"},
		{"/mcp", "Configured MCP servers"},
		{"/sandbox", "File, command, and OS sandbox policy"},
		{"/set [key=value]", "View or update settings (agency, puppy_name, owner_name)"},
		{"/clear", "Clear the terminal screen"},
		{"/exit, /quit", "Exit Code Puppy"},
	}
	fmt.Printf("\n%s🐾 Code Puppy Commands:%s\n", Bold, Reset)
	for _, r := range rows {
		fmt.Printf("  %s%-30s%s %s\n", Bold, r[0], Reset, r[1])
	}
	fmt.Printf("\n  %sInput: end a line with \\ to continue it, wrap blocks in \"\"\", Tab completes commands and @paths.%s\n\n", Dim, Reset)
}

func handleSkillsCommand(args []string, prov *skills.Provider) {
	if prov == nil {
		fmt.Println("Skills integration is not enabled.")
		return
	}

	sub := "list"
	if len(args) > 0 {
		sub = strings.ToLower(args[0])
	}

	switch sub {
	case "list":
		all := prov.List()
		fmt.Printf("\n%s📦 Discovered Skills (%d):%s\n", Bold, len(all), Reset)
		for _, s := range all {
			tags := ""
			if len(s.Tags) > 0 {
				tags = fmt.Sprintf("[%s]", strings.Join(s.Tags, ", "))
			}
			fmt.Printf("  • %s%s%s %s: %s\n", Bold, safe(s.Name), Reset, Dim+safe(tags)+Reset, safe(s.Description))
		}
		fmt.Println()

	case "search":
		query := ""
		if len(args) > 1 {
			query = strings.Join(args[1:], " ")
		}
		matched := prov.Search(query)
		fmt.Printf("\n%s🔍 Search Results for '%s' (%d):%s\n", Bold, safe(query), len(matched), Reset)
		for _, s := range matched {
			fmt.Printf("  • %s%s%s: %s\n", Bold, safe(s.Name), Reset, safe(s.Description))
		}
		fmt.Println()
	}
}

func handleSessionCommand(args []string, app *App) {
	storage := app.Storage
	if storage == nil {
		fmt.Println("Session storage is disabled.")
		return
	}

	sub := "list"
	if len(args) > 0 {
		sub = strings.ToLower(args[0])
	}

	switch sub {
	case "list":
		list, err := storage.List()
		if err != nil {
			fmt.Printf("Error listing sessions: %v\n", err)
			return
		}
		fmt.Printf("\n%s📁 Saved Sessions (%d):%s\n", Bold, len(list), Reset)
		for _, s := range list {
			fmt.Printf("  • %s%s%s (%s): %s [%d messages]\n", Bold, safe(s.ID), Reset, safe(s.Agent), safe(s.Title), s.MessageCount)
		}
		fmt.Println()

	case "load", "resume":
		cmdSessionLoad(args[1:], app)

	case "new":
		rec, err := storage.CreateSession(session.NewSessionID(), "New Chat", app.Engine.ActiveAgent())
		if err != nil {
			fmt.Printf("%s❌ Failed to create session: %v%s\n", Red, err, Reset)
			return
		}
		fmt.Printf("%s Started new session: %s%s\n", Green, rec.ID, Reset)
	}
}
