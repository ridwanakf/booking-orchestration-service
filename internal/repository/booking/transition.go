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

// The shape every guarded write follows: lock, guard, update, append, return.
// Locking before reading the prior status matters because a plain subquery
// answers from the statement's own snapshot, so under contention it reports a
// status that was already stale, and callers branch on that value.
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
		    needs_recovery     = CASE WHEN $6 THEN FALSE ELSE b.needs_recovery END,
		    updated_at         = now()
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
	SELECT (SELECT now_status FROM moved), (SELECT status FROM locked)`

func (r *Repo) markerPredicate(t repository.Transition) string {
	switch t.Marker {
	case repository.MarkerClear:
		return "l.in_flight_attempt IS NULL"
	case repository.MarkerOwned:
		return "l.in_flight_attempt = $13"
	default:
		return "TRUE"
	}
}

// Apply performs a guarded transition. A zero-row result is not an error, it is
// the answer: the returned status says what was actually there, so a lost race
// is named rather than silent.
func (r *Repo) Apply(ctx context.Context, id uuid.UUID, t repository.Transition) (model.Status, error) {
	if err := model.Transition(t.From, t.To); err != nil {
		return "", err
	}

	args := []any{
		id, t.To, t.From,
		t.SupplierReference, t.FailureReason, t.ClearRecoveryFlag,
		t.EventType, t.Attempt, t.RequestID, t.SupplierStatusCode, t.SupplierReason, t.PayloadDigest,
	}
	if t.Marker == repository.MarkerOwned {
		if t.Attempt == nil {
			return "", fmt.Errorf("%w: owned marker with no attempt", constant.ErrInvalidTransition)
		}
		args = append(args, *t.Attempt)
	}

	var applied, current *model.Status
	err := r.db.QueryRow(ctx, fmt.Sprintf(guardedTemplate, r.markerPredicate(t)), args...).Scan(&applied, &current)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("apply %s to %s: %w", t.From, t.To, err)
	}
	if applied != nil {
		return *applied, nil
	}
	if current == nil {
		return "", constant.ErrBookingNotFound
	}
	return *current, fmt.Errorf("%w: wanted %s, found %s", constant.ErrTransitionConflict, t.From, *current)
}

// AppendRefusal records something a supplier asserted that we declined to act
// on. It writes no transition, so from and to are the same, and it is the one
// lineage row with no update to share a statement with.
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
