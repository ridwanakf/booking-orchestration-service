package sweep

import (
	"context"
	"log/slog"
	"time"

	"github.com/ridwanakf/booking-orchestration-service/internal/model"
	"github.com/ridwanakf/booking-orchestration-service/internal/orchestrator"
	"github.com/ridwanakf/booking-orchestration-service/internal/repository"
)

// How many rows one pass will restart. A backlog drains over several passes
// rather than flooding the task queue in one.
const batchSize = 100

type Sweeper struct {
	repo       repository.BookingRepository
	starter    orchestrator.Orchestrator
	interval   time.Duration
	thresholds repository.StaleThresholds
	log        *slog.Logger
}

func New(repo repository.BookingRepository, starter orchestrator.Orchestrator, interval time.Duration, thresholds repository.StaleThresholds, log *slog.Logger) *Sweeper {
	return &Sweeper{repo: repo, starter: starter, interval: interval, thresholds: thresholds, log: log}
}

func (s *Sweeper) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.pass(ctx)
		}
	}
}

// The sweep writes no state. It only asks for a workflow to exist, and workflow
// id uniqueness makes asking twice harmless, so several replicas sweeping at
// once cannot start a booking twice or corrupt one.
func (s *Sweeper) pass(ctx context.Context) {
	// Bounded by the tick, so a query that stalls turns into a visible failure
	// rather than silently stopping the design's liveness backstop.
	ctx, cancel := context.WithTimeout(ctx, s.interval)
	defer cancel()

	stale, err := s.repo.FindStale(ctx, s.thresholds, batchSize)
	if err != nil {
		s.log.ErrorContext(ctx, "sweep query failed", "event", "sweep.restarted", "error", err)
		return
	}

	for _, id := range stale {
		started, err := s.starter.StartBooking(ctx, id)
		switch {
		case err != nil:
			s.log.ErrorContext(ctx, "sweep could not restart a booking",
				"event", "sweep.start_failed", "booking_id", id, "error", err)
		case started:
			s.log.InfoContext(ctx, "sweep restarted a booking",
				"event", model.EventSweepRestarted, "booking_id", id)
		default:
			// Already open, and stale anyway, so that run is stuck. Calling it a
			// restart would make a wedged booking look like recovery.
			s.log.WarnContext(ctx, "stale booking already has an open execution",
				"event", "sweep.already_running", "booking_id", id)
		}
	}
}
