package observability

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/retail-cortex/code_puppy/internal/config"
	"github.com/retail-cortex/code_puppy/internal/redact"
	"go.opentelemetry.io/otel/attribute"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

const secret = "sk-test-SECRET-123456"

func readLines(t *testing.T, dir string) []map[string]any {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(dir, logPrefix+"*"+logSuffix))
	if len(files) != 1 {
		t.Fatalf("want one log file, got %v", files)
	}
	f, err := os.Open(files[0])
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("bad line %q: %v", sc.Text(), err)
		}
		out = append(out, m)
	}
	return out
}

func TestLogFileMasksSecretsAndKeepsEveryRecord(t *testing.T) {
	dir := t.TempDir()
	logger, closer, err := OpenLog(config.LogConfig{Level: "debug", Dir: dir}, redact.New(secret))
	if err != nil {
		t.Fatal(err)
	}

	const writers, each = 8, 100 // 800 < queue size: nothing may be dropped
	var wg sync.WaitGroup
	for w := range writers {
		wg.Go(func() {
			for i := range each {
				logger.Info("call failed", "writer", w, "i", i, "error", errors.New("key "+secret+" rejected"))
			}
		})
	}
	wg.Wait()
	logger.Debug("token is " + secret)
	closer.Close()

	lines := readLines(t, dir)
	if len(lines) != writers*each+1 {
		t.Fatalf("want %d lines, got %d", writers*each+1, len(lines))
	}
	for _, l := range lines {
		if b, _ := json.Marshal(l); strings.Contains(string(b), secret) {
			t.Fatalf("secret written to log: %s", b)
		}
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("log dir mode = %v, %v", info.Mode().Perm(), err)
	}
}

func TestLogFileNeverBlocksAndReportsDrops(t *testing.T) {
	dir := t.TempDir()
	// A sink whose writer hasn't started: the queue fills and writes must
	// still return immediately.
	s := &fileSink{queue: make(chan []byte, 2), done: make(chan struct{}), dir: dir, now: time.Now}
	start := time.Now()
	for i := range 10 {
		s.Write(fmt.Appendf(nil, `{"n":%d}`+"\n", i))
	}
	if time.Since(start) > time.Second {
		t.Fatal("Write blocked on a full queue")
	}
	go s.run()
	s.Close()

	lines := readLines(t, dir)
	if len(lines) != 3 {
		t.Fatalf("want 2 records and a drop notice, got %v", lines)
	}
	if lines[2]["count"] != float64(8) {
		t.Fatalf("drop notice = %v", lines[2])
	}
	s.Write([]byte("after close\n")) // must not panic
}

func TestLogFilePrunesOldFiles(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, logPrefix+"2000-01-01"+logSuffix)
	other := filepath.Join(dir, "notes.txt")
	for _, p := range []string{old, other} {
		os.WriteFile(p, []byte("x\n"), 0o600)
	}
	logger, closer, err := OpenLog(config.LogConfig{Level: "info", Dir: dir, RetainDays: 7}, redact.New())
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("hello")
	closer.Close()
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("old log file not pruned")
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("unrelated file removed")
	}
}

func TestLogLevelOffWritesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	logger, closer, err := OpenLog(config.LogConfig{Level: "off", Dir: dir}, redact.New())
	if err != nil {
		t.Fatal(err)
	}
	logger.Error("x")
	closer.Close()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("log dir created with logging off")
	}
	if _, _, err := OpenLog(config.LogConfig{Level: "loud"}, redact.New()); err == nil {
		t.Fatal("unknown level accepted")
	}
}

func TestLogLinesCarryTraceIDs(t *testing.T) {
	dir := t.TempDir()
	logger, closer, _ := OpenLog(config.LogConfig{Level: "info", Dir: dir}, redact.New())
	tp := sdktrace.NewTracerProvider()
	ctx, span := tp.Tracer("t").Start(context.Background(), "op")
	logger.InfoContext(ctx, "inside")
	span.End()
	closer.Close()
	if l := readLines(t, dir)[0]; l["trace_id"] != span.SpanContext().TraceID().String() {
		t.Fatalf("trace_id missing: %v", l)
	}
}

func exportThrough(t *testing.T, capture bool, attrs ...attribute.KeyValue) []attribute.KeyValue {
	t.Helper()
	mem := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(&filterExporter{next: mem, capture: capture, r: redact.New(secret)}))
	_, span := tp.Tracer("t").Start(context.Background(), "execute_tool read_file")
	span.SetAttributes(attrs...)
	span.AddEvent("e", trace.WithAttributes(attrs...))
	span.End()
	spans := mem.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("want 1 span, got %d", len(spans))
	}
	if ev := spans[0].Events[0].Attributes; len(ev) != len(spans[0].Attributes) {
		t.Fatalf("event attributes filtered differently: %v vs %v", ev, spans[0].Attributes)
	}
	return spans[0].Attributes
}

func TestSpansDropContentUnlessCaptured(t *testing.T) {
	in := []attribute.KeyValue{
		attribute.String("gen_ai.tool.name", "read_file"),
		attribute.String("gcp.vertex.agent.tool_call_args", `{"path":".env"}`),
		attribute.String("gcp.vertex.agent.tool_response", "API_KEY="+secret),
		attribute.String("error.message", "bad key "+secret),
		attribute.StringSlice("list", []string{secret}),
	}

	got := map[attribute.Key]string{}
	for _, kv := range exportThrough(t, false, in...) {
		got[kv.Key] = kv.Value.Emit()
	}
	if _, ok := got["gcp.vertex.agent.tool_call_args"]; ok {
		t.Fatal("tool args exported without capture")
	}
	if _, ok := got["gcp.vertex.agent.tool_response"]; ok {
		t.Fatal("tool response exported without capture")
	}
	if got["gen_ai.tool.name"] != "read_file" {
		t.Fatalf("tool name lost: %v", got)
	}
	for k, v := range got {
		if strings.Contains(v, secret) {
			t.Fatalf("secret in %s: %s", k, v)
		}
	}

	got = map[attribute.Key]string{}
	for _, kv := range exportThrough(t, true, in...) {
		got[kv.Key] = kv.Value.Emit()
	}
	if got["gcp.vertex.agent.tool_call_args"] != `{"path":".env"}` {
		t.Fatalf("captured args missing: %v", got)
	}
	if v := got["gcp.vertex.agent.tool_response"]; v == "" || strings.Contains(v, secret) {
		t.Fatalf("captured response not masked: %q", v)
	}
}

type memLogExporter struct {
	mu   sync.Mutex
	recs []sdklog.Record
}

func (m *memLogExporter) Export(_ context.Context, recs []sdklog.Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range recs {
		m.recs = append(m.recs, r.Clone())
	}
	return nil
}
func (m *memLogExporter) Shutdown(context.Context) error   { return nil }
func (m *memLogExporter) ForceFlush(context.Context) error { return nil }

func TestTelemetryLogHandlerMasksSecrets(t *testing.T) {
	spans, logs := tracetest.NewInMemoryExporter(), &memLogExporter{}
	tel := NewTelemetry(config.TelemetryConfig{Enabled: true}, "test", redact.New(secret), spans, logs)
	logger, closer, err := OpenLog(config.LogConfig{Level: "off"}, redact.New(secret), tel.LogHandler())
	if err != nil {
		t.Fatal(err)
	}
	ctx, span := Start(context.Background(), "turn")
	logger.WarnContext(ctx, "auth failed with "+secret, "detail", "key="+secret)
	End(span, errors.New("401 for "+secret))
	closer.Close()
	tel.Flush(context.Background())
	got := spans.GetSpans() // read before Shutdown, which clears the exporter
	if err := tel.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	if len(logs.recs) != 1 {
		t.Fatalf("want 1 exported log record, got %d", len(logs.recs))
	}
	rec := logs.recs[0]
	text := rec.Body().Emit()
	rec.WalkAttributes(func(kv attribute.KeyValue) bool { text += " " + kv.Value.Emit(); return true })
	if strings.Contains(text, secret) || !strings.Contains(text, "auth failed") {
		t.Fatalf("log record not masked: %s", text)
	}
	if rec.TraceID() != span.SpanContext().TraceID() {
		t.Fatal("log record not linked to the span")
	}

	if len(got) != 1 || strings.Contains(got[0].Status.Description, secret) {
		t.Fatalf("span status not masked: %+v", got)
	}
}

func TestTelemetryOffIsNil(t *testing.T) {
	tel, err := StartTelemetry(context.Background(), config.TelemetryConfig{}, "v", redact.New())
	if tel != nil || err != nil {
		t.Fatalf("got %v, %v", tel, err)
	}
	if tel.LogHandler() != nil || tel.Shutdown(context.Background()) != nil {
		t.Fatal("nil telemetry must be a no-op")
	}
}

// StartTelemetry's real OTLP/HTTP exporters must post to <endpoint>/v1/traces
// and /v1/logs, and Shutdown must flush what is pending.
func TestStartTelemetryExportsOverOTLPHTTP(t *testing.T) {
	var mu sync.Mutex
	paths := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths[r.URL.Path]++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	defer srv.Close()

	tel, err := StartTelemetry(context.Background(), config.TelemetryConfig{Enabled: true, Endpoint: srv.URL + "/"}, "test", redact.New())
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(tel.LogHandler())
	ctx, span := Start(context.Background(), "turn")
	logger.InfoContext(ctx, "hello")
	End(span, nil)
	if err := tel.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if paths["/v1/traces"] == 0 || paths["/v1/logs"] == 0 {
		t.Fatalf("collector received %v", paths)
	}
}

func TestEndpointDefaultsToPlainHTTP(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	if got := endpointFor("", "TRACES"); got != "http://localhost:4318" {
		t.Fatalf("default = %q", got)
	}
	if got := endpointFor("https://otel.example.com/", "TRACES"); got != "https://otel.example.com" {
		t.Fatalf("configured = %q", got)
	}
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "https://x/v1/traces")
	if got := endpointFor("", "TRACES"); got != "" {
		t.Fatalf("env endpoint must be left to the exporter, got %q", got)
	}
}
