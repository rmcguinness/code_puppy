package tools

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/retail-cortex/code_puppy/internal/audit"
)

// ErrNoSearch is returned by Registry.WebSearch when no search provider is
// configured (web.search_provider), or it can't be used.
var ErrNoSearch = errors.New("web search is not set up")

// WebSearch runs a search the user asked for (/search web). It isn't an
// agent action, so it doesn't ask for approval; it is audited.
func (r *Registry) WebSearch(ctx context.Context, query string, n int) (WebSearchOutput, error) {
	switch {
	case r.searchErr != nil:
		return WebSearchOutput{}, fmt.Errorf("%w: %v", ErrNoSearch, r.searchErr)
	case r.searcher == nil:
		return WebSearchOutput{}, ErrNoSearch
	}
	r.hooks.Audit().Log(audit.Entry{Kind: audit.KindUserSearch, Tool: "web_search", Detail: r.searcher.cfg.Provider + ": " + query})
	out := r.searcher.run(ctx, query, n)
	if out.Error != "" {
		return out, errors.New(out.Error)
	}
	return out, nil
}

// SearchProvider names the configured search provider ("" if none).
func (r *Registry) SearchProvider() string {
	if r.searcher == nil {
		return ""
	}
	return r.searcher.cfg.Provider
}

// SearchError reports why the configured search provider can't be used.
func (r *Registry) SearchError() error { return r.searchErr }

// CanFetch reports whether web_fetch is available to agents.
func (r *Registry) CanFetch() bool { return r.fetch }

type fetchGrantsKey struct{}

// WithFetchGrants lets web_fetch read exactly these URLs without asking,
// for calls made with ctx (one turn). The user picked them (/search web);
// any other URL, including links found on these pages and redirects to
// another host, still needs approval.
func WithFetchGrants(ctx context.Context, urls []string) context.Context {
	set := make(map[string]bool, len(urls))
	for _, u := range urls {
		set[grantKey(u)] = true
	}
	return context.WithValue(ctx, fetchGrantsKey{}, set)
}

func fetchGranted(ctx context.Context, u *url.URL) bool {
	set, _ := ctx.Value(fetchGrantsKey{}).(map[string]bool)
	return set[grantKey(u.String())]
}

// grantKey compares URLs without their fragment, which isn't sent.
func grantKey(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.Fragment, u.RawFragment = "", ""
	return u.String()
}
