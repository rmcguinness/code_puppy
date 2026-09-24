package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/retail-cortex/code_puppy/pkg/agents"
	"github.com/retail-cortex/code_puppy/pkg/config"
	"github.com/retail-cortex/code_puppy/pkg/runtime"
	"github.com/retail-cortex/code_puppy/pkg/session"
	"github.com/retail-cortex/code_puppy/pkg/skills"
	"github.com/retail-cortex/code_puppy/pkg/tools"
	"github.com/retail-cortex/code_puppy/pkg/tui"
	"github.com/spf13/cobra"
	sessionsdk "google.golang.org/adk/v2/session"
)

var (
	version = "2.0.0-go"

	promptFlag      string
	interactiveFlag bool
	agentFlag       string
	modelFlag       string
	agencyFlag      string
	configFlag      string
	versionFlag     bool
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "code-puppy [flags] [prompt...]",
		Short: "🐶 Code Puppy - High-performance autonomous AI code agent built on Google ADK",
		RunE:  runApp,
	}

	rootCmd.Flags().StringVarP(&promptFlag, "prompt", "p", "", "One-shot prompt for non-interactive execution")
	rootCmd.Flags().BoolVarP(&interactiveFlag, "interactive", "i", false, "Start interactive REPL mode")
	rootCmd.Flags().StringVarP(&agentFlag, "agent", "a", "", "Agent persona to activate (code-puppy, helios, qa-kitten, etc.)")
	rootCmd.Flags().StringVarP(&modelFlag, "model", "m", "", "Model identifier to use")
	rootCmd.Flags().StringVar(&agencyFlag, "agency", "", "Agency level (low, medium, high, extreme)")
	rootCmd.Flags().StringVarP(&configFlag, "config", "c", "", "Directory containing .env.toml config")
	rootCmd.Flags().BoolVarP(&versionFlag, "version", "v", false, "Print Code Puppy version")

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func runApp(cmd *cobra.Command, args []string) error {
	if versionFlag {
		fmt.Printf("Code Puppy Go (Google ADK) version %s\n", version)
		return nil
	}

	ctx := context.Background()

	// 1. Load configuration via retail-cortex/modenv
	cfg, err := config.Load(configFlag)
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Apply CLI flag overrides
	if modelFlag != "" {
		cfg.CodePuppy.DefaultModel = modelFlag
	}
	if agentFlag != "" {
		cfg.CodePuppy.DefaultAgent = agentFlag
	}
	if agencyFlag != "" {
		cfg.CodePuppy.AgencyLevel = strings.ToLower(agencyFlag)
	}

	// 2. Load agent registry and markdown specifications
	agentReg, err := agents.NewRegistry()
	if err != nil {
		return fmt.Errorf("failed to load agent registry: %w", err)
	}
	// Scan workspace for custom local agents
	_ = agentReg.LoadExternalAgents("./agents", "~/.code_puppy/agents")

	// 3. Initialize Agent Skills
	skillProv, err := skills.NewProvider()
	if err != nil {
		return fmt.Errorf("failed to load skills provider: %w", err)
	}
	if cfg.Skills.Enabled {
		_ = skillProv.DiscoverExternal(cfg.Skills.Paths)
	}

	// 4. Initialize Tools suite
	toolReg, err := tools.NewRegistry(cfg, agentReg, skillProv)
	if err != nil {
		return fmt.Errorf("failed to initialize tools registry: %w", err)
	}

	// 5. Initialize Model
	llmModel, err := runtime.NewModel(ctx, cfg, modelFlag)
	if err != nil {
		// If credentials are missing in non-interactive mode, inform user clearly
		if os.Getenv("GEMINI_API_KEY") == "" && cfg.LLM.Gemini.APIKey == "" {
			fmt.Printf("%s⚠️  Notice: GEMINI_API_KEY is not configured.%s\n", tui.Yellow, tui.Reset)
			fmt.Println("Set it in .env.toml or export GEMINI_API_KEY='your-key'")
		}
	}

	// If model could not be initialized, use MockLLM to still allow inspection of commands and skills
	if llmModel == nil {
		llmModel = runtime.NewMockLLM("unconfigured-model")
	}

	// 6. Initialize ADK Engine
	eng, err := runtime.NewEngine(ctx, cfg, agentReg, skillProv, toolReg, llmModel)
	if err != nil {
		return fmt.Errorf("failed to initialize engine: %w", err)
	}

	// 7. Initialize Session Storage
	storage, err := session.NewStorage(cfg.Session.StorageDir)
	if err != nil {
		return fmt.Errorf("failed to initialize session storage: %w", err)
	}

	// Determine if running prompt or REPL
	promptText := promptFlag
	if promptText == "" && len(args) > 0 {
		promptText = strings.Join(args, " ")
	}

	if promptText != "" && !interactiveFlag {
		// Non-interactive one-shot run
		sess := storage.CreateSession("", "CLI Prompt", eng.ActiveAgent())
		storage.AddMessage("user", promptText)

		err = eng.Execute(ctx, sess.ID, promptText, func(ev *sessionsdk.Event) error {
			if ev.Content != nil {
				for _, part := range ev.Content.Parts {
					if part.Text != "" {
						fmt.Print(part.Text)
					}
					if part.FunctionCall != nil {
						tui.PrintToolCall(part.FunctionCall.Name, part.FunctionCall.Args)
					}
					if part.FunctionResponse != nil {
						summary := ""
						if part.FunctionResponse.Response != nil {
							if errStr, ok := part.FunctionResponse.Response["error"].(string); ok && errStr != "" {
								tui.PrintToolResult(part.FunctionResponse.Name, false, errStr)
								continue
							}
							if resStr, ok := part.FunctionResponse.Response["result"].(string); ok && resStr != "" {
								summary = resStr
							}
						}
						tui.PrintToolResult(part.FunctionResponse.Name, true, summary)
					}
				}
			}
			return nil
		})
		fmt.Println()
		return err
	}

	// Launch interactive REPL mode
	return tui.RunREPL(ctx, version, cfg, eng, agentReg, skillProv, storage)
}
