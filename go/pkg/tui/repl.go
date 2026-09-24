package tui

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/retail-cortex/code_puppy/pkg/agents"
	"github.com/retail-cortex/code_puppy/pkg/config"
	"github.com/retail-cortex/code_puppy/pkg/runtime"
	"github.com/retail-cortex/code_puppy/pkg/session"
	"github.com/retail-cortex/code_puppy/pkg/skills"
	sessionsdk "google.golang.org/adk/v2/session"
)

// RunREPL runs the interactive REPL prompt loop.
func RunREPL(
	ctx context.Context,
	version string,
	cfg *config.Config,
	eng *runtime.Engine,
	agentReg *agents.Registry,
	skillProv *skills.Provider,
	storage *session.Storage,
) error {
	PrintBanner(version, eng.ActiveAgent(), cfg.CodePuppy.DefaultModel)

	activeSession := storage.Active()
	if activeSession == nil {
		activeSession = storage.CreateSession("", "Interactive Session", eng.ActiveAgent())
	}

	scanner := bufio.NewScanner(os.Stdin)

	for {
		promptPrefix := fmt.Sprintf("%s🐶 [%s]> %s", Bold+Green, eng.ActiveAgent(), Reset)
		fmt.Print(promptPrefix)

		if !scanner.Scan() {
			break
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		// Handle slash command if applicable
		handled, err := HandleCommand(ctx, line, cfg, eng, agentReg, skillProv, storage)
		if err != nil && err.Error() == "exit" {
			return nil
		}
		if handled {
			continue
		}

		// Record user input
		storage.AddMessage("user", line)

		// Stream execution
		fmt.Println()
		var modelOutput strings.Builder
		streamErr := eng.Execute(ctx, activeSession.ID, line, func(ev *sessionsdk.Event) error {
			if ev.Content != nil {
				for _, part := range ev.Content.Parts {
					if part.Text != "" {
						fmt.Print(part.Text)
						modelOutput.WriteString(part.Text)
					}
					if part.FunctionCall != nil {
						PrintToolCall(part.FunctionCall.Name, part.FunctionCall.Args)
					}
					if part.FunctionResponse != nil {
						summary := ""
						if part.FunctionResponse.Response != nil {
							if errStr, ok := part.FunctionResponse.Response["error"].(string); ok && errStr != "" {
								PrintToolResult(part.FunctionResponse.Name, false, errStr)
								continue
							}
							if resStr, ok := part.FunctionResponse.Response["result"].(string); ok && resStr != "" {
								summary = resStr
							} else if cnt, ok := part.FunctionResponse.Response["content"].(string); ok && cnt != "" {
								summary = fmt.Sprintf("%d bytes read", len(cnt))
							}
						}
						PrintToolResult(part.FunctionResponse.Name, true, summary)
					}
				}
			}
			return nil
		})

		if streamErr != nil {
			fmt.Printf("\n%s❌ Error: %v%s\n", Red, streamErr, Reset)
		} else {
			fmt.Println()
		}

		if modelOutput.Len() > 0 {
			storage.AddMessage("model", modelOutput.String())
		}
		fmt.Println()
	}

	return scanner.Err()
}
