package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"github.com/ridwanakf/booking-orchestration-service/config"
	"github.com/ridwanakf/booking-orchestration-service/internal/constant"
	"github.com/ridwanakf/booking-orchestration-service/internal/model"
	"github.com/ridwanakf/booking-orchestration-service/internal/observability"
	"github.com/ridwanakf/booking-orchestration-service/internal/orchestrator"
	"github.com/ridwanakf/booking-orchestration-service/internal/repository"
	apikeyrepo "github.com/ridwanakf/booking-orchestration-service/internal/repository/apikey"
	bookingrepo "github.com/ridwanakf/booking-orchestration-service/internal/repository/booking"
	bookingsvc "github.com/ridwanakf/booking-orchestration-service/internal/service/booking"
	"github.com/ridwanakf/booking-orchestration-service/internal/supplier"
	"github.com/ridwanakf/booking-orchestration-service/internal/sweep"
	"github.com/ridwanakf/booking-orchestration-service/internal/workflow"
)

type App struct {
	cfg      config.AppConfig
	pg       *pgxpool.Pool
	temporal client.Client

	Booking    *bookingsvc.Service
	APIKeys    repository.APIKeyRepository
	Activities *workflow.Activities
	Sweeper    *sweep.Sweeper
	Mock       *supplier.Mock
}

func New(ctx context.Context, cfg config.AppConfig) (*App, error) {
	pg, err := pgxpool.New(ctx, cfg.PostgresDSN)
	if err != nil {
		return nil, fmt.Errorf("open postgres pool: %w", err)
	}

	// Lazy: an orchestrator outage must not stop a booking being committed.
	temporal, err := client.NewLazyClient(client.Options{
		HostPort: cfg.TemporalAddress,
		Logger:   observability.NewTemporalLogger(slog.Default()),
	})
	if err != nil {
		pg.Close()
		return nil, fmt.Errorf("build temporal client: %w", err)
	}

	repo := bookingrepo.New(pg)
	orch := orchestrator.NewTemporal(temporal, cfg.TaskQueue, constant.WorkflowParams{
		CreateAttempts:  cfg.CreateAttempts,
		RetryDelays:     []time.Duration{cfg.CreateRetryDelay},
		ParkTimeout:     cfg.ParkTimeout,
		ActivityTimeout: cfg.ActivityStartToClose,
		PersistWindow:   cfg.PersistWindow,
	})

	return &App{
		cfg:      cfg,
		pg:       pg,
		temporal: temporal,
		Booking:  bookingsvc.New(repo, orch, cfg.SupplierID, slog.Default()),
		APIKeys:  apikeyrepo.New(pg),
		Activities: workflow.NewActivities(repo,
			supplier.NewHTTPClient(cfg.SupplierBaseURL, cfg.SupplierDeadline), slog.Default()),
		Sweeper: sweep.New(repo, orch, cfg.SweepInterval, repository.StaleThresholds{
			Marker:   cfg.SweepMarkerThreshold,
			Received: cfg.SweepReceivedThreshold,
			Idle:     cfg.SweepIdleThreshold,
		}, slog.Default()),
		Mock: supplier.NewMock(cfg.CallbackBaseURL, cfg.CallbackToken, cfg.MockTimeoutHold, cfg.MockCallbackDelay, slog.Default()),
	}, nil
}

// Ready gates traffic, so it measures only what the request path needs. The
// orchestrator is deliberately excluded: a booking commits and returns 201
// while it is down, so failing readiness would pull every replica out of
// rotation and turn a survivable outage into a total one.
func (a *App) Ready(ctx context.Context) error {
	if err := a.pg.Ping(ctx); err != nil {
		return fmt.Errorf("database unreachable: %w", err)
	}
	return nil
}

// OrchestratorReachable is a separate signal for operators. It reports degraded
// capability, never readiness to serve.
func (a *App) OrchestratorReachable(ctx context.Context) error {
	if _, err := a.temporal.CheckHealth(ctx, &client.CheckHealthRequest{}); err != nil {
		return fmt.Errorf("orchestrator unreachable: %w", err)
	}
	return nil
}

func (a *App) Close() {
	if a.temporal != nil {
		a.temporal.Close()
	}
	if a.pg != nil {
		a.pg.Close()
	}
}

// RunWorker polls for durable work until the context ends. Starting a worker
// dials the orchestrator, so this retries in the background rather than
// returning an error: the API must keep accepting bookings while the
// orchestrator is down, and a booting worker must not be what stops it.
func (a *App) RunWorker(ctx context.Context) {
	for {
		w := worker.New(a.temporal, a.cfg.TaskQueue, worker.Options{
			WorkerStopTimeout: a.cfg.ShutdownTimeout,
		})
		workflow.Register(w, a.Activities)

		if err := w.Start(); err != nil {
			slog.WarnContext(ctx, "workflow worker could not start, retrying",
				"event", model.EventWorkerStartFail, "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(a.cfg.WorkerRetryInterval):
				continue
			}
		}

		slog.InfoContext(ctx, "workflow worker started", "task_queue", a.cfg.TaskQueue)
		<-ctx.Done()
		w.Stop()
		return
	}
}
