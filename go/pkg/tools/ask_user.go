package tools

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// AskUserQuestionInput defines arguments for asking the user a question.
type AskUserQuestionInput struct {
	Question string   `json:"question" jsonschema:"The question or clarification needed from the user"`
	Options  []string `json:"options,omitempty" jsonschema:"Optional list of multiple choice options"`
}

// AskUserQuestionOutput holds the user's answer.
type AskUserQuestionOutput struct {
	Answer string `json:"answer"`
	Error  string `json:"error,omitempty"`
}

// UserPromptFunc is an optional callback to hook interactive prompt responses.
type UserPromptFunc func(question string, options []string) (string, error)

var (
	customPromptHandler UserPromptFunc
)

// SetUserPromptHandler sets a custom prompt handler for interactive UI/TUI.
func SetUserPromptHandler(handler UserPromptFunc) {
	customPromptHandler = handler
}

// NewAskUserQuestionTool creates an ADK tool for asking user questions.
func NewAskUserQuestionTool() (tool.Tool, error) {
	return functiontool.New(
		functiontool.Config{
			Name:        "ask_user_question",
			Description: "Ask the user a question or clarification to resolve ambiguity or solicit guidance",
		},
		func(ctx agent.Context, input AskUserQuestionInput) (AskUserQuestionOutput, error) {
			if input.Question == "" {
				return AskUserQuestionOutput{Error: "question cannot be empty"}, nil
			}

			if customPromptHandler != nil {
				answer, err := customPromptHandler(input.Question, input.Options)
				if err != nil {
					return AskUserQuestionOutput{Error: fmt.Sprintf("prompt failed: %v", err)}, nil
				}
				return AskUserQuestionOutput{Answer: answer}, nil
			}

			// Fallback standard stdin prompt
			fmt.Printf("\n❓ [Puppy Question]: %s\n", input.Question)
			if len(input.Options) > 0 {
				for i, opt := range input.Options {
					fmt.Printf("   [%d] %s\n", i+1, opt)
				}
			}
			fmt.Print("👉 Answer: ")

			reader := bufio.NewReader(os.Stdin)
			line, err := reader.ReadString('\n')
			if err != nil {
				return AskUserQuestionOutput{Error: fmt.Sprintf("failed to read user input: %v", err)}, nil
			}

			return AskUserQuestionOutput{Answer: strings.TrimSpace(line)}, nil
		},
	)
}
