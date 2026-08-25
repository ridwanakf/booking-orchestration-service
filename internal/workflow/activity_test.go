package workflow_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	"github.com/ridwanakf/booking-orchestration-service/internal/constant"
	"github.com/ridwanakf/booking-orchestration-service/internal/model"
	"github.com/ridwanakf/booking-orchestration-service/internal/repository"
	repomocks "github.com/ridwanakf/booking-orchestration-service/internal/repository/mocks"
	"github.com/ridwanakf/booking-orchestration-service/internal/supplier"
	suppliermocks "github.com/ridwanakf/booking-orchestration-service/internal/supplier/mocks"
	"github.com/ridwanakf/booking-orchestration-service/internal/workflow"
)

const attemptBudget = 2

type ActivitySuite struct {
	suite.Suite
	ctrl     *gomock.Controller
	repo     *repomocks.MockBookingRepository
	supplier *suppliermocks.MockClient
	act      *workflow.Activities
	ctx      context.Context
	id       uuid.UUID
}

func TestActivities(t *testing.T) {
	suite.Run(t, new(ActivitySuite))
}

func (s *ActivitySuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
	s.repo = repomocks.NewMockBookingRepository(s.ctrl)
	s.supplier = suppliermocks.NewMockClient(s.ctrl)
	s.act = workflow.NewActivities(s.repo, s.supplier,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.ctx = context.Background()
	s.id = uuid.New()
}

func (s *ActivitySuite) booking(status model.Status) *model.Booking {
	return &model.Booking{
		ID: s.id, Status: status, SupplierID: "mock-supplier",
		PropertyID: "hotel-001", RoomTypeID: "room-x",
		CheckIn:  time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC),
		CheckOut: time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC),
	}
}

// The workflow runs these two in sequence, so the tests do too.
func (s *ActivitySuite) attempt(previous model.Status, answer supplier.Result) workflow.AttemptResult {
	s.repo.EXPECT().GetByID(gomock.Any(), s.id).Return(s.booking(previous), nil)
	s.repo.EXPECT().Authorize(gomock.Any(), s.id, 2, gomock.Any(), gomock.Any()).
		Return(repository.Authorization{Attempt: 1, Previous: previous}, nil)
	s.supplier.EXPECT().Book(gomock.Any(), gomock.Any()).Return(answer)

	got, err := s.act.Attempt(s.ctx, s.id.String(), attemptBudget)
	s.Require().NoError(err)

	applied, err := s.act.Apply(s.ctx, s.id.String(), got)
	s.Require().NoError(err)
	return applied
}

func (s *ActivitySuite) expectApply(to model.Status) *gomock.Call {
	return s.repo.EXPECT().Apply(gomock.Any(), s.id, gomock.Any()).
		DoAndReturn(func(_ context.Context, _ uuid.UUID, t repository.Transition) (model.Status, error) {
			s.Equal(to, t.To)
			s.Equal(repository.MarkerOwned, t.Marker, "only the attempt that set the marker may record its outcome")
			s.Require().NotNil(t.Attempt)
			return to, nil
		})
}

func (s *ActivitySuite) TestConfirmedSettlesWithTheReference() {
	s.expectApply(model.StatusConfirmed)

	got := s.attempt(model.StatusReceived, supplier.Result{Outcome: supplier.OutcomeConfirmed, Reference: "SUP-1"})

	s.Equal(workflow.OutcomeConfirmed, got.Outcome)
}

func (s *ActivitySuite) TestRejectedIsTerminal() {
	s.expectApply(model.StatusRejected)

	got := s.attempt(model.StatusPending, supplier.Result{Outcome: supplier.OutcomeRejected, Reason: "NO_AVAILABILITY"})

	s.Equal(workflow.OutcomeRejected, got.Outcome)
}

// A first ambiguity is what moves a booking into doubt.
func (s *ActivitySuite) TestAmbiguityFromPendingWritesDoubt() {
	s.expectApply(model.StatusUnknown)

	got := s.attempt(model.StatusPending, supplier.Result{Outcome: supplier.OutcomeAmbiguous, Reason: "deadline exceeded"})

	s.Equal(workflow.OutcomeAmbiguous, got.Outcome)
}

// The row already says UNKNOWN, so a second ambiguity only ends the attempt.
// Writing UNKNOWN again would be a self-edge the state machine refuses.
func (s *ActivitySuite) TestAmbiguityFromUnknownOnlyClearsTheMarker() {
	s.repo.EXPECT().ClearMarker(gomock.Any(), s.id, 1, gomock.Any()).Return(true, nil)

	got := s.attempt(model.StatusUnknown, supplier.Result{Outcome: supplier.OutcomeAmbiguous})

	s.Equal(workflow.OutcomeAmbiguous, got.Outcome)
}

// The one result that leaves a booking in PENDING, which is the state that
// makes supplier_unreachable an honest terminal answer.
func (s *ActivitySuite) TestProvablyNotSentClearsTheMarkerAndKeepsTheStatus() {
	s.repo.EXPECT().ClearMarker(gomock.Any(), s.id, 1, gomock.Any()).Return(true, nil)

	got := s.attempt(model.StatusPending, supplier.Result{Outcome: supplier.OutcomeNotSent, Reason: "connection refused"})

	s.Equal(workflow.OutcomeNotSent, got.Outcome)
}

func (s *ActivitySuite) TestPreSendFailureDependsOnWhetherTheBookingWasAlreadyInDoubt() {
	s.Run("never in doubt: fails honestly", func() {
		s.expectApply(model.StatusFailed)

		got := s.attempt(model.StatusPending, supplier.Result{Outcome: supplier.OutcomePreSendFailure, Reason: "encode request"})

		s.Equal(workflow.OutcomeFailed, got.Outcome)
	})

	s.Run("already in doubt: routes to park", func() {
		s.repo.EXPECT().ClearMarker(gomock.Any(), s.id, 1, gomock.Any()).Return(true, nil)

		got := s.attempt(model.StatusUnknown, supplier.Result{Outcome: supplier.OutcomePreSendFailure, Reason: "encode request"})

		s.Equal(workflow.OutcomeInDoubt, got.Outcome, "our own bug never outranks doubt an earlier attempt created")
	})
}

// A callback superseded this attempt while its call was outstanding. The
// repository records what the supplier said, because that answer is evidence of
// a second reservation. The workflow simply stops.
func (s *ActivitySuite) TestASupersededAttemptStopsWithoutOverwriting() {
	s.repo.EXPECT().Apply(gomock.Any(), s.id, gomock.Any()).
		Return(model.StatusConfirmed, constant.ErrAttemptSuperseded)

	got := s.attempt(model.StatusUnknown, supplier.Result{Outcome: supplier.OutcomeConfirmed, Reference: "SUP-2"})

	s.Equal(workflow.OutcomeSettled, got.Outcome)
}

// The interleaving that would double-book: another worker holds the marker and
// is mid-call. Treating that as a spent budget would settle the booking FAILED
// while a request is live at the supplier.
func (s *ActivitySuite) TestAnOutstandingAttemptMakesThisRunExitRatherThanPark() {
	s.repo.EXPECT().GetByID(gomock.Any(), s.id).Return(s.booking(model.StatusPending), nil)
	s.repo.EXPECT().Authorize(gomock.Any(), s.id, 2, gomock.Any(), gomock.Any()).
		Return(repository.Authorization{Previous: model.StatusPending, Refusal: repository.RefusalAttemptOutstanding},
			constant.ErrTransitionConflict)

	answer, err := s.act.Attempt(s.ctx, s.id.String(), attemptBudget)
	s.Require().NoError(err)
	got, err := s.act.Apply(s.ctx, s.id.String(), answer)

	s.Require().NoError(err)
	s.Equal(workflow.OutcomeExit, got.Outcome)
}

func (s *ActivitySuite) TestASpentBudgetParks() {
	s.repo.EXPECT().GetByID(gomock.Any(), s.id).Return(s.booking(model.StatusUnknown), nil)
	s.repo.EXPECT().Authorize(gomock.Any(), s.id, 2, gomock.Any(), gomock.Any()).
		Return(repository.Authorization{Previous: model.StatusUnknown, Refusal: repository.RefusalBudgetSpent},
			constant.ErrTransitionConflict)

	answer, err := s.act.Attempt(s.ctx, s.id.String(), attemptBudget)
	s.Require().NoError(err)
	got, err := s.act.Apply(s.ctx, s.id.String(), answer)

	s.Require().NoError(err)
	s.Equal(workflow.OutcomeBudgetSpent, got.Outcome)
}

func (s *ActivitySuite) TestASettledBookingIsNeverSentAgain() {
	s.repo.EXPECT().GetByID(gomock.Any(), s.id).Return(s.booking(model.StatusConfirmed), nil)

	answer, err := s.act.Attempt(s.ctx, s.id.String(), attemptBudget)
	s.Require().NoError(err)
	got, err := s.act.Apply(s.ctx, s.id.String(), answer)

	s.Require().NoError(err)
	s.Equal(workflow.OutcomeSettled, got.Outcome)
}

// Resolving a stale marker before anything else is what stops an abandoned
// attempt wedging the booking: the IS NULL guard would never clear otherwise.
func (s *ActivitySuite) TestDispatchResolvesAStaleMarkerFirst() {
	gomock.InOrder(
		s.repo.EXPECT().ResolveStaleMarker(gomock.Any(), s.id, gomock.Any()).Return(2, true, nil),
		s.repo.EXPECT().GetByID(gomock.Any(), s.id).Return(s.booking(model.StatusUnknown), nil),
	)

	status, err := s.act.Dispatch(s.ctx, s.id.String())

	s.Require().NoError(err)
	s.Equal("UNKNOWN", status)
}

func (s *ActivitySuite) TestParkSettlesUnreachableWhenEveryAttemptProvedNotSent() {
	s.repo.EXPECT().GetByID(gomock.Any(), s.id).Return(s.booking(model.StatusPending), nil)
	s.repo.EXPECT().Apply(gomock.Any(), s.id, gomock.Any()).
		DoAndReturn(func(_ context.Context, _ uuid.UUID, t repository.Transition) (model.Status, error) {
			s.Equal(model.StatusFailed, t.To)
			s.Require().NotNil(t.FailureReason)
			s.Equal(constant.ReasonSupplierUnreachable, *t.FailureReason)
			s.Equal(repository.MarkerClear, t.Marker, "nothing may be outstanding when we call a booking unreachable")
			return model.StatusFailed, nil
		})

	waited, err := s.act.Park(s.ctx, s.id.String())

	s.Require().NoError(err)
	s.False(waited, "an unreachable booking is settled, so there is nothing to wait for")
}

func (s *ActivitySuite) TestParkFlagsABookingStillInDoubt() {
	s.repo.EXPECT().GetByID(gomock.Any(), s.id).Return(s.booking(model.StatusUnknown), nil)
	s.repo.EXPECT().ParkIfUnknown(gomock.Any(), s.id, gomock.Any()).Return(true, nil)

	waited, err := s.act.Park(s.ctx, s.id.String())

	s.Require().NoError(err)
	s.True(waited)
}
