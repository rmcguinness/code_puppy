package app

import (
	"context"
	"errors"
	"net/url"
	"path"
	"strings"

	"github.com/retail-cortex/code_puppy/internal/config"
	"github.com/retail-cortex/code_puppy/internal/i18n"
	"github.com/retail-cortex/code_puppy/internal/images"
	"github.com/retail-cortex/code_puppy/internal/memory"
	"github.com/retail-cortex/code_puppy/internal/runtime"
	"github.com/retail-cortex/code_puppy/internal/session"
	"github.com/retail-cortex/code_puppy/internal/tools"
)

// What the model sees: usage and context size, compaction, project memory,
// the reply language, images, and searches whose results go to the agent.

// Usage is the token usage and estimated cost of a session.
type Usage = runtime.Usage

func (w *Workspace) activeID() (string, error) {
	r := w.storage.Active()
	if r == nil {
		return "", ErrNoActiveSession
	}
	return r.ID, nil
}

// SessionUsage returns the active session's usage.
func (w *Workspace) SessionUsage() (Usage, error) {
	id, err := w.activeID()
	if err != nil {
		return Usage{}, err
	}
	return w.engine.Usage(id), nil
}

// ContextInfo is the size of the model's context and when it is compacted.
type ContextInfo struct {
	Tokens int64 // the latest prompt's size
	// AutoCompact: the context is compacted when it passes Threshold
	// tokens, keeping the last Keep events.
	AutoCompact bool
	Threshold   int
	Keep        int
}

// Context returns the active session's context size and compaction setting.
func (w *Workspace) Context() (ContextInfo, error) {
	u, err := w.SessionUsage()
	if err != nil {
		return ContextInfo{}, err
	}
	c := w.cfg.Context
	return ContextInfo{Tokens: u.LastPrompt, AutoCompact: c.Compaction && c.TokenThreshold > 0, Threshold: c.TokenThreshold, Keep: c.RetainEvents}, nil
}

// ErrNothingToCompact reports a session too short to compact.
var ErrNothingToCompact = runtime.ErrNothingToCompact

// CompactResult is what Compact did.
type CompactResult struct {
	EventsCompacted int
	SummaryChars    int
	Before, After   Usage
}

// Compact summarises the active session's older turns, keeping the latest,
// to shrink the model's context. focus steers what the summary keeps.
func (w *Workspace) Compact(ctx context.Context, focus string) (CompactResult, error) {
	id, err := w.activeID()
	if err != nil {
		return CompactResult{}, err
	}
	before := w.engine.Usage(id)
	res, err := w.engine.Compact(ctx, id, focus, 1)
	if err != nil {
		return CompactResult{}, err
	}
	return CompactResult{EventsCompacted: res.EventsCompacted, SummaryChars: res.SummaryChars, Before: before, After: w.engine.Usage(id)}, nil
}

// MemoryFiles are the project instruction file names looked for, in order.
func (w *Workspace) MemoryFiles() []string { return w.cfg.Memory.Files }

// ReloadMemory re-reads project instruction files into the engine and
// returns their paths.
func (w *Workspace) ReloadMemory(ctx context.Context) ([]string, error) {
	w.memory = memory.Load(w.Dir(), w.cfg.Memory)
	if err := w.engine.SetInstructions(ctx, w.instructions()); err != nil {
		return nil, err
	}
	paths := make([]string, len(w.memory))
	for i, d := range w.memory {
		paths[i] = d.Path
	}
	return paths, nil
}

// AddMemory appends text to the last project instruction file (PUPPY.md
// when none is configured), reloads memory, and returns the file's path.
func (w *Workspace) AddMemory(ctx context.Context, text string) (string, error) {
	file := "PUPPY.md"
	if files := w.cfg.Memory.Files; len(files) > 0 {
		file = files[len(files)-1]
	}
	p, err := memory.Append(w.Dir(), file, text)
	if err != nil {
		return "", err
	}
	_, err = w.ReloadMemory(ctx)
	return p, err
}

// LocaleInfo names an interface language with a catalog.
type LocaleInfo struct{ Tag, Name string }

// AvailableLocales returns the interface languages with a catalog, and
// the directory custom catalogs are read from ("" if none is configured).
func (w *Workspace) AvailableLocales() (list []LocaleInfo, customDir string) {
	for _, m := range w.locales.Available() {
		list = append(list, LocaleInfo{Tag: m.Locale, Name: m.Name})
	}
	return list, w.cfg.UI.LocalesDir
}

// ErrUnknownLocale reports input that isn't a language.
var ErrUnknownLocale = errors.New("not a language")

// LocaleChange describes the interface language after SetLocale.
type LocaleChange struct {
	Tag          string
	NativeName   string // e.g. "Español"
	LanguageName string // in the new language's terms, e.g. "español"
	// HasCatalog: the interface is translated; otherwise only the model's
	// replies are in the language.
	HasCatalog bool
	Saved      Saved
}

// SetLocale switches the interface language (per process) and the language
// the model replies in, and saves it as ui.locale in the config file.
// input is a language tag or name.
func (w *Workspace) SetLocale(ctx context.Context, input string) (LocaleChange, error) {
	tag, err := w.locales.Resolve(input)
	if err != nil {
		return LocaleChange{}, ErrUnknownLocale
	}
	l := w.locales.Localizer(tag)
	i18n.SetCurrent(l)
	out := LocaleChange{Tag: tag.String(), NativeName: l.NativeName(), LanguageName: l.LanguageName(), HasCatalog: l.HasCatalog()}
	if err := w.engine.SetInstructions(ctx, w.instructions()); err != nil {
		out.Saved.Err = err
		return out, nil
	}
	w.cfg.UI.Locale = out.Tag
	out.Saved.Path, out.Saved.Err = config.SaveUILocale(config.ConfigDir(""), w.cfg.UI.Locale)
	return out, nil
}

// SandboxSummary describes the sandbox commands run in, one line each.
func (w *Workspace) SandboxSummary() []string { return w.tools.SandboxSummary() }

// ErrImagesDisabled reports that image support is turned off.
var ErrImagesDisabled = tools.ErrImagesDisabled

// LoadImage reads an image from the workspace for a prompt.
func (w *Workspace) LoadImage(path string) (*images.Image, error) { return w.tools.LoadImage(path) }

// AddImage stores image data (e.g. pasted from the clipboard) for a prompt.
func (w *Workspace) AddImage(name string, data []byte) (*images.Image, error) {
	if w.tools.Images() == nil {
		return nil, ErrImagesDisabled
	}
	return w.tools.AddImage(name, data)
}

// ErrNoFetch reports that the agent can't fetch web pages, so a web search
// would give it nothing to read.
var ErrNoFetch = errors.New("web fetching is disabled")

// ErrNoSearch reports that no search provider is set up.
var ErrNoSearch = tools.ErrNoSearch

// Search limits: how many results go to the agent, how many are asked for
// (room to drop ones it couldn't read), and how many transcript passages a
// session search sends.
const (
	searchLinks     = 5
	searchFetch     = 10
	sessionPassages = 12
)

// SearchProvider names the web search provider, or ErrNoFetch.
func (w *Workspace) SearchProvider() (string, error) {
	if !w.tools.CanFetch() {
		return "", ErrNoFetch
	}
	return w.tools.SearchProvider(), nil
}

// Link is a web search result the agent is asked to read.
type Link struct{ Title, URL string }

// WebSearch is a search whose results are handed to the agent: run Prompt
// as a turn with FetchGrants set to the links' URLs, so the agent may read
// exactly those pages without asking.
type WebSearch struct {
	Links  []Link // none: nothing worth handing over
	Prompt string
}

// URLs returns the links' URLs, for Turn.FetchGrants.
func (s WebSearch) URLs() []string {
	out := make([]string, len(s.Links))
	for i, l := range s.Links {
		out[i] = l.URL
	}
	return out
}

// SearchWeb searches the web for terms and prepares the agent's prompt from
// the results it can read.
func (w *Workspace) SearchWeb(ctx context.Context, terms string) (WebSearch, error) {
	if !w.tools.CanFetch() {
		return WebSearch{}, ErrNoFetch
	}
	out, err := w.tools.WebSearch(ctx, terms, searchFetch)
	if err != nil {
		return WebSearch{}, err
	}
	links := viableLinks(out.Results, searchLinks)
	if len(links) == 0 {
		return WebSearch{}, nil
	}
	res := WebSearch{Prompt: runtime.WebSearchPrompt(terms, links, out.Answer)}
	for _, r := range links {
		res.Links = append(res.Links, Link{Title: r.Title, URL: r.URL})
	}
	return res, nil
}

// SearchSession looks for terms in the active session's transcript (which
// keeps what compaction removed from the model's context) and returns how
// many passages matched and the prompt that asks the agent about them.
func (w *Workspace) SearchSession(terms string) (found int, prompt string) {
	var msgs []session.Message
	if r := w.storage.Active(); r != nil {
		msgs = r.Messages
	}
	matches, total := session.Search(msgs, terms, sessionPassages)
	return total, runtime.SessionSearchPrompt(terms, matches, total)
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
