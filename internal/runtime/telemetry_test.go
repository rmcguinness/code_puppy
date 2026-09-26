package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/retail-cortex/code_puppy/internal/config"
	"github.com/retail-cortex/code_puppy/internal/observability"
	"github.com/retail-cortex/code_puppy/internal/redact"
	"github.com/retail-cortex/code_puppy/internal/session"
	"go.opentelemetry.io/otel/attribute"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/genai"
)

type discardLogs struct{}

func (discardLogs) Export(context.Context, []sdklog.Record) error { return nil }
func (discardLogs) Shutdown(context.Context) error                { return nil }
func (discardLogs) ForceFlush(context.Context) error              { return nil }

// testTelemetry is shared by every test in the process: the ADK binds its
// tracer to the first provider installed (see observability.NewTelemetry).
var testTelemetry = sync.OnceValues(func() (*observability.Telemetry, *tracetest.InMemoryExporter) {
	spans := tracetest.NewInMemoryExporter()
	return observability.NewTelemetry(config.TelemetryConfig{Enabled: true}, "test", redact.New(), spans, discardLogs{}), spans
})

// A real run through the ADK must produce one trace rooted at our turn span,
// with the ADK's model and tool spans nested inside, and no file content.
func TestTurnTraceNestsADKSpansWithoutContent(t *testing.T) {
	const content = "TOP-SECRET-FILE-CONTENT"
	f := newEngineWith(t, fixtureOpts{},
		toolCall("read_file", map[string]any{"path": "notes.txt"}),
		textContent("done"))
	f.llm.Usage = &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 100, CandidatesTokenCount: 10}
	if err := os.WriteFile(filepath.Join(f.cfg.Tools.WorkspaceDir, "notes.txt"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	tel, spans := testTelemetry()
	spans.Reset()

	got, err := functionResponses(t, f.eng, "s", "read my notes")
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := got["read_file"]["content"].(string); !strings.Contains(c, content) {
		t.Fatalf("tool did not run as expected: %v", got)
	}
	tel.Flush(context.Background()) // not Shutdown: it clears the in-memory exporter

	byName := map[string]tracetest.SpanStub{}
	parent := map[string]string{} // span ID -> parent span ID
	idName := map[string]string{}
	for _, s := range spans.GetSpans() {
		byName[s.Name] = s
		idName[s.SpanContext.SpanID().String()] = s.Name
		parent[s.SpanContext.SpanID().String()] = s.Parent.SpanID().String()
		for _, kv := range s.Attributes {
			if strings.Contains(kv.Value.Emit(), content) {
				t.Errorf("span %q attribute %s carries file content", s.Name, kv.Key)
			}
		}
	}
	t.Logf("spans: %v", idName)
	turn, ok := byName["turn"]
	if !ok {
		t.Fatalf("no turn span; got %v", idName)
	}
	tool, ok := byName["execute_tool read_file"]
	if !ok {
		t.Fatalf("no execute_tool span; got %v", idName)
	}
	if tool.SpanContext.TraceID() != turn.SpanContext.TraceID() {
		t.Fatal("tool span is in a different trace from the turn")
	}
	// Walk up from the tool span: it must reach the turn span.
	id, hops := tool.SpanContext.SpanID().String(), 0
	for id != turn.SpanContext.SpanID().String() {
		if id = parent[id]; id == "" || hops > 10 {
			t.Fatalf("tool span is not nested under the turn span")
		}
		hops++
	}
	for _, kv := range tool.Attributes {
		if kv.Key == "gcp.vertex.agent.tool_call_args" || kv.Key == "gcp.vertex.agent.tool_response" {
			t.Errorf("tool content attribute %s exported", kv.Key)
		}
	}
	calls := false
	for _, kv := range turn.Attributes {
		if kv.Key == "model_calls" && kv.Value.AsInt64() == 2 {
			calls = true
		}
	}
	if !calls {
		t.Errorf("turn span model_calls != 2: %v", turn.Attributes)
	}
}

func turnSpans(spans *tracetest.InMemoryExporter) []tracetest.SpanStub {
	var out []tracetest.SpanStub
	for _, s := range spans.GetSpans() {
		if s.Name == "turn" {
			out = append(out, s)
		}
	}
	return out
}

func attr(s tracetest.SpanStub, key string) attribute.Value {
	for _, kv := range s.Attributes {
		if string(kv.Key) == key {
			return kv.Value
		}
	}
	return attribute.Value{}
}

// Turns of a session are separate traces chained by links to the previous
// turn, and the chain continues after a resume in a new process.
func TestTurnsChainAcrossResume(t *testing.T) {
	tel, spans := testTelemetry()
	spans.Reset()
	dir := t.TempDir()

	store, _ := session.NewStorage(dir)
	rec, _ := store.CreateSession("", "chain", "code-puppy")
	f := newEngineWith(t, fixtureOpts{opts: []Option{WithTurnStore(store)}}, textContent("one"), textContent("two"))
	for _, p := range []string{"first", "second"} {
		if _, err := collect(t, f.eng, rec.ID, p); err != nil {
			t.Fatal(err)
		}
	}

	// A later process: new storage and engine, same session.
	store2, _ := session.NewStorage(dir)
	if _, err := store2.Load(rec.ID); err != nil {
		t.Fatal(err)
	}
	f2 := newEngineWith(t, fixtureOpts{opts: []Option{WithTurnStore(store2)}}, textContent("three"))
	if _, err := collect(t, f2.eng, rec.ID, "third"); err != nil {
		t.Fatal(err)
	}
	tel.Flush(context.Background())

	turns := turnSpans(spans)
	if len(turns) != 3 {
		t.Fatalf("want 3 turn spans, got %d", len(turns))
	}
	for i, s := range turns {
		if got := attr(s, "gen_ai.conversation.id").AsString(); got != rec.ID {
			t.Errorf("turn %d: conversation id %q", i+1, got)
		}
		if got := attr(s, "turn.index").AsInt64(); got != int64(i+1) {
			t.Errorf("turn %d: turn.index = %d", i+1, got)
		}
		if s.Parent.IsValid() {
			t.Errorf("turn %d is not a trace root", i+1)
		}
		if i == 0 {
			if len(s.Links) != 0 {
				t.Errorf("first turn has links: %v", s.Links)
			}
			continue
		}
		prev := turns[i-1].SpanContext
		if s.SpanContext.TraceID() == prev.TraceID() {
			t.Errorf("turn %d shares a trace with the previous turn", i+1)
		}
		if len(s.Links) != 1 || s.Links[0].SpanContext.SpanID() != prev.SpanID() || s.Links[0].SpanContext.TraceID() != prev.TraceID() {
			t.Errorf("turn %d does not link to turn %d: %+v", i+1, i, s.Links)
		}
	}
}

type countingStore struct{ sets int }

func (c *countingStore) LastTurn(string) (string, int) { return "", 0 }
func (c *countingStore) SetLastTurn(string, string, int) error {
	c.sets++
	return nil
}

// With telemetry off spans carry no trace context, so nothing is saved.
func TestTurnNotRecordedWithoutTrace(t *testing.T) {
	store := &countingStore{}
	e := &Engine{turns: store}
	e.recordTurn(context.Background(), "s", trace.SpanFromContext(context.Background()), 1)
	if store.sets != 0 {
		t.Fatal("turn recorded without a trace")
	}
}
