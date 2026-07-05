package httpapi

import (
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
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
