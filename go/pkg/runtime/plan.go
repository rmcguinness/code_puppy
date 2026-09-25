package runtime

import (
	"fmt"
	"strings"
)

// planReadOnlyTools may run in plan mode: they read, search or ask, and
// change nothing. Anything else, including MCP tools (whose effects are
// unknown), is refused. Sub-agents run inside the same plan-mode run, so
// invoke_agent can research but its agents can't write either.
var planReadOnlyTools = map[string]bool{
	"read_file":             true,
	"list_files":            true,
	"grep":                  true,
	"view_image":            true,
	"web_fetch":             true,
	"web_search":            true,
	"list_agents":           true,
	"invoke_agent":          true,
	"list_or_search_skills": true,
	"activate_skill":        true,
	"ask_user_question":     true,
}

// PlanAllows reports whether a tool may run in plan mode.
func PlanAllows(toolName string) bool { return planReadOnlyTools[toolName] }

// WithPlanOnly runs the prompt in plan mode: the agent may read and search
// but every tool that could change something is refused, so the answer is
// a plan grounded in the code rather than an edit.
func WithPlanOnly() ExecOption { return func(s *runState) { s.planOnly = true } }

// planRefusal returns the tool result for a tool refused in plan mode, or
// nil when the tool may run.
func planRefusal(st *runState, toolName string) map[string]any {
	if st == nil || !st.planOnly || planReadOnlyTools[toolName] {
		return nil
	}
	return map[string]any{"error": fmt.Sprintf("plan mode: %s is disabled because it could change something. Describe this step in the plan instead.", toolName)}
}

// PlanPrompt wraps a goal in the instructions for plan mode.
func PlanPrompt(goal string) string {
	return "You are in plan-only mode. You may read files, search and look things up, but tools that change anything (editing files, running commands) are disabled. Investigate as much as you need, then answer with:\n" +
		"1. A short summary of the objective\n" +
		"2. A numbered implementation plan, naming the files and functions involved\n" +
		"3. Risks and unknowns\n" +
		"4. How to verify the change\n" +
		"5. Questions for the user, only if something blocks the plan\n\n" +
		"Goal:\n" + strings.TrimSpace(goal)
}
