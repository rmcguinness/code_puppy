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
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/mcptoolset"
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

// newMCPManagerWithToolsets is used by tests to supply in-memory servers.
func newMCPManagerWithToolsets(servers map[string]tool.Toolset, autoApprove map[string]bool, reserved []string) *MCPManager {
	m := &MCPManager{owner: map[string]*mcpServer{}, reserved: map[string]bool{}, Warn: func(string) {}}
	for _, r := range reserved {
		m.reserved[r] = true
	}
	names := make([]string, 0, len(servers))
	for n := range servers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		m.servers = append(m.servers, &mcpServer{cfg: config.MCPServerConfig{Name: n, AutoApprove: autoApprove[n]}, toolset: servers[n]})
	}
	return m
}

// Toolsets returns one toolset per server, filtered and recorded.
func (m *MCPManager) Toolsets() []tool.Toolset {
	if m == nil {
		return nil
	}
	out := make([]tool.Toolset, 0, len(m.servers))
	for _, s := range m.servers {
		out = append(out, &recordingToolset{m: m, srv: s})
	}
	return out
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
		name := t.Name()
		if r.srv.allowed != nil && !r.srv.allowed[name] {
			continue
		}
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
