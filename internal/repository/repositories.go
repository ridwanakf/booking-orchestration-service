package repository

//go:generate mockgen -destination=mocks/repositories.go -source=repositories.go -package=mocks

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/ridwanakf/booking-orchestration-service/internal/constant"

	"github.com/ridwanakf/booking-orchestration-service/internal/model"
)

const (
	// Deliberately invalid, so a literal that omits the rule fails loudly.
	MarkerUnset     MarkerRule = iota
	MarkerClear                // no attempt outstanding
	MarkerOwned                // owns the named attempt and may record its outcome
	MarkerSupersede            // supersedes whatever is outstanding; callbacks use this
)

const (
	RefusalNone Refusal = iota
	RefusalBudgetSpent
	RefusalAttemptOutstanding
	RefusalSettled
)

type MarkerRule int

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

// Collapsing these into one answer lets a caller settle FAILED while another
// worker's call is in flight, which is a double booking.
type Refusal int

type Authorization struct {
	Attempt  int
	Previous model.Status
	Refusal  Refusal
}

// The three ages at which the sweep considers a booking quiet.
type StaleThresholds struct {
	Marker   time.Duration // older than this and no live call can be behind it
	Received time.Duration // a booking whose workflow never started
	Idle     time.Duration // unsettled with no attempt outstanding
}

type APIKeyRepository interface {
	FindActive(ctx context.Context, keyID string) (*model.APIKey, error)
}

// A lost race is a named outcome, and that guarantee is the contract.
type BookingRepository interface {
	InsertOrLoad(ctx context.Context, b model.Booking, requestID *string) (*model.Booking, bool, error)
	GetByID(ctx context.Context, id uuid.UUID) (*model.Booking, error)
	GetByKey(ctx context.Context, distributorID, key string) (*model.Booking, error)
	FindPriorByFingerprint(ctx context.Context, distributorID, fingerprint, excludeKey string, within time.Duration) (uuid.UUID, bool, error)
	FindStale(ctx context.Context, t StaleThresholds, limit int) ([]uuid.UUID, error)
	Events(ctx context.Context, id uuid.UUID) ([]model.Event, error)

	Apply(ctx context.Context, id uuid.UUID, t Transition) (model.Status, error)
	// Ends an attempt without moving the booking.
	ClearMarker(ctx context.Context, id uuid.UUID, attempt int, e model.Event) (bool, error)
	AppendRefusal(ctx context.Context, id uuid.UUID, e model.Event) error
	Authorize(ctx context.Context, id uuid.UUID, maxAttempts int, supplierKey string, requestID *string) (Authorization, error)
	ResolveStaleMarker(ctx context.Context, id uuid.UUID, requestID *string) (int, bool, error)
	ParkIfUnknown(ctx context.Context, id uuid.UUID, requestID *string) (bool, error)
	Flag(ctx context.Context, id uuid.UUID) error
}

// A zero Marker makes every in-flight booking instantly stale, so the first
// sweep tick would write all of them to UNKNOWN.
func (t StaleThresholds) Validate() error {
	for name, d := range map[string]time.Duration{"marker": t.Marker, "received": t.Received, "idle": t.Idle} {
		if d <= 0 {
			return fmt.Errorf("%w: sweep %s threshold must be positive, got %s", constant.ErrInvalidConfig, name, d)
		}
	}
	return nil
}
