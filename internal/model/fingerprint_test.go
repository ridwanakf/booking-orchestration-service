package model_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/ridwanakf/booking-orchestration-service/internal/model"
)

type FingerprintSuite struct {
	suite.Suite
	req model.CreateRequest
}

func TestFingerprint(t *testing.T) {
	suite.Run(t, new(FingerprintSuite))
}

func (s *FingerprintSuite) SetupTest() {
	s.req = model.CreateRequest{
		DistributorID:  "distributor-001",
		IdempotencyKey: "partner-12345",
		PropertyID:     "hotel-001",
		RoomTypeID:     "room-deluxe-confirm",
		CheckIn:        s.date("2026-09-10"),
		CheckOut:       s.date("2026-09-12"),
		GuestFirstName: "Taro",
		GuestLastName:  "Yamada",
	}
}

func (s *FingerprintSuite) date(v string) time.Time {
	parsed, err := time.Parse(model.DateLayout, v)
	s.Require().NoError(err)
	return parsed
}

func (s *FingerprintSuite) TestIsStable() {
	s.Equal(s.req.Fingerprint(), s.req.Fingerprint())
	s.Len(s.req.Fingerprint(), 64)
}

func (s *FingerprintSuite) TestIgnoresIdempotencyKey() {
	other := s.req
	other.IdempotencyKey = "partner-99999"

	s.Equal(s.req.Fingerprint(), other.Fingerprint(),
		"the same booking under a new key must still look like the same request")
}

func (s *FingerprintSuite) TestIgnoresSurroundingWhitespace() {
	padded := s.req
	padded.PropertyID = "  hotel-001  "
	padded.GuestFirstName = "\tTaro "
	padded.GuestLastName = " Yamada\n"

	s.Equal(s.req.Fingerprint(), padded.Fingerprint())
}

func (s *FingerprintSuite) TestIgnoresClockTimeOnDates() {
	sameDates := s.req
	sameDates.CheckIn = time.Date(2026, 9, 10, 13, 45, 0, 0, time.UTC)
	sameDates.CheckOut = time.Date(2026, 9, 12, 6, 0, 0, 0, time.UTC)

	s.Equal(s.req.Fingerprint(), sameDates.Fingerprint())
}

func (s *FingerprintSuite) TestChangesWithEveryMeaningfulField() {
	base := s.req.Fingerprint()

	cases := []struct {
		name   string
		mutate func(*model.CreateRequest)
	}{
		{"distributor", func(r *model.CreateRequest) { r.DistributorID = "distributor-002" }},
		{"property", func(r *model.CreateRequest) { r.PropertyID = "hotel-002" }},
		{"room type", func(r *model.CreateRequest) { r.RoomTypeID = "room-standard" }},
		{"check in", func(r *model.CreateRequest) { r.CheckIn = s.date("2026-09-11") }},
		{"check out", func(r *model.CreateRequest) { r.CheckOut = s.date("2026-09-13") }},
		{"guest first name", func(r *model.CreateRequest) { r.GuestFirstName = "Hanako" }},
		{"guest last name", func(r *model.CreateRequest) { r.GuestLastName = "Suzuki" }},
	}

	for _, tc := range cases {
		s.Run(tc.name, func() {
			mutated := s.req
			tc.mutate(&mutated)
			s.NotEqual(base, mutated.Fingerprint())
		})
	}
}

// Field values are delimited, so no shift of characters between two adjacent
// fields can produce the same canonical form.
func (s *FingerprintSuite) TestDoesNotCollideAcrossFieldBoundaries() {
	shifted := s.req
	shifted.GuestFirstName = "Ta"
	shifted.GuestLastName = "roYamada"

	s.NotEqual(s.req.Fingerprint(), shifted.Fingerprint())
}
