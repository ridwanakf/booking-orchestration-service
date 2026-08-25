package sweep_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	orchmocks "github.com/ridwanakf/booking-orchestration-service/internal/orchestrator/mocks"
	"github.com/ridwanakf/booking-orchestration-service/internal/repository"
	repomocks "github.com/ridwanakf/booking-orchestration-service/internal/repository/mocks"
	"github.com/ridwanakf/booking-orchestration-service/internal/sweep"
)

type SweepSuite struct {
	suite.Suite
	ctrl    *gomock.Controller
	repo    *repomocks.MockBookingRepository
	starter *orchmocks.MockOrchestrator
	sweeper *sweep.Sweeper

	mu      sync.Mutex
	started []uuid.UUID
}

func TestSweep(t *testing.T) {
	suite.Run(t, new(SweepSuite))
}

func (s *SweepSuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
	s.repo = repomocks.NewMockBookingRepository(s.ctrl)
	s.starter = orchmocks.NewMockOrchestrator(s.ctrl)
	s.sweeper = sweep.New(s.repo, s.starter, 5*time.Millisecond, repository.StaleThresholds{
		Marker: 2 * time.Minute, Received: 30 * time.Second, Idle: 15 * time.Minute,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.started = nil
}

// Driven by the sweep's own progress rather than by wall clock: the run is
// stopped the moment the query under test has answered once, so assertions are
// exact and cannot flake on a loaded machine.
func (s *SweepSuite) runUntilFirstPass(stale []uuid.UUID, queryErr error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	queried := make(chan struct{})
	var once sync.Once
	s.repo.EXPECT().FindStale(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(context.Context, repository.StaleThresholds, int) ([]uuid.UUID, error) {
			defer once.Do(func() { close(queried) })
			return stale, queryErr
		}).MinTimes(1)

	finished := make(chan struct{})
	go func() { s.sweeper.Run(ctx); close(finished) }()

	select {
	case <-queried:
	case <-time.After(2 * time.Second):
		s.FailNow("the sweep never ran a pass")
	}
	// Cancelling here rather than straight after the query: the sweep abandons
	// a batch whose context is done, so cancelling early would test shutdown
	// while claiming to test batch semantics.
	s.waitForStarts(len(stale))
	cancel()

	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		s.FailNow("the sweep did not stop when its context was cancelled")
	}
}

func (s *SweepSuite) recordStarts(failFor uuid.UUID) {
	s.starter.EXPECT().StartBooking(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, id uuid.UUID) (bool, error) {
			s.mu.Lock()
			s.started = append(s.started, id)
			s.mu.Unlock()
			if id == failFor {
				return false, errors.New("temporal unavailable")
			}
			return true, nil
		}).AnyTimes()
}

func (s *SweepSuite) waitForStarts(want int) {
	deadline := time.Now().Add(2 * time.Second)
	for len(s.startedIDs()) < want && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
}

func (s *SweepSuite) startedIDs() []uuid.UUID {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]uuid.UUID(nil), s.started...)
}

func (s *SweepSuite) TestRestartsEveryStaleBooking() {
	first, second := uuid.New(), uuid.New()
	s.recordStarts(uuid.Nil)

	s.runUntilFirstPass([]uuid.UUID{first, second}, nil)

	s.Contains(s.startedIDs(), first)
	s.Contains(s.startedIDs(), second)
}

// The sweep writes no state and holds no cursor, so a failure to start one
// booking must not stop the rest of the batch.
func (s *SweepSuite) TestOneFailedStartDoesNotStopTheBatch() {
	broken, healthy := uuid.New(), uuid.New()
	s.recordStarts(broken)

	s.runUntilFirstPass([]uuid.UUID{broken, healthy}, nil)

	s.Contains(s.startedIDs(), healthy, "a failed start must not abandon the rest of the batch")
}

// A batch whose context is done is abandoned rather than driven to completion.
// The rows are still stale, so the next pass takes them; pushing on would log
// one deadline per remaining row and bury the reason the pass ran out of time.
func (s *SweepSuite) TestADoneContextAbandonsTheRestOfTheBatch() {
	first, second, third := uuid.New(), uuid.New(), uuid.New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s.repo.EXPECT().FindStale(gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]uuid.UUID{first, second, third}, nil).AnyTimes()
	s.starter.EXPECT().StartBooking(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, id uuid.UUID) (bool, error) {
			s.mu.Lock()
			s.started = append(s.started, id)
			s.mu.Unlock()
			cancel()
			return true, nil
		}).AnyTimes()

	finished := make(chan struct{})
	go func() { s.sweeper.Run(ctx); close(finished) }()

	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		s.FailNow("the sweep did not stop when its context was cancelled")
	}

	s.Len(s.startedIDs(), 1, "the batch must stop at the first booking after the context is done")
	s.NotContains(s.startedIDs(), second)
	s.NotContains(s.startedIDs(), third)
}

func (s *SweepSuite) TestAQueryFailureIsSurvivable() {
	s.runUntilFirstPass(nil, errors.New("connection refused"))

	s.Empty(s.startedIDs())
}

func (s *SweepSuite) TestNothingStaleStartsNothing() {
	s.runUntilFirstPass(nil, nil)

	s.Empty(s.startedIDs())
}

// An execution that is already open is not a restart. Logging it as one would
// make a booking wedged behind a stuck run look like recovery working.
func (s *SweepSuite) TestAnAlreadyRunningBookingIsNotCountedAsRestarted() {
	stale := uuid.New()
	s.starter.EXPECT().StartBooking(gomock.Any(), stale).
		DoAndReturn(func(_ context.Context, id uuid.UUID) (bool, error) {
			s.mu.Lock()
			s.started = append(s.started, id)
			s.mu.Unlock()
			return false, nil
		}).AnyTimes()

	s.runUntilFirstPass([]uuid.UUID{stale}, nil)

	s.Contains(s.startedIDs(), stale, "it is still asked for, it is just not a restart")
}
