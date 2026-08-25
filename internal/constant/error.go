package constant

import "errors"

var (
	ErrBookingNotFound           = errors.New("booking not found")
	ErrIdempotencyKeyReused      = errors.New("idempotency key reused with a different payload")
	ErrInvalidTransition         = errors.New("invalid state transition")
	ErrTransitionConflict        = errors.New("transition lost a race")
	ErrUnsupportedSupplierStatus = errors.New("unsupported supplier status")
	ErrInvalidCallbackToken      = errors.New("invalid callback token")
	ErrCallbackConflict          = errors.New("callback conflicts with the booking state")
)

// Shared by name so the API process never links the workflow package.
const (
	ReasonSupplierUnreachable = "supplier_unreachable"
	ReasonSupplierDeclined    = "supplier_declined"
)

const (
	WorkflowBooking       = "BookingWorkflow"
	SignalSupplierOutcome = "supplier_outcome"
)

// Passed as workflow input, not read from process configuration, so a replay
// reproduces the schedule recorded in history after a config change.
type WorkflowParams struct {
	CreateAttempts     int   `json:"createAttempts"`
	RetrieveDelaysSec  []int `json:"retrieveDelaysSec"`
	ParkTimeoutSec     int   `json:"parkTimeoutSec"`
	ActivityTimeoutSec int   `json:"activityTimeoutSec"`
}
