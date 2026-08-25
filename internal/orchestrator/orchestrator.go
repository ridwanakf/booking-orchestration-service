package orchestrator

//go:generate mockgen -destination=mocks/orchestrator.go -source=orchestrator.go -package=mocks

import (
	"context"

	"github.com/google/uuid"
)

type Orchestrator interface {
	// Reports whether a new execution was created. False means one was already
	// running, which is harmless but is not a restart.
	StartBooking(ctx context.Context, bookingID uuid.UUID) (bool, error)
	SignalOutcome(ctx context.Context, bookingID uuid.UUID, status string) error
}
