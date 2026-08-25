package booking

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ridwanakf/booking-orchestration-service/internal/constant"
	"github.com/ridwanakf/booking-orchestration-service/internal/model"
)

// ResolveStaleMarker turns an attempt that was never completed into the doubt it
// represents. Reading the marker and clearing it happen inside one locked
// statement, so observing and acting are simultaneous and two recoverers racing
// the same stale marker cannot both act on it. Reports the attempt it resolved.
func (r *Repo) ResolveStaleMarker(ctx context.Context, id uuid.UUID) (int, bool, error) {
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
			INSERT INTO booking_events (booking_id, from_status, to_status, event_type, attempt, supplier_reason)
			SELECT id, was, 'UNKNOWN', $2, stale, 'attempt abandoned without an outcome' FROM resolved
			RETURNING booking_id
		)
		SELECT (SELECT stale FROM resolved)`,
		id, model.EventSupplierTimeout).Scan(&stale)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, false, fmt.Errorf("resolve stale marker: %w", err)
	}
	if stale == nil {
		return 0, false, nil
	}
	return *stale, true, nil
}

// Authorize is the write that must precede every supplier call. It counts the
// attempt and records that a call is outstanding, in one statement, before any
// bytes leave. Status moves only on the first attempt, out of RECEIVED, so a
// healthy booking is never written as in doubt.
//
// The caller resolves any stale marker first; the IS NULL guard here is what
// stops two attempts running at once.
func (r *Repo) Authorize(ctx context.Context, id uuid.UUID, maxAttempts int, supplierKey string, requestID *string) (int, model.Status, error) {
	var attempt *int
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
		SELECT (SELECT supplier_attempts FROM moved), (SELECT status FROM locked)`,
		id, maxAttempts, supplierKey, model.EventSupplierRequest, requestID).Scan(&attempt, &previous)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, "", fmt.Errorf("authorize attempt: %w", err)
	}
	if previous == nil {
		return 0, "", constant.ErrBookingNotFound
	}
	if attempt != nil {
		return *attempt, *previous, nil
	}
	return 0, *previous, fmt.Errorf("%w: status %s", constant.ErrTransitionConflict, *previous)
}

// ParkIfUnknown flags a booking for outcome recovery only while it is still in
// doubt and nothing is outstanding, so a booking that settles or starts another
// attempt in the gap is never flagged. Reports whether it parked.
func (r *Repo) ParkIfUnknown(ctx context.Context, id uuid.UUID) (bool, error) {
	var parked *bool
	err := r.db.QueryRow(ctx, `
		WITH locked AS (
			SELECT id, status, in_flight_attempt FROM bookings WHERE id = $1 FOR UPDATE
		), parked AS (
			UPDATE bookings b
			SET needs_recovery = TRUE, version = b.version + 1, updated_at = now()
			FROM locked l
			WHERE b.id = l.id AND l.status = 'UNKNOWN' AND l.in_flight_attempt IS NULL
			RETURNING b.id, b.supplier_attempts
		), logged AS (
			INSERT INTO booking_events (booking_id, from_status, to_status, event_type, attempt)
			SELECT id, 'UNKNOWN', 'UNKNOWN', $2, supplier_attempts FROM parked
			RETURNING booking_id
		)
		SELECT (SELECT TRUE FROM parked)`, id, model.EventParked).Scan(&parked)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("park booking: %w", err)
	}
	return parked != nil, nil
}

// Flag marks a booking for outcome recovery without moving it. Deliberately
// unguarded on status: a booking already flagged staying flagged is correct.
func (r *Repo) Flag(ctx context.Context, id uuid.UUID) error {
	tag, err := r.db.Exec(ctx,
		`UPDATE bookings SET needs_recovery = TRUE, version = version + 1, updated_at = now() WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("flag booking: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return constant.ErrBookingNotFound
	}
	return nil
}
