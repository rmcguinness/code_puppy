package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/retail-cortex/code_puppy/pkg/agents"
	"github.com/retail-cortex/code_puppy/pkg/config"
	"github.com/retail-cortex/code_puppy/pkg/runtime"
	"github.com/retail-cortex/code_puppy/pkg/session"
	"github.com/retail-cortex/code_puppy/pkg/skills"
)

// HandleCommand processes slash commands. Returns true if input was a slash command.
func HandleCommand(
	ctx context.Context,
	input string,
	cfg *config.Config,
	eng *runtime.Engine,
	agentReg *agents.Registry,
	skillProv *skills.Provider,
	storage *session.Storage,
) (bool, error) {
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

	switch cmd {
	case "help":
		printHelp()

	case "agents":
		fmt.Printf("\n%s🤖 Available Agents:%s\n", Bold, Reset)
		for _, a := range agentReg.List() {
			marker := "  "
			if a.Name == eng.ActiveAgent() {
				marker = "👉"
			}
			fmt.Printf("%s %s%s%s (%s): %s\n", marker, Bold, a.DisplayName, Reset, a.Name, a.Description)
		}
		fmt.Println()

	case "agent":
		if len(args) == 0 {
			spec, _ := agentReg.Get(eng.ActiveAgent())
			fmt.Printf("Current agent: %s%s%s (%s)\n", Bold, spec.DisplayName, Reset, spec.Name)
			return true, nil
		}
		target := args[0]
		if err := eng.SetActiveAgent(ctx, target); err != nil {
			fmt.Printf("%s❌ Error switching agent: %v%s\n", Red, err, Reset)
		} else {
			spec, _ := agentReg.Get(target)
			fmt.Printf("%s Switched active agent to: %s%s%s\n", Green, Bold, spec.DisplayName, Reset)
		}

	case "model":
		if len(args) == 0 {
			fmt.Printf("Current model: %s%s%s (Provider: %s)\n", Cyan, cfg.CodePuppy.DefaultModel, Reset, cfg.LLM.Provider)
			return true, nil
		}
		cfg.CodePuppy.DefaultModel = args[0]
		fmt.Printf("%s Model set to: %s%s%s\n", Green, Cyan, args[0], Reset)

	case "skills":
		handleSkillsCommand(args, skillProv)

	case "session":
		handleSessionCommand(args, storage)

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
		kv := strings.SplitN(args[0], "=", 2)
		if len(kv) == 2 {
			k := strings.ToLower(kv[0])
			v := kv[1]
			switch k {
			case "agency", "agency_level":
				cfg.CodePuppy.AgencyLevel = v
				_ = eng.SetActiveAgent(ctx, eng.ActiveAgent())
				fmt.Printf("%s Agency level updated to: %s%s\n", Green, v, Reset)
			case "puppy_name":
				cfg.CodePuppy.PuppyName = v
				fmt.Printf("%s Puppy name updated to: %s%s\n", Green, v, Reset)
			case "owner_name":
				cfg.CodePuppy.OwnerName = v
				fmt.Printf("%s Owner name updated to: %s%s\n", Green, v, Reset)
			default:
				fmt.Printf("%sUnknown configuration setting '%s'%s\n", Yellow, k, Reset)
			}
		}

	case "clear":
		fmt.Print("\033[H\033[2J")

	case "exit", "quit":
		fmt.Printf("\n🐾 %sPuppy is going to take a nap. Goodbye!%s\n", Cyan, Reset)
		return true, fmt.Errorf("exit")

	default:
		fmt.Printf("%sUnknown command '/%s'. Type /help for a list of commands.%s\n", Yellow, cmd, Reset)
	}

	return true, nil
}

func printHelp() {
	fmt.Printf("\n%s🐾 Code Puppy Commands:%s\n", Bold, Reset)
	fmt.Printf("  %s/agents%s               List all available agent personas\n", Bold, Reset)
	fmt.Printf("  %s/agent [name]%s          Switch or show the active agent\n", Bold, Reset)
	fmt.Printf("  %s/model [name]%s          Switch or show the active LLM model\n", Bold, Reset)
	fmt.Printf("  %s/skills list%s           List all available Agent Skills\n", Bold, Reset)
	fmt.Printf("  %s/skills search [query]%s Search skills by keyword\n", Bold, Reset)
	fmt.Printf("  %s/session list%s          List saved sessions\n", Bold, Reset)
	fmt.Printf("  %s/session new%s           Start a clean session\n", Bold, Reset)
	fmt.Printf("  %s/set [key=value]%s       View or update settings (agency, puppy_name, etc.)\n", Bold, Reset)
	fmt.Printf("  %s/clear%s                 Clear the terminal screen\n", Bold, Reset)
	fmt.Printf("  %s/exit%s or %s/quit%s         Exit Code Puppy\n\n", Bold, Reset, Bold, Reset)
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
			fmt.Printf("  • %s%s%s %s: %s\n", Bold, s.Name, Reset, Dim+tags+Reset, s.Description)
		}
		fmt.Println()

	case "search":
		query := ""
		if len(args) > 1 {
			query = strings.Join(args[1:], " ")
		}
		matched := prov.Search(query)
		fmt.Printf("\n%s🔍 Search Results for '%s' (%d):%s\n", Bold, query, len(matched), Reset)
		for _, s := range matched {
			fmt.Printf("  • %s%s%s: %s\n", Bold, s.Name, Reset, s.Description)
		}
		fmt.Println()
	}
}

func handleSessionCommand(args []string, storage *session.Storage) {
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
			fmt.Printf("  • %s%s%s (%s): %s [%d messages]\n", Bold, s.ID, Reset, s.Agent, s.Title, len(s.Messages))
		}
		fmt.Println()

	case "new":
		rec := storage.CreateSession("", "New Chat", "code-puppy")
		fmt.Printf("%s Started new session: %s%s\n", Green, rec.ID, Reset)
	}
}
