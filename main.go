// Package main is the booking orchestration service entrypoint and the source
// of the API's general OpenAPI metadata, generated into docs/ by swag.
//
//	@title			Booking Orchestration API
//	@version		1.0
//	@description	Orchestrates hotel bookings between travel distributors and unreliable suppliers.
//	@description	A supplier timeout is an unknown outcome, never a rejection.
//	@BasePath		/
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/ridwanakf/booking-orchestration-service/cmd"
)

func main() {
	app := &cli.Command{
		Name:  "booking-orchestrator",
		Usage: "orchestrate hotel bookings against unreliable suppliers",
		Commands: []*cli.Command{
			{
				Name:   "serve",
				Usage:  "run the HTTP API",
				Action: cmd.Serve,
			},
			{
				Name:   "migrate",
				Usage:  "apply database migrations",
				Action: cmd.Migrate,
			},
		},
	}

	if err := app.Run(context.Background(), os.Args); err != nil {
		slog.Error("exited with error", "error", err)
		os.Exit(1)
	}
}
