package orchestrator

//go:generate mockgen -destination=mocks/orchestrator.go -source=orchestrator.go -package=mocks

import (
	"context"

	"github.com/google/uuid"
)

type Orchestrator interface {
	StartBooking(ctx context.Context, bookingID uuid.UUID) error
	SignalOutcome(ctx context.Context, bookingID uuid.UUID, status string) error
}
