package booking_test

import (
	"context"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"

	"github.com/ridwanakf/booking-orchestration-service/internal/constant"
	"github.com/ridwanakf/booking-orchestration-service/internal/model"
	"github.com/ridwanakf/booking-orchestration-service/internal/repository"
	"github.com/ridwanakf/booking-orchestration-service/internal/service"
)

func ptr(v string) *string { return &v }

func (s *ServiceSuite) stored(status model.Status, reference *string) *model.Booking {
	return &model.Booking{ID: s.id, Status: status, SupplierReference: reference}
}

func (s *ServiceSuite) confirm() service.SupplierOutcome {
	return service.SupplierOutcome{Reference: "MOCK-1", Status: "CONFIRMED"}
}

func (s *ServiceSuite) TestCallbackAppliesASupplierOutcome() {
	s.repo.EXPECT().GetByID(gomock.Any(), s.id).Return(s.stored(model.StatusPending, nil), nil)
	s.repo.EXPECT().Apply(gomock.Any(), s.id, gomock.Any()).
		DoAndReturn(func(_ context.Context, _ uuid.UUID, t repository.Transition) (model.Status, error) {
			s.Equal(repository.MarkerSupersede, t.Marker,
				"supplier truth outranks an attempt still waiting to find out")
			s.True(t.ClearRecoveryFlag, "a resolved booking has nothing left for recovery")
			return model.StatusConfirmed, nil
		})
	s.orch.EXPECT().SignalOutcome(gomock.Any(), s.id, "CONFIRMED").Return(nil)

	got, err := s.svc.ApplyCallback(s.ctx, s.id, s.confirm())

	s.Require().NoError(err)
	s.True(got.Applied)
}

// Redelivery is normal supplier behaviour, so the second one is a no-op rather
// than a conflict. No event id is needed: the state itself is the dedupe key.
func (s *ServiceSuite) TestARedeliveredCallbackIsANoOp() {
	s.repo.EXPECT().GetByID(gomock.Any(), s.id).Return(s.stored(model.StatusConfirmed, ptr("MOCK-1")), nil)

	got, err := s.svc.ApplyCallback(s.ctx, s.id, s.confirm())

	s.Require().NoError(err)
	s.False(got.Applied)
	s.True(got.Duplicate)
}

// A retry moving the row between the read and the write does not make a real
// confirmation inapplicable. Refusing it would discard supplier truth and leave
// an attempt in flight for a room already confirmed.
func (s *ServiceSuite) TestACallbackRetriesOnceAgainstTheStatusThatWon() {
	s.repo.EXPECT().GetByID(gomock.Any(), s.id).Return(s.stored(model.StatusPending, nil), nil)
	gomock.InOrder(
		s.repo.EXPECT().Apply(gomock.Any(), s.id, gomock.Any()).
			Return(model.StatusUnknown, constant.ErrTransitionConflict),
		s.repo.EXPECT().Apply(gomock.Any(), s.id, gomock.Any()).
			DoAndReturn(func(_ context.Context, _ uuid.UUID, t repository.Transition) (model.Status, error) {
				s.Equal(model.StatusUnknown, t.From, "the retry guards on the status that actually won")
				return model.StatusConfirmed, nil
			}),
	)
	s.orch.EXPECT().SignalOutcome(gomock.Any(), s.id, "CONFIRMED").Return(nil)

	got, err := s.svc.ApplyCallback(s.ctx, s.id, s.confirm())

	s.Require().NoError(err)
	s.True(got.Applied)
}

func (s *ServiceSuite) TestACallbackContradictingASettledOutcomeIsRefusedAndFlagged() {
	s.repo.EXPECT().GetByID(gomock.Any(), s.id).Return(s.stored(model.StatusConfirmed, ptr("MOCK-1")), nil)
	s.repo.EXPECT().Flag(gomock.Any(), s.id).Return(nil)

	_, err := s.svc.ApplyCallback(s.ctx, s.id, service.SupplierOutcome{Reference: "MOCK-1", Status: "REJECTED"})

	s.ErrorIs(err, constant.ErrCallbackConflict)
}

// A different reference for the same state means two reservations exist. That
// is the most expensive thing that can happen, so it never applies quietly.
func (s *ServiceSuite) TestASecondReferenceForTheSameStateIsAConflict() {
	s.repo.EXPECT().GetByID(gomock.Any(), s.id).Return(s.stored(model.StatusConfirmed, ptr("MOCK-1")), nil)
	s.repo.EXPECT().Flag(gomock.Any(), s.id).Return(nil)

	_, err := s.svc.ApplyCallback(s.ctx, s.id, service.SupplierOutcome{Reference: "MOCK-2-OTHER", Status: "CONFIRMED"})

	s.ErrorIs(err, constant.ErrCallbackConflict)
}

// Nothing was authorized, so an honest supplier cannot know this booking. It is
// refused but deliberately NOT flagged: flagging is for supplier truth we could
// not apply, and here there is no truth to preserve.
func (s *ServiceSuite) TestACallbackWhileStillReceivedIsRefusedWithoutFlagging() {
	s.repo.EXPECT().GetByID(gomock.Any(), s.id).Return(s.stored(model.StatusReceived, nil), nil)
	s.repo.EXPECT().AppendRefusal(gomock.Any(), s.id, gomock.Any()).Return(nil)

	_, err := s.svc.ApplyCallback(s.ctx, s.id, s.confirm())

	s.ErrorIs(err, constant.ErrCallbackConflict)
}

// Supplier truth this version cannot represent. The booking is looked up first
// so the refusal is recorded against a real row, and an unknown id cannot be
// used to flag arbitrary bookings.
func (s *ServiceSuite) TestAStatusOutsideTheVocabularyIsFlaggedAgainstARealBooking() {
	s.repo.EXPECT().GetByID(gomock.Any(), s.id).Return(s.stored(model.StatusPending, nil), nil)
	s.repo.EXPECT().Flag(gomock.Any(), s.id).Return(nil)

	_, err := s.svc.ApplyCallback(s.ctx, s.id, service.SupplierOutcome{Reference: "MOCK-1", Status: "ON_REQUEST"})

	s.ErrorIs(err, constant.ErrUnsupportedSupplierStatus)
}

func (s *ServiceSuite) TestACallbackForAnUnknownBooking() {
	s.repo.EXPECT().GetByID(gomock.Any(), gomock.Any()).Return(nil, constant.ErrBookingNotFound)

	_, err := s.svc.ApplyCallback(s.ctx, uuid.New(), s.confirm())

	s.ErrorIs(err, constant.ErrBookingNotFound)
}

// The row transition is primary. A signal failure means the parked run wakes on
// its timer instead, which is slower but not wrong.
func (s *ServiceSuite) TestASignalFailureDoesNotFailTheCallback() {
	s.repo.EXPECT().GetByID(gomock.Any(), s.id).Return(s.stored(model.StatusUnknown, nil), nil)
	s.repo.EXPECT().Apply(gomock.Any(), s.id, gomock.Any()).Return(model.StatusConfirmed, nil)
	s.orch.EXPECT().SignalOutcome(gomock.Any(), s.id, "CONFIRMED").Return(constant.ErrBookingNotFound)

	got, err := s.svc.ApplyCallback(s.ctx, s.id, s.confirm())

	s.Require().NoError(err)
	s.True(got.Applied)
}
