package model

import (
	"time"

	"github.com/google/uuid"
)

// Event kinds, shared with the log vocabulary. A lineage row needs a booking,
// so events that occur without one (a callback for an unknown id, a rejected
// token) are logged only and never appear here.
const (
	EventCreated          = "booking.created"
	EventTransition       = "booking.transition"
	EventConflict         = "booking.conflict"
	EventDuplicateRequest = "booking.duplicate_request"
	EventDuplicateSuspect = "booking.duplicate_suspect"
	EventSupplierRequest  = "supplier.request"
	EventSupplierResponse = "supplier.response"
	EventSupplierTimeout  = "supplier.timeout"
	EventSupplierCallback = "supplier.callback"
	EventCallbackRejected = "callback.rejected"
	EventParked           = "worker.parked"
	EventSweepRestarted   = "sweep.restarted"
)

// Event is one row of a booking's lineage, appended in the same statement as
// the transition it records so the two cannot diverge.
type Event struct {
	Seq                int64
	BookingID          uuid.UUID
	OccurredAt         time.Time
	FromStatus         *Status
	ToStatus           Status
	EventType          string
	Attempt            *int
	RequestID          *string
	SupplierStatusCode *string
	SupplierReason     *string
	PayloadDigest      *string
}
