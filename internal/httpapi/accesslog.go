package httpapi

import (
	"net/http"
	"time"

	oteltrace "go.opentelemetry.io/otel/trace"

	breengine "github.com/helmedeiros/bre-go/engine"

	"github.com/helmedeiros/funnel-sim/internal/jsonlog"
)

// WithAccessLog emits one funnel-sim.access event per handled request.
// Matches the markup-svc access-log shape (method, path, status,
// duration_ms) so Filebeat + Kibana see identical fields regardless of
// service.
func WithAccessLog(l *jsonlog.Logger, env string, next http.Handler) http.Handler {
	if l == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		ctx := r.Context()
		attrs := map[string]any{
			"method":      r.Method,
			"path":        r.URL.Path,
			"status":      sw.status,
			"duration_ms": float64(time.Since(start)) / float64(time.Millisecond),
		}
		if env != "" {
			attrs["env"] = env
		}
		if cid := breengine.CorrelationIDFromContext(ctx); cid != "" {
			attrs["correlation_id"] = cid
		}
		if sc := oteltrace.SpanContextFromContext(ctx); sc.IsValid() {
			attrs["trace_id"] = sc.TraceID().String()
			attrs["span_id"] = sc.SpanID().String()
		}
		l.Info("funnel-sim.access", attrs)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.wroteHeader {
		return
	}
	s.wroteHeader = true
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wroteHeader {
		s.wroteHeader = true
	}
	return s.ResponseWriter.Write(b)
}
