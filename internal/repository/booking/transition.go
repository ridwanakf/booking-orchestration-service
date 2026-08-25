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

// Lock, guard, update, append, return. Locking before reading the prior status
// matters because a subquery answers from the statement's own snapshot, which
// under contention is already stale. The trailing SELECT returns the marker too,
// so a status conflict is distinguishable from a superseded attempt.
const guardedTemplate = `
	WITH locked AS (
		SELECT id, status, in_flight_attempt FROM bookings WHERE id = $1 FOR UPDATE
	), moved AS (
		UPDATE bookings b
		SET status             = $2,
		    version            = b.version + 1,
		    in_flight_attempt  = NULL,
		    supplier_reference = COALESCE($4, b.supplier_reference),
		    failure_reason     = $5,
		    needs_recovery     = CASE WHEN $6 THEN FALSE ELSE b.needs_recovery END
		FROM locked l
		WHERE b.id = l.id AND l.status = $3 AND %s
		RETURNING b.id, l.status AS was, b.status AS now_status
	), logged AS (
		INSERT INTO booking_events (
			booking_id, from_status, to_status, event_type,
			attempt, request_id, supplier_status_code, supplier_reason, payload_digest)
		SELECT m.id, m.was, m.now_status, $7, $8, $9, $10, $11, $12 FROM moved m
		RETURNING booking_id
	)
	SELECT (SELECT now_status FROM moved), (SELECT status FROM locked), (SELECT in_flight_attempt FROM locked)`

// Apply performs a guarded transition and appends its lineage row in one
// statement, so the two cannot diverge. A zero-row result is the answer, not an
// error: the returned status says what was actually there.
func (r *Repo) Apply(ctx context.Context, id uuid.UUID, t repository.Transition) (model.Status, error) {
	if err := model.Transition(t.From, t.To); err != nil {
		return "", err
	}
	predicate, err := markerPredicate(t)
	if err != nil {
		return "", err
	}

	args := []any{
		id, t.To, t.From,
		t.SupplierReference, t.FailureReason, t.ClearRecoveryFlag,
		t.EventType, t.Attempt, t.RequestID, t.SupplierStatusCode, t.SupplierReason, t.PayloadDigest,
	}
	if t.Marker == repository.MarkerOwned {
		args = append(args, *t.Attempt)
	}

	var applied, current *model.Status
	var marker *int
	err = r.db.QueryRow(ctx, fmt.Sprintf(guardedTemplate, predicate), args...).Scan(&applied, &current, &marker)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("apply %s to %s: %w", t.From, t.To, err)
	}
	if applied != nil {
		return *applied, nil
	}
	if current == nil {
		return "", constant.ErrBookingNotFound
	}

	// Status matched, so the marker failed: this attempt was superseded while its
	// call was outstanding, and what the supplier told it is evidence of a second
	// reservation. Recorded, never dropped.
	if *current == t.From && t.Marker == repository.MarkerOwned {
		return *current, r.recordSupersededAttempt(ctx, id, *current, marker, t)
	}
	return *current, fmt.Errorf("%w: wanted %s, found %s", constant.ErrTransitionConflict, t.From, *current)
}

// ClearMarker ends an attempt without moving the booking, which is how a
// provably-not-sent result leaves the row in PENDING.
func (r *Repo) ClearMarker(ctx context.Context, id uuid.UUID, attempt int, e model.Event) (bool, error) {
	var cleared *bool
	err := r.db.QueryRow(ctx, `
		WITH locked AS (
			SELECT id, status, in_flight_attempt FROM bookings WHERE id = $1 FOR UPDATE
		), moved AS (
			UPDATE bookings b
			SET in_flight_attempt = NULL, version = b.version + 1
			FROM locked l
			WHERE b.id = l.id AND l.in_flight_attempt = $2
			RETURNING b.id, b.status
		), logged AS (
			INSERT INTO booking_events (
				booking_id, from_status, to_status, event_type, attempt, request_id, supplier_reason)
			SELECT id, status, status, $3, $2, $4, $5 FROM moved
			RETURNING booking_id
		)
		SELECT (SELECT TRUE FROM moved)`,
		id, attempt, e.EventType, e.RequestID, e.SupplierReason).Scan(&cleared)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("clear marker: %w", err)
	}
	return cleared != nil, nil
}

// AppendRefusal records something a supplier asserted that we declined to act
// on: from and to are the same, and there is no update to share a statement with.
func (r *Repo) AppendRefusal(ctx context.Context, id uuid.UUID, e model.Event) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO booking_events (
			booking_id, from_status, to_status, event_type,
			attempt, request_id, supplier_status_code, supplier_reason, payload_digest)
		VALUES ($1, $2, $2, $3, $4, $5, $6, $7, $8)`,
		id, e.ToStatus, e.EventType, e.Attempt, e.RequestID,
		e.SupplierStatusCode, e.SupplierReason, e.PayloadDigest)
	if err != nil {
		return fmt.Errorf("append refusal: %w", err)
	}
	return nil
}

func (r *Repo) recordSupersededAttempt(ctx context.Context, id uuid.UUID, current model.Status, marker *int, t repository.Transition) error {
	reason := fmt.Sprintf("attempt %v superseded before it could record %s", t.Attempt, t.To)
	if err := r.AppendRefusal(ctx, id, model.Event{
		ToStatus: current, EventType: model.EventConflict, Attempt: t.Attempt,
		RequestID: t.RequestID, SupplierStatusCode: t.SupplierStatusCode,
		SupplierReason: &reason, PayloadDigest: t.PayloadDigest,
	}); err != nil {
		return err
	}
	return fmt.Errorf("%w: attempt superseded, outstanding marker %v", constant.ErrAttemptSuperseded, marker)
}

func markerPredicate(t repository.Transition) (string, error) {
	switch t.Marker {
	case repository.MarkerClear:
		return "l.in_flight_attempt IS NULL", nil
	case repository.MarkerOwned:
		if t.Attempt == nil {
			return "", fmt.Errorf("%w: owned marker with no attempt", constant.ErrInvalidTransition)
		}
		return "l.in_flight_attempt = $13", nil
	case repository.MarkerSupersede:
		return "TRUE", nil
	default:
		return "", fmt.Errorf("%w: marker rule not stated", constant.ErrInvalidTransition)
	}
}
