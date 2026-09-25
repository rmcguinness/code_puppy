package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/retail-cortex/code_puppy/pkg/textutil"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

const (
	searchDefaultResults = 5
	searchMaxResults     = 20
	searchSnippetChars   = 400
	searchResponseLimit  = 2 << 20
)

// WebSearchConfig configures web_search.
type WebSearchConfig struct {
	Provider     string // brave | tavily | searxng
	APIKey       string // brave/tavily; falls back to BRAVE_API_KEY / TAVILY_API_KEY
	BaseURL      string // searxng instance, or an override for brave/tavily
	MaxResults   int
	DenyDomains  []string
	AllowNetwork bool
	Timeout      time.Duration
}

// SearchResult is one web search hit.
type SearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
}

var searchEndpoints = map[string]string{
	"brave":  "https://api.search.brave.com/res/v1/web/search",
	"tavily": "https://api.tavily.com/search",
}

type webSearcher struct {
	cfg    WebSearchConfig
	client *http.Client
}

// NewWebSearchTool creates web_search for a configured provider.
func NewWebSearchTool(cfg WebSearchConfig, hooks *Hooks) (tool.Tool, error) {
	s, err := newWebSearcher(cfg)
	if err != nil {
		return nil, err
	}
	return functiontool.New(
		functiontool.Config{
			Name:        "web_search",
			Description: "Search the web and return result titles, URLs and snippets; use web_fetch to read a result",
		},
		func(ctx agent.Context, input WebSearchInput) (WebSearchOutput, error) {
			return s.search(ctx, hooks, input), nil
		},
	)
}

func newWebSearcher(cfg WebSearchConfig) (*webSearcher, error) {
	cfg.Provider = strings.ToLower(strings.TrimSpace(cfg.Provider))
	switch cfg.Provider {
	case "brave", "tavily":
		if cfg.BaseURL == "" {
			cfg.BaseURL = searchEndpoints[cfg.Provider]
		}
		if cfg.APIKey == "" {
			cfg.APIKey = os.Getenv(strings.ToUpper(cfg.Provider) + "_API_KEY")
		}
		if cfg.APIKey == "" {
			return nil, fmt.Errorf("web.search_provider %q needs web.search_api_key or %s_API_KEY", cfg.Provider, strings.ToUpper(cfg.Provider))
		}
	case "searxng":
		if cfg.BaseURL == "" {
			return nil, errors.New(`web.search_provider "searxng" needs web.search_url (your instance, e.g. http://localhost:8888)`)
		}
		cfg.BaseURL = strings.TrimSuffix(cfg.BaseURL, "/") + "/search"
	default:
		return nil, fmt.Errorf("unknown web.search_provider %q (use brave, tavily, or searxng)", cfg.Provider)
	}
	if u, err := url.Parse(cfg.BaseURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("invalid search endpoint %q", cfg.BaseURL)
	}
	if cfg.MaxResults <= 0 {
		cfg.MaxResults = searchDefaultResults
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 20 * time.Second
	}
	return &webSearcher{cfg: cfg, client: &http.Client{Timeout: cfg.Timeout}}, nil
}

// WebSearchInput defines arguments for web_search.
type WebSearchInput struct {
	Query      string `json:"query" jsonschema:"The search query"`
	MaxResults int    `json:"max_results,omitempty" jsonschema:"Number of results (default from config, max 20)"`
}

// WebSearchOutput holds search results.
type WebSearchOutput struct {
	Query   string         `json:"query"`
	Results []SearchResult `json:"results"`
	Error   string         `json:"error,omitempty"`
}

func (s *webSearcher) search(ctx context.Context, hooks *Hooks, in WebSearchInput) WebSearchOutput {
	out := WebSearchOutput{Query: in.Query, Results: []SearchResult{}}
	fail := func(err error) WebSearchOutput { out.Error = err.Error(); return out }

	q := strings.TrimSpace(in.Query)
	if q == "" {
		return fail(errors.New("query must not be empty"))
	}
	if !s.cfg.AllowNetwork {
		return fail(errors.New("network access is disabled (sandbox.allow_network = false)"))
	}
	n := in.MaxResults
	if n <= 0 {
		n = s.cfg.MaxResults
	}
	n = min(n, searchMaxResults)

	// The query itself leaves the machine, so it is approved like a request.
	if err := hooks.Approve(ctx, ApprovalRequest{
		Tool: "web_search", Kind: ActionNetwork,
		Detail: fmt.Sprintf("Search %s for: %s", s.cfg.Provider, q),
		Key:    "search:" + s.cfg.Provider, KeyLabel: "searches via " + s.cfg.Provider,
	}); err != nil {
		return fail(err)
	}

	var results []SearchResult
	var err error
	switch s.cfg.Provider {
	case "brave":
		results, err = s.brave(ctx, q, n)
	case "tavily":
		results, err = s.tavily(ctx, q, n)
	case "searxng":
		results, err = s.searxng(ctx, q)
	}
	if err != nil {
		return fail(err)
	}
	for _, r := range results {
		u, perr := url.Parse(r.URL)
		if perr != nil || (u.Scheme != "http" && u.Scheme != "https") || matchDomain(s.cfg.DenyDomains, u.Hostname()) {
			continue
		}
		r.Title = textutil.Ellipsize(strings.TrimSpace(r.Title), 200)
		r.Snippet = textutil.Ellipsize(htmlToText(r.Snippet), searchSnippetChars)
		out.Results = append(out.Results, r)
		if len(out.Results) == n {
			break
		}
	}
	return out
}

func (s *webSearcher) do(req *http.Request, into any) error {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "code-puppy/2 (+web_search)")
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("%s search: %w", s.cfg.Provider, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, searchResponseLimit))
	if err != nil {
		return err
	}
	switch {
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		return fmt.Errorf("%s search: authentication failed (HTTP %d); check the API key", s.cfg.Provider, resp.StatusCode)
	case resp.StatusCode == 429:
		return fmt.Errorf("%s search: rate limited (HTTP 429)", s.cfg.Provider)
	case resp.StatusCode >= 300:
		return fmt.Errorf("%s search: HTTP %d: %s", s.cfg.Provider, resp.StatusCode, textutil.Ellipsize(strings.TrimSpace(string(body)), 200))
	}
	if err := json.Unmarshal(body, into); err != nil {
		return fmt.Errorf("%s search: unexpected response: %w", s.cfg.Provider, err)
	}
	return nil
}

func (s *webSearcher) brave(ctx context.Context, q string, n int) ([]SearchResult, error) {
	u := s.cfg.BaseURL + "?" + url.Values{"q": {q}, "count": {fmt.Sprint(n)}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Subscription-Token", s.cfg.APIKey)
	var body struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := s.do(req, &body); err != nil {
		return nil, err
	}
	var out []SearchResult
	for _, r := range body.Web.Results {
		out = append(out, SearchResult{Title: r.Title, URL: r.URL, Snippet: r.Description})
	}
	return out, nil
}

func (s *webSearcher) tavily(ctx context.Context, q string, n int) ([]SearchResult, error) {
	payload, _ := json.Marshal(map[string]any{"query": q, "max_results": n})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.BaseURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.cfg.APIKey)
	var body struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := s.do(req, &body); err != nil {
		return nil, err
	}
	var out []SearchResult
	for _, r := range body.Results {
		out = append(out, SearchResult{Title: r.Title, URL: r.URL, Snippet: r.Content})
	}
	return out, nil
}

func (s *webSearcher) searxng(ctx context.Context, q string) ([]SearchResult, error) {
	u := s.cfg.BaseURL + "?" + url.Values{"q": {q}, "format": {"json"}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	var body struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := s.do(req, &body); err != nil {
		return nil, err
	}
	var out []SearchResult
	for _, r := range body.Results {
		out = append(out, SearchResult{Title: r.Title, URL: r.URL, Snippet: r.Content})
	}
	return out, nil
}
