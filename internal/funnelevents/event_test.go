package funnelevents

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSearchEvent_PartitionIsEmpty(t *testing.T) {
	if got := (SearchEvent{}).Partition(); got != "" {
		t.Errorf("Partition = %q, want empty (search events land in a single partition per ADR-0002)", got)
	}
}

func TestBookingEvent_PartitionCarriesEventType(t *testing.T) {
	cases := []struct {
		event string
		want  string
	}{
		{"booking.initiated", "event_type=booking.initiated"},
		{"booking.reserved", "event_type=booking.reserved"},
		{"booking.purchased", "event_type=booking.purchased"},
		{"booking.timeout", "event_type=booking.timeout"},
	}
	for _, tc := range cases {
		got := BookingEvent{EventType: tc.event}.Partition()
		if got != tc.want {
			t.Errorf("Partition(%q) = %q, want %q", tc.event, got, tc.want)
		}
	}
}

func TestBookingEvent_JSONOmitsZeroFields(t *testing.T) {
	// A booking.initiated event should NOT have order_id, revenue, phase,
	// or timeout_kind in the JSON — those are only populated on purchase
	// or timeout. This prevents downstream consumers from seeing sparse
	// keys that could look like real values.
	e := BookingEvent{
		SchemaVersion: SchemaV1,
		EventType:     "booking.initiated",
		EventID:       "e-1",
		JourneyID:     "j-1",
		DecisionID:    "d-1",
		BookingID:     "b-1",
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(b)
	for _, banned := range []string{"order_id", "revenue", "phase", "timeout_kind"} {
		if strings.Contains(body, banned) {
			t.Errorf("initiated event body contains %q; body=%s", banned, body)
		}
	}
}

func TestNoopSink_AcceptsPublish(t *testing.T) {
	// A noop must not panic when called; it is the default deployment
	// posture per ADR-0005.
	NoopSink[SearchEvent]{}.Publish(SearchEvent{})
	NoopSink[BookingEvent]{}.Publish(BookingEvent{})
}
