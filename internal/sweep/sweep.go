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

// Writes no state: it asks for a workflow to exist, and workflow id uniqueness
// makes asking twice harmless, so replicas sweeping at once are safe.
func (s *Sweeper) pass(ctx context.Context) {
	// Bounded by the tick, so a stalled query fails visibly.
	ctx, cancel := context.WithTimeout(ctx, s.interval)
	defer cancel()

	stale, err := s.repo.FindStale(ctx, s.thresholds, batchSize)
	if err != nil {
		s.log.ErrorContext(ctx, "sweep query failed", "event", model.EventSweepFailed, "error", err)
		return
	}

	for i, id := range stale {
		if ctx.Err() != nil {
			// Still stale, so the next pass takes them; one deadline logged per
			// row would bury the reason this pass ran out of time.
			s.log.WarnContext(ctx, "sweep ran out of time before draining the batch",
				"event", model.EventSweepFailed, "remaining", len(stale)-i)
			return
		}

		started, err := s.starter.StartBooking(ctx, id)
		switch {
		case err != nil:
			s.log.ErrorContext(ctx, "sweep could not restart a booking",
				"event", model.EventSweepStartFailed, "booking_id", id, "error", err)
		case started:
			s.log.InfoContext(ctx, "sweep restarted a booking",
				"event", model.EventSweepRestarted, "booking_id", id)
		default:
			// Already open, and stale anyway, so that run is stuck. Calling it a
			// restart would make a wedged booking look like recovery.
			s.log.WarnContext(ctx, "stale booking already has an open execution",
				"event", model.EventSweepAlreadyOpen, "booking_id", id)
		}
	}
}
