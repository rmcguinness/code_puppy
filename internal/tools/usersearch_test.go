package tools

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeGemini answers generateContent with grounding metadata whose links
// are redirects served by the same server, like Google's.
func fakeGemini(t *testing.T, got *captured) string {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/grounding-api-redirect/go"):
			http.Redirect(w, r, "https://go.dev/doc/effective_go#names", http.StatusFound)
		case strings.HasPrefix(r.URL.Path, "/grounding-api-redirect/ads"):
			http.Redirect(w, r, "https://ads.evil.test/x", http.StatusFound)
		case strings.HasPrefix(r.URL.Path, "/grounding-api-redirect/"):
			http.NotFound(w, r) // can't be resolved: kept as it is
		default:
			got.method, got.path, got.token = r.Method, r.URL.Path, r.Header.Get("x-goog-api-key")
			b, _ := io.ReadAll(r.Body)
			json.Unmarshal(b, &got.body)
			io.WriteString(w, strings.ReplaceAll(`{"candidates":[{"content":{"parts":[
				{"text":"thinking about it","thought":true},
				{"text":"Go names use MixedCaps."}]},
			  "groundingMetadata":{
				"webSearchQueries":["go naming conventions"],
				"groundingChunks":[
				  {"web":{"uri":"SRV/grounding-api-redirect/go1","title":"go.dev"}},
				  {"web":{"uri":"SRV/grounding-api-redirect/ads1","title":"ads"}},
				  {"web":{"uri":"SRV/grounding-api-redirect/missing","title":"missing.example"}}],
				"groundingSupports":[
				  {"segment":{"text":"Go names use MixedCaps."},"groundingChunkIndices":[0,2]},
				  {"segment":{"text":"Not underscores."},"groundingChunkIndices":[0]}]}}]}`, "SRV", srv.URL))
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestGoogleSearchUsesGroundingAndResolvesLinks(t *testing.T) {
	var got captured
	base := fakeGemini(t, &got)
	s := searcher(t, WebSearchConfig{Provider: "google", APIKey: "gem-key", BaseURL: base, DenyDomains: []string{"*.evil.test"}})
	out := s.search(context.Background(), allowAll(), WebSearchInput{Query: "go naming conventions"})
	if out.Error != "" {
		t.Fatal(out.Error)
	}
	if got.method != "POST" || got.path != "/models/gemini-3.8-flash:generateContent" || got.token != "gem-key" {
		t.Errorf("request %+v", got)
	}
	tools, _ := got.body["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["google_search"] == nil {
		t.Errorf("no google_search tool in %v", got.body)
	}
	if out.Answer != "Go names use MixedCaps." {
		t.Errorf("answer %q (thoughts must be left out)", out.Answer)
	}
	// The redirect is resolved, the denied target dropped, and the
	// unresolvable link kept.
	if len(out.Results) != 2 {
		t.Fatalf("results %+v", out.Results)
	}
	if r := out.Results[0]; r.URL != "https://go.dev/doc/effective_go#names" || r.Title != "go.dev" || r.Snippet != "Go names use MixedCaps. … Not underscores." {
		t.Errorf("first result %+v", r)
	}
	if r := out.Results[1]; !strings.HasSuffix(r.URL, "/grounding-api-redirect/missing") {
		t.Errorf("second result %+v", r)
	}
}

func TestGoogleSearchConfig(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	if _, err := newWebSearcher(WebSearchConfig{Provider: "google"}); err == nil || !strings.Contains(err.Error(), "Gemini API key") {
		t.Fatalf("missing key: %v", err)
	}
	t.Setenv("GEMINI_API_KEY", "from-env")
	s, err := newWebSearcher(WebSearchConfig{Provider: "Google", Model: "gemini-3.7-flash"})
	if err != nil || s.cfg.APIKey != "from-env" || s.cfg.Model != "gemini-3.7-flash" || !strings.HasPrefix(s.cfg.BaseURL, "https://generativelanguage.googleapis.com/") {
		t.Fatalf("%+v %v", s, err)
	}
}

// The user's own search runs without asking; with nothing configured it
// says so.
func TestRegistryWebSearch(t *testing.T) {
	r := &Registry{hooks: NewHooks(Policy{})} // no approver: an approval would fail
	if _, err := r.WebSearch(context.Background(), "x", 5); !errors.Is(err, ErrNoSearch) {
		t.Fatalf("unconfigured: %v", err)
	}
	var got captured
	r.searcher = searcher(t, WebSearchConfig{Provider: "searxng", BaseURL: searchServer(t, `{"results":[{"title":"S","url":"https://s.example/","content":"hit"}]}`, 200, &got)})
	out, err := r.WebSearch(context.Background(), "q", 5)
	if err != nil || len(out.Results) != 1 || r.SearchProvider() != "searxng" {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestFetchGrantsSkipApprovalForExactlyThoseURLs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("page " + r.URL.Path)) }))
	defer srv.Close()
	f := testFetcher(WebFetchConfig{AllowPrivate: true})
	ctx := WithFetchGrants(context.Background(), []string{srv.URL + "/picked#section"})

	h, reqs := approverHooks(false)
	if out := f.fetch(ctx, h, srv.URL+"/picked"); out.Content != "page /picked" || len(*reqs) != 0 {
		t.Fatalf("granted URL: %+v, %d prompts", out, len(*reqs))
	}
	// Same host, another page: still asks (and is refused here).
	if out := f.fetch(ctx, h, srv.URL+"/other"); !strings.Contains(out.Error, "not approved") || len(*reqs) != 1 {
		t.Fatalf("other URL: %+v, %d prompts", out, len(*reqs))
	}
	// Without the grant in the context, the picked URL asks too.
	if out := f.fetch(context.Background(), h, srv.URL+"/picked"); !strings.Contains(out.Error, "not approved") {
		t.Fatalf("no grant: %+v", out)
	}
}
