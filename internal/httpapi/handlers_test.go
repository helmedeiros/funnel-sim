package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/helmedeiros/funnel-sim/internal/booking"
	"github.com/helmedeiros/funnel-sim/internal/funnelevents"
	"github.com/helmedeiros/funnel-sim/internal/httpapi"
	"github.com/helmedeiros/funnel-sim/internal/search"
)

// stubDecideClient returns the same DecideResponse for every call so the
// aggregator's fan-out is deterministic under test.
type stubDecideClient struct{ n int }

func (s *stubDecideClient) Decide(_ context.Context, _ search.DecideRequest) (search.DecideResponse, error) {
	s.n++
	return search.DecideResponse{
		DecisionID:   "det-decision",
		MarkupFactor: 1.08,
		Rule:         "off_peak_uplift",
		ModelVersion: "v1",
	}, nil
}

type collectSink[T funnelevents.Event] struct {
	events []T
}

func (c *collectSink[T]) Publish(e T) { c.events = append(c.events, e) }

// setup builds the same Deps main.go builds, minus the OTel/prom stack —
// we test the wire contract, not the observability stack.
func setup(t *testing.T) (httpapi.Deps, *collectSink[funnelevents.SearchEvent], *collectSink[funnelevents.BookingEvent]) {
	t.Helper()
	store := booking.New()
	searchSink := &collectSink[funnelevents.SearchEvent]{}
	bookingSink := &collectSink[funnelevents.BookingEvent]{}
	agg := search.NewAggregator(&stubDecideClient{}, searchSink, "test")
	return httpapi.Deps{
		Env: "test", Store: store, Searcher: agg, Bookings: bookingSink,
	}, searchSink, bookingSink
}

func post(t *testing.T, h http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestSearch_HappyPathEmitsSearchEventAndReturnsOffers(t *testing.T) {
	deps, searchSink, _ := setup(t)
	rec := post(t, httpapi.Search(deps), "/search",
		`{"journey_id":"j-1","route":"BR-DE","departure":"2024-09-14","offer_count":3}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	offers, _ := out["offers"].([]any)
	if len(offers) != 3 {
		t.Errorf("offers = %d, want 3", len(offers))
	}
	if len(searchSink.events) != 1 {
		t.Errorf("search events = %d, want 1", len(searchSink.events))
	}
}

func TestSearch_RequiresJourneyID(t *testing.T) {
	deps, _, _ := setup(t)
	rec := post(t, httpapi.Search(deps), "/search", `{"route":"BR-DE"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// TestBookingLifecycle drives the full happy path through the handlers,
// asserting that four booking events land in the right partitions with
// the right event_type values.
func TestBookingLifecycle(t *testing.T) {
	deps, _, bookingSink := setup(t)
	// init
	rec := post(t, httpapi.BookingInit(deps), "/booking/init",
		`{"journey_id":"j-1","decision_id":"d-1","offer_id":"o-1","price":49.99,"markup_factor":1.08}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("init status = %d, want 200", rec.Code)
	}
	var initResp map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &initResp)
	bkID := initResp["booking_id"]
	if bkID == "" {
		t.Fatal("empty booking_id in response")
	}
	// reserve
	rec = post(t, httpapi.BookingReserve(deps), "/booking/reserve",
		`{"booking_id":"`+bkID+`"}`)
	if rec.Code != http.StatusOK {
		t.Errorf("reserve status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	// purchase
	rec = post(t, httpapi.BookingPurchase(deps), "/booking/purchase",
		`{"booking_id":"`+bkID+`"}`)
	if rec.Code != http.StatusOK {
		t.Errorf("purchase status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var purchaseResp map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &purchaseResp)
	if purchaseResp["order_id"] == "" {
		t.Error("purchase response missing order_id")
	}

	// event assertions
	wantTypes := []string{"booking.initiated", "booking.reserved", "booking.purchased"}
	if len(bookingSink.events) != len(wantTypes) {
		t.Fatalf("booking events = %d, want %d", len(bookingSink.events), len(wantTypes))
	}
	for i, want := range wantTypes {
		if got := bookingSink.events[i].EventType; got != want {
			t.Errorf("event[%d].event_type = %q, want %q", i, got, want)
		}
	}
	if bookingSink.events[2].OrderID == "" {
		t.Error("purchased event missing order_id")
	}
}

// TestBookingTimeoutCapturesPhase drives init → timeout and asserts the
// event carries phase="initiated", proving the terminal-with-phase
// contract from the design doc.
func TestBookingTimeoutCapturesPhase(t *testing.T) {
	deps, _, bookingSink := setup(t)
	rec := post(t, httpapi.BookingInit(deps), "/booking/init",
		`{"journey_id":"j-1","decision_id":"d-1","price":49.99}`)
	var initResp map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &initResp)
	bkID := initResp["booking_id"]
	// timeout while still in initiated
	rec = post(t, httpapi.BookingTimeout(deps), "/booking/timeout",
		`{"booking_id":"`+bkID+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("timeout status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	// last event should be booking.timeout with phase=initiated
	last := bookingSink.events[len(bookingSink.events)-1]
	if last.EventType != "booking.timeout" {
		t.Errorf("event_type = %q, want booking.timeout", last.EventType)
	}
	if last.Phase != "initiated" {
		t.Errorf("phase = %q, want initiated", last.Phase)
	}
	if last.TimeoutKind != "customer_abandoned" {
		t.Errorf("timeout_kind = %q, want customer_abandoned", last.TimeoutKind)
	}
}

func TestBookingReserveOnMissingReturns404(t *testing.T) {
	deps, _, _ := setup(t)
	rec := post(t, httpapi.BookingReserve(deps), "/booking/reserve",
		`{"booking_id":"nope"}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestBookingPurchaseWithoutReserveReturns409(t *testing.T) {
	deps, _, _ := setup(t)
	rec := post(t, httpapi.BookingInit(deps), "/booking/init",
		`{"journey_id":"j-1","decision_id":"d-1"}`)
	var initResp map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &initResp)
	rec = post(t, httpapi.BookingPurchase(deps), "/booking/purchase",
		`{"booking_id":"`+initResp["booking_id"]+`"}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 (init → purchase skipped reserve)", rec.Code)
	}
}

func TestHealthz(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	httpapi.Healthz().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("healthz status = %d", rec.Code)
	}
}

func TestReadyz(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	httpapi.Readyz(func() (string, bool) { return "", true }).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("readyz status = %d", rec.Code)
	}
}
