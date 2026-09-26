package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/retail-cortex/code_puppy/internal/config"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/mcptoolset"
)

type echoArgs struct {
	Text string `json:"text"`
}

// inMemoryMCP starts an MCP server with the given tool names and returns an
// ADK toolset connected to it.
func inMemoryMCP(t *testing.T, names ...string) tool.Toolset {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1.0"}, nil)
	for _, n := range names {
		mcp.AddTool(server, &mcp.Tool{Name: n, Description: "echo " + n}, func(ctx context.Context, req *mcp.CallToolRequest, in echoArgs) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: in.Text}}}, nil, nil
		})
	}
	clientT, serverT := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if _, err := server.Connect(ctx, serverT, nil); err != nil {
		t.Fatal(err)
	}
	ts, err := mcptoolset.New(mcptoolset.Config{Transport: clientT})
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

func toolNames(ts []tool.Tool) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.Name())
	}
	return out
}

func TestMCPManagerRecordsAndFilters(t *testing.T) {
	m := newMCPManagerWithToolsets(map[string]tool.Toolset{
		"alpha": inMemoryMCP(t, "search", "read_file"), // read_file shadows a built-in
		"beta":  inMemoryMCP(t, "search", "deploy"),    // duplicate "search"
	}, map[string]bool{"beta": true}, []string{"read_file"})
	var warnings []string
	m.Warn = func(s string) { warnings = append(warnings, s) }

	var all []string
	for _, ts := range m.Toolsets() {
		tl, err := ts.Tools(createTestToolContext())
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, toolNames(tl)...)
	}
	if strings.Join(all, ",") != "search,deploy" {
		t.Errorf("tools = %v", all)
	}
	if len(warnings) != 2 {
		t.Errorf("expected shadow + duplicate warnings, got %v", warnings)
	}
	if srv, auto, ok := m.Lookup("search"); !ok || srv != "alpha" || auto {
		t.Errorf("lookup search = %s %v %v", srv, auto, ok)
	}
	if srv, auto, ok := m.Lookup("deploy"); !ok || srv != "beta" || !auto {
		t.Errorf("lookup deploy = %s %v %v", srv, auto, ok)
	}
	if _, _, ok := m.Lookup("read_file"); ok {
		t.Error("built-in name should not be owned by MCP")
	}
}

func TestRegistryApprovesMCPTools(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.WorkspaceDir = t.TempDir()
	cfg.Tools.ApprovalsFile = ""
	reg, err := NewRegistry(cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()
	reg.mcp = newMCPManagerWithToolsets(map[string]tool.Toolset{"gh": inMemoryMCP(t, "create_issue")}, nil, nil)
	for _, ts := range reg.mcp.Toolsets() {
		ts.Tools(createTestToolContext())
	}

	h, reqs := approverHooks(false)
	reg.hooks = h
	err = reg.ApproveMCP(context.Background(), "create_issue", map[string]any{"title": "bug"})
	if err == nil || len(*reqs) != 1 {
		t.Fatalf("expected denied approval, got %v (%d prompts)", err, len(*reqs))
	}
	req := (*reqs)[0]
	if req.Kind != ActionMCP || req.Key != "mcp:gh:create_issue" || !strings.Contains(req.Detail, "title=bug") {
		t.Errorf("approval request %+v", req)
	}
	// Non-MCP tools pass straight through.
	if err := reg.ApproveMCP(context.Background(), "grep", nil); err != nil {
		t.Errorf("built-in tool gated as MCP: %v", err)
	}
}

func TestNewMCPManagerValidation(t *testing.T) {
	for name, cfgs := range map[string][]config.MCPServerConfig{
		"no name":   {{Command: "x"}},
		"both":      {{Name: "a", Command: "x", URL: "http://x"}},
		"neither":   {{Name: "a"}},
		"duplicate": {{Name: "a", Command: "x"}, {Name: "a", URL: "http://y"}},
	} {
		if _, err := NewMCPManager(cfgs, nil, nil); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	m, err := NewMCPManager([]config.MCPServerConfig{{Name: "local", Command: "true"}, {Name: "remote", URL: "https://example.com/mcp"}}, nil, nil)
	if err != nil || strings.Join(m.Servers(), ",") != "local,remote" {
		t.Errorf("valid config: %v %v", err, m.Servers())
	}
	m.Close()
	// An unreachable server yields no tools and a warning, not an error.
	var warned bool
	m.Warn = func(string) { warned = true }
	for _, ts := range m.Toolsets() {
		if tl, err := ts.Tools(createTestToolContext()); err != nil || len(tl) != 0 {
			t.Errorf("broken server: %v %v", tl, err)
		}
	}
	if !warned {
		t.Error("expected a warning for unreachable servers")
	}
}

func TestMCPPrefixedTools(t *testing.T) {
	m := NewMCPManagerFromToolsets([]MCPToolset{{
		Config:  config.MCPServerConfig{Name: "github", Prefix: "gh"},
		Toolset: inMemoryMCP(t, "create_issue", "grep"), // "grep" would shadow a built-in without the prefix
	}}, []string{"grep"})
	tl, err := m.Toolsets()[0].Tools(createTestToolContext())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(toolNames(tl), ","); got != "gh__create_issue,gh__grep" {
		t.Fatalf("prefixed names = %s", got)
	}
	pt := tl[0].(*managedTool)
	if d := pt.Declaration(); d == nil || d.Name != "gh__create_issue" {
		t.Errorf("declaration name %+v", d)
	}
	// The call reaches the server under the original name.
	res, err := pt.Run(createTestToolContext(), map[string]any{"text": "hello from gh"})
	if err != nil || !strings.Contains(fmt.Sprint(res), "hello from gh") {
		t.Errorf("run through prefix: %v %v", res, err)
	}
	// Registered in the request under the prefixed name.
	req := &model.LLMRequest{}
	if err := pt.ProcessRequest(createTestToolContext(), req); err != nil {
		t.Fatal(err)
	}
	if _, ok := req.Tools["gh__create_issue"]; !ok {
		t.Errorf("request tools %v", req.Tools)
	}
	if srv, _, ok := m.Lookup("gh__grep"); !ok || srv != "github" {
		t.Errorf("lookup prefixed: %s %v", srv, ok)
	}
	if _, _, ok := m.Lookup("create_issue"); ok {
		t.Error("the unprefixed name must not be routed")
	}
}

func TestMCPToolsetsForAgents(t *testing.T) {
	m := NewMCPManagerFromToolsets([]MCPToolset{
		{Config: config.MCPServerConfig{Name: "primary-only"}, Toolset: inMemoryMCP(t, "a")},
		{Config: config.MCPServerConfig{Name: "kitten", Agents: []string{"qa-kitten"}}, Toolset: inMemoryMCP(t, "b")},
		{Config: config.MCPServerConfig{Name: "everyone", Agents: []string{"*"}}, Toolset: inMemoryMCP(t, "c")},
	}, nil)
	names := func(ts []tool.Toolset) string {
		var out []string
		for _, x := range ts {
			out = append(out, x.Name())
		}
		return strings.Join(out, ",")
	}
	cases := []struct {
		agent   string
		primary bool
		want    string
	}{
		{"code-puppy", true, "mcp:primary-only,mcp:everyone"},
		{"qa-kitten", false, "mcp:kitten,mcp:everyone"},
		{"qa-kitten", true, "mcp:primary-only,mcp:kitten,mcp:everyone"},
		{"helios", false, "mcp:everyone"},
	}
	for _, c := range cases {
		if got := names(m.ToolsetsFor(c.agent, c.primary)); got != c.want {
			t.Errorf("ToolsetsFor(%s, %v) = %s, want %s", c.agent, c.primary, got, c.want)
		}
	}
	var nilManager *MCPManager
	if nilManager.ToolsetsFor("x", true) != nil {
		t.Error("nil manager should offer nothing")
	}
}
