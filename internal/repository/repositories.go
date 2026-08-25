package repository

//go:generate mockgen -destination=mocks/repositories.go -source=repositories.go -package=mocks

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ridwanakf/booking-orchestration-service/internal/model"
)

// MarkerRule states which in-flight attempt a write claims to own.
type MarkerRule int

const (
	// The write must find no attempt outstanding.
	MarkerClear MarkerRule = iota
	// The write owns the named attempt and may record its outcome.
	MarkerOwned
	// The write supersedes whatever is outstanding. Callbacks use this.
	MarkerSupersede
)

// Transition is one guarded state change plus the lineage row recording it.
type Transition struct {
	From      model.Status
	To        model.Status
	Marker    MarkerRule
	Attempt   *int
	EventType string

	SupplierReference  *string
	FailureReason      *string
	ClearRecoveryFlag  bool
	RequestID          *string
	SupplierStatusCode *string
	SupplierReason     *string
	PayloadDigest      *string
}

// StaleThresholds are the three ages at which the sweep considers a booking
// quiet. They are separate because they answer different questions.
type StaleThresholds struct {
	// A marker older than this cannot have a live call behind it.
	Marker time.Duration
	// A booking whose workflow never started.
	Received time.Duration
	// A booking between attempts.
	InFlight time.Duration
}

// The guarded writes belong to this contract rather than sitting above it: the
// guarantee that a lost race is a named outcome is the contract.
type BookingRepository interface {
	InsertOrLoad(ctx context.Context, b model.Booking, requestID *string) (*model.Booking, bool, error)
	GetByID(ctx context.Context, id uuid.UUID) (*model.Booking, error)
	GetByKey(ctx context.Context, distributorID, key string) (*model.Booking, error)
	FindPriorByFingerprint(ctx context.Context, distributorID, fingerprint, excludeKey string, within time.Duration) (uuid.UUID, bool, error)
	FindStale(ctx context.Context, t StaleThresholds, limit int) ([]uuid.UUID, error)
	Events(ctx context.Context, id uuid.UUID) ([]model.Event, error)

	Apply(ctx context.Context, id uuid.UUID, t Transition) (model.Status, error)
	AppendRefusal(ctx context.Context, id uuid.UUID, e model.Event) error
	Authorize(ctx context.Context, id uuid.UUID, maxAttempts int, supplierKey string, requestID *string) (int, model.Status, error)
	ResolveStaleMarker(ctx context.Context, id uuid.UUID) (int, bool, error)
	ParkIfUnknown(ctx context.Context, id uuid.UUID) (bool, error)
	Flag(ctx context.Context, id uuid.UUID) error
}
