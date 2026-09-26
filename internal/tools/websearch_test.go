package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/retail-cortex/code_puppy/internal/config"
)

type captured struct {
	method, path, query, auth, token string
	body                             map[string]any
}

func searchServer(t *testing.T, reply string, status int, got *captured) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.method, got.path, got.query = r.Method, r.URL.Path, r.URL.RawQuery
		got.auth, got.token = r.Header.Get("Authorization"), r.Header.Get("X-Subscription-Token")
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			json.Unmarshal(b, &got.body)
		}
		w.WriteHeader(status)
		io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func searcher(t *testing.T, cfg WebSearchConfig) *webSearcher {
	t.Helper()
	cfg.AllowNetwork = true
	s, err := newWebSearcher(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestWebSearchProviders(t *testing.T) {
	ctx := context.Background()

	var b captured
	braveURL := searchServer(t, `{"web":{"results":[
		{"title":"Go","url":"https://go.dev/","description":"The <strong>Go</strong> language"},
		{"title":"Blocked","url":"https://ads.evil.test/x","description":"no"},
		{"title":"FTP","url":"ftp://files.example.com/","description":"no"}]}}`, 200, &b)
	out := searcher(t, WebSearchConfig{Provider: "brave", APIKey: "brv-key", BaseURL: braveURL, DenyDomains: []string{"*.evil.test"}}).
		search(ctx, allowAll(), WebSearchInput{Query: "golang", MaxResults: 3})
	if out.Error != "" || len(out.Results) != 1 || out.Results[0].URL != "https://go.dev/" || out.Results[0].Snippet != "The Go language" {
		t.Errorf("brave results %+v", out)
	}
	if b.token != "brv-key" || !strings.Contains(b.query, "q=golang") || !strings.Contains(b.query, "count=3") {
		t.Errorf("brave request %+v", b)
	}

	var tv captured
	tavilyURL := searchServer(t, `{"results":[{"title":"A","url":"https://a.example/","content":"alpha"}]}`, 200, &tv)
	out = searcher(t, WebSearchConfig{Provider: "tavily", APIKey: "tvly-key", BaseURL: tavilyURL}).search(ctx, allowAll(), WebSearchInput{Query: "alpha"})
	if len(out.Results) != 1 || out.Results[0].Snippet != "alpha" {
		t.Errorf("tavily results %+v", out)
	}
	if tv.method != "POST" || tv.auth != "Bearer tvly-key" || tv.body["query"] != "alpha" || tv.body["max_results"] != float64(5) {
		t.Errorf("tavily request %+v", tv)
	}

	var sx captured
	sxURL := searchServer(t, `{"results":[{"title":"S","url":"https://s.example/","content":"searx"}]}`, 200, &sx)
	out = searcher(t, WebSearchConfig{Provider: "searxng", BaseURL: sxURL + "/"}).search(ctx, allowAll(), WebSearchInput{Query: "q"})
	if len(out.Results) != 1 || sx.path != "/search" || !strings.Contains(sx.query, "format=json") {
		t.Errorf("searxng %+v / %+v", out, sx)
	}
}

func TestWebSearchApprovalAndErrors(t *testing.T) {
	ctx := context.Background()
	var c captured
	u := searchServer(t, `{"web":{"results":[]}}`, 200, &c)
	s := searcher(t, WebSearchConfig{Provider: "brave", APIKey: "k", BaseURL: u})

	h, reqs := approverHooks(false)
	if out := s.search(ctx, h, WebSearchInput{Query: "secret project name"}); !strings.Contains(out.Error, "not approved") {
		t.Errorf("expected denial: %+v", out)
	}
	if len(*reqs) != 1 || (*reqs)[0].Key != "search:brave" || !strings.Contains((*reqs)[0].Detail, "secret project name") {
		t.Errorf("approval request %+v", *reqs)
	}
	if c.method != "" {
		t.Error("a denied search reached the provider")
	}
	if out := s.search(ctx, allowAll(), WebSearchInput{Query: "  "}); out.Error == "" {
		t.Error("empty query should fail")
	}
	off := searcher(t, WebSearchConfig{Provider: "brave", APIKey: "k", BaseURL: u})
	off.cfg.AllowNetwork = false
	if out := off.search(ctx, allowAll(), WebSearchInput{Query: "x"}); !strings.Contains(out.Error, "disabled") {
		t.Errorf("network off: %+v", out)
	}

	for status, want := range map[int]string{401: "authentication failed", 429: "rate limited", 500: "HTTP 500"} {
		var e captured
		bad := searcher(t, WebSearchConfig{Provider: "brave", APIKey: "k", BaseURL: searchServer(t, `{"error":"x"}`, status, &e)})
		if out := bad.search(ctx, allowAll(), WebSearchInput{Query: "x"}); !strings.Contains(out.Error, want) {
			t.Errorf("status %d: %q", status, out.Error)
		}
	}
	var g captured
	garbage := searcher(t, WebSearchConfig{Provider: "brave", APIKey: "k", BaseURL: searchServer(t, `<html>`, 200, &g)})
	if out := garbage.search(ctx, allowAll(), WebSearchInput{Query: "x"}); !strings.Contains(out.Error, "unexpected response") {
		t.Errorf("garbage: %q", out.Error)
	}
}

func TestWebSearchConfigValidation(t *testing.T) {
	t.Setenv("BRAVE_API_KEY", "")
	t.Setenv("TAVILY_API_KEY", "from-env")
	for name, cfg := range map[string]WebSearchConfig{
		"unknown":        {Provider: "bing"},
		"brave no key":   {Provider: "brave"},
		"searxng no url": {Provider: "searxng"},
		"bad url":        {Provider: "searxng", BaseURL: "file:///etc"},
	} {
		if _, err := newWebSearcher(cfg); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	s, err := newWebSearcher(WebSearchConfig{Provider: "Tavily"})
	if err != nil || s.cfg.APIKey != "from-env" || s.cfg.BaseURL != searchEndpoints["tavily"] {
		t.Errorf("env key / default endpoint: %+v %v", s, err)
	}

	// Registry: registered only when a provider is configured; bad config fails startup.
	cfg := config.DefaultConfig()
	cfg.Tools.WorkspaceDir = t.TempDir()
	cfg.Tools.ApprovalsFile = ""
	reg, _ := NewRegistry(cfg, nil, nil)
	if len(reg.GetToolsForAgent([]string{"web_search"})) != 0 {
		t.Error("web_search registered without a provider")
	}
	reg.Close()
	cfg.Web.SearchProvider, cfg.Web.SearchAPIKey = "brave", "k"
	reg, err = NewRegistry(cfg, nil, nil)
	if err != nil || len(reg.GetToolsForAgent([]string{"web_search"})) != 1 {
		t.Errorf("web_search not registered: %v", err)
	}
	reg.Close()
	cfg.Web.SearchAPIKey = ""
	if _, err := NewRegistry(cfg, nil, nil); err == nil || !strings.Contains(err.Error(), "BRAVE_API_KEY") {
		t.Errorf("missing key should fail startup clearly: %v", err)
	}
}
