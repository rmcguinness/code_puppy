package tools

import (
	"fmt"
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
func NewReplaceInFileTool(ws *Workspace, hooks *Hooks) (tool.Tool, error) {
	return functiontool.New(
		functiontool.Config{
			Name:        "replace_in_file",
			Description: "Replace exact text snippet in an existing file with replacement text",
		},
		func(ctx agent.Context, input ReplaceInFileInput) (ReplaceInFileOutput, error) {
			fail := func(msg string) (ReplaceInFileOutput, error) {
				return ReplaceInFileOutput{Path: input.Path, Error: msg}, nil
			}
			if input.TargetContent == "" {
				return fail("target_content must not be empty")
			}
			rel, err := ws.WritablePath(input.Path)
			if err != nil {
				return fail(err.Error())
			}
			data, err := ws.ReadFile(rel)
			if err != nil {
				return fail(fmt.Sprintf("failed to read file: %v", err))
			}

			content := string(data)
			occurrences := strings.Count(content, input.TargetContent)
			if occurrences == 0 {
				return fail("target_content not found in file")
			}
			if occurrences > 1 && !input.AllowMultiple {
				return fail(fmt.Sprintf("target_content matched %d times; set allow_multiple=true or provide more surrounding lines for uniqueness", occurrences))
			}

			count := 1
			if input.AllowMultiple {
				count = occurrences
			}
			newContent := strings.Replace(content, input.TargetContent, input.ReplacementContent, count)
			if err := hooks.Approve(ctx, writeApproval(ws, "replace_in_file",
				fmt.Sprintf("Edit %s (%d replacement(s))", rel, count), unifiedDiff(rel, content, newContent))); err != nil {
				return fail(err.Error())
			}

			if err := ws.WriteFileAtomic(rel, []byte(newContent)); err != nil {
				return fail(fmt.Sprintf("failed to write updated file: %v", err))
			}

			return ReplaceInFileOutput{Path: input.Path, Success: true, ReplacementsCount: count}, nil
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
func NewDeleteSnippetTool(ws *Workspace, hooks *Hooks) (tool.Tool, error) {
	return functiontool.New(
		functiontool.Config{
			Name:        "delete_snippet",
			Description: "Delete an exact code snippet from a file",
		},
		func(ctx agent.Context, input DeleteSnippetInput) (DeleteSnippetOutput, error) {
			fail := func(msg string) (DeleteSnippetOutput, error) {
				return DeleteSnippetOutput{Path: input.Path, Error: msg}, nil
			}
			if input.Snippet == "" {
				return fail("snippet must not be empty")
			}
			rel, err := ws.WritablePath(input.Path)
			if err != nil {
				return fail(err.Error())
			}
			data, err := ws.ReadFile(rel)
			if err != nil {
				return fail(fmt.Sprintf("failed to read file: %v", err))
			}

			content := string(data)
			if !strings.Contains(content, input.Snippet) {
				return fail("snippet not found in file")
			}
			newContent := strings.Replace(content, input.Snippet, "", 1)
			if err := hooks.Approve(ctx, writeApproval(ws, "delete_snippet",
				fmt.Sprintf("Remove a %d byte snippet from %s", len(input.Snippet), rel), unifiedDiff(rel, content, newContent))); err != nil {
				return fail(err.Error())
			}
			if err := ws.WriteFileAtomic(rel, []byte(newContent)); err != nil {
				return fail(fmt.Sprintf("failed to save file: %v", err))
			}
			return DeleteSnippetOutput{Path: input.Path, Success: true}, nil
		},
	)
}
