package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/urfave/cli/v3"

	"github.com/ridwanakf/booking-orchestration-service/config"
	"github.com/ridwanakf/booking-orchestration-service/internal/app"
	"github.com/ridwanakf/booking-orchestration-service/internal/delivery/rest"
	"github.com/ridwanakf/booking-orchestration-service/internal/delivery/rest/handler"
	"github.com/ridwanakf/booking-orchestration-service/internal/migrate"
	"github.com/ridwanakf/booking-orchestration-service/internal/observability"
)

func Serve(ctx context.Context, _ *cli.Command) error {
	slog.SetDefault(slog.New(observability.NewHandler(slog.NewJSONHandler(os.Stdout, nil))))

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	gin.SetMode(gin.ReleaseMode)

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)

	if err := migrate.Up(ctx, cfg.PostgresDSN); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}

	application, err := app.New(ctx, cfg)
	if err != nil {
		return fmt.Errorf("build app: %w", err)
	}
	defer application.Close()

	engine := rest.NewEngine(handler.NewHealth(application.Ready), handler.NewBooking(application.Booking), cfg.SwaggerEnabled)

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           engine,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("http listening", "addr", cfg.HTTPAddr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		// Hand the signal back to the runtime before the slow part, so a second
		// interrupt during a hung shutdown kills the process instead of being
		// swallowed by the handler that is still installed.
		stop()
		slog.Info("shutdown signal received")
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http server failed: %w", err)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	shutdownErr := srv.Shutdown(shutdownCtx)
	if shutdownErr != nil {
		_ = srv.Close()
	}

	// Joined before the deferred Close tears down the pool and the orchestrator
	// client: an activity still finishing a supplier call needs both.

	if shutdownErr != nil {
		return fmt.Errorf("http server did not shut down cleanly: %w", shutdownErr)
	}
	slog.Info("shutdown complete")
	return nil
}
