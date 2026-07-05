package booking

import (
	"errors"
	"testing"
)

func TestStore_InitAssignsInitiatedPhase(t *testing.T) {
	s := New()
	e := s.Init("bk-1", Entry{JourneyID: "j-1", DecisionID: "d-1", Price: 49.99})
	if e.Phase != PhaseInitiated {
		t.Fatalf("phase = %q, want %q", e.Phase, PhaseInitiated)
	}
	if e.BookingID != "bk-1" {
		t.Errorf("booking_id = %q, want bk-1", e.BookingID)
	}
	if s.Len() != 1 {
		t.Errorf("Len = %d, want 1", s.Len())
	}
}

func TestStore_InitIsIdempotent(t *testing.T) {
	s := New()
	_ = s.Init("bk-1", Entry{JourneyID: "j-1"})
	// Second Init with different seed does not overwrite.
	e := s.Init("bk-1", Entry{JourneyID: "j-2"})
	if e.JourneyID != "j-1" {
		t.Fatalf("journey_id after second Init = %q, want j-1 (first-writer-wins)", e.JourneyID)
	}
	if s.Len() != 1 {
		t.Errorf("Len = %d, want 1 (no duplicate)", s.Len())
	}
}

func TestStore_ReserveHappyPath(t *testing.T) {
	s := New()
	s.Init("bk-1", Entry{})
	e, err := s.Reserve("bk-1")
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if e.Phase != PhaseReserved {
		t.Errorf("phase = %q, want %q", e.Phase, PhaseReserved)
	}
}

func TestStore_ReserveErrorsBeforeInit(t *testing.T) {
	s := New()
	_, err := s.Reserve("bk-missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestStore_PurchaseHappyPath(t *testing.T) {
	s := New()
	s.Init("bk-1", Entry{Price: 49.99})
	_, _ = s.Reserve("bk-1")
	e, err := s.Purchase("bk-1", "ord-42")
	if err != nil {
		t.Fatalf("Purchase: %v", err)
	}
	if e.Phase != PhasePurchased {
		t.Errorf("phase = %q, want %q", e.Phase, PhasePurchased)
	}
	if e.OrderID != "ord-42" {
		t.Errorf("order_id = %q, want ord-42", e.OrderID)
	}
}

func TestStore_PurchaseWithoutReserveIsTerminalError(t *testing.T) {
	s := New()
	s.Init("bk-1", Entry{})
	_, err := s.Purchase("bk-1", "ord-42")
	if !errors.Is(err, ErrTerminal) {
		t.Fatalf("err = %v, want ErrTerminal (init → purchase skipped reserve)", err)
	}
}

func TestStore_TimeoutRecordsPhaseWalkedAwayFrom(t *testing.T) {
	// After init but before reserve → TimeoutFrom = initiated.
	s := New()
	s.Init("bk-1", Entry{})
	e, err := s.Timeout("bk-1")
	if err != nil {
		t.Fatalf("Timeout: %v", err)
	}
	if e.Phase != PhaseTimedOut {
		t.Errorf("phase = %q, want %q", e.Phase, PhaseTimedOut)
	}
	if e.TimeoutFrom != PhaseInitiated {
		t.Errorf("timeout_from = %q, want %q", e.TimeoutFrom, PhaseInitiated)
	}

	// After reserve → TimeoutFrom = reserved.
	s.Init("bk-2", Entry{})
	_, _ = s.Reserve("bk-2")
	e2, _ := s.Timeout("bk-2")
	if e2.TimeoutFrom != PhaseReserved {
		t.Errorf("bk-2 timeout_from = %q, want %q", e2.TimeoutFrom, PhaseReserved)
	}
}

func TestStore_TimeoutAfterPurchaseIsTerminalError(t *testing.T) {
	s := New()
	s.Init("bk-1", Entry{})
	_, _ = s.Reserve("bk-1")
	_, _ = s.Purchase("bk-1", "ord-1")
	_, err := s.Timeout("bk-1")
	if !errors.Is(err, ErrTerminal) {
		t.Fatalf("err = %v, want ErrTerminal (purchased is terminal)", err)
	}
}

func TestStore_ReserveIsIdempotent(t *testing.T) {
	s := New()
	s.Init("bk-1", Entry{})
	_, _ = s.Reserve("bk-1")
	e, err := s.Reserve("bk-1")
	if err != nil {
		t.Fatalf("second Reserve: %v", err)
	}
	if e.Phase != PhaseReserved {
		t.Errorf("phase = %q, want %q", e.Phase, PhaseReserved)
	}
}
