package booking_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	"github.com/ridwanakf/booking-orchestration-service/internal/constant"
	"github.com/ridwanakf/booking-orchestration-service/internal/model"
	orchmocks "github.com/ridwanakf/booking-orchestration-service/internal/orchestrator/mocks"
	repomocks "github.com/ridwanakf/booking-orchestration-service/internal/repository/mocks"
	booking "github.com/ridwanakf/booking-orchestration-service/internal/service/booking"
)

type ServiceSuite struct {
	suite.Suite
	ctrl *gomock.Controller
	repo *repomocks.MockBookingRepository
	orch *orchmocks.MockOrchestrator
	svc  *booking.Service
	ctx  context.Context
	req  model.CreateRequest
	id   uuid.UUID
}

func TestService(t *testing.T) {
	suite.Run(t, new(ServiceSuite))
}

func (s *ServiceSuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
	s.repo = repomocks.NewMockBookingRepository(s.ctrl)
	s.orch = orchmocks.NewMockOrchestrator(s.ctrl)
	s.svc = booking.New(s.repo, s.orch, "mock-supplier", slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.ctx = context.Background()
	s.id = uuid.New()
	s.req = model.CreateRequest{
		DistributorID:  "distributor-001",
		IdempotencyKey: "partner-12345",
		PropertyID:     "hotel-001",
		RoomTypeID:     "room-deluxe-confirm",
		CheckIn:        time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC),
		CheckOut:       time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC),
		GuestFirstName: "Taro",
		GuestLastName:  "Yamada",
	}
}

// Echoes back whatever the service asked to insert, as a real insert would.
func (s *ServiceSuite) expectInsert() {
	s.repo.EXPECT().InsertOrLoad(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, b model.Booking, _ *string) (*model.Booking, bool, error) {
			b.CreatedAt = time.Now()
			b.UpdatedAt = b.CreatedAt
			return &b, true, nil
		})
}

func (s *ServiceSuite) expectNoDuplicateSuspect() {
	s.repo.EXPECT().FindPriorByFingerprint(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(uuid.Nil, false, nil)
}

func (s *ServiceSuite) TestCreatePersistsAndStartsWorkflow() {
	s.expectInsert()
	s.expectNoDuplicateSuspect()
	s.orch.EXPECT().StartBooking(gomock.Any(), gomock.Any()).Return(nil)

	b, created, err := s.svc.Create(s.ctx, s.req)

	s.Require().NoError(err)
	s.True(created)
	s.Equal(model.StatusReceived, b.Status)
	s.Equal("mock-supplier", b.SupplierID)
	s.Equal(s.req.Fingerprint(), b.RequestFingerprint)
}

// The row is committed before the workflow starts, so an orchestrator outage
// costs time to confirmation and never the booking itself.
func (s *ServiceSuite) TestCreateSucceedsWhenTheOrchestratorIsDown() {
	s.expectInsert()
	s.expectNoDuplicateSuspect()
	s.orch.EXPECT().StartBooking(gomock.Any(), gomock.Any()).Return(errors.New("temporal unavailable"))

	b, created, err := s.svc.Create(s.ctx, s.req)

	s.Require().NoError(err)
	s.True(created)
	s.Equal(model.StatusReceived, b.Status)
}

func (s *ServiceSuite) TestCreateReplaysTheSamePayload() {
	existing := &model.Booking{
		ID:                 uuid.New(),
		DistributorID:      s.req.DistributorID,
		IdempotencyKey:     s.req.IdempotencyKey,
		RequestFingerprint: s.req.Fingerprint(),
		Status:             model.StatusConfirmed,
	}
	s.repo.EXPECT().InsertOrLoad(gomock.Any(), gomock.Any(), gomock.Any()).Return(existing, false, nil)

	b, created, err := s.svc.Create(s.ctx, s.req)

	s.Require().NoError(err)
	s.False(created)
	s.Equal(existing.ID, b.ID)
	s.Equal(model.StatusConfirmed, b.Status, "a replay reports current state, not the state at creation")
}

func (s *ServiceSuite) TestCreateRefusesAReusedKeyWithADifferentPayload() {
	s.repo.EXPECT().InsertOrLoad(gomock.Any(), gomock.Any(), gomock.Any()).Return(&model.Booking{
		ID:                 uuid.New(),
		IdempotencyKey:     s.req.IdempotencyKey,
		RequestFingerprint: "a-different-booking-entirely",
		Status:             model.StatusReceived,
	}, false, nil)

	_, _, err := s.svc.Create(s.ctx, s.req)

	s.ErrorIs(err, constant.ErrIdempotencyKeyReused)
}

// The duplicate-suspect signal is observability, so a hit must not change the
// outcome and a lookup failure must not fail the create.
func (s *ServiceSuite) TestCreateIsUnaffectedByADuplicateSuspectHit() {
	s.expectInsert()
	s.repo.EXPECT().FindPriorByFingerprint(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(uuid.New(), true, nil)
	s.orch.EXPECT().StartBooking(gomock.Any(), gomock.Any()).Return(nil)

	b, created, err := s.svc.Create(s.ctx, s.req)

	s.Require().NoError(err)
	s.True(created)
	s.Equal(model.StatusReceived, b.Status)
}

func (s *ServiceSuite) TestCreateIsUnaffectedByADuplicateSuspectLookupFailure() {
	s.expectInsert()
	s.repo.EXPECT().FindPriorByFingerprint(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(uuid.Nil, false, errors.New("query failed"))
	s.orch.EXPECT().StartBooking(gomock.Any(), gomock.Any()).Return(nil)

	_, created, err := s.svc.Create(s.ctx, s.req)

	s.Require().NoError(err)
	s.True(created)
}

func (s *ServiceSuite) TestGet() {
	want := &model.Booking{ID: uuid.New(), Status: model.StatusPending}
	s.repo.EXPECT().GetByID(gomock.Any(), want.ID).Return(want, nil)

	got, err := s.svc.Get(s.ctx, want.ID)

	s.Require().NoError(err)
	s.Equal(want, got)
}

func (s *ServiceSuite) TestGetNotFound() {
	s.repo.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(nil, constant.ErrBookingNotFound)

	_, err := s.svc.Get(s.ctx, uuid.New())

	s.ErrorIs(err, constant.ErrBookingNotFound)
}
