package supplier

//go:generate mockgen -destination=mocks/supplier.go -source=supplier.go -package=mocks

import (
	"context"
	"strings"
	"time"
)

// Closed set: the workflow branches on these and nothing else.
type Outcome string

const (
	OutcomeConfirmed Outcome = "CONFIRMED"
	OutcomeRejected  Outcome = "REJECTED"
	// Bytes reached the supplier and we cannot tell what it did with them.
	OutcomeAmbiguous Outcome = "AMBIGUOUS"
	// No request bytes ever left, so the supplier cannot be holding this booking.
	OutcomeNotSent        Outcome = "NOT_SENT"
	OutcomePreSendFailure Outcome = "PRE_SEND_FAILURE"
)

type BookRequest struct {
	BookingID      string
	IdempotencyKey string
	PropertyID     string
	RoomTypeID     string
	CheckIn        time.Time
	CheckOut       time.Time
	GuestFirstName string
	GuestLastName  string
}

type Result struct {
	Outcome   Outcome
	Reference string
	Reason    string
	Latency   time.Duration
}

type Client interface {
	Book(ctx context.Context, req BookRequest) Result
}

// Stored on first authorize and reused by every later attempt. Lossless here;
// a supplier with a shorter limit needs a lossy encoding, and the unique index
// on (supplier_id, supplier_idempotency_key) is what catches that collision.
func IdempotencyKey(bookingID string) string {
	return strings.ToUpper(strings.ReplaceAll(bookingID, "-", ""))
}
