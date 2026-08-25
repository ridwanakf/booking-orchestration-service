-- +goose Up
CREATE TABLE bookings (
    id                       UUID PRIMARY KEY,
    distributor_id           TEXT        NOT NULL,
    idempotency_key          TEXT        NOT NULL,
    request_fingerprint      TEXT        NOT NULL,

    supplier_id              TEXT        NOT NULL,
    supplier_idempotency_key TEXT,
    supplier_reference       TEXT,

    property_id              TEXT        NOT NULL,
    room_type_id             TEXT        NOT NULL,
    check_in                 DATE        NOT NULL,
    check_out                DATE        NOT NULL,
    guest_first_name         TEXT        NOT NULL,
    guest_last_name          TEXT        NOT NULL,

    status                   TEXT        NOT NULL,
    failure_reason           TEXT,
    needs_recovery           BOOLEAN     NOT NULL DEFAULT FALSE,
    supplier_attempts        INTEGER     NOT NULL DEFAULT 0,

    -- The attempt number of a supplier call that is outstanding, null otherwise.
    -- Doubt lives here rather than in status, which is what keeps a healthy
    -- booking out of UNKNOWN. Written before any bytes leave.
    in_flight_attempt        INTEGER,

    version                  INTEGER     NOT NULL DEFAULT 0,
    created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT bookings_distributor_idempotency_key UNIQUE (distributor_id, idempotency_key),

    -- A status outside the state machine wedges a row permanently: every
    -- outgoing transition is refused and no recovery query selects it.
    CONSTRAINT bookings_status_known CHECK (status IN (
        'RECEIVED', 'PENDING', 'UNKNOWN', 'CONFIRMED', 'REJECTED', 'FAILED', 'CANCELLED'
    )),
    CONSTRAINT bookings_stay_is_positive CHECK (check_out > check_in),
    CONSTRAINT bookings_marker_within_budget CHECK (
        in_flight_attempt IS NULL OR in_flight_attempt <= supplier_attempts
    )
);

-- The supplier key is the booking id encoded down to a supplier's length and
-- charset limit, so two bookings can encode to the same value. Without this the
-- supplier would deduplicate them into one reservation and a booking would be
-- lost silently.
CREATE UNIQUE INDEX idx_bookings_supplier_idempotency_key
    ON bookings (supplier_id, supplier_idempotency_key)
    WHERE supplier_idempotency_key IS NOT NULL;

-- Suppliers commonly correlate by their own reference rather than ours.
CREATE INDEX idx_bookings_supplier_reference ON bookings (supplier_reference)
    WHERE supplier_reference IS NOT NULL;

-- Every create checks the fingerprint for the duplicate-suspect signal.
CREATE INDEX idx_bookings_fingerprint ON bookings (distributor_id, request_fingerprint);

-- The sweep's predicate. Partial on unsettled rows because most of the table is
-- settled; deliberately not partial on needs_recovery, which would hide exactly
-- the rows a refusal happened to flag.
CREATE INDEX idx_bookings_sweep ON bookings (updated_at)
    WHERE status IN ('RECEIVED', 'PENDING', 'UNKNOWN');

-- Append-only lineage. seq is a global sequence, not a per-booking MAX: under
-- Read Committed a writer blocked on the row lock still computes its maximum
-- from a pre-block snapshot, so two writers on one booking collide.
CREATE TABLE booking_events (
    seq                  BIGSERIAL PRIMARY KEY,
    booking_id           UUID        NOT NULL REFERENCES bookings (id),
    occurred_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    from_status          TEXT,
    to_status            TEXT        NOT NULL,
    event_type           TEXT        NOT NULL,
    attempt              INTEGER,
    request_id           TEXT,
    supplier_status_code TEXT,
    supplier_reason      TEXT,
    payload_digest       TEXT
);

CREATE INDEX idx_booking_events_booking ON booking_events (booking_id, seq);

CREATE TABLE distributor_api_keys (
    key_id         TEXT PRIMARY KEY,
    distributor_id TEXT        NOT NULL,
    secret_hash    TEXT        NOT NULL,
    label          TEXT        NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at     TIMESTAMPTZ
);

CREATE INDEX idx_distributor_api_keys_distributor ON distributor_api_keys (distributor_id);

-- Staleness is measured entirely from updated_at, so a write path that forgets
-- to set it either restarts a healthy booking forever or hides a stuck one.
-- Only fills it in when the statement did not set it itself, so an operator
-- repairing a row can still choose the value.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION bookings_touch_updated_at() RETURNS TRIGGER AS $$
BEGIN
    IF NEW.updated_at IS NOT DISTINCT FROM OLD.updated_at THEN
        NEW.updated_at = now();
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER bookings_set_updated_at
    BEFORE UPDATE ON bookings
    FOR EACH ROW EXECUTE FUNCTION bookings_touch_updated_at();

-- +goose Down
DROP TRIGGER bookings_set_updated_at ON bookings;
DROP FUNCTION bookings_touch_updated_at();
DROP TABLE distributor_api_keys;
DROP TABLE booking_events;
DROP TABLE bookings;
