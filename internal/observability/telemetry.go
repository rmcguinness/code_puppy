package observability

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/retail-cortex/code_puppy/internal/config"
	"github.com/retail-cortex/code_puppy/internal/redact"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	logglobal "go.opentelemetry.io/otel/log/global"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// ServiceName identifies Code Puppy in exported telemetry.
const ServiceName = "code-puppy"

// captureContentEnv is read by the ADK to decide whether prompts and replies
// go into spans and log events.
const captureContentEnv = "OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT"

// shutdownTimeout bounds the final flush so an unreachable collector can't
// hold up exit.
const shutdownTimeout = 3 * time.Second

// contentKeys are span attributes that carry conversation or tool content.
// The ADK sets the tool ones on every execute_tool span regardless of its
// content-capture setting, so they are removed here unless capture is on.
var contentKeys = map[attribute.Key]bool{
	"gcp.vertex.agent.tool_call_args": true,
	"gcp.vertex.agent.tool_response":  true,
	"gcp.vertex.agent.llm_request":    true,
	"gcp.vertex.agent.llm_response":   true,
	"gen_ai.input.messages":           true,
	"gen_ai.output.messages":          true,
	"gen_ai.system_instructions":      true,
	"gen_ai.tool.call.arguments":      true,
	"gen_ai.tool.call.result":         true,
}

// Telemetry owns the OpenTelemetry providers. A nil *Telemetry is valid and
// does nothing.
type Telemetry struct {
	tp *sdktrace.TracerProvider
	lp *sdklog.LoggerProvider
}

// StartTelemetry installs global trace and log providers exporting over
// OTLP/HTTP when cfg.Enabled; otherwise it returns nil. Export happens on
// the SDK's batch goroutines, so instrumented code only enqueues.
func StartTelemetry(ctx context.Context, cfg config.TelemetryConfig, version string, r *redact.Redactor) (*Telemetry, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	// The ADK reads this once, on first use. Without capture, force it off so
	// a globally exported value can't turn content capture on behind the
	// user's back; with capture, default to spans and events.
	if !cfg.CaptureContent {
		os.Setenv(captureContentEnv, "false")
	} else if os.Getenv(captureContentEnv) == "" {
		os.Setenv(captureContentEnv, "SPAN_AND_EVENT")
	}

	var traceOpts []otlptracehttp.Option
	var logOpts []otlploghttp.Option
	if ep := endpointFor(cfg.Endpoint, "TRACES"); ep != "" {
		traceOpts = append(traceOpts, otlptracehttp.WithEndpointURL(ep+"/v1/traces"))
	}
	if ep := endpointFor(cfg.Endpoint, "LOGS"); ep != "" {
		logOpts = append(logOpts, otlploghttp.WithEndpointURL(ep+"/v1/logs"))
	}
	spanExp, err := otlptracehttp.New(ctx, traceOpts...)
	if err != nil {
		return nil, err
	}
	logExp, err := otlploghttp.New(ctx, logOpts...)
	if err != nil {
		return nil, err
	}
	return NewTelemetry(cfg, version, r, spanExp, logExp), nil
}

// DefaultEndpoint is the OTLP/HTTP collector address from the OpenTelemetry
// spec. The Go exporters would otherwise default to https, which local
// collectors don't serve.
const DefaultEndpoint = "http://localhost:4318"

// endpointFor returns the base URL to export a signal to, or "" to let the
// exporter apply OTEL_EXPORTER_OTLP_[<signal>_]ENDPOINT itself.
func endpointFor(configured, signal string) string {
	if configured != "" {
		return strings.TrimRight(configured, "/")
	}
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" || os.Getenv("OTEL_EXPORTER_OTLP_"+signal+"_ENDPOINT") != "" {
		return ""
	}
	return DefaultEndpoint
}

// NewTelemetry installs global providers exporting through the given
// exporters (content filtering and masking still apply). StartTelemetry uses
// it with OTLP/HTTP exporters; embedders and tests can pass their own.
// Install telemetry once per process: the ADK binds its tracer to the first
// global provider, so spans from later providers would miss the ADK's.
func NewTelemetry(cfg config.TelemetryConfig, version string, r *redact.Redactor, spanExp sdktrace.SpanExporter, logExp sdklog.Exporter) *Telemetry {
	res := resource.NewSchemaless(
		attribute.String("service.name", ServiceName),
		attribute.String("service.version", version),
	)
	t := &Telemetry{
		tp: sdktrace.NewTracerProvider(
			sdktrace.WithResource(res),
			sdktrace.WithBatcher(&filterExporter{next: spanExp, capture: cfg.CaptureContent, r: r}),
		),
		lp: sdklog.NewLoggerProvider(
			sdklog.WithResource(res),
			sdklog.WithProcessor(&redactProcessor{capture: cfg.CaptureContent, r: r}),
			sdklog.WithProcessor(sdklog.NewBatchProcessor(logExp)),
		),
	}
	otel.SetTracerProvider(t.tp)
	logglobal.SetLoggerProvider(t.lp)
	// Export failures (collector down) go to the log file, not the terminal.
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		slog.Debug("telemetry export failed", "error", err)
	}))
	return t
}

// LogHandler returns an slog handler that sends records to the collector,
// or nil when telemetry is off.
func (t *Telemetry) LogHandler() slog.Handler {
	if t == nil {
		return nil
	}
	return otelslog.NewHandler(ServiceName, otelslog.WithLoggerProvider(t.lp))
}

// Flush exports everything recorded so far.
func (t *Telemetry) Flush(ctx context.Context) error {
	if t == nil {
		return nil
	}
	return errors.Join(t.tp.ForceFlush(ctx), t.lp.ForceFlush(ctx))
}

// Shutdown flushes pending spans and logs, waiting at most a few seconds.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	if t == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, shutdownTimeout)
	defer cancel()
	return errors.Join(t.tp.Shutdown(ctx), t.lp.Shutdown(ctx))
}

// filterExporter removes (or, with capture on, masks secrets in) content
// attributes before spans leave the process. It runs on the batch
// processor's export goroutine, off the instrumented code path.
type filterExporter struct {
	next    sdktrace.SpanExporter
	capture bool
	r       *redact.Redactor
}

func (f *filterExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	out := make([]sdktrace.ReadOnlySpan, len(spans))
	for i, s := range spans {
		stub := tracetest.SpanStubFromReadOnlySpan(s)
		stub.Attributes = f.attrs(stub.Attributes)
		for j := range stub.Events {
			stub.Events[j].Attributes = f.attrs(stub.Events[j].Attributes)
		}
		stub.Status.Description = f.r.String(stub.Status.Description)
		out[i] = stub.Snapshot()
	}
	return f.next.ExportSpans(ctx, out)
}

func (f *filterExporter) Shutdown(ctx context.Context) error { return f.next.Shutdown(ctx) }

func (f *filterExporter) attrs(in []attribute.KeyValue) []attribute.KeyValue {
	out := in[:0:0]
	for _, kv := range in {
		if contentKeys[kv.Key] && !f.capture {
			continue
		}
		kv.Value = redactValue(f.r, kv.Value)
		out = append(out, kv)
	}
	return out
}

// redactProcessor masks secrets in log records and, without capture, drops
// the bodies of the ADK's GenAI content events. Registered before the batch
// processor, it edits each record in place on the emitting goroutine; the
// work is string scanning, no I/O.
type redactProcessor struct {
	capture bool
	r       *redact.Redactor
}

func (p *redactProcessor) OnEmit(_ context.Context, rec *sdklog.Record) error {
	if !p.capture && strings.HasPrefix(rec.EventName(), "gen_ai.") {
		rec.SetBody(attribute.Value{})
	} else {
		rec.SetBody(redactValue(p.r, rec.Body()))
	}
	var attrs []attribute.KeyValue
	rec.WalkAttributes(func(kv attribute.KeyValue) bool {
		if !contentKeys[kv.Key] || p.capture {
			attrs = append(attrs, attribute.KeyValue{Key: kv.Key, Value: redactValue(p.r, kv.Value)})
		}
		return true
	})
	rec.SetAttributes(attrs...)
	return nil
}

// redactValue masks secrets in string values, including inside slices and maps.
func redactValue(r *redact.Redactor, v attribute.Value) attribute.Value {
	switch v.Type() {
	case attribute.STRING:
		return attribute.StringValue(r.String(v.AsString()))
	case attribute.STRINGSLICE:
		in := v.AsStringSlice()
		out := make([]string, len(in))
		for i, s := range in {
			out[i] = r.String(s)
		}
		return attribute.StringSliceValue(out)
	case attribute.SLICE:
		in := v.AsSlice()
		out := make([]attribute.Value, len(in))
		for i, e := range in {
			out[i] = redactValue(r, e)
		}
		return attribute.SliceValue(out...)
	case attribute.MAP:
		in := v.AsMap()
		out := make([]attribute.KeyValue, len(in))
		for i, kv := range in {
			out[i] = attribute.KeyValue{Key: kv.Key, Value: redactValue(r, kv.Value)}
		}
		return attribute.MapValue(out...)
	}
	return v
}

func (p *redactProcessor) Enabled(context.Context, sdklog.EnabledParameters) bool { return true }
func (p *redactProcessor) Shutdown(context.Context) error                         { return nil }
func (p *redactProcessor) ForceFlush(context.Context) error                       { return nil }
