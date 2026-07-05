// Package prom wires funnel-sim's Prometheus surface: HTTP-layer
// counters + histogram, the funnel-event sink counters, and the
// bookings-in-flight gauge. Every counter carries {env} so multi-env
// deployments slice cleanly.
package prom

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/helmedeiros/funnel-sim/internal/funnelevents"
)

// HTTPMetrics counts requests + duration on every handled endpoint.
type HTTPMetrics struct {
	Requests *prometheus.CounterVec
	Duration *prometheus.HistogramVec
}

func newHTTPMetrics(reg prometheus.Registerer, env string) *HTTPMetrics {
	labels := prometheus.Labels{}
	if env != "" {
		labels["env"] = env
	}
	m := &HTTPMetrics{
		Requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name:        "funnel_sim_requests_total",
			Help:        "funnel-sim HTTP requests, keyed by method+path+status_class",
			ConstLabels: labels,
		}, []string{"method", "path", "status_class"}),
		Duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:        "funnel_sim_request_duration_seconds",
			Help:        "funnel-sim HTTP request duration in seconds",
			ConstLabels: labels,
			Buckets: []float64{
				0.0001, 0.00025, 0.0005, 0.001, 0.002, 0.005, 0.01, 0.025, 0.05,
				0.1, 0.25, 0.5, 1.0, 1.5, 2.0, 3.0, 5.0,
			},
		}, []string{"method", "path"}),
	}
	reg.MustRegister(m.Requests, m.Duration)
	return m
}

// SinkMetrics implements funnelevents.Metrics against a Prometheus
// registry. One instance per event-type sink so counters are labeled
// by {event_family: "search" | "booking"} and (dropped) reason.
type SinkMetrics struct {
	family  string
	env     string
	dropped *prometheus.CounterVec
	flushed prometheus.Counter
	bytes   prometheus.Counter
	objects prometheus.Counter
}

func newSinkMetrics(reg prometheus.Registerer, env, family string) *SinkMetrics {
	labels := prometheus.Labels{"event_family": family}
	if env != "" {
		labels["env"] = env
	}
	m := &SinkMetrics{
		family: family,
		env:    env,
		dropped: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name:        "funnel_event_sink_dropped_total",
			Help:        "funnel-events dropped by the sink, keyed by reason (buffer_full | serialize_failed | flush_failed)",
			ConstLabels: labels,
		}, []string{"reason"}),
		flushed: prometheus.NewCounter(prometheus.CounterOpts{
			Name:        "funnel_event_sink_flushed_total",
			Help:        "funnel-events successfully written to the substrate",
			ConstLabels: labels,
		}),
		bytes: prometheus.NewCounter(prometheus.CounterOpts{
			Name:        "funnel_event_sink_flushed_bytes_total",
			Help:        "funnel-event bytes written (post-gzip)",
			ConstLabels: labels,
		}),
		objects: prometheus.NewCounter(prometheus.CounterOpts{
			Name:        "funnel_event_sink_objects_total",
			Help:        "funnel-event object writes (one per successful PUT)",
			ConstLabels: labels,
		}),
	}
	reg.MustRegister(m.dropped, m.flushed, m.bytes, m.objects)
	return m
}

func (m *SinkMetrics) IncDropped(reason string, n int) {
	m.dropped.WithLabelValues(reason).Add(float64(n))
}

func (m *SinkMetrics) IncFlushed(events, bytes int) {
	m.flushed.Add(float64(events))
	m.bytes.Add(float64(bytes))
}

func (m *SinkMetrics) IncObject() { m.objects.Inc() }

// Bookings is the gauge of in-flight bookings tracked in the Store.
type Bookings struct {
	Gauge prometheus.Gauge
}

func newBookings(reg prometheus.Registerer, env string) *Bookings {
	labels := prometheus.Labels{}
	if env != "" {
		labels["env"] = env
	}
	b := &Bookings{
		Gauge: prometheus.NewGauge(prometheus.GaugeOpts{
			Name:        "funnel_sim_bookings_in_flight",
			Help:        "current number of bookings in the funnel-sim in-memory store",
			ConstLabels: labels,
		}),
	}
	reg.MustRegister(b.Gauge)
	return b
}

// New wires the full registry + handler and returns the typed metrics
// packs cmd hands to the middleware and the sinks. env may be "" to
// suppress the label everywhere.
func New(env string) (*HTTPMetrics, *SinkMetrics, *SinkMetrics, *Bookings, http.Handler) {
	reg := prometheus.NewRegistry()
	// Compile-time assertion that our SinkMetrics matches the sink port.
	var _ funnelevents.Metrics = (*SinkMetrics)(nil)
	http := newHTTPMetrics(reg, env)
	searchSink := newSinkMetrics(reg, env, "search")
	bookingSink := newSinkMetrics(reg, env, "booking")
	bookings := newBookings(reg, env)
	handler := promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
	return http, searchSink, bookingSink, bookings, handler
}
