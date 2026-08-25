package booking

//go:generate mockgen -destination=mocks/repository.go -source=repository.go -package=mocks

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ridwanakf/booking-orchestration-service/internal/constant"
	"github.com/ridwanakf/booking-orchestration-service/internal/model"
	"github.com/ridwanakf/booking-orchestration-service/internal/repository"
)

const columns = `id, distributor_id, idempotency_key, request_fingerprint,
	supplier_id, supplier_idempotency_key, supplier_reference,
	property_id, room_type_id, check_in, check_out, guest_first_name, guest_last_name,
	status, failure_reason, needs_recovery, supplier_attempts, in_flight_attempt, version,
	created_at, updated_at`

// Narrow enough that a test can substitute it, wide enough for everything the
// repository does. *pgxpool.Pool satisfies it as-is.
type DB interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

var _ repository.BookingRepository = (*Repo)(nil)

type Repo struct {
	db DB
}

func New(db DB) *Repo {
	return &Repo{db: db}
}

// InsertOrLoad resolves the create race in the database rather than in the
// process: the loser of two concurrent identical creates reads the winner's row
// instead of failing. Reports whether this call was the one that inserted.
func (r *Repo) InsertOrLoad(ctx context.Context, b model.Booking, requestID *string) (*model.Booking, bool, error) {
	inserted, err := scan(r.db.QueryRow(ctx, `
		WITH created AS (
			INSERT INTO bookings (
				id, distributor_id, idempotency_key, request_fingerprint,
				supplier_id, property_id, room_type_id,
				check_in, check_out, guest_first_name, guest_last_name, status
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
			ON CONFLICT (distributor_id, idempotency_key) DO NOTHING
			RETURNING `+columns+`
		), logged AS (
			INSERT INTO booking_events (booking_id, from_status, to_status, event_type, request_id)
			SELECT id, NULL, status, $13, $14 FROM created
			RETURNING booking_id
		)
		SELECT `+columns+` FROM created`,
		b.ID, b.DistributorID, b.IdempotencyKey, b.RequestFingerprint,
		b.SupplierID, b.PropertyID, b.RoomTypeID,
		b.CheckIn, b.CheckOut, b.GuestFirstName, b.GuestLastName, b.Status,
		model.EventCreated, requestID,
	))
	if err == nil {
		return inserted, true, nil
	}
	if !errors.Is(err, constant.ErrBookingNotFound) {
		return nil, false, fmt.Errorf("insert booking: %w", err)
	}

	existing, err := r.GetByKey(ctx, b.DistributorID, b.IdempotencyKey)
	if err != nil {
		return nil, false, err
	}
	return existing, false, nil
}

func (r *Repo) GetByID(ctx context.Context, id uuid.UUID) (*model.Booking, error) {
	return scan(r.db.QueryRow(ctx, `SELECT `+columns+` FROM bookings WHERE id = $1`, id))
}

func (r *Repo) GetByKey(ctx context.Context, distributorID, key string) (*model.Booking, error) {
	return scan(r.db.QueryRow(ctx,
		`SELECT `+columns+` FROM bookings WHERE distributor_id = $1 AND idempotency_key = $2`,
		distributorID, key))
}

// FindPriorByFingerprint powers the duplicate-suspect signal: the same booking
// arriving under a fresh key. It never blocks a create, so a miss is not an error.
func (r *Repo) FindPriorByFingerprint(ctx context.Context, distributorID, fingerprint, excludeKey string, within time.Duration) (uuid.UUID, bool, error) {
	var id uuid.UUID
	err := r.db.QueryRow(ctx, `
		SELECT id FROM bookings
		WHERE distributor_id = $1 AND request_fingerprint = $2 AND idempotency_key <> $3
		  AND created_at > now() - $4::interval
		ORDER BY created_at
		LIMIT 1`,
		distributorID, fingerprint, excludeKey, within.String(),
	).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("find prior by fingerprint: %w", err)
	}
	return id, true, nil
}

// FindStale selects bookings that have gone quiet. A stale marker is the
// fastest signal: it proves no call can still be running, so those rows are
// restarted long before the age-only thresholds would notice. Parked rows are
// skipped because they are outcome recovery's, not the sweep's.
func (r *Repo) FindStale(ctx context.Context, t repository.StaleThresholds, limit int) ([]uuid.UUID, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id FROM bookings
		WHERE status IN ('RECEIVED', 'PENDING', 'UNKNOWN')
		  AND NOT (needs_recovery AND status = 'UNKNOWN')
		  AND (
		        (in_flight_attempt IS NOT NULL AND updated_at < now() - $1::interval)
		     OR (status = 'RECEIVED'           AND updated_at < now() - $2::interval)
		     OR (in_flight_attempt IS NULL     AND updated_at < now() - $3::interval)
		      )
		ORDER BY updated_at
		LIMIT $4`,
		t.Marker.String(), t.Received.String(), t.InFlight.String(), limit)
	if err != nil {
		return nil, fmt.Errorf("find stale bookings: %w", err)
	}
	defer rows.Close()

	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan stale booking: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Events returns a booking's lineage oldest first.
func (r *Repo) Events(ctx context.Context, id uuid.UUID) ([]model.Event, error) {
	rows, err := r.db.Query(ctx, `
		SELECT seq, booking_id, occurred_at, from_status, to_status, event_type,
		       attempt, request_id, supplier_status_code, supplier_reason, payload_digest
		FROM booking_events WHERE booking_id = $1 ORDER BY seq`, id)
	if err != nil {
		return nil, fmt.Errorf("read lineage: %w", err)
	}
	defer rows.Close()

	var events []model.Event
	for rows.Next() {
		var e model.Event
		if err := rows.Scan(&e.Seq, &e.BookingID, &e.OccurredAt, &e.FromStatus, &e.ToStatus,
			&e.EventType, &e.Attempt, &e.RequestID, &e.SupplierStatusCode, &e.SupplierReason, &e.PayloadDigest); err != nil {
			return nil, fmt.Errorf("scan lineage: %w", err)
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

func scan(row pgx.Row) (*model.Booking, error) {
	var b model.Booking
	err := row.Scan(
		&b.ID, &b.DistributorID, &b.IdempotencyKey, &b.RequestFingerprint,
		&b.SupplierID, &b.SupplierIdempotencyKey, &b.SupplierReference,
		&b.PropertyID, &b.RoomTypeID, &b.CheckIn, &b.CheckOut, &b.GuestFirstName, &b.GuestLastName,
		&b.Status, &b.FailureReason, &b.NeedsRecovery, &b.SupplierAttempts, &b.InFlightAttempt, &b.Version,
		&b.CreatedAt, &b.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, constant.ErrBookingNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan booking: %w", err)
	}
	return &b, nil
}
