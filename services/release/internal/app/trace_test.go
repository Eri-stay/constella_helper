package app

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// The worker of an event-mode release must continue the trace of the request
// that created it, and a failed step must be visible as an Error span with
// the rollback below it.
//
// It sets the global tracer provider, so it does not run in parallel.
func TestEDAWorkerContinuesRequestTraceAndMarksErrors(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	prevTP, prevProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTracerProvider(prevTP); otel.SetTextMapPropagator(prevProp) })

	r, fc := newEDARig(t)
	fc.markErr = errors.New("catalog refused the mark")
	ctx, root := otel.Tracer("test").Start(context.Background(), "POST /v1/releases")
	rel, err := r.svc.RequestEDA(ctx, "b1")
	if err != nil {
		t.Fatal(err)
	}
	root.End()

	// The worker has no request context: only the stored traceparent links it.
	if err := r.svc.Advance(context.Background(), rel.ID); err == nil {
		t.Fatal("the release should fail at mark_released")
	}

	want := root.SpanContext().TraceID()
	names := map[string]sdktrace.ReadOnlySpan{}
	for _, s := range rec.Ended() {
		names[s.Name()] = s
		if s.SpanContext().TraceID() != want {
			t.Errorf("span %q is in another trace", s.Name())
		}
	}
	for _, n := range []string{"release request", "release saga", "saga reserve", "saga switch", "saga mark_released",
		"saga compensate", "compensate restore manifest", "compensate cancel reservation"} {
		if names[n] == nil {
			t.Errorf("span %q is missing; have %v", n, keys(names))
		}
	}
	if s := names["saga mark_released"]; s != nil && s.Status().Code != codes.Error {
		t.Error("the failed step span must have status Error")
	}
	if s := names["release saga"]; s != nil && s.Status().Code != codes.Error {
		t.Error("the saga span must have status Error")
	}
}

func keys(m map[string]sdktrace.ReadOnlySpan) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
