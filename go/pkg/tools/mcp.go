package tools

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/retail-cortex/code_puppy/pkg/config"
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
	cfg     config.MCPServerConfig
	toolset tool.Toolset
	allowed map[string]bool // optional allow-list
	cmd     *guardedCmd     // stdio server process, if any
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

		srv := &mcpServer{cfg: c}
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
				serverEnv = &ExecEnv{ScrubEnv: env.ScrubEnv}
			}
			cmd, err := serverEnv.command(context.Background(), append([]string{c.Command}, c.Args...))
			if err != nil {
				return nil, fmt.Errorf("mcp server %q: %w", c.Name, err)
			}
			base := cmd.Env
			if base == nil {
				base = os.Environ()
			}
			for k, v := range c.Env {
				base = append(base, k+"="+v)
			}
			cmd.Env = base
			srv.cmd = cmd
			tsCfg.Transport = &mcp.CommandTransport{Command: cmd.Cmd}
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
		srv := &mcpServer{cfg: s.Config, toolset: s.Toolset}
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
		if s.cmd != nil && s.cmd.Process != nil {
			_ = killProcessGroup(s.cmd.Cmd)
			s.cmd.Release()
		}
	}
}

type recordingToolset struct {
	m   *MCPManager
	srv *mcpServer
}

func (r *recordingToolset) Name() string { return "mcp:" + r.srv.cfg.Name }

// Tools lists the server's tools, dropping ones not allow-listed or that
// would shadow a built-in tool, and records ownership for approvals.
func (r *recordingToolset) Tools(ctx agent.ReadonlyContext) ([]tool.Tool, error) {
	tools, err := r.srv.toolset.Tools(ctx)
	if err != nil {
		r.m.Warn(fmt.Sprintf("mcp server %q unavailable: %v", r.srv.cfg.Name, err))
		return nil, nil // one broken server shouldn't break every turn
	}
	out := tools[:0:0]
	r.m.mu.Lock()
	defer r.m.mu.Unlock()
	for _, t := range tools {
		if r.srv.allowed != nil && !r.srv.allowed[t.Name()] {
			continue
		}
		if p := r.srv.cfg.Prefix; p != "" {
			ft, ok := t.(functionTool)
			if !ok {
				r.m.Warn(fmt.Sprintf("mcp server %q: tool %q can't be renamed and was skipped", r.srv.cfg.Name, t.Name()))
				continue
			}
			t = &prefixedTool{inner: ft, name: p + "__" + t.Name()}
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

// prefixedTool exposes an MCP tool under a namespaced name. Calls still go
// to the server under the tool's original name.
type prefixedTool struct {
	inner functionTool
	name  string
}

func (p *prefixedTool) Name() string        { return p.name }
func (p *prefixedTool) Description() string { return p.inner.Description() }
func (p *prefixedTool) IsLongRunning() bool { return p.inner.IsLongRunning() }

// Declaration is the inner declaration under the prefixed name.
func (p *prefixedTool) Declaration() *genai.FunctionDeclaration {
	d := p.inner.Declaration()
	if d == nil {
		return nil
	}
	c := *d
	c.Name = p.name
	return &c
}

func (p *prefixedTool) Run(ctx agent.Context, args any) (map[string]any, error) {
	return p.inner.Run(ctx, args)
}

// ProcessRequest registers the tool in the request under its prefixed name.
func (p *prefixedTool) ProcessRequest(ctx agent.Context, req *model.LLMRequest) error {
	return toolutils.PackTool(req, p)
}

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
