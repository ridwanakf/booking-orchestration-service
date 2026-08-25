package supplier_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/ridwanakf/booking-orchestration-service/internal/supplier"
)

type ClientSuite struct {
	suite.Suite
	req supplier.BookRequest
}

func TestClient(t *testing.T) {
	suite.Run(t, new(ClientSuite))
}

func (s *ClientSuite) SetupTest() {
	s.req = supplier.BookRequest{
		BookingID:      "0198f2c4-6d1a-7c3e-9f4b-2f6f0a1d9b10",
		IdempotencyKey: "0198F2C46D1A7C3E9F4B2F6F0A1D9B10",
		PropertyID:     "hotel-001",
		RoomTypeID:     "room-deluxe",
		CheckIn:        time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC),
		CheckOut:       time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC),
		GuestFirstName: "Taro",
		GuestLastName:  "Yamada",
	}
}

func (s *ClientSuite) book(handler http.HandlerFunc) supplier.Result {
	srv := httptest.NewServer(handler)
	defer srv.Close()
	return supplier.NewHTTPClient(srv.URL, 2*time.Second).Book(context.Background(), s.req)
}

func (s *ClientSuite) TestConfirmed() {
	got := s.book(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"CONFIRMED","reference":"SUP-1"}`))
	})

	s.Equal(supplier.OutcomeConfirmed, got.Outcome)
	s.Equal("SUP-1", got.Reference)
}

// A recognized decline code is a business rejection whatever the HTTP status
// says. Reading the status first would call this an ambiguity and retry a
// booking the supplier has already refused.
func (s *ClientSuite) TestARecognizedDeclineInsideA5xxIsStillARejection() {
	got := s.book(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"status":"REJECTED","declineCode":"NO_AVAILABILITY"}`))
	})

	s.Equal(supplier.OutcomeRejected, got.Outcome)
	s.Equal("NO_AVAILABILITY", got.Reason)
}

// A 200 carrying an error envelope is neither success nor decline. The bytes
// arrived, so nothing about the supplier's side effects is proven.
func (s *ClientSuite) TestAnUnclassifiableAnswerIsAmbiguous() {
	cases := map[string]http.HandlerFunc{
		"200 with an error envelope": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"status":"ERROR","message":"channel manager unavailable"}`))
		},
		"confirmation with no reference": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"status":"CONFIRMED"}`))
		},
		"4xx with no decline code": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"message":"bad request"}`))
		},
		"unrecognized decline code": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"status":"REJECTED","declineCode":"SOMETHING_NEW"}`))
		},
	}

	for name, handler := range cases {
		s.Run(name, func() {
			s.Equal(supplier.OutcomeAmbiguous, s.book(handler).Outcome)
		})
	}
}

// Nothing reached the supplier, so the booking may honestly leave UNKNOWN.
// This is the only result permitted to withdraw doubt, so it is the one that
// must never be reported by mistake.
func (s *ClientSuite) TestARefusedDialIsProvablyNotSent() {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	s.Require().NoError(err)
	addr := listener.Addr().String()
	s.Require().NoError(listener.Close())

	got := supplier.NewHTTPClient("http://"+addr, time.Second).Book(context.Background(), s.req)

	s.Equal(supplier.OutcomeNotSent, got.Outcome)
}

// The request left this process, so a timeout proves nothing either way.
func (s *ClientSuite) TestATimeoutAfterTheRequestLeftIsAmbiguous() {
	got := s.book(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(5 * time.Second):
		case <-r.Context().Done():
		}
	})

	s.Equal(supplier.OutcomeAmbiguous, got.Outcome,
		"bytes reached the supplier, so doubt cannot be withdrawn")
}

// net/http replays requests carrying an Idempotency-Key header, which would put
// two creates on the wire under one counted attempt.
func (s *ClientSuite) TestTheRequestDoesNotOptIntoTransparentRetry() {
	var seen []string
	got := s.book(func(w http.ResponseWriter, r *http.Request) {
		for name := range r.Header {
			seen = append(seen, name)
		}
		_, _ = w.Write([]byte(`{"status":"CONFIRMED","reference":"SUP-1"}`))
	})

	s.Equal(supplier.OutcomeConfirmed, got.Outcome)
	s.NotContains(seen, "Idempotency-Key")
	s.Contains(seen, "X-Supplier-Idempotency")
}

func (s *ClientSuite) TestIdempotencyKeyEncoding() {
	s.Equal("0198F2C46D1A7C3E9F4B2F6F0A1D9B10",
		supplier.IdempotencyKey("0198f2c4-6d1a-7c3e-9f4b-2f6f0a1d9b10"))
}
