package runtime

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/retail-cortex/code_puppy/pkg/config"
	"github.com/retail-cortex/code_puppy/pkg/tools"
	"google.golang.org/adk/v2/tool/mcptoolset"
)

func inMemoryServer(t *testing.T, name string) *mcptoolset.Config {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: name, Description: name}, func(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil, nil
	})
	ct, st := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	return &mcptoolset.Config{Transport: ct}
}

func TestMCPToolsReachChosenSubAgentOnly(t *testing.T) {
	f := newEngineWith(t, fixtureOpts{}, textContent("root reply"), textContent("kitten reply"))
	ts, err := mcptoolset.New(*inMemoryServer(t, "run_e2e_tests"))
	if err != nil {
		t.Fatal(err)
	}
	f.tools.SetMCP(tools.NewMCPManagerFromToolsets([]tools.MCPToolset{{
		Config: config.MCPServerConfig{Name: "qa", Agents: []string{"qa-kitten"}, Prefix: "qa"}, Toolset: ts,
	}}, nil))
	if err := f.eng.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}

	if _, err := collect(t, f.eng, "s", "hello"); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.llm.Requests[0].Tools["qa__run_e2e_tests"]; ok {
		t.Error("primary agent was offered a server scoped to qa-kitten")
	}
	if _, err := f.eng.InvokeSubagent(context.Background(), "qa-kitten", "run the tests"); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.llm.Requests[1].Tools["qa__run_e2e_tests"]; !ok {
		t.Errorf("qa-kitten not offered its MCP tool; tools: %v", keys(f.llm.Requests[1].Tools))
	}
}

func keys(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
