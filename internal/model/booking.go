package model

import (
	"time"

	"github.com/google/uuid"
)

type Booking struct {
	ID                 uuid.UUID
	DistributorID      string
	IdempotencyKey     string
	RequestFingerprint string

	SupplierID             string
	SupplierIdempotencyKey *string
	SupplierReference      *string

	PropertyID     string
	RoomTypeID     string
	CheckIn        time.Time
	CheckOut       time.Time
	GuestFirstName string
	GuestLastName  string

	Status           Status
	FailureReason    *string
	NeedsRecovery    bool
	SupplierAttempts int

	// The attempt number of an outstanding supplier call, nil otherwise.
	InFlightAttempt *int
	Version         int

	CreatedAt time.Time
	UpdatedAt time.Time
}

// APIKey is a distributor credential. The secret is never stored, only its hash.
type APIKey struct {
	KeyID         string
	DistributorID string
	SecretHash    string
}
