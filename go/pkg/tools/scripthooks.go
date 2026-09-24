package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/retail-cortex/code_puppy/pkg/audit"
	"github.com/retail-cortex/code_puppy/pkg/config"
)

const defaultHookTimeout = 30 * time.Second

// HookEvent is the JSON document a hook receives on stdin.
type HookEvent struct {
	Event     string         `json:"event"` // pre_tool, post_tool, prompt_submit
	SessionID string         `json:"session_id,omitempty"`
	Workspace string         `json:"workspace"`
	Tool      string         `json:"tool,omitempty"`
	Args      map[string]any `json:"args,omitempty"`
	Result    map[string]any `json:"result,omitempty"`
	Error     string         `json:"error,omitempty"`
	Prompt    string         `json:"prompt,omitempty"`
}

// hookReply is the optional JSON a hook may print on stdout.
type hookReply struct {
	Decision string `json:"decision"` // "block" to block
	Reason   string `json:"reason"`
}

// ScriptHooks runs user-configured commands at lifecycle points. Hooks run
// through the same ExecEnv as shell commands (sandboxed, guarded, scrubbed
// environment) in the workspace directory.
type ScriptHooks struct {
	pre, post, prompt []config.HookConfig
	exec              *ExecEnv
	workspace         string
	audit             *audit.Logger
	// Warn reports hook failures that don't block (defaults to no-op).
	Warn func(string)
}

// NewScriptHooks validates and prepares hooks.
func NewScriptHooks(cfg config.HooksConfig, env *ExecEnv, workspace string, log *audit.Logger) (*ScriptHooks, error) {
	for _, list := range [][]config.HookConfig{cfg.PreTool, cfg.PostTool, cfg.PromptSubmit} {
		for _, h := range list {
			if strings.TrimSpace(h.Command) == "" {
				return nil, fmt.Errorf("hook with empty command (match %q)", h.Match)
			}
			if _, err := path.Match(h.Match, ""); err != nil {
				return nil, fmt.Errorf("invalid hook match %q: %w", h.Match, err)
			}
		}
	}
	return &ScriptHooks{pre: cfg.PreTool, post: cfg.PostTool, prompt: cfg.PromptSubmit, exec: env, workspace: workspace, audit: log, Warn: func(string) {}}, nil
}

// Empty reports whether no hooks are configured.
func (s *ScriptHooks) Empty() bool {
	return s == nil || len(s.pre)+len(s.post)+len(s.prompt) == 0
}

// PreTool runs pre_tool hooks; a non-empty reason means the call is blocked.
func (s *ScriptHooks) PreTool(ctx context.Context, session, tool string, args map[string]any) string {
	if s == nil {
		return ""
	}
	return s.runAll(ctx, s.pre, tool, HookEvent{Event: "pre_tool", SessionID: session, Tool: tool, Args: args})
}

// PostTool runs post_tool hooks (they observe; they can't change the result).
func (s *ScriptHooks) PostTool(ctx context.Context, session, tool string, args, result map[string]any, toolErr error) {
	if s == nil {
		return
	}
	ev := HookEvent{Event: "post_tool", SessionID: session, Tool: tool, Args: args, Result: result}
	if toolErr != nil {
		ev.Error = toolErr.Error()
	}
	s.runAll(ctx, s.post, tool, ev)
}

// PromptSubmit runs prompt_submit hooks; a non-empty reason blocks the prompt.
func (s *ScriptHooks) PromptSubmit(ctx context.Context, session, prompt string) string {
	if s == nil {
		return ""
	}
	return s.runAll(ctx, s.prompt, "", HookEvent{Event: "prompt_submit", SessionID: session, Prompt: prompt})
}

func (s *ScriptHooks) runAll(ctx context.Context, hooks []config.HookConfig, tool string, ev HookEvent) string {
	ev.Workspace = s.workspace
	for _, h := range hooks {
		if tool != "" && h.Match != "" {
			if ok, _ := path.Match(h.Match, tool); !ok {
				continue
			}
		}
		blocked, reason, err := s.run(ctx, h, ev)
		entry := audit.Entry{Kind: audit.KindHook, Tool: ev.Tool, Detail: ev.Event + ": " + h.Command}
		switch {
		case blocked:
			entry.Decision = "block"
			entry.Error = reason
			s.audit.Log(entry)
			return reason
		case err != nil:
			entry.Error = err.Error()
			s.audit.Log(entry)
			if h.FailClosed {
				return fmt.Sprintf("hook %q failed and is fail_closed: %v", h.Command, err)
			}
			s.Warn(fmt.Sprintf("hook %q failed: %v", h.Command, err))
		}
	}
	return ""
}

// run executes one hook. Exit 0 continues unless stdout is a JSON block
// decision; exit 2 blocks with stderr as the reason; other failures are errors.
func (s *ScriptHooks) run(ctx context.Context, h config.HookConfig, ev HookEvent) (blocked bool, reason string, err error) {
	timeout := defaultHookTimeout
	if h.TimeoutSeconds > 0 {
		timeout = time.Duration(h.TimeoutSeconds) * time.Second
	}
	hctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	payload, err := json.Marshal(ev)
	if err != nil {
		return false, "", err
	}
	cmd, err := s.exec.command(hctx, []string{"bash", "-c", h.Command})
	if err != nil {
		return false, "", err
	}
	cmd.Dir = s.workspace
	cmd.Stdin = bytes.NewReader(payload)
	stdout, stderr := newCappedBuffer(64*1024), newCappedBuffer(16*1024)
	cmd.Stdout, cmd.Stderr = stdout, stderr

	runErr := cmd.Run()
	if hctx.Err() == context.DeadlineExceeded {
		return false, "", fmt.Errorf("timed out after %s", timeout)
	}
	if runErr != nil {
		if cmd.ProcessState != nil && cmd.ProcessState.ExitCode() == 2 {
			msg := strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = "blocked by hook"
			}
			return true, msg, nil
		}
		return false, "", fmt.Errorf("%v: %s", runErr, strings.TrimSpace(stderr.String()))
	}
	var reply hookReply
	if out := strings.TrimSpace(stdout.String()); strings.HasPrefix(out, "{") && json.Unmarshal([]byte(out), &reply) == nil &&
		strings.EqualFold(reply.Decision, "block") {
		if reply.Reason == "" {
			reply.Reason = "blocked by hook"
		}
		return true, reply.Reason, nil
	}
	return false, "", nil
}
