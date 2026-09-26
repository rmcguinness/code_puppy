package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	core "github.com/retail-cortex/code_puppy/internal/app"
	"github.com/retail-cortex/code_puppy/internal/i18n"
	"github.com/retail-cortex/code_puppy/internal/textutil"
)

// searchTurn is the agent turn a /search command leads to.
type searchTurn struct {
	prompt   string   // what the agent is sent
	recorded string   // what the transcript records: the command
	grants   []string // URLs the agent may fetch without asking (/search web)
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
		return searchTurn{prompt: sessionSearch(app, terms), recorded: recorded}, true
	}

	provider, err := app.Workspace.SearchProvider()
	if err != nil {
		fmt.Printf("%s%s%s\n", Yellow, i18n.T("search.no_fetch"), Reset)
		return searchTurn{}, false
	}
	fmt.Printf("%s🔎 %s%s\n", Dim, i18n.T("search.searching", "provider", safe(provider), "query", safe(terms)), Reset)
	sctx, stop := cancelOnSignal(ctx, interrupts)
	res, err := app.Workspace.SearchWeb(sctx, terms)
	stop()
	switch {
	case errors.Is(err, core.ErrNoSearch):
		fmt.Printf("%s⚠️  %s%s\n", Yellow, i18n.T("search.not_setup", "error", safe(err.Error())), Reset)
		return searchTurn{}, false
	case err != nil:
		fmt.Printf("%s❌ %s%s\n", Red, i18n.T("search.failed", "error", safe(err.Error())), Reset)
		return searchTurn{}, false
	}
	if len(res.Links) == 0 {
		fmt.Printf("%s%s%s\n", Yellow, i18n.T("search.none", "query", safe(terms)), Reset)
		return searchTurn{}, false
	}
	for i, l := range res.Links {
		fmt.Printf("  %s%d. %s%s\n     %s%s%s\n", Bold, i+1, safe(textutil.Ellipsize(l.Title, 100)), Reset, Dim, safe(l.URL), Reset)
	}
	fmt.Printf("%s%s%s\n", Dim, i18n.T("search.handoff"), Reset)
	return searchTurn{prompt: res.Prompt, recorded: recorded, grants: res.URLs()}, true
}

// sessionSearch looks for terms in the active session's transcript (which
// keeps what compaction removed from the model's context), reports what
// it found, and returns the prompt that asks the agent about it.
func sessionSearch(app *App, terms string) string {
	total, prompt := app.Workspace.SearchSession(terms)
	if total == 0 {
		fmt.Printf("%s🔎 %s%s\n", Dim, i18n.T("search.session_none"), Reset)
	} else {
		fmt.Printf("%s🔎 %s%s\n", Dim, i18n.T("search.session_found", "passages", i18n.N("search.passages", total)), Reset)
	}
	return prompt
}
