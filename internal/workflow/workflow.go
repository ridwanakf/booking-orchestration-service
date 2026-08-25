package workflow

import (
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/ridwanakf/booking-orchestration-service/internal/constant"
)

type Config struct {
	CreateAttempts       int
	RetrieveDelays       []time.Duration
	ParkTimeout          time.Duration
	ActivityStartToClose time.Duration
}

// Outcome is the closed contract between an attempt and the workflow. Every
// branch below is reachable and every value is handled.
type Outcome string

const (
	OutcomeConfirmed Outcome = "CONFIRMED"
	OutcomeRejected  Outcome = "REJECTED"
	OutcomeAmbiguous Outcome = "AMBIGUOUS"
	OutcomeNotSent   Outcome = "NOT_SENT"
	OutcomeFailed    Outcome = "FAILED"
	OutcomeSettled   Outcome = "ALREADY_SETTLED"
	// Our own failure on a booking that was already in doubt. No further
	// attempt would be honest, but the doubt cannot be closed either.
	OutcomeInDoubt Outcome = "IN_DOUBT"
	// Another worker holds the marker, so this run leaves it to finish.
	OutcomeExit Outcome = "EXIT"
	// The lifetime budget is spent; nothing more may be sent.
	OutcomeBudgetSpent Outcome = "BUDGET_SPENT"
)

type AttemptResult struct {
	Outcome Outcome
	Attempt int
	Reason  string
}

// BookingWorkflow drives one booking to a settled state or to a parked one. It
// holds no business state of its own: every decision reads from the row, so a
// replay, a restart, or a second run all reach the same place.
func BookingWorkflow(ctx workflow.Context, bookingID string, params constant.WorkflowParams) error {
	cfg := fromParams(params)
	log := workflow.GetLogger(ctx)

	// Persisting and reading are safe to retry; calling the supplier is not.
	// A retried call is a second create that no attempt counter ever saw.
	idempotent := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: cfg.ActivityStartToClose,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
	})
	sending := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: cfg.ActivityStartToClose,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 1},
	})
	ctx = idempotent

	// Entry dispatch. A restarted or swept run must never assume it is the
	// first: the row already knows whether this booking is finished.
	var status string
	if err := workflow.ExecuteActivity(ctx, ActivityDispatch, bookingID).Get(ctx, &status); err != nil {
		return err
	}
	if settled(status) {
		log.Info("booking already settled on entry", "status", status)
		return nil
	}

	for attempt := 1; attempt <= cfg.CreateAttempts; attempt++ {
		var answer Answer
		if err := workflow.ExecuteActivity(sending, ActivityAttempt, bookingID).Get(ctx, &answer); err != nil {
			return err
		}

		var result AttemptResult
		if err := workflow.ExecuteActivity(idempotent, ActivityApply, bookingID, answer).Get(ctx, &result); err != nil {
			return err
		}

		switch result.Outcome {
		case OutcomeConfirmed, OutcomeRejected, OutcomeSettled, OutcomeFailed:
			return nil
		case OutcomeInDoubt, OutcomeBudgetSpent:
			return park(ctx, cfg, bookingID)
		case OutcomeExit:
			return nil
		case OutcomeAmbiguous, OutcomeNotSent:
			if attempt < cfg.CreateAttempts {
				_ = workflow.Sleep(ctx, delay(cfg, attempt))
			}
		}
	}

	return park(ctx, cfg, bookingID)
}

// The budget is spent and the booking is still in doubt. Parking is not giving
// up: the row is flagged for outcome recovery and the run stays alive for a
// bounded window so a late callback resolves it without human involvement.
func park(ctx workflow.Context, cfg Config, bookingID string) error {
	var parked bool
	if err := workflow.ExecuteActivity(ctx, ActivityPark, bookingID).Get(ctx, &parked); err != nil {
		return err
	}
	if !parked {
		return nil
	}

	signalled := workflow.GetSignalChannel(ctx, constant.SignalSupplierOutcome)
	timer := workflow.NewTimer(ctx, cfg.ParkTimeout)

	selector := workflow.NewSelector(ctx)
	selector.AddReceive(signalled, func(c workflow.ReceiveChannel, _ bool) {
		var status string
		c.Receive(ctx, &status)
		workflow.GetLogger(ctx).Info("parked booking resolved by callback", "status", status)
	})
	selector.AddFuture(timer, func(workflow.Future) {
		workflow.GetLogger(ctx).Info("parked booking reached its timer with no outcome")
	})
	selector.Select(ctx)

	return nil
}

// Deterministic on purpose: the tests assert exact schedules, and jitter buys
// nothing at one workflow per booking.
func delay(cfg Config, attempt int) time.Duration {
	if len(cfg.RetrieveDelays) == 0 {
		return 30 * time.Second
	}
	if attempt-1 < len(cfg.RetrieveDelays) {
		return cfg.RetrieveDelays[attempt-1]
	}
	return cfg.RetrieveDelays[len(cfg.RetrieveDelays)-1]
}

func fromParams(p constant.WorkflowParams) Config {
	delays := make([]time.Duration, 0, len(p.RetrieveDelaysSec))
	for _, sec := range p.RetrieveDelaysSec {
		delays = append(delays, time.Duration(sec)*time.Second)
	}
	return Config{
		CreateAttempts:       p.CreateAttempts,
		RetrieveDelays:       delays,
		ParkTimeout:          time.Duration(p.ParkTimeoutSec) * time.Second,
		ActivityStartToClose: time.Duration(p.ActivityTimeoutSec) * time.Second,
	}
}

func settled(status string) bool {
	switch status {
	case "CONFIRMED", "REJECTED", "FAILED", "CANCELLED":
		return true
	}
	return false
}
