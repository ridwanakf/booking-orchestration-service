package workflow_test

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"
	"go.temporal.io/sdk/testsuite"
	"go.uber.org/mock/gomock"

	"github.com/ridwanakf/booking-orchestration-service/internal/constant"
	repomocks "github.com/ridwanakf/booking-orchestration-service/internal/repository/mocks"
	suppliermocks "github.com/ridwanakf/booking-orchestration-service/internal/supplier/mocks"
	"github.com/ridwanakf/booking-orchestration-service/internal/workflow"
)

type WorkflowSuite struct {
	suite.Suite
	testsuite.WorkflowTestSuite
	env       *testsuite.TestWorkflowEnvironment
	bookingID string
	params    constant.WorkflowParams
}

func TestBookingWorkflow(t *testing.T) {
	suite.Run(t, new(WorkflowSuite))
}

func (s *WorkflowSuite) SetupTest() {
	s.env = s.NewTestWorkflowEnvironment()
	s.bookingID = uuid.NewString()
	s.params = constant.WorkflowParams{
		CreateAttempts:     2,
		RetrieveDelaysSec:  []int{30, 60},
		ParkTimeoutSec:     int((24 * time.Hour).Seconds()),
		ActivityTimeoutSec: 105,
	}

	ctrl := gomock.NewController(s.T())
	acts := workflow.NewActivities(
		repomocks.NewMockBookingRepository(ctrl),
		suppliermocks.NewMockClient(ctrl),
		workflow.Config{CreateAttempts: 2},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	workflow.Register(s.env, acts)
}

func (s *WorkflowSuite) AfterTest(_, _ string) {
	s.env.AssertExpectations(s.T())
}

func (s *WorkflowSuite) run() {
	s.env.ExecuteWorkflow(constant.WorkflowBooking, s.bookingID, s.params)
	s.True(s.env.IsWorkflowCompleted())
	s.NoError(s.env.GetWorkflowError())
}

func (s *WorkflowSuite) onLoad(status string) {
	s.env.OnActivity(workflow.ActivityDispatch, mock.Anything, s.bookingID).Return(status, nil).Once()
}

func (s *WorkflowSuite) onAttempt(outcome workflow.Outcome, times int) {
	s.env.OnActivity(workflow.ActivityAttempt, mock.Anything, s.bookingID).
		Return(workflow.Answer{Attempt: 1}, nil).Times(times)
	s.env.OnActivity(workflow.ActivityApply, mock.Anything, s.bookingID, mock.Anything).
		Return(workflow.AttemptResult{Outcome: outcome}, nil).Times(times)
}

// Entry dispatch: a run started against an already-settled booking must not
// send anything. The activity is registered with a failing body rather than
// asserted absent afterwards, because AssertNotCalled on an activity the test
// never registered passes whether or not it ran.
func (s *WorkflowSuite) TestASettledBookingSendsNothingOnEntry() {
	s.onLoad("CONFIRMED")
	s.env.OnActivity(workflow.ActivityAttempt, mock.Anything, s.bookingID).
		Run(func(mock.Arguments) {
			s.Fail("a settled booking must never reach the supplier")
		}).Return(workflow.Answer{}, nil).Maybe()

	s.run()
}

func (s *WorkflowSuite) TestAConfirmedAttemptEndsTheWorkflow() {
	s.onLoad("RECEIVED")
	s.onAttempt(workflow.OutcomeConfirmed, 1)

	s.run()
}

func (s *WorkflowSuite) TestARejectionIsNeverRetried() {
	s.onLoad("RECEIVED")
	s.onAttempt(workflow.OutcomeRejected, 1)

	s.run()
}

// The budget is spent on ambiguity, so the booking parks rather than being
// called a failure. Time skipping makes the retrieve delay free.
func (s *WorkflowSuite) TestAmbiguityExhaustsTheBudgetAndParks() {
	s.onLoad("RECEIVED")
	s.onAttempt(workflow.OutcomeAmbiguous, 2)
	s.env.OnActivity(workflow.ActivityPark, mock.Anything, s.bookingID).Return(true, nil).Once()

	s.run()
}

// Another worker holds the marker and is mid-call, so this run exits rather
// than parking. Parking here would settle a booking whose request is live at
// the supplier.
func (s *WorkflowSuite) TestAnOutstandingAttemptEndsThisRunWithoutParking() {
	s.onLoad("PENDING")
	s.onAttempt(workflow.OutcomeExit, 1)

	s.run()
}

// Our own failure on a booking already in doubt must reach park, not end the
// run. Ending it would leave the booking unreachable by a late callback and
// invisible to an operator.
func (s *WorkflowSuite) TestAPreSendFailureWhileInDoubtStillParks() {
	s.onLoad("RECEIVED")
	s.onAttempt(workflow.OutcomeInDoubt, 1)
	s.env.OnActivity(workflow.ActivityPark, mock.Anything, s.bookingID).Return(true, nil).Once()

	s.run()
}

// A parked booking is not abandoned: a late callback resolves it and the run
// completes without waiting out the timer.
func (s *WorkflowSuite) TestAParkedBookingIsResolvedByALateCallback() {
	s.onLoad("RECEIVED")
	s.onAttempt(workflow.OutcomeAmbiguous, 2)
	s.env.OnActivity(workflow.ActivityPark, mock.Anything, s.bookingID).Return(true, nil).Once()
	s.env.RegisterDelayedCallback(func() {
		s.env.SignalWorkflow(constant.SignalSupplierOutcome, "CONFIRMED")
	}, 2*time.Minute)

	s.run()
}

// Nothing resolves it, so the bounded timer ends the run rather than leaking an
// execution forever.
func (s *WorkflowSuite) TestAParkedBookingGivesUpOnItsTimer() {
	s.onLoad("RECEIVED")
	s.onAttempt(workflow.OutcomeAmbiguous, 2)
	s.env.OnActivity(workflow.ActivityPark, mock.Anything, s.bookingID).Return(true, nil).Once()

	s.run()
}

// Park reported that nothing needs waiting for, either because the booking
// settled or because it was unreachable and is now FAILED.
func (s *WorkflowSuite) TestNoWaitWhenParkHasNothingToHoldOpen() {
	s.onLoad("RECEIVED")
	s.onAttempt(workflow.OutcomeAmbiguous, 2)
	s.env.OnActivity(workflow.ActivityPark, mock.Anything, s.bookingID).Return(false, nil).Once()

	s.run()
}

// The schedule comes from workflow input, not process configuration, so a
// replay reproduces the history it recorded even if the deployment changed.
func (s *WorkflowSuite) TestTheScheduleComesFromInput() {
	s.params.CreateAttempts = 1
	s.onLoad("RECEIVED")
	s.onAttempt(workflow.OutcomeAmbiguous, 1)
	s.env.OnActivity(workflow.ActivityPark, mock.Anything, s.bookingID).Return(true, nil).Once()

	s.run()
}
