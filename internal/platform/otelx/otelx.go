// Package otelx sets up OpenTelemetry tracing for a service: W3C trace
// context propagation and an OTLP/gRPC exporter to Jaeger.
package otelx

import (
	"context"
	"fmt"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// EndpointEnv is the standard variable with the collector address, for
// example "http://jaeger:4317" (the http scheme means no TLS).
const EndpointEnv = "OTEL_EXPORTER_OTLP_ENDPOINT"

// Shutdown flushes buffered spans and stops the exporter.
type Shutdown func(context.Context) error

// Init installs the global propagator and, when EndpointEnv is set, a tracer
// provider that exports spans of the named service. Without the variable
// tracing context is still propagated, but nothing is exported.
func Init(ctx context.Context, service string) (Shutdown, error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))

	if os.Getenv(EndpointEnv) == "" {
		return func(context.Context) error { return nil }, nil
	}
	exp, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("create otlp exporter: %w", err)
	}
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		attribute.String("service.name", service),
		attribute.String("service.namespace", "constella-platform"),
	))
	if err != nil {
		return nil, fmt.Errorf("build otel resource: %w", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}
