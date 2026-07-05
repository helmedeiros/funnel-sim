package httpapi

import (
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// WithTraceContext extracts the W3C trace context so funnel-sim spans
// become children of the upstream caller (typically decision-gateway
// or traffic-gen). Safe to mount unconditionally — when OTel is off the
// global propagator is a no-op.
func WithTraceContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// WithServerSpan opens one SpanKindServer span per request, named after
// the request path (e.g. "funnel-sim POST /booking/init"), so funnel-sim
// shows up as its own node in the Jaeger waterfall. Placed INSIDE
// WithTraceContext so the extracted parent is honored, OUTSIDE the mux
// so the span covers every handler including probes.
func WithServerSpan(next http.Handler) http.Handler {
	tracer := otel.Tracer("github.com/helmedeiros/funnel-sim")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, span := tracer.Start(r.Context(), "funnel-sim "+r.Method+" "+r.URL.Path,
			trace.WithSpanKind(trace.SpanKindServer))
		defer span.End()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
