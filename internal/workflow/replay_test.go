package workflow_test

import (
	"testing"

	"github.com/stretchr/testify/suite"
	sdkworker "go.temporal.io/sdk/worker"
	sdkworkflow "go.temporal.io/sdk/workflow"

	"github.com/ridwanakf/booking-orchestration-service/internal/constant"
	"github.com/ridwanakf/booking-orchestration-service/internal/workflow"
)

// Replay is the contract that makes a durable workflow durable: the code in
// this build must produce, event for event, the command sequence recorded when
// an execution first ran. Break it and a running booking cannot resume, which
// matters most for a parked one holding a 24 hour timer.
//
// The histories under testdata/ are real, exported from a live server.
type ReplaySuite struct {
	suite.Suite
}

func TestReplay(t *testing.T) {
	suite.Run(t, new(ReplaySuite))
}

func (s *ReplaySuite) replay(history string) error {
	replayer := sdkworker.NewWorkflowReplayer()
	replayer.RegisterWorkflowWithOptions(workflow.BookingWorkflow,
		sdkworkflow.RegisterOptions{Name: constant.WorkflowBooking})
	return replayer.ReplayWorkflowHistoryFromJSONFile(nil, history)
}

func (s *ReplaySuite) TestASettledBookingReplays() {
	s.NoError(s.replay("testdata/confirmed.json"))
}

// The one that would hurt: a parked booking is mid-execution on a long timer,
// so it is the booking most likely to be alive across a deployment.
func (s *ReplaySuite) TestAParkedBookingReplays() {
	s.NoError(s.replay("testdata/parked.json"))
}
