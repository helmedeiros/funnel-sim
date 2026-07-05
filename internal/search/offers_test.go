package search

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/helmedeiros/funnel-sim/internal/funnelevents"
)

type stubDecideClient struct {
	calls int
	err   error
}

func (s *stubDecideClient) Decide(_ context.Context, _ DecideRequest) (DecideResponse, error) {
	s.calls++
	if s.err != nil {
		return DecideResponse{}, s.err
	}
	return DecideResponse{
		DecisionID:   "det-" + timeStamp(),
		MarkupFactor: 1.08,
		Rule:         "off_peak_uplift",
		ModelVersion: "v1",
		Experiment:   "BR_peak_uplift_v3",
	}, nil
}

func timeStamp() string { return "fixed" } // deterministic in tests

type collectSink struct {
	events []funnelevents.SearchEvent
}

func (c *collectSink) Publish(e funnelevents.SearchEvent) { c.events = append(c.events, e) }

func TestAggregator_FanoutMatchesOfferCount(t *testing.T) {
	stub := &stubDecideClient{}
	sink := &collectSink{}
	a := NewAggregator(stub, sink, "test")
	_, err := a.Search(context.Background(), Query{
		JourneyID: "j-1", Route: "BR-DE", Departure: "2024-09-14",
		Passengers: 1, CustomerTier: "gold", Country: "DE",
		OfferCount: 5,
	}, "cid", "tid", "sid")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if stub.calls != 5 {
		t.Errorf("stub calls = %d, want 5", stub.calls)
	}
	if len(sink.events) != 1 {
		t.Fatalf("sink events = %d, want 1 search.v1 emission", len(sink.events))
	}
	e := sink.events[0]
	if len(e.Offers) != 5 {
		t.Errorf("offers = %d, want 5", len(e.Offers))
	}
	if e.SchemaVersion != funnelevents.SchemaV1 {
		t.Errorf("schema_version = %q", e.SchemaVersion)
	}
	if e.EventType != "search.viewed" {
		t.Errorf("event_type = %q", e.EventType)
	}
	if e.JourneyID != "j-1" {
		t.Errorf("journey_id = %q", e.JourneyID)
	}
}

func TestAggregator_DecideFailuresSkipOfferButEmitEvent(t *testing.T) {
	// All /decide calls fail; the search event still emits with zero offers
	// so analytics sees the "we tried to render and everything upstream broke" case.
	stub := &stubDecideClient{err: errors.New("upstream boom")}
	sink := &collectSink{}
	a := NewAggregator(stub, sink, "test")
	res, err := a.Search(context.Background(), Query{
		JourneyID: "j-1", Route: "BR-DE", Departure: "2024-09-14",
		OfferCount: 3,
	}, "", "", "")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res.Offers) != 0 {
		t.Errorf("offers = %d, want 0 (all decide failed)", len(res.Offers))
	}
	if len(sink.events) != 1 {
		t.Errorf("sink events = %d, want 1", len(sink.events))
	}
	if len(sink.events[0].Offers) != 0 {
		t.Errorf("event offers = %d, want 0", len(sink.events[0].Offers))
	}
}

func TestAggregator_SearchIDPropagatesToOffers(t *testing.T) {
	stub := &stubDecideClient{}
	sink := &collectSink{}
	a := NewAggregator(stub, sink, "test")
	res, _ := a.Search(context.Background(), Query{
		JourneyID: "j-1", OfferCount: 2,
	}, "", "", "")
	if res.SearchID == "" {
		t.Fatal("SearchID must be minted, not empty")
	}
	if sink.events[0].SearchID != res.SearchID {
		t.Errorf("event search_id = %q, want %q", sink.events[0].SearchID, res.SearchID)
	}
}

func TestAggregator_PricingGradientVaries(t *testing.T) {
	stub := &stubDecideClient{}
	sink := &collectSink{}
	a := NewAggregator(stub, sink, "test")
	_, _ = a.Search(context.Background(), Query{
		JourneyID: "j-1", BasePrice: 100.0, OfferCount: 3,
	}, "", "", "")
	offers := sink.events[0].Offers
	// Position gradient means each successive offer has a higher pre-markup base,
	// so the emitted final price should be strictly increasing.
	prev := offers[0].Price
	for i := 1; i < len(offers); i++ {
		if offers[i].Price <= prev {
			t.Errorf("offer %d price %v not > prev %v", i, offers[i].Price, prev)
		}
		prev = offers[i].Price
	}
}

// timestampNoUnused silences the deprecation of the timeStamp helper if
// tests get reorganized; not called at runtime.
var _ = time.RFC3339Nano
