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
	Output     int64 // candidates + thinking tokens
	LastPrompt int64 // prompt size of the latest call: the current context size
	CostUSD    float64
	Priced     bool // false if any call's model had no price
}

func (u *Usage) add(o Usage) {
	u.Calls += o.Calls
	u.Input += o.Input
	u.Cached += o.Cached
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
// ("gemini-2.5-flash-001" uses "gemini-2.5-flash").
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

// Estimate converts token counts to USD for model.
func (t *UsageTracker) Estimate(model string, m *genai.GenerateContentResponseUsageMetadata) Usage {
	u := Usage{Calls: 1, Priced: true}
	if m == nil {
		return u
	}
	u.Input = int64(m.PromptTokenCount) + int64(m.ToolUsePromptTokenCount)
	u.Cached = int64(m.CachedContentTokenCount)
	u.Output = int64(m.CandidatesTokenCount) + int64(m.ThoughtsTokenCount)
	u.LastPrompt = u.Input
	p, ok := t.price(model)
	if !ok {
		u.Priced = false
		return u
	}
	uncached := max(u.Input-u.Cached, 0)
	u.CostUSD = (float64(uncached)*p.InputPerMTok + float64(u.Cached)*p.CachedInputPerMTok + float64(u.Output)*p.OutputPerMTok) / 1e6
	return u
}

// Record adds one model call's usage to session.
func (t *UsageTracker) Record(session, model string, m *genai.GenerateContentResponseUsageMetadata) Usage {
	u := t.Estimate(model, m)
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
