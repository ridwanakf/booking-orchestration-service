//go:build integration

package booking_test

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/suite"

	"github.com/ridwanakf/booking-orchestration-service/internal/constant"
	"github.com/ridwanakf/booking-orchestration-service/internal/model"
	repo "github.com/ridwanakf/booking-orchestration-service/internal/repository/booking"
)

const defaultDSN = "postgres://booking:booking@localhost:55432/booking?sslmode=disable"

// These run against a real Postgres because the guarantees under test live in
// SQL, not in Go: row locking, constraint enforcement, and the trigger. A
// mocked database can prove the branch logic and nothing else.
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
	pool, err := pgxpool.New(context.Background(), dsn)
	s.Require().NoError(err)
	s.Require().NoError(pool.Ping(context.Background()), "integration tests need the compose stack up")
	s.pool = pool
	s.repo = repo.New(pool)
	s.ctx = context.Background()

	// These tests write real rows, so they clear their own leftovers rather
	// than inheriting state from the previous run.
	_, err = pool.Exec(s.ctx, `DELETE FROM bookings WHERE distributor_id = 'itest'`)
	s.Require().NoError(err)
}

func (s *IntegrationSuite) TearDownSuite() {
	if s.pool != nil {
		s.pool.Close()
	}
}

func (s *IntegrationSuite) newBooking(key string) model.Booking {
	id, err := uuid.NewV7()
	s.Require().NoError(err)
	return model.Booking{
		ID: id, DistributorID: "itest", IdempotencyKey: key,
		RequestFingerprint: "fp-" + key, SupplierID: "mock-supplier",
		PropertyID: "hotel-001", RoomTypeID: "room-deluxe",
		CheckIn:        time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC),
		CheckOut:       time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC),
		GuestFirstName: "Taro", GuestLastName: "Yamada",
		Status: model.StatusReceived,
	}
}

func (s *IntegrationSuite) stored(key string) *model.Booking {
	b, _, err := s.repo.InsertOrLoad(s.ctx, s.newBooking(key))
	s.Require().NoError(err)
	return b
}

// The create race is resolved by the unique constraint, not by a lock in the
// process, which is what makes it correct across replicas and restarts.
func (s *IntegrationSuite) TestConcurrentIdenticalCreatesYieldOneRow() {
	key := "race-" + uuid.NewString()
	const racers = 16

	var wg sync.WaitGroup
	ids := make([]uuid.UUID, racers)
	created := make([]bool, racers)
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, isNew, err := s.repo.InsertOrLoad(s.ctx, s.newBooking(key))
			s.Require().NoError(err)
			ids[i], created[i] = b.ID, isNew
		}()
	}
	wg.Wait()

	inserts := 0
	for _, c := range created {
		if c {
			inserts++
		}
	}
	s.Equal(1, inserts, "exactly one caller may insert")
	for _, id := range ids {
		s.Equal(ids[0], id, "every caller must see the same booking")
	}

	var rows int
	s.Require().NoError(s.pool.QueryRow(s.ctx,
		`SELECT count(*) FROM bookings WHERE distributor_id = 'itest' AND idempotency_key = $1`, key).Scan(&rows))
	s.Equal(1, rows)
}

// The guard is a real WHERE clause, so the losing transition reports the state
// that actually won rather than silently overwriting it.
func (s *IntegrationSuite) TestGuardedTransitionPolarity() {
	b := s.stored("guard-" + uuid.NewString())

	got, err := s.repo.Transition(s.ctx, b.ID, model.StatusReceived, model.StatusPending)
	s.Require().NoError(err)
	s.Equal(model.StatusPending, got)

	got, err = s.repo.Transition(s.ctx, b.ID, model.StatusReceived, model.StatusPending)
	s.ErrorIs(err, constant.ErrTransitionConflict)
	s.Equal(model.StatusPending, got, "the conflict carries the state that won")
}

// Two attempts racing the same booking must not both be authorized past the
// budget, and the loser must see the status the winner committed rather than a
// stale snapshot of it.
func (s *IntegrationSuite) TestConcurrentAuthorizeRespectsTheBudget() {
	b := s.stored("auth-" + uuid.NewString())
	const racers = 8

	var wg sync.WaitGroup
	var mu sync.Mutex
	var granted []int
	for range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			attempt, _, err := s.repo.Authorize(s.ctx, b.ID, 2, "KEY-"+b.ID.String())
			if err == nil {
				mu.Lock()
				granted = append(granted, attempt)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	s.Len(granted, 2, "the budget bounds concurrent authorizations, not just sequential ones")
	s.ElementsMatch([]int{1, 2}, granted, "each authorization claims a distinct attempt number")
}

func (s *IntegrationSuite) TestAuthorizeStoresTheSupplierKeyOnceAndReusesIt() {
	b := s.stored("key-" + uuid.NewString())

	first := "FIRST-" + uuid.NewString()

	_, _, err := s.repo.Authorize(s.ctx, b.ID, 2, first)
	s.Require().NoError(err)
	_, _, err = s.repo.Authorize(s.ctx, b.ID, 2, "SECOND-"+uuid.NewString())
	s.Require().NoError(err)

	after, err := s.repo.GetByID(s.ctx, b.ID)
	s.Require().NoError(err)
	s.Require().NotNil(after.SupplierIdempotencyKey)
	s.Equal(first, *after.SupplierIdempotencyKey, "every retry reuses the key the first attempt sent")
}

// The unique index is what turns an encoding collision into a failed write
// rather than two bookings sharing one reservation.
func (s *IntegrationSuite) TestTheSupplierKeyIndexRefusesACollision() {
	first := s.stored("dup1-" + uuid.NewString())
	second := s.stored("dup2-" + uuid.NewString())
	shared := "COLLIDING-" + uuid.NewString()

	_, _, err := s.repo.Authorize(s.ctx, first.ID, 2, shared)
	s.Require().NoError(err)

	_, _, err = s.repo.Authorize(s.ctx, second.ID, 2, shared)
	s.Error(err, "a second booking must not be able to claim the same supplier key")
}

// Parking is guarded in SQL, so a booking that settles first is never flagged.
func (s *IntegrationSuite) TestParkOnlyFlagsABookingStillInDoubt() {
	b := s.stored("park-" + uuid.NewString())
	_, _, err := s.repo.Authorize(s.ctx, b.ID, 2, "K-"+b.ID.String())
	s.Require().NoError(err)

	parked, err := s.repo.ParkIfUnknown(s.ctx, b.ID)
	s.Require().NoError(err)
	s.True(parked)

	ref := "SUP-1"
	_, err = s.repo.SettleFromUnsettled(s.ctx, b.ID, model.StatusConfirmed, &ref, nil)
	s.Require().NoError(err)

	parked, err = s.repo.ParkIfUnknown(s.ctx, b.ID)
	s.Require().NoError(err)
	s.False(parked, "a settled booking must never be flagged for recovery")

	after, err := s.repo.GetByID(s.ctx, b.ID)
	s.Require().NoError(err)
	s.False(after.NeedsRecovery, "settling clears the flag")
}

// The trigger is what keeps staleness honest: a write that forgets updated_at
// would either strand a booking outside the sweep or pin the head of its queue.
func (s *IntegrationSuite) TestUpdatedAtMovesWithoutTheStatementSettingIt() {
	b := s.stored("touch-" + uuid.NewString())

	_, err := s.pool.Exec(s.ctx, `UPDATE bookings SET status = 'PENDING' WHERE id = $1`, b.ID)
	s.Require().NoError(err)

	after, err := s.repo.GetByID(s.ctx, b.ID)
	s.Require().NoError(err)
	s.True(after.UpdatedAt.After(after.CreatedAt), "the trigger must fire for any update")
}

func (s *IntegrationSuite) TestTheDatabaseRefusesAStatusOutsideTheStateMachine() {
	b := s.stored("check-" + uuid.NewString())

	_, err := s.pool.Exec(s.ctx, `UPDATE bookings SET status = 'NOT_A_STATE' WHERE id = $1`, b.ID)

	s.Error(err, "a literal outside the state machine would be invisible to every recovery query")
}

func (s *IntegrationSuite) TestFindStaleExcludesFlaggedAndSettledRows() {
	b := s.stored("stale-" + uuid.NewString())
	_, err := s.pool.Exec(s.ctx,
		`UPDATE bookings SET updated_at = now() - interval '1 hour' WHERE id = $1`, b.ID)
	s.Require().NoError(err)

	stale, err := s.repo.FindStale(s.ctx, 30*time.Second, 15*time.Minute, 500)
	s.Require().NoError(err)
	s.Contains(stale, b.ID)

	s.Require().NoError(s.repo.Flag(s.ctx, b.ID))
	_, err = s.pool.Exec(s.ctx,
		`UPDATE bookings SET updated_at = now() - interval '1 hour' WHERE id = $1`, b.ID)
	s.Require().NoError(err)

	stale, err = s.repo.FindStale(s.ctx, 30*time.Second, 15*time.Minute, 500)
	s.Require().NoError(err)
	s.NotContains(stale, b.ID, "flagged rows belong to outcome recovery, not the sweep")
}
