package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/sdk/client"

	"github.com/ridwanakf/booking-orchestration-service/config"
	"github.com/ridwanakf/booking-orchestration-service/internal/constant"
	"github.com/ridwanakf/booking-orchestration-service/internal/observability"
	"github.com/ridwanakf/booking-orchestration-service/internal/orchestrator"
	bookingrepo "github.com/ridwanakf/booking-orchestration-service/internal/repository/booking"
	bookingsvc "github.com/ridwanakf/booking-orchestration-service/internal/service/booking"
)

type App struct {
	cfg      config.AppConfig
	pg       *pgxpool.Pool
	temporal client.Client

	Booking *bookingsvc.Service
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
		CreateAttempts:     cfg.CreateAttempts,
		RetrieveDelaysSec:  []int{int(cfg.CreateRetryDelay.Seconds())},
		ParkTimeoutSec:     int(cfg.ParkTimeout.Seconds()),
		ActivityTimeoutSec: int(cfg.ActivityStartToClose.Seconds()),
	})

	return &App{
		cfg:      cfg,
		pg:       pg,
		temporal: temporal,
		Booking:  bookingsvc.New(repo, orch, cfg.SupplierID, slog.Default()),
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
