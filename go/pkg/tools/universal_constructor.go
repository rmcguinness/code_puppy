package tools

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// UCToolMetadata holds metadata about a dynamically forged tool.
type UCToolMetadata struct {
	Name        string `json:"name"`
	Language    string `json:"language"`
	Description string `json:"description"`
	Path        string `json:"path"`
}

var (
	ucMu    sync.RWMutex
	ucTools = make(map[string]*UCToolMetadata)
)

// UniversalConstructorInput defines arguments for the universal constructor.
type UniversalConstructorInput struct {
	Action      string `json:"action" jsonschema:"Action to perform: create, run, list, or info"`
	ToolName    string `json:"tool_name,omitempty" jsonschema:"Name of tool to create, run, or inspect"`
	Language    string `json:"language,omitempty" jsonschema:"Programming language (go, python, bash)"`
	Code        string `json:"code,omitempty" jsonschema:"Source code of tool to create"`
	Description string `json:"description,omitempty" jsonschema:"Description of what tool does"`
	Args        string `json:"args,omitempty" jsonschema:"Arguments to pass when running tool"`
}

// UniversalConstructorOutput holds result of universal constructor.
type UniversalConstructorOutput struct {
	Success bool     `json:"success"`
	Result  string   `json:"result"`
	Tools   []string `json:"tools,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// NewUniversalConstructorTool creates the Helios Universal Constructor tool.
func NewUniversalConstructorTool(toolsDir string) (tool.Tool, error) {
	if toolsDir == "" {
		home, _ := os.UserHomeDir()
		toolsDir = filepath.Join(home, ".code_puppy", "uc_tools")
	}
	_ = os.MkdirAll(toolsDir, 0755)

	return functiontool.New(
		functiontool.Config{
			Name:        "universal_constructor",
			Description: "Helios power: forge, run, list, and manage dynamic executable tools and scripts",
		},
		func(ctx agent.Context, input UniversalConstructorInput) (UniversalConstructorOutput, error) {
			switch input.Action {
			case "list":
				ucMu.RLock()
				defer ucMu.RUnlock()
				names := make([]string, 0, len(ucTools))
				for name, meta := range ucTools {
					names = append(names, fmt.Sprintf("%s (%s): %s", name, meta.Language, meta.Description))
				}
				return UniversalConstructorOutput{
					Success: true,
					Result:  fmt.Sprintf("%d custom tools available", len(names)),
					Tools:   names,
				}, nil

			case "create":
				if input.ToolName == "" || input.Code == "" {
					return UniversalConstructorOutput{Error: "tool_name and code are required to create a tool"}, nil
				}
				lang := input.Language
				if lang == "" {
					lang = "bash"
				}

				ext := ".sh"
				if lang == "python" || lang == "py" {
					ext = ".py"
				} else if lang == "go" {
					ext = ".go"
				}

				filePath := filepath.Join(toolsDir, input.ToolName+ext)
				if err := os.WriteFile(filePath, []byte(input.Code), 0755); err != nil {
					return UniversalConstructorOutput{Error: fmt.Sprintf("failed to save tool: %v", err)}, nil
				}

				ucMu.Lock()
				ucTools[input.ToolName] = &UCToolMetadata{
					Name:        input.ToolName,
					Language:    lang,
					Description: input.Description,
					Path:        filePath,
				}
				ucMu.Unlock()

				return UniversalConstructorOutput{
					Success: true,
					Result:  fmt.Sprintf("Tool '%s' successfully forged and saved to %s", input.ToolName, filePath),
				}, nil

			case "run":
				ucMu.RLock()
				meta, exists := ucTools[input.ToolName]
				ucMu.RUnlock()

				if !exists {
					return UniversalConstructorOutput{Error: fmt.Sprintf("tool '%s' not found", input.ToolName)}, nil
				}

				runCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
				defer cancel()

				var cmd *exec.Cmd
				switch meta.Language {
				case "python", "py":
					cmd = exec.CommandContext(runCtx, "python3", meta.Path, input.Args)
				case "go":
					cmd = exec.CommandContext(runCtx, "go", "run", meta.Path, input.Args)
				default:
					cmd = exec.CommandContext(runCtx, "bash", meta.Path, input.Args)
				}

				var stdout, stderr bytes.Buffer
				cmd.Stdout = &stdout
				cmd.Stderr = &stderr

				err := cmd.Run()
				combined := stdout.String() + stderr.String()
				if err != nil {
					return UniversalConstructorOutput{
						Success: false,
						Result:  combined,
						Error:   fmt.Sprintf("tool execution failed: %v", err),
					}, nil
				}

				return UniversalConstructorOutput{
					Success: true,
					Result:  combined,
				}, nil

			default:
				return UniversalConstructorOutput{Error: "unknown action; use 'create', 'run', or 'list'"}, nil
			}
		},
	)
}
