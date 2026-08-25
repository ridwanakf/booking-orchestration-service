package cmd

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v3"

	"github.com/ridwanakf/booking-orchestration-service/config"
	"github.com/ridwanakf/booking-orchestration-service/internal/migrate"
)

func Migrate(ctx context.Context, _ *cli.Command) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	return migrate.Up(ctx, cfg.PostgresDSN)
}
