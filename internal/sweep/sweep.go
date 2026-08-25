package sweep

import (
	"context"
	"log/slog"
	"time"

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
		if err := s.starter.StartBooking(ctx, id); err != nil {
			s.log.ErrorContext(ctx, "sweep could not restart a booking",
				"event", "sweep.restarted", "booking_id", id, "error", err)
			continue
		}
		s.log.InfoContext(ctx, "sweep restarted a booking",
			"event", "sweep.restarted", "booking_id", id)
	}
}
