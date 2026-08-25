package workflow

import (
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/workflow"

	"github.com/ridwanakf/booking-orchestration-service/internal/constant"
)

// nomock: satisfied by the SDK's worker and test environment, so tests use the real thing.
type Registry interface {
	RegisterWorkflowWithOptions(w any, options workflow.RegisterOptions)
	RegisterActivityWithOptions(a any, options activity.RegisterOptions)
}

func Register(r Registry, a *Activities) {
	r.RegisterWorkflowWithOptions(BookingWorkflow, workflow.RegisterOptions{Name: constant.WorkflowBooking})
	r.RegisterActivityWithOptions(a.Dispatch, activity.RegisterOptions{Name: ActivityDispatch})
	r.RegisterActivityWithOptions(a.Attempt, activity.RegisterOptions{Name: ActivityAttempt})
	r.RegisterActivityWithOptions(a.Apply, activity.RegisterOptions{Name: ActivityApply})
	r.RegisterActivityWithOptions(a.Park, activity.RegisterOptions{Name: ActivityPark})
}
