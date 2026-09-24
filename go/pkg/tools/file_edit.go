package tools

import (
	"fmt"
	"os"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// ReplaceInFileInput defines arguments for targeted search-and-replace.
type ReplaceInFileInput struct {
	Path               string `json:"path" jsonschema:"The file path to edit"`
	TargetContent      string `json:"target_content" jsonschema:"The exact text snippet to replace"`
	ReplacementContent string `json:"replacement_content" jsonschema:"The new text content to substitute"`
	AllowMultiple      bool   `json:"allow_multiple,omitempty" jsonschema:"Whether to replace multiple occurrences if found"`
}

// ReplaceInFileOutput holds result of replace_in_file.
type ReplaceInFileOutput struct {
	Path              string `json:"path"`
	Success           bool   `json:"success"`
	ReplacementsCount int    `json:"replacements_count"`
	Error             string `json:"error,omitempty"`
}

// NewReplaceInFileTool creates an ADK tool for targeted search-and-replace.
func NewReplaceInFileTool(workspaceDir string) (tool.Tool, error) {
	return functiontool.New(
		functiontool.Config{
			Name:        "replace_in_file",
			Description: "Replace exact text snippet in an existing file with replacement text",
		},
		func(ctx agent.Context, input ReplaceInFileInput) (ReplaceInFileOutput, error) {
			targetPath := resolveSafePath(workspaceDir, input.Path)
			data, err := os.ReadFile(targetPath)
			if err != nil {
				return ReplaceInFileOutput{Path: input.Path, Success: false, Error: fmt.Sprintf("failed to read file: %v", err)}, nil
			}

			content := string(data)
			occurrences := strings.Count(content, input.TargetContent)
			if occurrences == 0 {
				return ReplaceInFileOutput{
					Path:    input.Path,
					Success: false,
					Error:   "target_content not found in file",
				}, nil
			}

			if occurrences > 1 && !input.AllowMultiple {
				return ReplaceInFileOutput{
					Path:    input.Path,
					Success: false,
					Error:   fmt.Sprintf("target_content matched %d times; set allow_multiple=true or provide more surrounding lines for uniqueness", occurrences),
				}, nil
			}

			var newContent string
			var count int
			if input.AllowMultiple {
				newContent = strings.ReplaceAll(content, input.TargetContent, input.ReplacementContent)
				count = occurrences
			} else {
				newContent = strings.Replace(content, input.TargetContent, input.ReplacementContent, 1)
				count = 1
			}

			if err := os.WriteFile(targetPath, []byte(newContent), 0644); err != nil {
				return ReplaceInFileOutput{Path: input.Path, Success: false, Error: fmt.Sprintf("failed to write updated file: %v", err)}, nil
			}

			return ReplaceInFileOutput{
				Path:              input.Path,
				Success:           true,
				ReplacementsCount: count,
			}, nil
		},
	)
}

// DeleteSnippetInput defines arguments for deleting a code snippet.
type DeleteSnippetInput struct {
	Path    string `json:"path" jsonschema:"The file path"`
	Snippet string `json:"snippet" jsonschema:"The exact snippet to remove"`
}

// DeleteSnippetOutput holds result of delete_snippet.
type DeleteSnippetOutput struct {
	Path    string `json:"path"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// NewDeleteSnippetTool creates an ADK tool for removing code snippets.
func NewDeleteSnippetTool(workspaceDir string) (tool.Tool, error) {
	return functiontool.New(
		functiontool.Config{
			Name:        "delete_snippet",
			Description: "Delete an exact code snippet from a file",
		},
		func(ctx agent.Context, input DeleteSnippetInput) (DeleteSnippetOutput, error) {
			targetPath := resolveSafePath(workspaceDir, input.Path)
			data, err := os.ReadFile(targetPath)
			if err != nil {
				return DeleteSnippetOutput{Path: input.Path, Success: false, Error: fmt.Sprintf("failed to read file: %v", err)}, nil
			}

			content := string(data)
			if !strings.Contains(content, input.Snippet) {
				return DeleteSnippetOutput{Path: input.Path, Success: false, Error: "snippet not found in file"}, nil
			}

			newContent := strings.Replace(content, input.Snippet, "", 1)
			if err := os.WriteFile(targetPath, []byte(newContent), 0644); err != nil {
				return DeleteSnippetOutput{Path: input.Path, Success: false, Error: fmt.Sprintf("failed to save file: %v", err)}, nil
			}

			return DeleteSnippetOutput{Path: input.Path, Success: true}, nil
		},
	)
}
