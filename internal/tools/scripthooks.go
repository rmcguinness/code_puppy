package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/retail-cortex/code_puppy/internal/audit"
	"github.com/retail-cortex/code_puppy/internal/config"
	"github.com/retail-cortex/code_puppy/internal/observability"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const defaultHookTimeout = 30 * time.Second

// Variables so tests can shrink them.
var (
	// postQueueSize bounds post_tool events waiting for the hook worker.
	postQueueSize = 256
	// postDrainTimeout is how long Close lets queued post_tool hooks finish
	// before killing them.
	postDrainTimeout = 5 * time.Second
)

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

	// post_tool hooks only observe, so they run on one background worker
	// (in order) instead of delaying the turn. See PostTool and Close.
	postQ postQueue
}

// postJob is one post_tool event, encoded when queued so later changes to
// the tool's result maps can't race with or alter what the hook sees.
type postJob struct {
	ctx     context.Context // detached from the turn; keeps its trace
	tool    string
	payload []byte
	barrier chan struct{} // flush marker: closed when reached
}

type postQueue struct {
	start   sync.Once
	mu      sync.RWMutex // write lock only to close jobs
	closed  bool
	jobs    chan postJob
	done    chan struct{}
	ctx     context.Context // cancelled to kill hooks still running at Close
	cancel  context.CancelFunc
	dropped atomic.Int64
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

// PostTool queues post_tool hooks for the background worker and returns
// immediately. The hooks observe; they can't change the result. Events are
// delivered in order. If hooks fall so far behind that the queue fills, the
// event is dropped and reported rather than slowing the agent.
func (s *ScriptHooks) PostTool(ctx context.Context, session, tool string, args, result map[string]any, toolErr error) {
	if s == nil || !anyMatch(s.post, tool) {
		return
	}
	ev := HookEvent{Event: "post_tool", SessionID: session, Workspace: s.workspace, Tool: tool, Args: args, Result: result}
	if toolErr != nil {
		ev.Error = toolErr.Error()
	}
	payload, err := json.Marshal(ev)
	if err != nil {
		s.Warn(fmt.Sprintf("post_tool hook event for %s could not be encoded: %v", tool, err))
		return
	}
	q := s.startPost()
	job := postJob{ctx: trace.ContextWithSpanContext(q.ctx, trace.SpanContextFromContext(ctx)), tool: tool, payload: payload}

	q.mu.RLock()
	defer q.mu.RUnlock()
	if q.closed {
		return
	}
	select {
	case q.jobs <- job:
	default:
		if q.dropped.Add(1) == 1 {
			s.Warn("post_tool hooks are falling behind; events are being dropped (see the log)")
		}
		slog.WarnContext(ctx, "post_tool hook queue full; event dropped", "tool", tool)
		s.audit.Log(audit.Entry{Kind: audit.KindHook, Tool: tool, Detail: "post_tool: queue full", Decision: "dropped"})
	}
}

// startPost starts the worker on first use, so no goroutine exists unless
// a post_tool hook actually fires.
func (s *ScriptHooks) startPost() *postQueue {
	q := &s.postQ
	q.start.Do(func() {
		q.jobs = make(chan postJob, postQueueSize)
		q.done = make(chan struct{})
		q.ctx, q.cancel = context.WithCancel(context.Background())
		go func() {
			defer close(q.done)
			for job := range q.jobs {
				if job.barrier != nil {
					close(job.barrier)
					continue
				}
				s.dispatchSafely(job)
			}
		}()
	})
	return q
}

// dispatchSafely runs one queued event. The worker lives for the whole
// session, so a panic (in a hook's Warn callback, say) is contained to the
// event and logged; later events still run.
func (s *ScriptHooks) dispatchSafely(job postJob) {
	defer func() {
		if r := recover(); r != nil {
			slog.ErrorContext(job.ctx, "post_tool hook panicked", "tool", job.tool, "panic", r, "stack", string(debug.Stack()))
		}
	}()
	s.dispatch(job.ctx, s.post, job.tool, "post_tool", job.payload)
}

// flush waits until every post_tool event queued so far has been handled.
func (s *ScriptHooks) flush(ctx context.Context) error {
	q := s.startPost()
	barrier := make(chan struct{})
	q.mu.RLock()
	if q.closed {
		q.mu.RUnlock()
		return nil
	}
	select {
	case q.jobs <- postJob{barrier: barrier}:
	case <-ctx.Done():
		q.mu.RUnlock()
		return ctx.Err()
	}
	q.mu.RUnlock()
	select {
	case <-barrier:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close stops accepting post_tool events and lets queued hooks finish for
// up to postDrainTimeout, then kills any still running. Safe to call more
// than once.
func (s *ScriptHooks) Close() {
	if s == nil {
		return
	}
	q := &s.postQ
	q.mu.Lock()
	started := q.jobs != nil
	if started && !q.closed {
		close(q.jobs)
	}
	q.closed = true
	q.mu.Unlock()
	if !started {
		return
	}
	select {
	case <-q.done:
	case <-time.After(postDrainTimeout):
		slog.Warn("post_tool hooks still running at exit were stopped", "after", postDrainTimeout)
		q.cancel()
		<-q.done
	}
	q.cancel()
}

// anyMatch reports whether any hook applies to tool.
func anyMatch(hooks []config.HookConfig, tool string) bool {
	for _, h := range hooks {
		if h.Match == "" {
			return true
		}
		if ok, _ := path.Match(h.Match, tool); ok {
			return true
		}
	}
	return false
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
	payload, err := json.Marshal(ev)
	if err != nil {
		payload = nil // each hook then fails with the encoding error below
	}
	return s.dispatch(ctx, hooks, tool, ev.Event, payload)
}

// dispatch runs the hooks matching tool on an encoded event; the first
// block wins.
func (s *ScriptHooks) dispatch(ctx context.Context, hooks []config.HookConfig, tool, event string, payload []byte) string {
	for _, h := range hooks {
		if tool != "" && h.Match != "" {
			if ok, _ := path.Match(h.Match, tool); !ok {
				continue
			}
		}
		blocked, reason, err := s.run(ctx, h, event, tool, payload)
		entry := audit.Entry{Kind: audit.KindHook, Tool: tool, Detail: event + ": " + h.Command}
		switch {
		case blocked:
			entry.Decision = "block"
			entry.Error = reason
			s.audit.Log(entry)
			return reason
		case err != nil:
			entry.Error = err.Error()
			s.audit.Log(entry)
			slog.WarnContext(ctx, "hook failed", "event", event, "command", h.Command, "error", err)
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
func (s *ScriptHooks) run(ctx context.Context, h config.HookConfig, event, tool string, payload []byte) (blocked bool, reason string, err error) {
	ctx, span := observability.Start(ctx, "hook "+event,
		attribute.String("hook.event", event), attribute.String("tool", tool))
	defer func() {
		span.SetAttributes(attribute.Bool("blocked", blocked))
		observability.End(span, err)
	}()
	timeout := defaultHookTimeout
	if h.TimeoutSeconds > 0 {
		timeout = time.Duration(h.TimeoutSeconds) * time.Second
	}
	hctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if payload == nil {
		return false, "", errors.New("hook event could not be encoded")
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
