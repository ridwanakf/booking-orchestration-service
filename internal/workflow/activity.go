package workflow

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/ridwanakf/booking-orchestration-service/internal/constant"
	"github.com/ridwanakf/booking-orchestration-service/internal/model"
	"github.com/ridwanakf/booking-orchestration-service/internal/observability"
	"github.com/ridwanakf/booking-orchestration-service/internal/repository"
	"github.com/ridwanakf/booking-orchestration-service/internal/supplier"
)

const (
	ActivityDispatch = "LoadBookingStatus"
	ActivityAttempt  = "ProcessSupplierBooking"
	ActivityApply    = "ApplySupplierOutcome"
	ActivityPark     = "ParkUnknown"
)

// Two internal outcomes that never reach a supplier.
const (
	outcomeAlreadySettled supplier.Outcome = "ALREADY_SETTLED"
	outcomeBudgetSpent    supplier.Outcome = "BUDGET_SPENT"
	outcomeOutstanding    supplier.Outcome = "ATTEMPT_OUTSTANDING"
)

// Answer is what the supplier said, carried between the calling activity and
// the persisting one so a retry of the persist never re-sends the call.
type Answer struct {
	Outcome   supplier.Outcome
	Reference string
	Reason    string
	Attempt   int
	From      model.Status
	LatencyMS int64
}

type Activities struct {
	repo     repository.BookingRepository
	supplier supplier.Client
	cfg      Config
	log      *slog.Logger
}

func NewActivities(repo repository.BookingRepository, client supplier.Client, cfg Config, log *slog.Logger) *Activities {
	return &Activities{repo: repo, supplier: client, cfg: cfg, log: log}
}

// Dispatch is the first thing every started or restarted run does. Resolving a
// stale marker before anything else is what stops an abandoned attempt wedging
// the booking: the IS NULL guard on authorization would otherwise never clear.
func (a *Activities) Dispatch(ctx context.Context, bookingID string) (string, error) {
	id, err := uuid.Parse(bookingID)
	if err != nil {
		return "", fmt.Errorf("parse booking id: %w", err)
	}

	attempt, resolved, err := a.repo.ResolveStaleMarker(ctx, id, observability.RequestIDPtr(ctx))
	if err != nil {
		return "", err
	}
	if resolved {
		a.log.WarnContext(ctx, "an attempt was abandoned without recording an outcome",
			"event", model.EventSupplierTimeout, "booking_id", bookingID, "attempt", attempt)
	}

	b, err := a.repo.GetByID(ctx, id)
	if err != nil {
		return "", err
	}
	return string(b.Status), nil
}

// Attempt authorizes one supplier call and makes it. Authorization commits
// before any bytes leave, so a crash below this line needs no forensics.
func (a *Activities) Attempt(ctx context.Context, bookingID string) (Answer, error) {
	id, err := uuid.Parse(bookingID)
	if err != nil {
		return Answer{}, fmt.Errorf("parse booking id: %w", err)
	}

	b, err := a.repo.GetByID(ctx, id)
	if err != nil {
		return Answer{}, err
	}
	if b.Status.Settled() {
		return Answer{Outcome: outcomeAlreadySettled}, nil
	}

	auth, err := a.repo.Authorize(ctx, id, a.cfg.CreateAttempts, supplier.IdempotencyKey(bookingID), observability.RequestIDPtr(ctx))
	if err != nil {
		if !errors.Is(err, constant.ErrTransitionConflict) {
			return Answer{}, err
		}
		// These three look identical in the row and need opposite handling.
		switch auth.Refusal {
		case repository.RefusalSettled:
			return Answer{Outcome: outcomeAlreadySettled}, nil
		case repository.RefusalAttemptOutstanding:
			return Answer{Outcome: outcomeOutstanding, From: auth.Previous}, nil
		default:
			return Answer{Outcome: outcomeBudgetSpent, From: auth.Previous}, nil
		}
	}

	a.log.InfoContext(ctx, "supplier attempt",
		"event", model.EventSupplierRequest, "booking_id", bookingID,
		"attempt", auth.Attempt, "supplier_id", b.SupplierID)

	result := a.supplier.Book(ctx, supplier.BookRequest{
		BookingID:      bookingID,
		IdempotencyKey: supplier.IdempotencyKey(bookingID),
		PropertyID:     b.PropertyID,
		RoomTypeID:     b.RoomTypeID,
		CheckIn:        b.CheckIn,
		CheckOut:       b.CheckOut,
		GuestFirstName: b.GuestFirstName,
		GuestLastName:  b.GuestLastName,
	})

	// Authorization moves the first attempt out of RECEIVED and leaves every
	// later one where it was, so this is the status the outcome writes from.
	from := auth.Previous
	if from == model.StatusReceived {
		from = model.StatusPending
	}

	return Answer{
		Outcome:   result.Outcome,
		Reference: result.Reference,
		Reason:    result.Reason,
		Attempt:   auth.Attempt,
		From:      from,
		LatencyMS: result.Latency.Milliseconds(),
	}, nil
}

// Apply persists an answer the supplier already gave. Separate from the call
// because it must be safe to retry: retrying the call above would send a second
// create, and a failure there would discard a confirmation already received.
func (a *Activities) Apply(ctx context.Context, bookingID string, answer Answer) (AttemptResult, error) {
	id, err := uuid.Parse(bookingID)
	if err != nil {
		return AttemptResult{}, fmt.Errorf("parse booking id: %w", err)
	}
	return a.apply(ctx, id, answer)
}

// Park closes out an exhausted budget. There are two honest endings and they
// are not the same: a booking still in doubt parks and keeps waiting, while one
// whose every attempt proved nothing was sent is unreachable and settles.
func (a *Activities) Park(ctx context.Context, bookingID string) (bool, error) {
	id, err := uuid.Parse(bookingID)
	if err != nil {
		return false, fmt.Errorf("parse booking id: %w", err)
	}

	b, err := a.repo.GetByID(ctx, id)
	if err != nil {
		return false, err
	}
	requestID := observability.RequestIDPtr(ctx)

	if b.Status == model.StatusPending {
		reason := constant.ReasonSupplierUnreachable
		_, err := a.repo.Apply(ctx, id, repository.Transition{
			From: model.StatusPending, To: model.StatusFailed,
			Marker: repository.MarkerClear, EventType: model.EventTransition,
			FailureReason: &reason, RequestID: requestID,
		})
		if err != nil && !errors.Is(err, constant.ErrTransitionConflict) {
			return false, err
		}
		a.log.ErrorContext(ctx, "every attempt was proven not sent and the budget is spent",
			"event", model.EventTransition, "booking_id", bookingID,
			"reason", reason, "attempts", b.SupplierAttempts)
		return false, nil
	}

	parked, err := a.repo.ParkIfUnknown(ctx, id, requestID)
	if err != nil || !parked {
		return false, err
	}
	a.log.ErrorContext(ctx, "booking parked with its retry budget exhausted",
		"event", model.EventParked, "booking_id", bookingID, "attempts", b.SupplierAttempts)
	return true, nil
}

func (a *Activities) apply(ctx context.Context, id uuid.UUID, answer Answer) (AttemptResult, error) {
	requestID := observability.RequestIDPtr(ctx)
	attempt := answer.Attempt

	switch answer.Outcome {
	case outcomeAlreadySettled:
		return AttemptResult{Outcome: OutcomeSettled}, nil
	case outcomeOutstanding:
		// Another worker holds the marker and is mid-call. Treating this as a
		// spent budget would settle the booking while a request is live at the
		// supplier, so this run exits and leaves the owner to finish.
		return AttemptResult{Outcome: OutcomeExit, Reason: "another attempt is outstanding"}, nil
	case outcomeBudgetSpent:
		return AttemptResult{Outcome: OutcomeBudgetSpent, Reason: "attempt budget exhausted"}, nil

	case supplier.OutcomeConfirmed:
		return a.settle(ctx, id, answer, model.StatusConfirmed, &answer.Reference, nil, requestID)
	case supplier.OutcomeRejected:
		return a.settle(ctx, id, answer, model.StatusRejected, nil, &answer.Reason, requestID)

	case supplier.OutcomeNotSent:
		// The one result that may leave the row in PENDING: nothing left, so
		// the marker clears and the status does not move.
		a.event(ctx, model.EventSupplierRequest, id, attempt, "outcome", "not_sent", "reason", answer.Reason)
		if _, err := a.repo.ClearMarker(ctx, id, attempt, model.Event{
			EventType: model.EventSupplierRequest, RequestID: requestID, SupplierReason: &answer.Reason,
		}); err != nil {
			return AttemptResult{}, err
		}
		return AttemptResult{Outcome: OutcomeNotSent, Attempt: attempt, Reason: answer.Reason}, nil

	case supplier.OutcomePreSendFailure:
		return a.preSendFailure(ctx, id, answer, requestID)

	default:
		return a.ambiguous(ctx, id, answer, requestID)
	}
}

func (a *Activities) settle(ctx context.Context, id uuid.UUID, answer Answer, to model.Status, reference, reason *string, requestID *string) (AttemptResult, error) {
	a.event(ctx, model.EventSupplierResponse, id, answer.Attempt, "outcome", string(to), "latency_ms", answer.LatencyMS)

	status := string(answer.Outcome)
	_, err := a.repo.Apply(ctx, id, repository.Transition{
		From: answer.From, To: to,
		Marker: repository.MarkerOwned, Attempt: &answer.Attempt,
		EventType:         model.EventSupplierResponse,
		SupplierReference: reference, FailureReason: reason,
		ClearRecoveryFlag: true, RequestID: requestID, SupplierStatusCode: &status,
	})
	switch {
	case errors.Is(err, constant.ErrAttemptSuperseded):
		// Recorded as a conflict by the repository, because an answer for a
		// superseded attempt is evidence of a second reservation.
		return AttemptResult{Outcome: OutcomeSettled, Reason: "superseded"}, nil
	case errors.Is(err, constant.ErrTransitionConflict):
		return AttemptResult{Outcome: OutcomeSettled}, nil
	case err != nil:
		return AttemptResult{}, err
	}

	out := OutcomeConfirmed
	if to == model.StatusRejected {
		out = OutcomeRejected
	}
	return AttemptResult{Outcome: out, Attempt: answer.Attempt, Reason: answer.Reason}, nil
}

// The row is already where it needs to be if doubt existed before, so an
// ambiguous answer from UNKNOWN only clears the marker.
func (a *Activities) ambiguous(ctx context.Context, id uuid.UUID, answer Answer, requestID *string) (AttemptResult, error) {
	a.event(ctx, model.EventSupplierTimeout, id, answer.Attempt, "reason", answer.Reason)

	if answer.From == model.StatusUnknown {
		if _, err := a.repo.ClearMarker(ctx, id, answer.Attempt, model.Event{
			EventType: model.EventSupplierTimeout, RequestID: requestID, SupplierReason: &answer.Reason,
		}); err != nil {
			return AttemptResult{}, err
		}
		return AttemptResult{Outcome: OutcomeAmbiguous, Attempt: answer.Attempt, Reason: answer.Reason}, nil
	}

	_, err := a.repo.Apply(ctx, id, repository.Transition{
		From: model.StatusPending, To: model.StatusUnknown,
		Marker: repository.MarkerOwned, Attempt: &answer.Attempt,
		EventType: model.EventSupplierTimeout, RequestID: requestID, SupplierReason: &answer.Reason,
	})
	if err != nil && !errors.Is(err, constant.ErrTransitionConflict) && !errors.Is(err, constant.ErrAttemptSuperseded) {
		return AttemptResult{}, err
	}
	return AttemptResult{Outcome: OutcomeAmbiguous, Attempt: answer.Attempt, Reason: answer.Reason}, nil
}

// Our own bug. From PENDING nothing was ever in doubt, so FAILED is honest.
// From UNKNOWN, doubt an earlier attempt created outranks our own bug.
func (a *Activities) preSendFailure(ctx context.Context, id uuid.UUID, answer Answer, requestID *string) (AttemptResult, error) {
	a.event(ctx, model.EventTransition, id, answer.Attempt, "outcome", "pre_send_failure", "reason", answer.Reason)

	if answer.From == model.StatusUnknown {
		if _, err := a.repo.ClearMarker(ctx, id, answer.Attempt, model.Event{
			EventType: model.EventTransition, RequestID: requestID, SupplierReason: &answer.Reason,
		}); err != nil {
			return AttemptResult{}, err
		}
		return AttemptResult{Outcome: OutcomeInDoubt, Attempt: answer.Attempt, Reason: answer.Reason}, nil
	}

	reason := constant.ReasonRequestBuildFailed
	_, err := a.repo.Apply(ctx, id, repository.Transition{
		From: model.StatusPending, To: model.StatusFailed,
		Marker: repository.MarkerOwned, Attempt: &answer.Attempt,
		EventType: model.EventTransition, FailureReason: &reason,
		RequestID: requestID, SupplierReason: &answer.Reason,
	})
	if err != nil && !errors.Is(err, constant.ErrTransitionConflict) && !errors.Is(err, constant.ErrAttemptSuperseded) {
		return AttemptResult{}, err
	}
	return AttemptResult{Outcome: OutcomeFailed, Attempt: answer.Attempt, Reason: answer.Reason}, nil
}

func (a *Activities) event(ctx context.Context, event string, id uuid.UUID, attempt int, kv ...any) {
	args := append([]any{"event", event, "booking_id", id.String(), "attempt", attempt}, kv...)
	a.log.InfoContext(ctx, "supplier attempt outcome", args...)
}
