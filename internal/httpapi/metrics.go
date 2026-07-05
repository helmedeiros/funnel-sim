package httpapi

import (
	"fmt"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// WithMetrics counts every request in {method, path, status_class} and
// records duration in {method, path}. Skip is a set of paths whose
// self-scrapes should NOT be counted (typically "/metrics" itself).
func WithMetrics(requests *prometheus.CounterVec, duration *prometheus.HistogramVec, skip map[string]struct{}, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := skip[r.URL.Path]; ok {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		sw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		requests.WithLabelValues(r.Method, r.URL.Path, statusClass(sw.status)).Inc()
		duration.WithLabelValues(r.Method, r.URL.Path).Observe(time.Since(start).Seconds())
	})
}

func statusClass(code int) string {
	return fmt.Sprintf("%dxx", code/100)
}
