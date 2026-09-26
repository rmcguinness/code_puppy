package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"math"

	"github.com/retail-cortex/code_puppy/internal/observability"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/platform"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/session/compaction"
	"google.golang.org/genai"
)

// asideMode names read-only side questions in tool refusals.
const asideMode = "btw"

// Aside answers prompt with everything session sessionID knows, without
// adding to it (/btw). The session's events are copied into a throwaway
// in-memory session that the question runs in; it is discarded
// afterwards, so neither the question nor the answer reaches the history,
// the saved event log or later prompts. The turn is read-only, and its
// tokens count towards sessionID's usage because they were spent there.
func (e *Engine) Aside(ctx context.Context, sessionID, prompt string, handler EventHandler) error {
	e.mu.RLock()
	root, cc, streaming := e.rootAgent, e.compactionCfg, e.streaming
	e.mu.RUnlock()
	if root == nil {
		return fmt.Errorf("runner is not initialized")
	}
	ctx, span := observability.Start(ctx, "btw", observability.ConversationID.String(sessionID))
	var err error
	defer func() { observability.End(span, err) }()

	scratch := session.InMemoryService()
	asideID := fmt.Sprintf("btw-%d", e.subagentSeq.Add(1))
	if err = copySession(ctx, e.sessions, scratch, sessionID, asideID); err != nil {
		return err
	}
	// Keep honouring compaction summaries in the copy, but never compact it:
	// that would cost a summary nobody keeps.
	var c *compaction.Config
	if cc != nil {
		cp := *cc
		cp.TokenThreshold = math.MaxInt32
		c = &cp
	}
	r, err := runner.New(runner.Config{
		AppName: appName, Agent: root, SessionService: scratch,
		ArtifactService: e.artifacts, MemoryService: e.memories, Compaction: c,
	})
	if err != nil {
		return err
	}

	st := &runState{sessionID: sessionID, planOnly: true, mode: asideMode}
	ctx = context.WithValue(ctx, runStateKey{}, st)
	ctx = withSettingsLookup(ctx, e.lookupSettings)
	if n := e.cfg.Tools.MaxParallel; n > 0 {
		ctx = platform.WithTaskRunner(ctx, boundedRunner(n))
	}
	rc := agent.RunConfig{}
	if streaming {
		rc.StreamingMode = agent.StreamingModeSSE
	}
	err = drain(r.Run(ctx, "user", asideID, genai.NewContentFromText(prompt, genai.RoleUser), rc), handler)
	return err
}

// copySession creates session to in dst holding copies of the events of
// session from in src (none if it doesn't exist yet). Events are copied
// through JSON, as the persistent store saves them, so the two sessions
// share nothing.
func copySession(ctx context.Context, src, dst session.Service, from, to string) error {
	created, err := dst.Create(ctx, &session.CreateRequest{AppName: appName, UserID: "user", SessionID: to})
	if err != nil {
		return err
	}
	got, err := src.Get(ctx, &session.GetRequest{AppName: appName, UserID: "user", SessionID: from})
	if err != nil {
		return nil // a session with no turns yet: start empty
	}
	for ev := range got.Session.Events().All() {
		b, err := json.Marshal(ev)
		if err != nil {
			return fmt.Errorf("copy event: %w", err)
		}
		var cp session.Event
		if err := json.Unmarshal(b, &cp); err != nil {
			return fmt.Errorf("copy event: %w", err)
		}
		if err := dst.AppendEvent(ctx, created.Session, &cp); err != nil {
			return fmt.Errorf("copy event: %w", err)
		}
	}
	return nil
}
