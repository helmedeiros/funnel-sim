// Package search fans out one /search HTTP call into N /decide calls
// against markup-svc and aggregates the priced offers back onto one
// search.v1 event. See ADR-0001 (funnel-sim role) and ADR-0002
// (search.v1 contract).
package search

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	"github.com/helmedeiros/funnel-sim/internal/funnelevents"
)

// DecideClient calls markup-svc's POST /decide once per offer. Real
// funnel-sim uses HTTPDecideClient; tests inject a stub.
type DecideClient interface {
	Decide(ctx context.Context, req DecideRequest) (DecideResponse, error)
}

// DecideRequest mirrors markup-svc's decideRequest JSON — the fields we
// populate for pricing a search-page offer.
type DecideRequest struct {
	ProductID    string  `json:"product_id"`
	Category     string  `json:"category"`
	CustomerTier string  `json:"customer_tier"`
	Channel      string  `json:"channel"`
	Country      string  `json:"country"`
	TimeWindow   string  `json:"time_window,omitempty"`
	Amount       float64 `json:"amount"`
	RequestContext DecideRequestContext `json:"request_context"`
}

type DecideRequestContext struct {
	JourneyID string `json:"journey_id,omitempty"`
}

// DecideResponse mirrors the ADR-0037 markup-svc response — the fields
// we care about for aggregating a priced offer.
type DecideResponse struct {
	DecisionID    string  `json:"decision_id"`
	MarkupFactor  float64 `json:"markup_factor"`
	Rule          string  `json:"rule"`
	ModelVersion  string  `json:"model_version"`
	Experiment    string  `json:"experiment,omitempty"`
	CorrelationID string  `json:"correlation_id"`
	EngineAdapter string  `json:"engine_adapter"`
}

// Aggregator wires the /decide fan-out and the search.v1 emission.
type Aggregator struct {
	client   DecideClient
	sink     funnelevents.Sink[funnelevents.SearchEvent]
	env      string
	nowFn    func() time.Time
	newIDFn  func() string
}

func NewAggregator(client DecideClient, sink funnelevents.Sink[funnelevents.SearchEvent], env string) *Aggregator {
	return &Aggregator{
		client:  client,
		sink:    sink,
		env:     env,
		nowFn:   time.Now,
		newIDFn: newHexID,
	}
}

// Query is the funnel-sim-owned view of what a /search call carries. It
// mirrors the customer-visible search parameters and adds a couple of
// synthetic knobs (BasePrice, OfferCount) that would come from an
// inventory service in production but are configured per-request today.
type Query struct {
	JourneyID    string
	Route        string
	Departure    string
	Passengers   int
	Channel      string
	Device       string
	Country      string
	CustomerTier string
	BasePrice    float64
	OfferCount   int
}

// Result is the aggregate returned to the caller after fan-out. Every
// offer's decision_id is available so the caller can associate a
// subsequent booking event with a specific priced offer.
type Result struct {
	SearchID string
	Offers   []funnelevents.SearchOffer
}

// Search fans out N /decide calls (one per synthetic offer), aggregates
// them into a Result, and emits one search.v1 event to the sink. Any
// per-offer /decide failure is logged (via the caller's context) but
// does not abort the search — the failing offer is skipped, remaining
// offers continue. Empty result if every offer fails.
func (a *Aggregator) Search(ctx context.Context, q Query, correlationID, traceID, spanID string) (Result, error) {
	if q.OfferCount <= 0 {
		q.OfferCount = 8
	}
	if q.BasePrice <= 0 {
		q.BasePrice = 49.99
	}
	searchID := a.newIDFn()
	offers := make([]funnelevents.SearchOffer, 0, q.OfferCount)

	for i := 0; i < q.OfferCount; i++ {
		productID := fmt.Sprintf("route-%s-%s-t%d", q.Route, q.Departure, i+1)
		// Deterministic per-position price gradient so an operator eyeballing
		// the emitted search.v1 sees a range of decisions, not the same price N times.
		price := q.BasePrice * (1.0 + 0.05*float64(i))
		dr := DecideRequest{
			ProductID:    productID,
			Category:     "rail",
			CustomerTier: q.CustomerTier,
			Channel:      q.Channel,
			Country:      q.Country,
			TimeWindow:   "off_peak",
			Amount:       price,
			RequestContext: DecideRequestContext{JourneyID: q.JourneyID},
		}
		resp, err := a.client.Decide(ctx, dr)
		if err != nil {
			continue
		}
		offers = append(offers, funnelevents.SearchOffer{
			OfferID:      productID,
			DecisionID:   resp.DecisionID,
			Position:     i + 1,
			Price:        price * resp.MarkupFactor,
			MarkupFactor: resp.MarkupFactor,
			Experiment:   resp.Experiment,
			// Variant is stamped as model_version so downstream analytics
			// can slice by the arm identity emitted on the decision event.
			// Post-ADR-0037 a caller-supplied variant flag can override.
			Variant: resp.ModelVersion,
		})
	}

	result := Result{SearchID: searchID, Offers: offers}

	// Emit search.v1 regardless of how many offers survived; a zero-offer
	// search is a real event (nothing to show the customer) and analytics
	// wants to see it.
	event := funnelevents.SearchEvent{
		SchemaVersion: funnelevents.SchemaV1,
		EventType:     "search.viewed",
		EventID:       a.newIDFn(),
		Ts:            a.nowFn().UTC().Format(time.RFC3339Nano),
		Env:           a.env,
		JourneyID:     q.JourneyID,
		SearchID:      searchID,
		Query: funnelevents.SearchQuery{
			Route:        q.Route,
			Departure:    q.Departure,
			Passengers:   q.Passengers,
			Channel:      q.Channel,
			Device:       q.Device,
			Country:      q.Country,
			CustomerTier: q.CustomerTier,
		},
		Offers:        offers,
		CorrelationID: correlationID,
		TraceID:       traceID,
		SpanID:        spanID,
	}
	a.sink.Publish(event)
	return result, nil
}

// HTTPDecideClient is the production DecideClient — issues POST /decide
// against a markup-svc URL and unmarshals the response.
type HTTPDecideClient struct {
	URL        string
	HTTPClient *http.Client
}

// ErrDecideStatus is returned when /decide replies with a non-2xx.
var ErrDecideStatus = errors.New("decide: non-2xx status")

func (c *HTTPDecideClient) Decide(ctx context.Context, req DecideRequest) (DecideResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return DecideResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return DecideResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	// Inject the W3C traceparent so markup-svc joins the incoming trace.
	// A missing / no-op propagator leaves headers unchanged, so this is
	// safe when --otel-enabled is off.
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(httpReq.Header))
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return DecideResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return DecideResponse{}, fmt.Errorf("%w: %d %s", ErrDecideStatus, resp.StatusCode, string(bodyBytes))
	}
	var out DecideResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return DecideResponse{}, err
	}
	return out, nil
}

func newHexID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}
