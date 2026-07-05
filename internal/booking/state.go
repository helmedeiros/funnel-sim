// Package booking manages the in-memory booking state map that
// funnel-sim uses to answer booking.v1 events. Real production
// booking-svc will replace this with a durable store; the funnel-sim
// contract is that state is ephemeral and only lives for the duration
// of the traffic-gen session that created it. See ADR-0004.
package booking

import (
	"errors"
	"sync"
	"time"
)

// Phase is the booking's current state. Transitions are strictly
// initiated → reserved → purchased, with terminal timeout at any post-init
// phase.
type Phase string

const (
	PhaseInitiated Phase = "initiated"
	PhaseReserved  Phase = "reserved"
	PhasePurchased Phase = "purchased"
	PhaseTimedOut  Phase = "timed_out" // terminal, sub-typed via TimeoutFromPhase
)

// Entry is the load-bearing state carried per booking. Fields are
// snake_case where they cross the wire; camelCase inside the struct.
type Entry struct {
	BookingID    string
	JourneyID    string
	SearchID     string
	DecisionID   string
	OfferID      string
	Price        float64
	MarkupFactor float64
	Experiment   string
	Variant      string

	Phase       Phase
	TimeoutFrom Phase // populated when Phase == PhaseTimedOut, records the phase we walked away from
	OrderID     string
	CreatedAt   time.Time
}

// ErrNotFound is returned by Reserve/Purchase/Timeout when the booking_id
// has no corresponding init.
var ErrNotFound = errors.New("booking: not found")

// ErrTerminal is returned when a transition is attempted on a booking
// already in a terminal phase (purchased or timed_out).
var ErrTerminal = errors.New("booking: already terminal")

// Store is the in-memory booking map. Zero value is not usable — use New.
type Store struct {
	mu       sync.Mutex
	bookings map[string]*Entry
	now      func() time.Time
}

func New() *Store {
	return &Store{
		bookings: map[string]*Entry{},
		now:      time.Now,
	}
}

// Init records a new booking in PhaseInitiated. Idempotent under retry
// with the same bookingID: the second call returns the existing entry
// without changing state.
func (s *Store) Init(bookingID string, seed Entry) *Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.bookings[bookingID]; ok {
		return e
	}
	e := seed
	e.BookingID = bookingID
	e.Phase = PhaseInitiated
	e.CreatedAt = s.now().UTC()
	s.bookings[bookingID] = &e
	return &e
}

// Reserve transitions initiated → reserved. Returns ErrNotFound if the
// booking does not exist, ErrTerminal if the booking is purchased or
// timed out. Idempotent under retry from PhaseReserved.
func (s *Store) Reserve(bookingID string) (*Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.bookings[bookingID]
	if !ok {
		return nil, ErrNotFound
	}
	if e.Phase == PhaseReserved {
		return e, nil
	}
	if e.Phase != PhaseInitiated {
		return nil, ErrTerminal
	}
	e.Phase = PhaseReserved
	return e, nil
}

// Purchase transitions reserved → purchased. Returns ErrNotFound if the
// booking does not exist, ErrTerminal if the booking is already
// purchased or timed out. Idempotent under retry from PhasePurchased.
// Callers pass a caller-minted order ID; funnel-sim does not mint it.
func (s *Store) Purchase(bookingID, orderID string) (*Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.bookings[bookingID]
	if !ok {
		return nil, ErrNotFound
	}
	if e.Phase == PhasePurchased {
		return e, nil
	}
	if e.Phase != PhaseReserved {
		return nil, ErrTerminal
	}
	e.Phase = PhasePurchased
	e.OrderID = orderID
	return e, nil
}

// Timeout marks the booking terminal with the walked-away-from phase
// captured on TimeoutFrom. Returns ErrNotFound if the booking does not
// exist, ErrTerminal if the booking is already terminal.
func (s *Store) Timeout(bookingID string) (*Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.bookings[bookingID]
	if !ok {
		return nil, ErrNotFound
	}
	if e.Phase == PhaseTimedOut || e.Phase == PhasePurchased {
		return nil, ErrTerminal
	}
	e.TimeoutFrom = e.Phase
	e.Phase = PhaseTimedOut
	return e, nil
}

// Get returns a copy of the entry for bookingID, or ErrNotFound.
func (s *Store) Get(bookingID string) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.bookings[bookingID]
	if !ok {
		return Entry{}, ErrNotFound
	}
	return *e, nil
}

// Len returns the current number of tracked bookings. Useful for the
// /metrics gauge and tests that assert bounded state.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.bookings)
}
