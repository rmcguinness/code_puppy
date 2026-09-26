package tools

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/retail-cortex/code_puppy/internal/breaker"
	"github.com/retail-cortex/code_puppy/internal/config"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/mcptoolset"
	"google.golang.org/adk/v2/tool/toolutils"
	"google.golang.org/genai"
)

// MCPManager owns the configured MCP servers and knows which tool names each
// one serves, so tool calls can be approved per server.
type MCPManager struct {
	mu       sync.Mutex
	servers  []*mcpServer
	owner    map[string]*mcpServer // tool name -> server
	reserved map[string]bool       // built-in tool names MCP tools may not shadow
	Warn     func(string)
}

type mcpServer struct {
	cfg       config.MCPServerConfig
	toolset   tool.Toolset
	allowed   map[string]bool // optional allow-list
	transport *stdioTransport // stdio servers only
	health    *breaker.Breaker
}

const (
	// mcpFailThreshold failures in a row pause a server.
	mcpFailThreshold = 2
	// mcpListTimeout bounds connecting to a server and listing its tools,
	// which happens before every model call.
	mcpListTimeout = 30 * time.Second
	// defaultMCPCallTimeout bounds one tool call unless timeout_seconds is set.
	defaultMCPCallTimeout = 5 * time.Minute
)

func (s *mcpServer) callTimeout() time.Duration {
	if s.cfg.TimeoutSeconds > 0 {
		return time.Duration(s.cfg.TimeoutSeconds) * time.Second
	}
	return defaultMCPCallTimeout
}

// stdioTransport starts a new server process on every Connect. The SDK's
// CommandTransport wraps a single exec.Cmd, which can only be started once,
// so after a server crash the ADK's automatic reconnect could never succeed.
// Each process is guarded (dies with Code Puppy); the previous one is killed
// when a new one starts.
type stdioTransport struct {
	build func() (*guardedCmd, error)

	mu  sync.Mutex
	cur *guardedCmd
}

func (t *stdioTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	cmd, err := t.build()
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	prev := t.cur
	t.cur = cmd
	t.mu.Unlock()
	stopProcess(prev)

	conn, err := (&mcp.CommandTransport{Command: cmd.Cmd}).Connect(ctx)
	if cmd.childEnd != nil {
		cmd.childEnd.Close() // the child holds its end; see guardedCmd.Start
	}
	if err != nil {
		cmd.Release()
		return nil, err
	}
	return conn, nil
}

// Close kills the current server process.
func (t *stdioTransport) Close() {
	t.mu.Lock()
	cur := t.cur
	t.cur = nil
	t.mu.Unlock()
	stopProcess(cur)
}

func stopProcess(cmd *guardedCmd) {
	if cmd != nil && cmd.Process != nil {
		_ = killProcessGroup(cmd.Cmd)
		cmd.Release()
	}
}

// NewMCPManager validates configs and prepares toolsets. Servers connect
// lazily when their tools are first listed. Stdio servers run through env
// (process guard, scrubbed environment and, unless disabled per server, the
// OS sandbox).
func NewMCPManager(cfgs []config.MCPServerConfig, env *ExecEnv, reserved []string) (*MCPManager, error) {
	m := &MCPManager{owner: map[string]*mcpServer{}, reserved: map[string]bool{}, Warn: func(string) {}}
	for _, r := range reserved {
		m.reserved[r] = true
	}
	names := map[string]bool{}
	for _, c := range cfgs {
		if c.Name == "" {
			return nil, fmt.Errorf("mcp server needs a name")
		}
		if names[c.Name] {
			return nil, fmt.Errorf("duplicate mcp server name %q", c.Name)
		}
		names[c.Name] = true
		if (c.Command == "") == (c.URL == "") {
			return nil, fmt.Errorf("mcp server %q: set exactly one of command or url", c.Name)
		}

		srv := &mcpServer{cfg: c, health: breaker.New(mcpFailThreshold)}
		if len(c.Tools) > 0 {
			srv.allowed = map[string]bool{}
			for _, t := range c.Tools {
				srv.allowed[t] = true
			}
		}

		tsCfg := mcptoolset.Config{}
		if c.Command != "" {
			serverEnv := env
			if c.Sandbox != nil && !*c.Sandbox && env != nil {
				serverEnv = &ExecEnv{ScrubEnv: env.ScrubEnv, Dir: env.Dir}
			}
			argv := append([]string{c.Command}, c.Args...)
			build := func() (*guardedCmd, error) {
				// Not tied to a request context: the server outlives the call
				// that started it.
				cmd, err := serverEnv.command(context.Background(), argv)
				if err != nil {
					return nil, err
				}
				base := cmd.Env
				if base == nil {
					base = os.Environ()
				}
				for k, v := range c.Env {
					base = append(base, k+"="+v)
				}
				cmd.Env = base
				return cmd, nil
			}
			if _, err := build(); err != nil { // validate the sandbox wrapping up front
				return nil, fmt.Errorf("mcp server %q: %w", c.Name, err)
			}
			srv.transport = &stdioTransport{build: build}
			tsCfg.Transport = srv.transport
		} else {
			tsCfg.Endpoint = c.URL
		}
		ts, err := mcptoolset.New(tsCfg)
		if err != nil {
			return nil, fmt.Errorf("mcp server %q: %w", c.Name, err)
		}
		srv.toolset = ts
		m.servers = append(m.servers, srv)
	}
	return m, nil
}

// MCPToolset pairs a server configuration with an already-connected toolset,
// for embedding applications (and tests) that manage transports themselves.
// Command and URL in Config are ignored.
type MCPToolset struct {
	Config  config.MCPServerConfig
	Toolset tool.Toolset
}

// NewMCPManagerFromToolsets builds a manager over existing toolsets.
func NewMCPManagerFromToolsets(servers []MCPToolset, reserved []string) *MCPManager {
	m := &MCPManager{owner: map[string]*mcpServer{}, reserved: map[string]bool{}, Warn: func(string) {}}
	for _, r := range reserved {
		m.reserved[r] = true
	}
	for _, s := range servers {
		srv := &mcpServer{cfg: s.Config, toolset: s.Toolset, health: breaker.New(mcpFailThreshold)}
		if len(s.Config.Tools) > 0 {
			srv.allowed = map[string]bool{}
			for _, t := range s.Config.Tools {
				srv.allowed[t] = true
			}
		}
		m.servers = append(m.servers, srv)
	}
	return m
}

// newMCPManagerWithToolsets is a test shorthand keyed by server name.
func newMCPManagerWithToolsets(servers map[string]tool.Toolset, autoApprove map[string]bool, reserved []string) *MCPManager {
	names := make([]string, 0, len(servers))
	for n := range servers {
		names = append(names, n)
	}
	sort.Strings(names)
	var specs []MCPToolset
	for _, n := range names {
		specs = append(specs, MCPToolset{Config: config.MCPServerConfig{Name: n, AutoApprove: autoApprove[n]}, Toolset: servers[n]})
	}
	return NewMCPManagerFromToolsets(specs, reserved)
}

// Toolsets returns one toolset per server for the primary agent.
func (m *MCPManager) Toolsets() []tool.Toolset { return m.ToolsetsFor("", true) }

// ToolsetsFor returns the toolsets offered to agent. Servers without an
// agents list go to the primary agent only; "*" matches every agent.
func (m *MCPManager) ToolsetsFor(agent string, primary bool) []tool.Toolset {
	if m == nil {
		return nil
	}
	var out []tool.Toolset
	for _, s := range m.servers {
		if s.offeredTo(agent, primary) {
			out = append(out, &recordingToolset{m: m, srv: s})
		}
	}
	return out
}

func (s *mcpServer) offeredTo(agent string, primary bool) bool {
	if len(s.cfg.Agents) == 0 {
		return primary
	}
	for _, a := range s.cfg.Agents {
		if a == "*" || a == agent {
			return true
		}
	}
	return false
}

// Servers returns configured server names.
func (m *MCPManager) Servers() []string {
	if m == nil {
		return nil
	}
	var out []string
	for _, s := range m.servers {
		out = append(out, s.cfg.Name)
	}
	return out
}

// Lookup reports which server serves toolName.
func (m *MCPManager) Lookup(toolName string) (server string, autoApprove, ok bool) {
	if m == nil {
		return "", false, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.owner[toolName]
	if s == nil {
		return "", false, false
	}
	return s.cfg.Name, s.cfg.AutoApprove, true
}

// Close stops stdio server processes.
func (m *MCPManager) Close() {
	if m == nil {
		return
	}
	for _, s := range m.servers {
		if s.transport != nil {
			s.transport.Close()
		}
	}
}

type recordingToolset struct {
	m   *MCPManager
	srv *mcpServer
}

func (r *recordingToolset) Name() string { return "mcp:" + r.srv.cfg.Name }

// Tools lists the server's tools, dropping ones not allow-listed or that
// would shadow a built-in tool, and records ownership for approvals. It runs
// before every model call, so an unhealthy server is skipped (its tools are
// simply absent) until its circuit breaker lets a trial through.
func (r *recordingToolset) Tools(ctx agent.ReadonlyContext) ([]tool.Tool, error) {
	if ok, _ := r.srv.health.Allow(); !ok {
		return nil, nil
	}
	lctx, cancel := context.WithTimeout(ctx, mcpListTimeout)
	defer cancel()
	tools, err := r.srv.toolset.Tools(readonlyWith{ctx, lctx})
	if err != nil {
		r.m.recordFailure(ctx, r.srv, err)
		return nil, nil // one broken server shouldn't break every turn
	}
	r.m.recordSuccess(ctx, r.srv)
	out := tools[:0:0]
	r.m.mu.Lock()
	defer r.m.mu.Unlock()
	for _, t := range tools {
		if r.srv.allowed != nil && !r.srv.allowed[t.Name()] {
			continue
		}
		if ft, ok := t.(functionTool); ok {
			name := t.Name()
			if p := r.srv.cfg.Prefix; p != "" {
				name = p + "__" + name
			}
			t = &managedTool{inner: ft, name: name, m: r.m, srv: r.srv}
		} else if r.srv.cfg.Prefix != "" {
			r.m.Warn(fmt.Sprintf("mcp server %q: tool %q can't be renamed and was skipped", r.srv.cfg.Name, t.Name()))
			continue
		}
		name := t.Name()
		if r.m.reserved[name] {
			r.m.Warn(fmt.Sprintf("mcp server %q: tool %q shadows a built-in tool and was skipped", r.srv.cfg.Name, name))
			continue
		}
		if other := r.m.owner[name]; other != nil && other != r.srv {
			r.m.Warn(fmt.Sprintf("mcp tool %q is served by both %q and %q; using %q", name, other.cfg.Name, r.srv.cfg.Name, other.cfg.Name))
			continue
		}
		r.m.owner[name] = r.srv
		out = append(out, t)
	}
	return out, nil
}

// functionTool is the shape ADK's tool executor calls (Declaration + Run).
type functionTool interface {
	tool.Tool
	Declaration() *genai.FunctionDeclaration
	Run(ctx agent.Context, args any) (map[string]any, error)
}

// managedTool wraps an MCP tool: it may expose it under a prefixed name
// (calls still reach the server under the original name), bounds each call
// with the server's timeout, and feeds the server's circuit breaker.
type managedTool struct {
	inner functionTool
	name  string
	m     *MCPManager
	srv   *mcpServer
}

func (p *managedTool) Name() string        { return p.name }
func (p *managedTool) Description() string { return p.inner.Description() }
func (p *managedTool) IsLongRunning() bool { return p.inner.IsLongRunning() }

// Declaration is the inner declaration under the exposed name.
func (p *managedTool) Declaration() *genai.FunctionDeclaration {
	d := p.inner.Declaration()
	if d == nil {
		return nil
	}
	c := *d
	c.Name = p.name
	return &c
}

func (p *managedTool) Run(ctx agent.Context, args any) (map[string]any, error) {
	if ok, retryIn := p.srv.health.Allow(); !ok {
		return nil, fmt.Errorf("MCP server %q is unavailable after repeated failures; retrying in %s", p.srv.cfg.Name, retryIn.Round(time.Second))
	}
	cctx, cancel := context.WithTimeout(ctx, p.srv.callTimeout())
	defer cancel()
	res, err := p.inner.Run(toolWith{ctx, cctx}, args)
	switch {
	case err == nil:
		p.m.recordSuccess(ctx, p.srv)
	case cctx.Err() == context.DeadlineExceeded && ctx.Err() == nil:
		err = fmt.Errorf("MCP tool %q timed out after %s", p.name, p.srv.callTimeout())
		p.m.recordFailure(ctx, p.srv, err)
	case transportFailure(err):
		p.m.recordFailure(ctx, p.srv, err)
	default:
		// The server answered with a tool error: it is healthy.
		p.m.recordSuccess(ctx, p.srv)
	}
	return res, err
}

// transportFailure reports whether a tool call failed to reach the server
// (as opposed to the server reporting a tool error). The ADK wraps
// connection errors this way; tool errors start "Tool execution failed".
func transportFailure(err error) bool {
	return strings.HasPrefix(err.Error(), "failed to call MCP tool")
}

// ProcessRequest registers the tool in the request under its exposed name.
func (p *managedTool) ProcessRequest(ctx agent.Context, req *model.LLMRequest) error {
	return toolutils.PackTool(req, p)
}

// recordFailure feeds the breaker. The terminal hears about the first
// failure of a streak and about the server being paused, not about every
// failed call; the log gets each one.
func (m *MCPManager) recordFailure(ctx context.Context, s *mcpServer, err error) {
	slog.WarnContext(ctx, "mcp server call failed", "server", s.cfg.Name, "error", err)
	switch streak, opened, cooldown := s.health.Failure(); {
	case opened:
		m.Warn(fmt.Sprintf("mcp server %q keeps failing; its tools are paused for %s", s.cfg.Name, cooldown))
	case streak == 1:
		m.Warn(fmt.Sprintf("mcp server %q unavailable: %v", s.cfg.Name, err))
	}
}

func (m *MCPManager) recordSuccess(ctx context.Context, s *mcpServer) {
	if s.health.Success() {
		slog.InfoContext(ctx, "mcp server recovered", "server", s.cfg.Name)
		m.Warn(fmt.Sprintf("mcp server %q is available again", s.cfg.Name))
	}
}

// readonlyWith and toolWith keep an ADK context's methods but take
// deadline, cancellation and values from a derived context.
type readonlyWith struct {
	agent.ReadonlyContext
	ctx context.Context
}

func (c readonlyWith) Deadline() (time.Time, bool) { return c.ctx.Deadline() }
func (c readonlyWith) Done() <-chan struct{}       { return c.ctx.Done() }
func (c readonlyWith) Err() error                  { return c.ctx.Err() }
func (c readonlyWith) Value(key any) any           { return c.ctx.Value(key) }

type toolWith struct {
	agent.Context
	ctx context.Context
}

func (c toolWith) Deadline() (time.Time, bool) { return c.ctx.Deadline() }
func (c toolWith) Done() <-chan struct{}       { return c.ctx.Done() }
func (c toolWith) Err() error                  { return c.ctx.Err() }
func (c toolWith) Value(key any) any           { return c.ctx.Value(key) }

// mcpApproval builds the approval request for an MCP tool call.
func mcpApproval(server, toolName string, args map[string]any) ApprovalRequest {
	var parts []string
	for k, v := range args {
		parts = append(parts, fmt.Sprintf("%s=%v", k, v))
	}
	sort.Strings(parts)
	detail := fmt.Sprintf("%s (MCP server %q)", toolName, server)
	if len(parts) > 0 {
		detail += "\n" + strings.Join(parts, "\n")
	}
	return ApprovalRequest{
		Tool: toolName, Kind: ActionMCP, Detail: detail,
		Key: "mcp:" + server + ":" + toolName, KeyLabel: fmt.Sprintf("%s from %s", toolName, server),
	}
}

// ApproveMCP gates an MCP tool call; it returns nil for non-MCP tools.
func (r *Registry) ApproveMCP(ctx context.Context, toolName string, args map[string]any) error {
	server, auto, ok := r.mcp.Lookup(toolName)
	if !ok || auto {
		return nil
	}
	return r.hooks.Approve(ctx, mcpApproval(server, toolName, args))
}
