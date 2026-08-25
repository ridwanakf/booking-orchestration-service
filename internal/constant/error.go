package constant

import (
	"errors"
	"time"
)

var (
	ErrBookingNotFound           = errors.New("booking not found")
	ErrIdempotencyKeyReused      = errors.New("idempotency key reused with a different payload")
	ErrInvalidTransition         = errors.New("invalid state transition")
	ErrTransitionConflict        = errors.New("transition lost a race")
	ErrUnsupportedSupplierStatus = errors.New("unsupported supplier status")
	ErrInvalidCallbackToken      = errors.New("invalid callback token")
	ErrUnauthorized              = errors.New("unauthorized")
	ErrMissingSupplierReference  = errors.New("confirmation carried no supplier reference")
	ErrInvalidConfig             = errors.New("invalid configuration")
	ErrAttemptSuperseded         = errors.New("attempt superseded while its call was outstanding")
	ErrCallbackConflict          = errors.New("callback conflicts with the booking state")
)

// Shared by name so the API process never links the workflow package.
const (
	ReasonSupplierUnreachable = "supplier_unreachable"
	ReasonSupplierDeclined    = "supplier_declined"
	ReasonAttemptAbandoned    = "attempt_abandoned"
	ReasonRequestBuildFailed  = "request_build_failed"
)

const (
	WorkflowBooking       = "BookingWorkflow"
	SignalSupplierOutcome = "supplier_outcome"
)

// Passed as workflow input, not read from process configuration, so a replay
// reproduces the schedule recorded in history after a config change.
type WorkflowParams struct {
	CreateAttempts  int             `json:"createAttempts"`
	RetryDelays     []time.Duration `json:"retryDelays"`
	ParkTimeout     time.Duration   `json:"parkTimeout"`
	ActivityTimeout time.Duration   `json:"activityTimeout"`
	PersistWindow   time.Duration   `json:"persistWindow"`
}
