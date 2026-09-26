package tools

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/retail-cortex/code_puppy/internal/textutil"
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
	Provider     string // brave | tavily | searxng | google
	APIKey       string // brave/tavily/google; falls back to BRAVE_API_KEY / TAVILY_API_KEY / GEMINI_API_KEY
	BaseURL      string // searxng instance, or an override for the others
	Model        string // google: the Gemini model that runs the search (default gemini-3.8-flash)
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
	"google": "https://generativelanguage.googleapis.com/v1beta",
}

const googleSearchModel = "gemini-3.8-flash"

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
	return newWebSearchTool(s, hooks)
}

func newWebSearchTool(s *webSearcher, hooks *Hooks) (tool.Tool, error) {
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
	case "google":
		if cfg.BaseURL == "" {
			cfg.BaseURL = searchEndpoints["google"]
		}
		cfg.APIKey = cmp.Or(cfg.APIKey, os.Getenv("GEMINI_API_KEY"), os.Getenv("GOOGLE_API_KEY"))
		if cfg.APIKey == "" {
			return nil, errors.New(`web.search_provider "google" needs a Gemini API key: web.search_api_key, [llm.gemini] api_key, or GEMINI_API_KEY`)
		}
		if cfg.Model == "" {
			cfg.Model = googleSearchModel
		}
	case "searxng":
		if cfg.BaseURL == "" {
			return nil, errors.New(`web.search_provider "searxng" needs web.search_url (your instance, e.g. http://localhost:8888)`)
		}
		cfg.BaseURL = strings.TrimSuffix(cfg.BaseURL, "/") + "/search"
	default:
		return nil, fmt.Errorf("unknown web.search_provider %q (use brave, tavily, searxng, or google)", cfg.Provider)
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
	// Answer is the provider's own summary with the results (google only).
	Answer string `json:"answer,omitempty"`
	Error  string `json:"error,omitempty"`
}

func (s *webSearcher) search(ctx context.Context, hooks *Hooks, in WebSearchInput) WebSearchOutput {
	q := strings.TrimSpace(in.Query)
	if q == "" || !s.cfg.AllowNetwork {
		return s.run(ctx, q, in.MaxResults) // reports the problem
	}
	// The query itself leaves the machine, so it is approved like a request.
	if err := hooks.Approve(ctx, ApprovalRequest{
		Tool: "web_search", Kind: ActionNetwork,
		Detail: fmt.Sprintf("Search %s for: %s", s.cfg.Provider, q),
		Key:    "search:" + s.cfg.Provider, KeyLabel: "searches via " + s.cfg.Provider,
	}); err != nil {
		return WebSearchOutput{Query: in.Query, Results: []SearchResult{}, Error: err.Error()}
	}
	return s.run(ctx, q, in.MaxResults)
}

// run searches without asking: for the agent after approval, or for the
// user's own /search.
func (s *webSearcher) run(ctx context.Context, q string, n int) WebSearchOutput {
	out := WebSearchOutput{Query: q, Results: []SearchResult{}}
	fail := func(err error) WebSearchOutput { out.Error = err.Error(); return out }
	if q == "" {
		return fail(errors.New("query must not be empty"))
	}
	if !s.cfg.AllowNetwork {
		return fail(errors.New("network access is disabled (sandbox.allow_network = false)"))
	}
	if n <= 0 {
		n = s.cfg.MaxResults
	}
	n = min(n, searchMaxResults)

	var results []SearchResult
	var err error
	switch s.cfg.Provider {
	case "brave":
		results, err = s.brave(ctx, q, n)
	case "tavily":
		results, err = s.tavily(ctx, q, n)
	case "searxng":
		results, err = s.searxng(ctx, q)
	case "google":
		results, out.Answer, err = s.google(ctx, q)
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
	if s.cfg.Provider == "google" {
		req.Header.Set("x-goog-api-key", s.cfg.APIKey)
	}
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

// google searches with Gemini's grounding with Google Search: the model
// runs the queries and answers, and the grounding metadata lists the pages
// it used. Those links are Google redirects, which web_fetch won't follow
// to another host, so they are resolved to the pages they point to.
func (s *webSearcher) google(ctx context.Context, q string) ([]SearchResult, string, error) {
	payload, _ := json.Marshal(map[string]any{
		"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": q}}}},
		"tools":    []any{map[string]any{"google_search": map[string]any{}}},
	})
	u := strings.TrimSuffix(s.cfg.BaseURL, "/") + "/models/" + url.PathEscape(s.cfg.Model) + ":generateContent"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(payload))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	var body struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text    string `json:"text"`
					Thought bool   `json:"thought"`
				} `json:"parts"`
			} `json:"content"`
			GroundingMetadata struct {
				GroundingChunks []struct {
					Web struct {
						URI   string `json:"uri"`
						Title string `json:"title"`
					} `json:"web"`
				} `json:"groundingChunks"`
				GroundingSupports []struct {
					Segment struct {
						Text string `json:"text"`
					} `json:"segment"`
					GroundingChunkIndices []int `json:"groundingChunkIndices"`
				} `json:"groundingSupports"`
			} `json:"groundingMetadata"`
		} `json:"candidates"`
	}
	if err := s.do(req, &body); err != nil {
		return nil, "", err
	}
	if len(body.Candidates) == 0 {
		return nil, "", errors.New("google search: no answer")
	}
	c := body.Candidates[0]
	var answer strings.Builder
	for _, p := range c.Content.Parts {
		if !p.Thought {
			answer.WriteString(p.Text)
		}
	}
	gm := c.GroundingMetadata
	// A chunk's snippet is the answer text it supports.
	snippets := make([][]string, len(gm.GroundingChunks))
	for _, sup := range gm.GroundingSupports {
		for _, i := range sup.GroundingChunkIndices {
			if i >= 0 && i < len(snippets) && sup.Segment.Text != "" {
				snippets[i] = append(snippets[i], sup.Segment.Text)
			}
		}
	}
	out := make([]SearchResult, 0, len(gm.GroundingChunks))
	for i, ch := range gm.GroundingChunks {
		if ch.Web.URI == "" {
			continue
		}
		out = append(out, SearchResult{Title: ch.Web.Title, URL: ch.Web.URI, Snippet: strings.Join(snippets[i], " … ")})
	}
	s.resolveRedirects(ctx, out)
	return out, textutil.Ellipsize(strings.TrimSpace(answer.String()), 2000), nil
}

// resolveRedirects replaces Google grounding redirect links with their
// targets, in parallel. A link that can't be resolved is kept as it is.
func (s *webSearcher) resolveRedirects(ctx context.Context, results []SearchResult) {
	client := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	var wg sync.WaitGroup
	for i := range results {
		u, err := url.Parse(results[i].URL)
		if err != nil || !strings.HasPrefix(u.Path, "/grounding-api-redirect/") {
			continue
		}
		wg.Add(1)
		go func(r *SearchResult) {
			defer wg.Done()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.URL, nil)
			if err != nil {
				return
			}
			resp, err := client.Do(req)
			if err != nil {
				return
			}
			resp.Body.Close()
			if loc, err := resp.Location(); err == nil && resp.StatusCode >= 300 && resp.StatusCode < 400 {
				r.URL = loc.String()
			}
		}(&results[i])
	}
	wg.Wait()
}
