// Package funnelevents defines the funnel-event contract emitted by
// funnel-sim: search.v1 and booking.v1. Sink is the port every
// substrate adapter implements; the s3sink subpackage provides the
// MinIO / S3 adapter that ships in v0.1.0. See funnel-sim ADR-0002
// (search.v1 contract) and ADR-0003 (booking.v1 contract).
package funnelevents

// SchemaV1 pins the initial schema version. Additive-minor bumps stay
// on this constant; a breaking major bump would land as
// funnelevents.SchemaV2 alongside a rename of the event_type prefix.
const SchemaV1 = "1.0.0"

// SearchEvent mirrors the search.v1 JSON payload. Field order matches
// the contract in ADR-0002 so a downstream consumer eyeballing the raw
// JSONL sees the identity block first.
type SearchEvent struct {
	SchemaVersion string       `json:"schema_version"`
	EventType     string       `json:"event_type"` // always "search.viewed" in v1
	EventID       string       `json:"event_id"`
	Ts            string       `json:"ts"`
	Env           string       `json:"env"`

	JourneyID string `json:"journey_id"`
	SearchID  string `json:"search_id"`

	Query  SearchQuery   `json:"query"`
	Offers []SearchOffer `json:"offers"`

	CorrelationID string `json:"correlation_id,omitempty"`
	TraceID       string `json:"trace_id,omitempty"`
	SpanID        string `json:"span_id,omitempty"`
}

type SearchQuery struct {
	Route         string `json:"route"`
	Departure     string `json:"departure"`
	Passengers    int    `json:"passengers"`
	Channel       string `json:"channel"`
	Device        string `json:"device"`
	Country       string `json:"country"`
	CustomerTier  string `json:"customer_tier"`
}

type SearchOffer struct {
	OfferID      string  `json:"offer_id"`
	DecisionID   string  `json:"decision_id"`
	Position     int     `json:"position"`
	Price        float64 `json:"price"`
	MarkupFactor float64 `json:"markup_factor"`
	// Experiment and Variant emit explicitly (empty string when the
	// underlying decision had no arm identity) so downstream schema
	// consumers see a stable column set — per ADR-0002 §Field additivity.
	Experiment string `json:"experiment"`
	Variant    string `json:"variant"`
}

// Partition returns the Hive partition segment for search events. All
// search events land in one partition since the event_type is fixed.
func (e SearchEvent) Partition() string { return "" }

// BookingEvent mirrors the booking.v1 JSON payload. One schema, four
// event_types (booking.initiated, booking.reserved, booking.purchased,
// booking.timeout). See ADR-0003.
type BookingEvent struct {
	SchemaVersion string `json:"schema_version"`
	EventType     string `json:"event_type"`
	EventID       string `json:"event_id"`
	Ts            string `json:"ts"`
	Env           string `json:"env"`

	JourneyID  string `json:"journey_id"`
	SearchID   string `json:"search_id,omitempty"`
	DecisionID string `json:"decision_id"`
	OfferID    string `json:"offer_id,omitempty"`
	BookingID  string `json:"booking_id"`
	OrderID    string `json:"order_id,omitempty"`

	Price        float64 `json:"price"`
	MarkupFactor float64 `json:"markup_factor"`
	Revenue      float64 `json:"revenue,omitempty"` // only on booking.purchased
	Currency     string  `json:"currency"`

	// Experiment and Variant emit on every event so per-arm analytics
	// does not need to re-join to the decision event (ADR-0003).
	Experiment string `json:"experiment"`
	Variant    string `json:"variant"`

	// Phase + TimeoutKind are populated on booking.timeout only.
	Phase       string `json:"phase,omitempty"`
	TimeoutKind string `json:"timeout_kind,omitempty"`

	CorrelationID string `json:"correlation_id,omitempty"`
	TraceID       string `json:"trace_id,omitempty"`
	SpanID        string `json:"span_id,omitempty"`
}

// Partition returns the Hive partition segment carrying event_type so
// downstream readers hitting "event_type=booking.purchased/" only
// scan the purchased slice. See ADR-0003.
func (e BookingEvent) Partition() string { return "event_type=" + e.EventType }

// Event is the constraint every publishable funnel event satisfies.
type Event interface {
	SearchEvent | BookingEvent
	Partition() string
}

// Sink is the port every substrate implements. NoopSink is the default
// so wire-off deployments incur zero per-Publish cost past the interface
// dispatch.
type Sink[T Event] interface {
	Publish(e T)
}

// NoopSink discards every event. Wired by default so a service without
// substrate configuration keeps working end-to-end.
type NoopSink[T Event] struct{}

func (NoopSink[T]) Publish(_ T) {}

// Metrics is the observability port the s3sink adapter reports to.
// prom.FunnelSinkMetrics implements it against a Prometheus registry.
type Metrics interface {
	IncDropped(reason string, n int)
	IncFlushed(events, bytes int)
	IncObject()
}

// NoopMetrics discards every counter tick. Wired when --metrics-enabled
// is off so the s3sink adapter can call unconditionally.
type NoopMetrics struct{}

func (NoopMetrics) IncDropped(string, int) {}
func (NoopMetrics) IncFlushed(int, int)    {}
func (NoopMetrics) IncObject()             {}

// Logger is the structured-log port the s3sink adapter reports to for
// the flush_failed / buffer_full events. jsonlog.Logger satisfies it.
type Logger interface {
	Info(msg string, attrs map[string]any)
	Warn(msg string, attrs map[string]any)
}
