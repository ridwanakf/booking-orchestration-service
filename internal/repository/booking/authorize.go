package booking

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ridwanakf/booking-orchestration-service/internal/constant"
	"github.com/ridwanakf/booking-orchestration-service/internal/model"
	"github.com/ridwanakf/booking-orchestration-service/internal/repository"
)

// ResolveStaleMarker turns an attempt that was never completed into the doubt it
// represents. Observing and clearing happen in one locked statement, so two
// recoverers racing the same stale marker cannot both act on it.
func (r *Repo) ResolveStaleMarker(ctx context.Context, id uuid.UUID, requestID *string) (int, bool, error) {
	var stale *int
	err := r.db.QueryRow(ctx, `
		WITH locked AS (
			SELECT id, status, in_flight_attempt FROM bookings WHERE id = $1 FOR UPDATE
		), resolved AS (
			UPDATE bookings b
			SET status = 'UNKNOWN', in_flight_attempt = NULL, version = b.version + 1, updated_at = now()
			FROM locked l
			WHERE b.id = l.id AND l.in_flight_attempt IS NOT NULL AND l.status IN ('PENDING', 'UNKNOWN')
			RETURNING b.id, l.status AS was, l.in_flight_attempt AS stale
		), logged AS (
			INSERT INTO booking_events (booking_id, from_status, to_status, event_type, attempt, request_id, supplier_reason)
			SELECT id, was, 'UNKNOWN', $2, stale, $3, $4 FROM resolved
			RETURNING booking_id
		)
		SELECT (SELECT stale FROM resolved)`,
		id, model.EventSupplierTimeout, requestID, constant.ReasonAttemptAbandoned).Scan(&stale)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, false, fmt.Errorf("resolve stale marker: %w", err)
	}
	if stale == nil {
		return 0, false, nil
	}
	return *stale, true, nil
}

// Precedes every supplier call: counts the attempt and records that one is
// outstanding, before any bytes leave. Status moves only on the first attempt,
// so a healthy booking is never written as in doubt.
func (r *Repo) Authorize(ctx context.Context, id uuid.UUID, maxAttempts int, supplierKey string, requestID *string) (repository.Authorization, error) {
	var attempt, marker, attemptsSoFar *int
	var previous *model.Status
	err := r.db.QueryRow(ctx, `
		WITH locked AS (
			SELECT id, status, supplier_attempts, in_flight_attempt
			FROM bookings WHERE id = $1 FOR UPDATE
		), moved AS (
			UPDATE bookings b
			SET supplier_attempts        = b.supplier_attempts + 1,
			    in_flight_attempt        = b.supplier_attempts + 1,
			    status                   = CASE WHEN b.status = 'RECEIVED' THEN 'PENDING' ELSE b.status END,
			    supplier_idempotency_key = COALESCE(b.supplier_idempotency_key, $3),
			    version                  = b.version + 1,
			    updated_at               = now()
			FROM locked l
			WHERE b.id = l.id
			  AND l.in_flight_attempt IS NULL
			  AND l.supplier_attempts < $2
			  AND l.status IN ('RECEIVED', 'PENDING', 'UNKNOWN')
			RETURNING b.id, l.status AS was, b.status AS now_status, b.supplier_attempts
		), logged AS (
			INSERT INTO booking_events (booking_id, from_status, to_status, event_type, attempt, request_id)
			SELECT id, was, now_status, $4, supplier_attempts, $5 FROM moved
			RETURNING booking_id
		)
		SELECT (SELECT supplier_attempts FROM moved), (SELECT status FROM locked),
		       (SELECT in_flight_attempt FROM locked), (SELECT supplier_attempts FROM locked)`,
		id, maxAttempts, supplierKey, model.EventSupplierRequest, requestID).Scan(&attempt, &previous, &marker, &attemptsSoFar)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return repository.Authorization{}, fmt.Errorf("authorize attempt: %w", err)
	}
	if previous == nil {
		return repository.Authorization{}, constant.ErrBookingNotFound
	}
	if attempt != nil {
		return repository.Authorization{Attempt: *attempt, Previous: *previous}, nil
	}

	// Three refusals look identical from the row alone, and they need opposite
	// handling: an outstanding attempt means another worker is mid-call, and
	// treating that as a spent budget would settle the booking FAILED while a
	// request is live at the supplier.
	out := repository.Authorization{Previous: *previous, Refusal: repository.RefusalBudgetSpent}
	switch {
	case previous.Settled():
		out.Refusal = repository.RefusalSettled
	case marker != nil:
		out.Refusal = repository.RefusalAttemptOutstanding
	case attemptsSoFar != nil && *attemptsSoFar < maxAttempts:
		out.Refusal = repository.RefusalSettled
	}
	return out, fmt.Errorf("%w: status %s", constant.ErrTransitionConflict, *previous)
}

// Flags for recovery while the booking is in doubt with nothing outstanding.
// Idempotent: an already-flagged booking still reports parked, because the
// caller must arm its window either way, but only the first park records it.
func (r *Repo) ParkIfUnknown(ctx context.Context, id uuid.UUID, requestID *string) (bool, error) {
	var parked *bool
	err := r.db.QueryRow(ctx, `
		WITH locked AS (
			SELECT id, status, in_flight_attempt, needs_recovery FROM bookings WHERE id = $1 FOR UPDATE
		), parked AS (
			UPDATE bookings b
			SET needs_recovery = TRUE, version = b.version + 1, updated_at = now()
			FROM locked l
			WHERE b.id = l.id AND l.status = 'UNKNOWN' AND l.in_flight_attempt IS NULL
			RETURNING b.id, b.supplier_attempts, l.needs_recovery AS was_flagged
		), logged AS (
			INSERT INTO booking_events (booking_id, from_status, to_status, event_type, attempt, request_id)
			SELECT id, 'UNKNOWN', 'UNKNOWN', $2, supplier_attempts, $3 FROM parked WHERE NOT was_flagged
			RETURNING booking_id
		)
		SELECT (SELECT TRUE FROM parked)`, id, model.EventParked, requestID).Scan(&parked)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("park booking: %w", err)
	}
	return parked != nil, nil
}

// Flag marks a booking for outcome recovery without moving it. Deliberately
// unguarded on status: a booking already flagged staying flagged is correct.
func (r *Repo) Flag(ctx context.Context, id uuid.UUID) error {
	// This does not opt out of the touch trigger: its IS NOT DISTINCT FROM test
	// is true for a self-assignment, so updated_at still moves. Flagged rows are
	// excluded from the sweep by their own predicate, which is what protects it.
	tag, err := r.db.Exec(ctx,
		`UPDATE bookings SET needs_recovery = TRUE, version = version + 1, updated_at = updated_at
		 WHERE id = $1 AND NOT needs_recovery`, id)
	if err != nil {
		return fmt.Errorf("flag booking: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Already flagged is the correct outcome, so only a missing row is an error.
		return r.mustExist(ctx, id)
	}
	return nil
}

func (r *Repo) mustExist(ctx context.Context, id uuid.UUID) error {
	var exists bool
	if err := r.db.QueryRow(ctx, `SELECT TRUE FROM bookings WHERE id = $1`, id).Scan(&exists); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return constant.ErrBookingNotFound
		}
		return fmt.Errorf("confirm booking exists: %w", err)
	}
	return nil
}
