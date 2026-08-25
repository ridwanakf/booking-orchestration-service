package orchestrator

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"

	"github.com/ridwanakf/booking-orchestration-service/internal/constant"
)

type Temporal struct {
	client    client.Client
	taskQueue string
	params    constant.WorkflowParams
}

func NewTemporal(c client.Client, taskQueue string, params constant.WorkflowParams) *Temporal {
	return &Temporal{client: c, taskQueue: taskQueue, params: params}
}

// The workflow id is the booking id, which is what makes duplicate starts
// harmless: every path that can start a booking converges on one execution.
// An already-started error is therefore success, not failure.
func (t *Temporal) StartBooking(ctx context.Context, bookingID uuid.UUID) (bool, error) {
	_, err := t.client.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:        bookingID.String(),
		TaskQueue: t.taskQueue,
	}, constant.WorkflowBooking, bookingID.String(), t.params)

	if _, ok := errors.AsType[*serviceerror.WorkflowExecutionAlreadyStarted](err); ok {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("start booking workflow: %w", err)
	}
	return true, nil
}

// The row transition is primary; this only wakes a parked run early. A booking
// whose run already completed is resolved either way, so a missing execution is
// not an error worth failing the callback for.
func (t *Temporal) SignalOutcome(ctx context.Context, bookingID uuid.UUID, status string) error {
	err := t.client.SignalWorkflow(ctx, bookingID.String(), "", constant.SignalSupplierOutcome, status)

	if _, ok := errors.AsType[*serviceerror.NotFound](err); ok {
		return nil
	}
	if err != nil {
		return fmt.Errorf("signal booking workflow: %w", err)
	}
	return nil
}
