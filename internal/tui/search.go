package tui

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"strings"

	"github.com/retail-cortex/code_puppy/internal/i18n"
	"github.com/retail-cortex/code_puppy/internal/runtime"
	"github.com/retail-cortex/code_puppy/internal/session"
	"github.com/retail-cortex/code_puppy/internal/textutil"
	"github.com/retail-cortex/code_puppy/internal/tools"
)

const (
	// searchLinks is how many results /search web hands to the agent.
	searchLinks = 5
	// searchFetch is how many results are asked for, so there's room to
	// drop ones the agent couldn't read.
	searchFetch = 10
	// sessionPassages caps the transcript passages sent for /search session.
	sessionPassages = 12
)

// searchTurn is the agent turn a /search command leads to.
type searchTurn struct {
	ctx      context.Context // carries the fetch grants for /search web
	prompt   string          // what the agent is sent
	recorded string          // what the transcript records: the command
}

// prepareSearch handles "/search web|session <terms>": it runs the search,
// shows what was found, and returns the turn to run, or ok=false when
// there is nothing to hand to the agent (usage, errors, no results).
func prepareSearch(ctx context.Context, app *App, args string, interrupts <-chan os.Signal) (searchTurn, bool) {
	sub, terms, _ := strings.Cut(strings.TrimSpace(args), " ")
	terms = strings.TrimSpace(terms)
	if terms == "" || (sub != "web" && sub != "session") {
		fmt.Printf("%s%s%s\n", Yellow, i18n.T("search.usage"), Reset)
		return searchTurn{}, false
	}
	recorded := "/search " + sub + " " + terms
	if sub == "session" {
		return searchTurn{ctx: ctx, prompt: sessionSearch(app, terms), recorded: recorded}, true
	}

	if app.Tools == nil || !app.Tools.CanFetch() {
		fmt.Printf("%s%s%s\n", Yellow, i18n.T("search.no_fetch"), Reset)
		return searchTurn{}, false
	}
	provider := app.Tools.SearchProvider()
	fmt.Printf("%s🔎 %s%s\n", Dim, i18n.T("search.searching", "provider", safe(provider), "query", safe(terms)), Reset)
	sctx, stop := cancelOnSignal(ctx, interrupts)
	out, err := app.Tools.WebSearch(sctx, terms, searchFetch)
	stop()
	switch {
	case errors.Is(err, tools.ErrNoSearch):
		fmt.Printf("%s⚠️  %s%s\n", Yellow, i18n.T("search.not_setup", "error", safe(err.Error())), Reset)
		return searchTurn{}, false
	case err != nil:
		fmt.Printf("%s❌ %s%s\n", Red, i18n.T("search.failed", "error", safe(err.Error())), Reset)
		return searchTurn{}, false
	}
	links := viableLinks(out.Results, searchLinks)
	if len(links) == 0 {
		fmt.Printf("%s%s%s\n", Yellow, i18n.T("search.none", "query", safe(terms)), Reset)
		return searchTurn{}, false
	}
	urls := make([]string, len(links))
	for i, r := range links {
		urls[i] = r.URL
		fmt.Printf("  %s%d. %s%s\n     %s%s%s\n", Bold, i+1, safe(textutil.Ellipsize(r.Title, 100)), Reset, Dim, safe(r.URL), Reset)
	}
	fmt.Printf("%s%s%s\n", Dim, i18n.T("search.handoff"), Reset)
	return searchTurn{
		ctx:      tools.WithFetchGrants(ctx, urls),
		prompt:   runtime.WebSearchPrompt(terms, links, out.Answer),
		recorded: recorded,
	}, true
}

// unreadable are extensions web_fetch can't turn into text.
var unreadable = map[string]bool{
	".pdf": true, ".zip": true, ".gz": true, ".tgz": true, ".tar": true, ".exe": true, ".dmg": true, ".iso": true,
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".svg": true,
	".mp3": true, ".mp4": true, ".mov": true, ".doc": true, ".docx": true, ".xls": true, ".xlsx": true, ".ppt": true, ".pptx": true,
}

// viableLinks keeps up to n results the agent can read: http(s), not a
// binary or document download, not an unresolved Google redirect (web_fetch
// won't follow it to another host), and not a duplicate. Search already
// removed denied domains.
func viableLinks(results []tools.SearchResult, n int) []tools.SearchResult {
	seen := map[string]bool{}
	var out []tools.SearchResult
	for _, r := range results {
		u, err := url.Parse(r.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
			unreadable[strings.ToLower(path.Ext(u.Path))] || strings.HasPrefix(u.Path, "/grounding-api-redirect/") {
			continue
		}
		u.Fragment = ""
		if key := strings.ToLower(u.Host) + u.RequestURI(); !seen[key] {
			seen[key] = true
			out = append(out, r)
		}
		if len(out) == n {
			break
		}
	}
	return out
}

// sessionSearch looks for terms in the active session's transcript (which
// keeps what compaction removed from the model's context), reports what
// it found, and returns the prompt that asks the agent about it.
func sessionSearch(app *App, terms string) string {
	var msgs []session.Message
	if active := app.Storage.Active(); active != nil {
		msgs = active.Messages
	}
	matches, total := session.Search(msgs, terms, sessionPassages)
	if total == 0 {
		fmt.Printf("%s🔎 %s%s\n", Dim, i18n.T("search.session_none"), Reset)
	} else {
		fmt.Printf("%s🔎 %s%s\n", Dim, i18n.T("search.session_found", "passages", i18n.N("search.passages", total)), Reset)
	}
	return runtime.SessionSearchPrompt(terms, matches, total)
}
