//go:build integration

package booking_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/suite"

	"github.com/ridwanakf/booking-orchestration-service/internal/model"
	"github.com/ridwanakf/booking-orchestration-service/internal/repository"
	repo "github.com/ridwanakf/booking-orchestration-service/internal/repository/booking"
)

const defaultDSN = "postgres://booking:booking@localhost:55432/booking?sslmode=disable"

// Runs the guarded statements, the CHECK constraints and the touch trigger
// against a real database. The mocked unit tests assert what the code sends;
// only these assert what PostgreSQL does with it.
type IntegrationSuite struct {
	suite.Suite
	pool *pgxpool.Pool
	repo *repo.Repo
	ctx  context.Context
}

func TestIntegration(t *testing.T) {
	suite.Run(t, new(IntegrationSuite))
}

func (s *IntegrationSuite) SetupSuite() {
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		dsn = defaultDSN
	}
	s.ctx = context.Background()

	pool, err := pgxpool.New(s.ctx, dsn)
	s.Require().NoError(err)
	s.Require().NoError(pool.Ping(s.ctx), "these tests need the compose stack up: make up")
	s.pool = pool
	s.repo = repo.New(pool)
}

func (s *IntegrationSuite) TearDownSuite() {
	if s.pool != nil {
		s.pool.Close()
	}
}

// The zero value is deliberately not a marker rule, so a caller that forgets to
// say what it expects of the marker is refused rather than defaulted.
func (s *IntegrationSuite) TestAnUnstatedMarkerRuleIsRefused() {
	b := s.seed()

	_, err := s.repo.Apply(s.ctx, b.ID, repository.Transition{
		From: model.StatusReceived, To: model.StatusPending,
		Marker: repository.MarkerUnset, EventType: model.EventTransition,
	})

	s.Error(err, "the zero marker rule must never be treated as a default")
}

func (s *IntegrationSuite) TestAGuardedTransitionRefusesFromTheWrongState() {
	b := s.seed()

	applied, err := s.repo.Apply(s.ctx, b.ID, repository.Transition{
		From: model.StatusPending, To: model.StatusConfirmed,
		Marker: repository.MarkerClear, EventType: model.EventTransition,
	})

	s.Error(err, "a legal edge from a state the row is not in must still be refused")
	s.Equal(model.StatusReceived, applied, "the refusal must report what was actually there")
}

func (s *IntegrationSuite) TestOnlyTheAttemptThatSetTheMarkerCanClearIt() {
	b := s.seed()
	auth, err := s.repo.Authorize(s.ctx, b.ID, 2, "key-"+b.ID.String(), nil)
	s.Require().NoError(err)
	s.Require().Equal(1, auth.Attempt)

	cleared, err := s.repo.ClearMarker(s.ctx, b.ID, 2, model.Event{EventType: model.EventTransition})
	s.Require().NoError(err)
	s.False(cleared, "a different attempt must not clear a marker it does not own")

	cleared, err = s.repo.ClearMarker(s.ctx, b.ID, 1, model.Event{EventType: model.EventTransition})
	s.Require().NoError(err)
	s.True(cleared)
}

func (s *IntegrationSuite) TestASecondAttemptIsRefusedWhileOneIsOutstanding() {
	b := s.seed()
	_, err := s.repo.Authorize(s.ctx, b.ID, 2, "key-"+b.ID.String(), nil)
	s.Require().NoError(err)

	auth, err := s.repo.Authorize(s.ctx, b.ID, 2, "key-"+b.ID.String(), nil)

	s.Error(err, "a call may not be authorized while another is in flight")
	s.Equal(repository.RefusalAttemptOutstanding, auth.Refusal,
		"the refusal must be distinguishable from a spent budget: one means wait, the other means stop")
}

func (s *IntegrationSuite) TestTheAttemptBudgetIsEnforcedByTheDatabase() {
	b := s.seed()
	for i := 1; i <= 2; i++ {
		auth, err := s.repo.Authorize(s.ctx, b.ID, 2, "key-"+b.ID.String(), nil)
		s.Require().NoError(err, "attempt %d should be authorized", i)
		_, err = s.repo.ClearMarker(s.ctx, b.ID, auth.Attempt, model.Event{EventType: model.EventTransition})
		s.Require().NoError(err)
	}

	auth, err := s.repo.Authorize(s.ctx, b.ID, 2, "key-"+b.ID.String(), nil)

	s.Error(err)
	s.Equal(repository.RefusalBudgetSpent, auth.Refusal)
}

func (s *IntegrationSuite) TestTheIdempotencyKeyIsUniquePerDistributor() {
	first := s.seed()

	second := s.newBooking()
	second.DistributorID = first.DistributorID
	second.IdempotencyKey = first.IdempotencyKey
	loaded, created, err := s.repo.InsertOrLoad(s.ctx, second, nil)

	s.Require().NoError(err)
	s.False(created, "the constraint, not the application, must resolve a duplicate create")
	s.Equal(first.ID, loaded.ID)
}

// Documents behaviour that contradicts what Flag's own comment claimed: a
// self-assignment does not opt out of the touch trigger, because the trigger's
// IS NOT DISTINCT FROM test is true for it. Flagged rows stay out of the sweep
// through FindStale's own predicate instead.
func (s *IntegrationSuite) TestFlaggingMovesTheStalenessClock() {
	b := s.seed()
	before := s.updatedAt(b.ID)
	time.Sleep(10 * time.Millisecond)

	s.Require().NoError(s.repo.Flag(s.ctx, b.ID))

	s.True(s.updatedAt(b.ID).After(before),
		"if this ever starts passing as equal, Flag's opt-out works and the comment can be restored")
}

func (s *IntegrationSuite) TestParkingTwiceReportsParkedButRecordsOneEvent() {
	b := s.seed()
	s.mustApply(b.ID, model.StatusReceived, model.StatusPending)
	s.mustApply(b.ID, model.StatusPending, model.StatusUnknown)

	first, err := s.repo.ParkIfUnknown(s.ctx, b.ID, nil)
	s.Require().NoError(err)
	second, err := s.repo.ParkIfUnknown(s.ctx, b.ID, nil)
	s.Require().NoError(err)

	s.True(first)
	s.True(second, "the caller must arm its recovery window either way")
	s.Equal(1, s.countEvents(b.ID, model.EventParked), "only the first park is a state change")
}

func (s *IntegrationSuite) TestLineageSequenceIsUniqueAcrossConcurrentBookings() {
	a, c := s.seed(), s.seed()

	done := make(chan error, 2)
	for _, id := range []uuid.UUID{a.ID, c.ID} {
		go func(id uuid.UUID) {
			_, err := s.repo.Apply(s.ctx, id, repository.Transition{
				From: model.StatusReceived, To: model.StatusPending,
				Marker: repository.MarkerClear, EventType: model.EventSupplierRequest,
			})
			done <- err
		}(id)
	}
	s.Require().NoError(<-done)
	s.Require().NoError(<-done)

	seqs := map[int64]bool{}
	for _, id := range []uuid.UUID{a.ID, c.ID} {
		events, err := s.repo.Events(s.ctx, id)
		s.Require().NoError(err)
		for _, e := range events {
			s.False(seqs[e.Seq], "seq %d was issued twice; a per-booking MAX+1 is unsafe here", e.Seq)
			seqs[e.Seq] = true
		}
	}
}

func (s *IntegrationSuite) TestAnEventIsWrittenWithTheTransitionOrNotAtAll() {
	b := s.seed()

	_, err := s.repo.Apply(s.ctx, b.ID, repository.Transition{
		From: model.StatusPending, To: model.StatusConfirmed,
		Marker: repository.MarkerClear, EventType: model.EventTransition,
	})
	s.Require().Error(err)

	s.Equal(0, s.countEvents(b.ID, model.EventTransition),
		"a refused transition must leave no lineage row, or the ledger records something that did not happen")
}

func (s *IntegrationSuite) seed() model.Booking {
	b := s.newBooking()
	stored, created, err := s.repo.InsertOrLoad(s.ctx, b, nil)
	s.Require().NoError(err)
	s.Require().True(created)
	return *stored
}

func (s *IntegrationSuite) newBooking() model.Booking {
	id, err := uuid.NewV7()
	s.Require().NoError(err)
	return model.Booking{
		ID:                 id,
		DistributorID:      "it-" + id.String()[:8],
		IdempotencyKey:     "key-" + id.String(),
		RequestFingerprint: "fp-" + id.String(),
		SupplierID:         "supplier-mock",
		PropertyID:         "hotel-001",
		RoomTypeID:         "room-deluxe-confirm",
		CheckIn:            time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC),
		CheckOut:           time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC),
		GuestFirstName:     "Taro",
		GuestLastName:      "Yamada",
		Status:             model.StatusReceived,
	}
}

func (s *IntegrationSuite) mustApply(id uuid.UUID, from, to model.Status) {
	_, err := s.repo.Apply(s.ctx, id, repository.Transition{
		From: from, To: to, Marker: repository.MarkerClear, EventType: model.EventTransition,
	})
	s.Require().NoError(err)
}

func (s *IntegrationSuite) updatedAt(id uuid.UUID) time.Time {
	var at time.Time
	s.Require().NoError(s.pool.QueryRow(s.ctx,
		`SELECT updated_at FROM bookings WHERE id = $1`, id).Scan(&at))
	return at
}

func (s *IntegrationSuite) countEvents(id uuid.UUID, eventType string) int {
	var n int
	s.Require().NoError(s.pool.QueryRow(s.ctx,
		`SELECT count(*) FROM booking_events WHERE booking_id = $1 AND event_type = $2`,
		id, eventType).Scan(&n))
	return n
}
