package tools

import (
	"fmt"

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

// NewAskUserQuestionTool creates an ADK tool for asking user questions. The
// prompt is delegated to hooks so it shares the host's stdin reader instead of
// competing with it for buffered input.
func NewAskUserQuestionTool(hooks *Hooks) (tool.Tool, error) {
	return functiontool.New(
		functiontool.Config{
			Name:        "ask_user_question",
			Description: "Ask the user a question or clarification to resolve ambiguity or solicit guidance",
		},
		func(ctx agent.Context, input AskUserQuestionInput) (AskUserQuestionOutput, error) {
			if input.Question == "" {
				return AskUserQuestionOutput{Error: "question cannot be empty"}, nil
			}
			prompter := hooks.userPrompter()
			if prompter == nil {
				return AskUserQuestionOutput{Error: "interactive input is not available; proceed with your best judgement"}, nil
			}
			answer, err := prompter(ctx, input.Question, input.Options)
			if err != nil {
				return AskUserQuestionOutput{Error: fmt.Sprintf("prompt failed: %v", err)}, nil
			}
			return AskUserQuestionOutput{Answer: answer}, nil
		},
	)
}
