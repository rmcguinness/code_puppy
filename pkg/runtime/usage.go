package runtime

import (
	"strings"
	"sync"

	"github.com/retail-cortex/code_puppy/pkg/config"
	"google.golang.org/genai"
)

// Usage accumulates token counts and estimated cost.
type Usage struct {
	Calls      int
	Input      int64 // prompt tokens, including cached
	Cached     int64
	CacheWrite int64 // prompt tokens written to the cache (part of Input)
	Output     int64 // candidates + thinking tokens
	LastPrompt int64 // prompt size of the latest call: the current context size
	CostUSD    float64
	Priced     bool // false if any call's model had no price
}

func (u *Usage) add(o Usage) {
	u.Calls += o.Calls
	u.Input += o.Input
	u.Cached += o.Cached
	u.CacheWrite += o.CacheWrite
	u.Output += o.Output
	u.CostUSD += o.CostUSD
	u.Priced = u.Priced && o.Priced
	if o.LastPrompt > 0 {
		u.LastPrompt = o.LastPrompt
	}
}

// UsageTracker records usage per session.
type UsageTracker struct {
	mu       sync.Mutex
	pricing  map[string]config.ModelPrice
	sessions map[string]*Usage
}

// NewUsageTracker creates a tracker with the given price table.
func NewUsageTracker(pricing map[string]config.ModelPrice) *UsageTracker {
	return &UsageTracker{pricing: pricing, sessions: map[string]*Usage{}}
}

// price finds the price for model: exact, else the longest configured prefix
// ("gemini-3.8-flash-001" uses "gemini-3.8-flash").
func (t *UsageTracker) price(model string) (config.ModelPrice, bool) {
	if p, ok := t.pricing[model]; ok {
		return p, true
	}
	best, found := "", false
	for name := range t.pricing {
		if strings.HasPrefix(model, name) && len(name) > len(best) {
			best, found = name, true
		}
	}
	return t.pricing[best], found
}

// CacheWriteTokensKey is the LLMResponse.CustomMetadata key a model uses to
// report prompt tokens written to the cache (genai usage has no such field).
const CacheWriteTokensKey = "cache_creation_input_tokens"

// HasPrice reports whether a price is configured for model.
func (t *UsageTracker) HasPrice(model string) bool {
	_, ok := t.price(model)
	return ok
}

// Estimate converts token counts to USD for model. cacheWrites are prompt
// tokens (already counted in the prompt total) written to the cache.
func (t *UsageTracker) Estimate(model string, m *genai.GenerateContentResponseUsageMetadata, cacheWrites int64) Usage {
	u := Usage{Calls: 1, Priced: true}
	if m == nil {
		return u
	}
	u.Input = int64(m.PromptTokenCount) + int64(m.ToolUsePromptTokenCount)
	u.Cached = int64(m.CachedContentTokenCount)
	u.CacheWrite = min(max(cacheWrites, 0), max(u.Input-u.Cached, 0))
	u.Output = int64(m.CandidatesTokenCount) + int64(m.ThoughtsTokenCount)
	u.LastPrompt = u.Input
	p, ok := t.price(model)
	if !ok {
		u.Priced = false
		return u
	}
	writeRate := p.CacheWritePerMTok
	if writeRate == 0 {
		writeRate = p.InputPerMTok
	}
	uncached := max(u.Input-u.Cached-u.CacheWrite, 0)
	u.CostUSD = (float64(uncached)*p.InputPerMTok + float64(u.Cached)*p.CachedInputPerMTok +
		float64(u.CacheWrite)*writeRate + float64(u.Output)*p.OutputPerMTok) / 1e6
	return u
}

// Record adds one model call's usage to session.
func (t *UsageTracker) Record(session, model string, m *genai.GenerateContentResponseUsageMetadata) Usage {
	return t.RecordWrites(session, model, m, 0)
}

// RecordWrites is Record with a count of cache-write tokens.
func (t *UsageTracker) RecordWrites(session, model string, m *genai.GenerateContentResponseUsageMetadata, cacheWrites int64) Usage {
	u := t.Estimate(model, m, cacheWrites)
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.sessions[session]
	if s == nil {
		s = &Usage{Priced: true}
		t.sessions[session] = s
	}
	s.add(u)
	return u
}

// Session returns the usage recorded for session.
func (t *UsageTracker) Session(session string) Usage {
	t.mu.Lock()
	defer t.mu.Unlock()
	if s := t.sessions[session]; s != nil {
		return *s
	}
	return Usage{Priced: true}
}
