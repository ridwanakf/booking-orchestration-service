package service

//go:generate mockgen -destination=mocks/services.go -source=services.go -package=mocks

import (
	"context"

	"github.com/google/uuid"

	"github.com/ridwanakf/booking-orchestration-service/internal/model"
)

// SupplierOutcome is what a callback asserts. A decline carries a reason and no
// reservation, which is why the reference cannot be required.
type SupplierOutcome struct {
	Reference     string
	Status        string
	DeclineReason string
}

type CallbackResult struct {
	Status    model.Status
	Applied   bool
	Duplicate bool
}

type BookingService interface {
	Create(ctx context.Context, req model.CreateRequest) (*model.Booking, bool, error)
	Get(ctx context.Context, id uuid.UUID) (*model.Booking, error)
	ApplyCallback(ctx context.Context, id uuid.UUID, outcome SupplierOutcome) (CallbackResult, error)
}
