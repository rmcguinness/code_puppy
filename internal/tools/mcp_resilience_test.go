package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/retail-cortex/code_puppy/internal/breaker"
	"github.com/retail-cortex/code_puppy/internal/config"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// failingToolset fails while broken is set and counts how often it is asked.
type failingToolset struct {
	inner  tool.Toolset
	broken atomic.Bool
	calls  atomic.Int32
}

func (f *failingToolset) Name() string { return "failing" }
func (f *failingToolset) Tools(ctx agent.ReadonlyContext) ([]tool.Tool, error) {
	f.calls.Add(1)
	if f.broken.Load() {
		return nil, errors.New("connection refused")
	}
	return f.inner.Tools(ctx)
}

func TestUnhealthyServerIsSkippedUntilCooldown(t *testing.T) {
	fts := &failingToolset{inner: inMemoryMCP(t, "create_issue")}
	fts.broken.Store(true)
	m := NewMCPManagerFromToolsets([]MCPToolset{{Config: config.MCPServerConfig{Name: "gh"}, Toolset: fts}}, nil)
	var warnings []string
	m.Warn = func(s string) { warnings = append(warnings, s) }
	clk := &fakeClock{t: time.Unix(0, 0)}
	m.servers[0].health.SetClock(clk.now)
	ts := m.Toolsets()[0]

	for range 5 { // model calls while the server is down
		if tl, err := ts.Tools(createTestToolContext()); err != nil || len(tl) != 0 {
			t.Fatalf("down server: %v %v", tl, err)
		}
	}
	if n := fts.calls.Load(); n != mcpFailThreshold {
		t.Fatalf("server contacted %d times while down; want %d then paused", n, mcpFailThreshold)
	}
	if len(warnings) != 2 || !strings.Contains(warnings[0], "connection refused") || !strings.Contains(warnings[1], "paused") {
		t.Fatalf("warnings = %q", warnings)
	}

	fts.broken.Store(false)
	clk.advance(breaker.InitialCooldown)
	tl, err := ts.Tools(createTestToolContext())
	if err != nil || len(tl) != 1 {
		t.Fatalf("recovered server: %v %v", tl, err)
	}
	if last := warnings[len(warnings)-1]; !strings.Contains(last, "available again") {
		t.Fatalf("no recovery notice: %q", warnings)
	}
}

// TestMCPHelperServer is not a test: run with CODE_PUPPY_MCP_HELPER=1 it is
// a stdio MCP server for the tests below, so they exercise real processes.
func TestMCPHelperServer(t *testing.T) {
	if os.Getenv("CODE_PUPPY_MCP_HELPER") != "1" {
		t.Skip("helper process")
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "helper", Version: "1.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "echo"}, func(ctx context.Context, req *mcp.CallToolRequest, in echoArgs) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("%s from %d", in.Text, os.Getpid())}}}, nil, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "crash"}, func(ctx context.Context, req *mcp.CallToolRequest, in echoArgs) (*mcp.CallToolResult, any, error) {
		os.Exit(3)
		return nil, nil, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "slow"}, func(ctx context.Context, req *mcp.CallToolRequest, in echoArgs) (*mcp.CallToolResult, any, error) {
		time.Sleep(10 * time.Second)
		return &mcp.CallToolResult{}, nil, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "fail"}, func(ctx context.Context, req *mcp.CallToolRequest, in echoArgs) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "bad input"}}}, nil, nil
	})
	_ = server.Run(context.Background(), &mcp.StdioTransport{})
	os.Exit(0)
}

func helperServer(t *testing.T, timeoutSeconds int) (*MCPManager, map[string]runnerTool) {
	t.Helper()
	m, err := NewMCPManager([]config.MCPServerConfig{{
		Name:           "helper",
		Command:        os.Args[0],
		Args:           []string{"-test.run=^TestMCPHelperServer$"},
		Env:            map[string]string{"CODE_PUPPY_MCP_HELPER": "1"},
		TimeoutSeconds: timeoutSeconds,
	}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	tl, err := m.Toolsets()[0].Tools(createTestToolContext())
	if err != nil || len(tl) != 4 {
		t.Fatalf("helper tools: %v %v", toolNames(tl), err)
	}
	tools := map[string]runnerTool{}
	for _, x := range tl {
		tools[x.Name()] = x.(runnerTool)
	}
	return m, tools
}

func echoText(t *testing.T, rt runnerTool) (string, error) {
	t.Helper()
	res, err := rt.Run(createTestToolContext(), map[string]any{"text": "hi"})
	return fmt.Sprint(res), err
}

func TestCrashedStdioServerIsRestarted(t *testing.T) {
	m, tools := helperServer(t, 0)
	first, err := echoText(t, tools["echo"])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tools["crash"].Run(createTestToolContext(), map[string]any{"text": "x"}); err == nil {
		t.Fatal("crash tool returned no error")
	}
	second, err := echoText(t, tools["echo"])
	if err != nil {
		t.Fatalf("server not restarted after a crash: %v", err)
	}
	if first == second {
		t.Fatalf("same process answered before and after the crash: %s", first)
	}
	if ok, _ := m.servers[0].health.Allow(); !ok {
		t.Fatal("breaker open after a successful call")
	}
}

func TestSlowMCPCallTimesOutAndToolErrorsKeepServerHealthy(t *testing.T) {
	m, tools := helperServer(t, 1)
	start := time.Now()
	_, err := tools["slow"].Run(createTestToolContext(), map[string]any{"text": "x"})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("slow call: %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("timeout took %v", d)
	}
	if m.servers[0].health.Failures() != 1 {
		t.Fatalf("timeout not counted: %d failures", m.servers[0].health.Failures())
	}

	// A tool-level error means the server is working: it resets the streak.
	if _, err := tools["fail"].Run(createTestToolContext(), map[string]any{"text": "x"}); err == nil || !strings.Contains(err.Error(), "bad input") {
		t.Fatalf("fail tool: %v", err)
	}
	if m.servers[0].health.Failures() != 0 {
		t.Fatal("a tool error was counted as a server failure")
	}
}
