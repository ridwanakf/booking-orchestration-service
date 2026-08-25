package model

import (
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"slices"
	"strings"
	"time"
)

const DateLayout = "2006-01-02"

type CreateRequest struct {
	DistributorID  string
	IdempotencyKey string
	PropertyID     string
	RoomTypeID     string
	CheckIn        time.Time
	CheckOut       time.Time
	GuestFirstName string
	GuestLastName  string
}

// Hashing the parsed fields rather than the raw body makes the result immune to
// serializer differences. The idempotency key is excluded so that the same
// booking sent under a new key is still recognisable as a duplicate suspect.
func (r CreateRequest) Fingerprint() string {
	fields := map[string]string{
		"checkIn":        r.CheckIn.UTC().Format(DateLayout),
		"checkOut":       r.CheckOut.UTC().Format(DateLayout),
		"distributorId":  strings.TrimSpace(r.DistributorID),
		"guestFirstName": strings.TrimSpace(r.GuestFirstName),
		"guestLastName":  strings.TrimSpace(r.GuestLastName),
		"propertyId":     strings.TrimSpace(r.PropertyID),
		"roomTypeId":     strings.TrimSpace(r.RoomTypeID),
	}

	var canonical strings.Builder
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		canonical.WriteString(name)
		canonical.WriteByte('=')
		canonical.WriteString(fields[name])
		canonical.WriteByte('\n')
	}

	sum := sha256.Sum256([]byte(canonical.String()))
	return hex.EncodeToString(sum[:])
}
