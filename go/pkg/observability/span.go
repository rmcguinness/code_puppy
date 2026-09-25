package observability

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "github.com/retail-cortex/code_puppy"

// ConversationID is the OpenTelemetry GenAI key for the session a span
// belongs to. The ADK sets it on its spans; ours use the same key so one
// query finds every turn of a session.
const ConversationID = attribute.Key("gen_ai.conversation.id")

// Start begins a span on the current global provider (a no-op with
// telemetry off). Attributes must not carry content (prompts, file text,
// arguments): use sizes, names and outcomes.
func Start(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return StartWith(ctx, name, trace.WithAttributes(attrs...))
}

// StartWith is Start with arbitrary span options, e.g. trace.WithLinks.
func StartWith(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	return otel.Tracer(tracerName).Start(ctx, name, opts...)
}

// Traceparent encodes a span's context in W3C Trace Context form, or ""
// when the span is not recording a real trace (telemetry off).
func Traceparent(span trace.Span) string {
	if !span.SpanContext().IsValid() {
		return ""
	}
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(trace.ContextWithSpan(context.Background(), span), carrier)
	return carrier.Get("traceparent")
}

// ParseTraceparent decodes a W3C traceparent into a remote span context.
func ParseTraceparent(tp string) (trace.SpanContext, bool) {
	if tp == "" {
		return trace.SpanContext{}, false
	}
	ctx := propagation.TraceContext{}.Extract(context.Background(), propagation.MapCarrier{"traceparent": tp})
	sc := trace.SpanContextFromContext(ctx)
	return sc, sc.IsValid()
}

// End records err (cancellation is not an error) and ends the span.
func End(span trace.Span, err error) {
	switch {
	case err == nil:
	case errors.Is(err, context.Canceled):
		span.SetAttributes(attribute.Bool("cancelled", true))
	default:
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}
