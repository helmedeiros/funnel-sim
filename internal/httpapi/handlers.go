// Package httpapi holds the funnel-sim HTTP surface: /search plus the
// four booking endpoints. Wire types are unexported; they mirror the
// funnel-events schema so the JSON body a client sends and the JSON
// event that lands in MinIO are consistent field-by-field.
package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	oteltrace "go.opentelemetry.io/otel/trace"

	breengine "github.com/helmedeiros/bre-go/engine"

	"github.com/helmedeiros/funnel-sim/internal/booking"
	"github.com/helmedeiros/funnel-sim/internal/funnelevents"
	"github.com/helmedeiros/funnel-sim/internal/search"
)

// Deps groups the wiring every handler pulls from.
type Deps struct {
	Env      string
	Store    *booking.Store
	Searcher *search.Aggregator
	Bookings funnelevents.Sink[funnelevents.BookingEvent]
}

// --- /search ---

type searchRequest struct {
	JourneyID    string `json:"journey_id"`
	Route        string `json:"route"`
	Departure    string `json:"departure"`
	Passengers   int    `json:"passengers"`
	Channel      string `json:"channel"`
	Device       string `json:"device"`
	Country      string `json:"country"`
	CustomerTier string `json:"customer_tier"`
	BasePrice    float64 `json:"base_price,omitempty"`
	OfferCount   int     `json:"offer_count,omitempty"`
}

type searchResponse struct {
	SearchID string                     `json:"search_id"`
	Offers   []funnelevents.SearchOffer `json:"offers"`
}

func Search(d Deps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req searchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "malformed JSON body")
			return
		}
		if req.JourneyID == "" {
			writeError(w, http.StatusBadRequest, "journey_id required")
			return
		}
		ctx := r.Context()
		cid := breengine.CorrelationIDFromContext(ctx)
		var tid, sid string
		if sc := oteltrace.SpanContextFromContext(ctx); sc.IsValid() {
			tid = sc.TraceID().String()
			sid = sc.SpanID().String()
		}
		result, err := d.Searcher.Search(ctx, search.Query{
			JourneyID:    req.JourneyID,
			Route:        req.Route,
			Departure:    req.Departure,
			Passengers:   req.Passengers,
			Channel:      req.Channel,
			Device:       req.Device,
			Country:      req.Country,
			CustomerTier: req.CustomerTier,
			BasePrice:    req.BasePrice,
			OfferCount:   req.OfferCount,
		}, cid, tid, sid)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "search aggregation failed")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(searchResponse{
			SearchID: result.SearchID,
			Offers:   result.Offers,
		})
	})
}

// --- /booking/init ---

type bookingInitRequest struct {
	JourneyID    string  `json:"journey_id"`
	SearchID     string  `json:"search_id"`
	DecisionID   string  `json:"decision_id"`
	OfferID      string  `json:"offer_id"`
	Price        float64 `json:"price"`
	MarkupFactor float64 `json:"markup_factor"`
	Experiment   string  `json:"experiment,omitempty"`
	Variant      string  `json:"variant,omitempty"`
	Currency     string  `json:"currency,omitempty"`
}

type bookingInitResponse struct {
	BookingID string `json:"booking_id"`
	Status    string `json:"status"`
}

func BookingInit(d Deps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req bookingInitRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "malformed JSON body")
			return
		}
		if req.JourneyID == "" || req.DecisionID == "" {
			writeError(w, http.StatusBadRequest, "journey_id and decision_id required")
			return
		}
		bookingID := newHexID()
		if bookingID == "" {
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}
		entry := d.Store.Init(bookingID, booking.Entry{
			JourneyID:    req.JourneyID,
			SearchID:     req.SearchID,
			DecisionID:   req.DecisionID,
			OfferID:      req.OfferID,
			Price:        req.Price,
			MarkupFactor: req.MarkupFactor,
			Experiment:   req.Experiment,
			Variant:      req.Variant,
		})
		emitBookingEvent(r, d, "booking.initiated", *entry, req.Currency, 0)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(bookingInitResponse{
			BookingID: entry.BookingID,
			Status:    string(entry.Phase),
		})
	})
}

// --- /booking/reserve ---

type bookingByIDRequest struct {
	BookingID string `json:"booking_id"`
}

type bookingStatusResponse struct {
	Status  string `json:"status"`
	Phase   string `json:"phase,omitempty"`
	OrderID string `json:"order_id,omitempty"`
}

func BookingReserve(d Deps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req bookingByIDRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "malformed JSON body")
			return
		}
		if req.BookingID == "" {
			writeError(w, http.StatusBadRequest, "booking_id required")
			return
		}
		entry, err := d.Store.Reserve(req.BookingID)
		if err != nil {
			mapBookingError(w, err)
			return
		}
		emitBookingEvent(r, d, "booking.reserved", *entry, "", 0)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(bookingStatusResponse{Status: string(entry.Phase)})
	})
}

// --- /booking/purchase ---

func BookingPurchase(d Deps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req bookingByIDRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "malformed JSON body")
			return
		}
		if req.BookingID == "" {
			writeError(w, http.StatusBadRequest, "booking_id required")
			return
		}
		orderID := newHexID()
		if orderID == "" {
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}
		entry, err := d.Store.Purchase(req.BookingID, orderID)
		if err != nil {
			mapBookingError(w, err)
			return
		}
		emitBookingEvent(r, d, "booking.purchased", *entry, "", entry.Price)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(bookingStatusResponse{
			Status:  string(entry.Phase),
			OrderID: entry.OrderID,
		})
	})
}

// --- /booking/timeout ---

func BookingTimeout(d Deps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req bookingByIDRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "malformed JSON body")
			return
		}
		if req.BookingID == "" {
			writeError(w, http.StatusBadRequest, "booking_id required")
			return
		}
		entry, err := d.Store.Timeout(req.BookingID)
		if err != nil {
			mapBookingError(w, err)
			return
		}
		emitBookingEvent(r, d, "booking.timeout", *entry, "", 0)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(bookingStatusResponse{
			Status: string(entry.Phase),
			Phase:  string(entry.TimeoutFrom),
		})
	})
}

// --- shared emit helper ---

func emitBookingEvent(r *http.Request, d Deps, eventType string, e booking.Entry, currency string, revenue float64) {
	ctx := r.Context()
	cid := breengine.CorrelationIDFromContext(ctx)
	var tid, sid string
	if sc := oteltrace.SpanContextFromContext(ctx); sc.IsValid() {
		tid = sc.TraceID().String()
		sid = sc.SpanID().String()
	}
	event := funnelevents.BookingEvent{
		SchemaVersion: funnelevents.SchemaV1,
		EventType:     eventType,
		EventID:       newHexID(),
		Ts:            time.Now().UTC().Format(time.RFC3339Nano),
		Env:           d.Env,
		JourneyID:     e.JourneyID,
		SearchID:      e.SearchID,
		DecisionID:    e.DecisionID,
		OfferID:       e.OfferID,
		BookingID:     e.BookingID,
		OrderID:       e.OrderID,
		Price:         e.Price,
		MarkupFactor:  e.MarkupFactor,
		Revenue:       revenue,
		Currency:      currency,
		Experiment:    e.Experiment,
		Variant:       e.Variant,
		CorrelationID: cid,
		TraceID:       tid,
		SpanID:        sid,
	}
	if eventType == "booking.timeout" && e.TimeoutFrom != "" {
		event.Phase = string(e.TimeoutFrom)
		event.TimeoutKind = "customer_abandoned"
	}
	d.Bookings.Publish(event)
}

func mapBookingError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, booking.ErrNotFound):
		writeError(w, http.StatusNotFound, "booking not found")
	case errors.Is(err, booking.ErrTerminal):
		writeError(w, http.StatusConflict, "booking already terminal")
	default:
		writeError(w, http.StatusInternalServerError, "internal")
	}
}

func newHexID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}
