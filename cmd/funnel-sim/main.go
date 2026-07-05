// Package main boots funnel-sim: five HTTP endpoints, an in-memory
// booking store, two typed s3sinks, and the full observability
// (Prometheus, OTel, structured JSON logs, health probes).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/helmedeiros/funnel-sim/internal/booking"
	"github.com/helmedeiros/funnel-sim/internal/funnelevents"
	"github.com/helmedeiros/funnel-sim/internal/funnelevents/s3sink"
	"github.com/helmedeiros/funnel-sim/internal/httpapi"
	"github.com/helmedeiros/funnel-sim/internal/jsonlog"
	"github.com/helmedeiros/funnel-sim/internal/observability/metrics/prom"
	obsotel "github.com/helmedeiros/funnel-sim/internal/observability/otel"
	"github.com/helmedeiros/funnel-sim/internal/search"
)

func main() {
	listen := flag.String("listen", ":8081", "HTTP listen address")
	env := flag.String("env", "default", "environment identifier stamped on logs, metrics, spans, and events")
	markupURL := flag.String("markup-url", "http://markup-svc:8080/decide", "markup-svc /decide URL")

	otelEnabled := flag.Bool("otel-enabled", false, "bootstrap OpenTelemetry SDK and export via OTLP gRPC")
	metricsEnabled := flag.Bool("metrics-enabled", false, "expose Prometheus /metrics endpoint")

	sinkMode := flag.String("event-sink", "", "funnel-event substrate: '' (noop) or 's3'")
	sinkEndpoint := flag.String("event-sink-endpoint", "minio:9000", "S3 endpoint host:port")
	sinkBucket := flag.String("event-sink-bucket", "funnel-events", "S3 bucket for funnel events")
	sinkRegion := flag.String("event-sink-region", "us-east-1", "S3 region")
	sinkAccessKey := flag.String("event-sink-access-key", os.Getenv("AWS_ACCESS_KEY_ID"), "S3 access key")
	sinkSecretKey := flag.String("event-sink-secret-key", os.Getenv("AWS_SECRET_ACCESS_KEY"), "S3 secret key")
	sinkUseSSL := flag.Bool("event-sink-use-ssl", false, "use HTTPS to the S3 endpoint")
	sinkAutoCreate := flag.Bool("event-sink-bucket-auto-create", false, "create the bucket if missing")
	sinkBatchEvery := flag.Duration("event-sink-batch-every", 30*time.Second, "flush interval for batched writes")
	flag.Parse()

	log := jsonlog.New(os.Stdout)

	// Metrics wiring (or a noop set when disabled).
	var (
		httpMetricsWrap    func(http.Handler) http.Handler
		metricsHandler     http.Handler
		searchSinkMetrics  funnelevents.Metrics = funnelevents.NoopMetrics{}
		bookingSinkMetrics funnelevents.Metrics = funnelevents.NoopMetrics{}
		bookingsGauge      *prom.Bookings
	)
	if *metricsEnabled {
		httpM, searchM, bookingM, bookingsG, handler := prom.New(*env)
		metricsHandler = handler
		searchSinkMetrics = searchM
		bookingSinkMetrics = bookingM
		bookingsGauge = bookingsG
		skip := map[string]struct{}{"/metrics": {}, "/healthz": {}, "/readyz": {}}
		httpMetricsWrap = func(next http.Handler) http.Handler {
			return httpapi.WithMetrics(httpM.Requests, httpM.Duration, skip, next)
		}
	} else {
		httpMetricsWrap = func(next http.Handler) http.Handler { return next }
	}

	// OTel bootstrap. Named tracer is the binary path so Jaeger's
	// Services list shows funnel-sim as a first-class service.
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	var tracer oteltrace.Tracer
	if *otelEnabled {
		t, shutdown, err := obsotel.Bootstrap(ctx, "github.com/helmedeiros/funnel-sim")
		if err != nil {
			log.Error("otel.bootstrap_failed", map[string]any{"error": err.Error()})
			os.Exit(1)
		}
		tracer = t
		defer func() {
			shutdownCtx, sc := context.WithTimeout(context.Background(), 5*time.Second)
			defer sc()
			_ = shutdown(shutdownCtx)
		}()
	}
	_ = tracer // reserved for a per-handler span decorator; currently the OTel middleware alone suffices

	// Booking store.
	store := booking.New()

	// Sinks: two typed s3sinks or noop defaults.
	instance, _ := os.Hostname()
	if instance == "" {
		instance = "funnel-sim-unknown"
	}
	var (
		searchSink  funnelevents.Sink[funnelevents.SearchEvent]  = funnelevents.NoopSink[funnelevents.SearchEvent]{}
		bookingSink funnelevents.Sink[funnelevents.BookingEvent] = funnelevents.NoopSink[funnelevents.BookingEvent]{}
	)
	switch strings.ToLower(*sinkMode) {
	case "", "noop":
		// keep noop defaults
	case "s3":
		searchCfg := s3sink.Config{
			Endpoint: *sinkEndpoint, Region: *sinkRegion, Bucket: *sinkBucket,
			AccessKey: *sinkAccessKey, SecretKey: *sinkSecretKey, UseSSL: *sinkUseSSL,
			AutoCreate: *sinkAutoCreate, KeyPrefix: "search-v1/",
			Env: *env, Instance: instance, BatchEvery: *sinkBatchEvery, Logger: log,
		}
		ss, err := s3sink.New[funnelevents.SearchEvent](ctx, searchCfg, searchSinkMetrics)
		if err != nil {
			log.Error("search_sink.init_failed", map[string]any{"error": err.Error()})
			os.Exit(1)
		}
		ss.Start(ctx)
		searchSink = ss

		bookingCfg := searchCfg
		bookingCfg.KeyPrefix = "booking-v1/"
		bs, err := s3sink.New[funnelevents.BookingEvent](ctx, bookingCfg, bookingSinkMetrics)
		if err != nil {
			log.Error("booking_sink.init_failed", map[string]any{"error": err.Error()})
			os.Exit(1)
		}
		bs.Start(ctx)
		bookingSink = bs
	default:
		log.Error("event-sink.unknown_mode", map[string]any{"mode": *sinkMode})
		os.Exit(2)
	}

	// Aggregator.
	decideClient := &search.HTTPDecideClient{URL: *markupURL, HTTPClient: &http.Client{Timeout: 2 * time.Second}}
	agg := search.NewAggregator(decideClient, searchSink, *env)

	// Handler deps.
	deps := httpapi.Deps{
		Env: *env, Store: store, Searcher: agg, Bookings: bookingSink,
	}

	// Mux.
	mux := http.NewServeMux()
	mux.Handle("/search", httpapi.Search(deps))
	mux.Handle("/booking/init", httpapi.BookingInit(deps))
	mux.Handle("/booking/reserve", httpapi.BookingReserve(deps))
	mux.Handle("/booking/purchase", httpapi.BookingPurchase(deps))
	mux.Handle("/booking/timeout", httpapi.BookingTimeout(deps))
	mux.Handle("/healthz", httpapi.Healthz())
	// Readiness closure — once the sinks initialised and the listener
	// bound, we call this. On the noop path we're always ready.
	ready := true
	mux.Handle("/readyz", httpapi.Readyz(func() (string, bool) {
		if !ready {
			return "starting", false
		}
		return "", true
	}))
	if metricsHandler != nil {
		mux.Handle("/metrics", metricsHandler)
	}

	// Middleware chain: outermost first. WithMetrics is INSIDE
	// WithAccessLog so a 500 emitted by WithAccessLog itself would not
	// be double-counted, and OUTSIDE the handlers so the histogram sees
	// the full handler wall-clock.
	handler := httpapi.WithCorrelationID(
		httpapi.WithTraceContext(
			httpapi.WithServerSpan(
				httpapi.WithAccessLog(log, *env,
					httpMetricsWrap(mux),
				),
			),
		),
	)

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Error("listen_failed", map[string]any{"addr": *listen, "error": err.Error()})
		os.Exit(1)
	}
	log.Info("funnel-sim.boot", map[string]any{
		"listen":     *listen,
		"env":        *env,
		"markup_url": *markupURL,
		"sink":       *sinkMode,
		"otel":       *otelEnabled,
		"metrics":    *metricsEnabled,
	})

	// Bookings gauge poll — runs every 5s, cheap.
	if bookingsGauge != nil {
		go func() {
			t := time.NewTicker(5 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					bookingsGauge.Gauge.Set(float64(store.Len()))
				}
			}
		}()
	}

	server := &http.Server{Handler: handler, ReadHeaderTimeout: 3 * time.Second}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(ln) }()

	select {
	case <-ctx.Done():
		log.Info("funnel-sim.shutdown_signal", nil)
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("funnel-sim.serve_error", map[string]any{"error": err.Error()})
		}
	}

	shutdownCtx, sc := context.WithTimeout(context.Background(), 10*time.Second)
	defer sc()
	_ = server.Shutdown(shutdownCtx)
	log.Info("funnel-sim.stopped", nil)
	fmt.Fprintln(os.Stderr, "funnel-sim stopped cleanly")
}
