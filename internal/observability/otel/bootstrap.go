// Package otel bootstraps the funnel-sim OTel SDK. Wires the OTLP gRPC
// exporter, a batch span processor, and the composite W3C traceparent +
// baggage propagator so incoming spans from decision-gateway /
// traffic-gen become the parent of funnel-sim's spans.
package otel

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

type Shutdown func(ctx context.Context) error

// Bootstrap initialises an OTel TracerProvider with an OTLP gRPC
// exporter. Reads OTEL_EXPORTER_OTLP_ENDPOINT / OTEL_SERVICE_NAME /
// OTEL_RESOURCE_ATTRIBUTES from env. When cmd does not call Bootstrap
// (--otel-enabled off), the global TracerProvider stays the SDK
// no-op and span operations are zero-alloc.
func Bootstrap(ctx context.Context, instrumentationName string) (trace.Tracer, Shutdown, error) {
	exp, err := otlptrace.New(ctx, otlptracegrpc.NewClient())
	if err != nil {
		return nil, nil, fmt.Errorf("otlptrace gRPC exporter: %w", err)
	}
	res, err := resource.New(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("resource detection: %w", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	return tp.Tracer(instrumentationName), tp.Shutdown, nil
}
