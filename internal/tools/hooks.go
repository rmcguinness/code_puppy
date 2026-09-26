package tools

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/retail-cortex/code_puppy/internal/audit"
	"github.com/retail-cortex/code_puppy/internal/observability"
	"go.opentelemetry.io/otel/attribute"
)

// ActionKind classifies a sensitive tool action for approval purposes.
type ActionKind string

const (
	ActionCommand ActionKind = "run_command" // shell commands and forged tool execution
	ActionWrite   ActionKind = "write_file"  // file creation and edits
	ActionDelete  ActionKind = "delete_file" // file deletion
	ActionNetwork ActionKind = "network"     // outbound web requests
	ActionMCP     ActionKind = "mcp_tool"    // tools served by MCP servers
)

// ApprovalRequest describes an action awaiting user approval.
type ApprovalRequest struct {
	Tool   string
	Kind   ActionKind
	Detail string
	// Diff is a unified diff of the proposed file changes, if any.
	Diff string
	// Key identifies what "allow for this session" / "always allow" covers,
	// e.g. an exact command or all edits in a workspace. Empty means the
	// approval can only be given once.
	Key string
	// KeyLabel describes Key for the user ("this exact command").
	KeyLabel string
	// Targets are what the action is on, for policies that match them (a
	// worker's permissions): workspace-relative paths for file changes (a
	// patch may touch several), the command, the URL's host, the search
	// provider, or the MCP tool as "server:tool". Empty when nothing can
	// match, which such policies refuse.
	Targets []string
}

// Decision is the user's answer to an approval request.
type Decision int

const (
	DecisionDeny    Decision = iota
	DecisionOnce             // allow this one action
	DecisionSession          // allow actions with the same Key until exit
	DecisionAlways           // allow actions with the same Key, persisted
)

func (d Decision) String() string {
	switch d {
	case DecisionOnce:
		return "once"
	case DecisionSession:
		return "session"
	case DecisionAlways:
		return "always"
	default:
		return "deny"
	}
}

// Approver asks the user to decide on a sensitive action.
type Approver func(ctx context.Context, req ApprovalRequest) (Decision, error)

// UserPromptFunc asks the user a question and returns the answer.
type UserPromptFunc func(ctx context.Context, question string, options []string) (string, error)

// InvokeAgentFunc runs a registered sub-agent with a prompt and returns its reply.
type InvokeAgentFunc func(ctx context.Context, agentName, prompt string) (string, error)

// Policy controls which actions skip the approval prompt.
type Policy struct {
	AutoApproveAll      bool // code_puppy.auto_approve
	AutoApproveCommands bool // tools.auto_approve_commands
}

// ErrNotApproved is returned when an action is denied or cannot be approved.
var ErrNotApproved = errors.New("action not approved")

// Hooks holds callbacks injected by the host application (TUI, engine) and the
// approval rules remembered for this session or persisted across sessions.
type Hooks struct {
	mu       sync.RWMutex
	policy   Policy
	approver Approver
	prompter UserPromptFunc
	invoker  InvokeAgentFunc
	session  map[string]bool
	store    *ApprovalStore
	audit    *audit.Logger
}

// NewHooks creates hooks with the given approval policy.
func NewHooks(policy Policy) *Hooks { return &Hooks{policy: policy, session: map[string]bool{}} }

func (h *Hooks) SetApprover(a Approver) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.approver = a
}

func (h *Hooks) SetUserPrompter(p UserPromptFunc) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.prompter = p
}

func (h *Hooks) SetSubagentInvoker(i InvokeAgentFunc) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.invoker = i
}

// SetStore attaches the persistent "always allow" rules.
func (h *Hooks) SetStore(s *ApprovalStore) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.store = s
}

// SetAudit attaches the audit log.
func (h *Hooks) SetAudit(l *audit.Logger) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.audit = l
}

// Audit returns the audit logger (possibly nil, which is a no-op).
func (h *Hooks) Audit() *audit.Logger {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.audit
}

func (h *Hooks) userPrompter() UserPromptFunc {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.prompter
}

func (h *Hooks) subagentInvoker() InvokeAgentFunc {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.invoker
}

// SessionRules returns the keys allowed for this session, sorted.
func (h *Hooks) SessionRules() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	keys := make([]string, 0, len(h.session))
	for k := range h.session {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Store returns the persistent rule store (may be nil).
func (h *Hooks) Store() *ApprovalStore {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.store
}

// RevokeSession forgets a session rule; it reports whether one existed.
func (h *Hooks) RevokeSession(key string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	ok := h.session[key]
	delete(h.session, key)
	return ok
}

// Approve returns nil when the action may proceed. It fails closed: with no
// approver configured and no auto-approve policy or remembered rule covering
// the action, the action is denied.
func (h *Hooks) Approve(ctx context.Context, req ApprovalRequest) error {
	if h == nil {
		return fmt.Errorf("%w: no approval hooks configured", ErrNotApproved)
	}
	h.mu.RLock()
	policy, approver, store, log := h.policy, h.approver, h.store, h.audit
	remembered := req.Key != "" && h.session[req.Key]
	h.mu.RUnlock()

	record := func(decision string) {
		kind := audit.KindApproval
		if decision == "deny" || decision == "no-approver" || decision == "error" {
			kind = audit.KindDenial
		}
		log.Log(audit.Entry{Kind: kind, Tool: req.Tool, Detail: req.Detail, Decision: decision})
	}

	switch {
	case policy.AutoApproveAll || (req.Kind == ActionCommand && policy.AutoApproveCommands):
		record("auto-policy")
		return nil
	case remembered:
		record("session-rule")
		return nil
	case req.Key != "" && store.Has(req.Key):
		record("saved-rule")
		return nil
	case approver == nil:
		record("no-approver")
		return fmt.Errorf("%w: %s requires approval but no interactive approver is available (enable auto-approve in config to allow it)", ErrNotApproved, req.Tool)
	}

	// The span isolates time spent waiting for the user from tool runtime.
	actx, span := observability.Start(ctx, "approval",
		attribute.String("tool", req.Tool), attribute.String("kind", string(req.Kind)))
	decision, err := approver(actx, req)
	span.SetAttributes(attribute.String("decision", decision.String()))
	observability.End(span, err)
	if err != nil {
		record("error")
		return fmt.Errorf("%w: %v", ErrNotApproved, err)
	}
	record(decision.String())

	switch decision {
	case DecisionOnce:
		return nil
	case DecisionSession, DecisionAlways:
		if req.Key == "" {
			return nil
		}
		h.mu.Lock()
		h.session[req.Key] = true
		h.mu.Unlock()
		if decision == DecisionAlways && store != nil {
			if err := store.Add(req.Key, req.KeyLabel); err != nil {
				return fmt.Errorf("approved, but saving the rule failed: %w", err)
			}
		}
		return nil
	default:
		return fmt.Errorf("%w: the user declined this %s", ErrNotApproved, req.Kind)
	}
}
