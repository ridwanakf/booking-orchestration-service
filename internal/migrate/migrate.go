package migrate

import (
	"context"
	"database/sql"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	"github.com/ridwanakf/booking-orchestration-service/migrations"
)

// Spells "bookmig8" so a stray row in pg_locks is identifiable.
const lockID int64 = 0x626f6f6b6d696738

// Bounded rather than blocking: migrations run before the listener binds, so a
// lock held by a stuck peer would otherwise stall startup with no log line and
// no open port, which reads to an orchestrator as a crash loop.
const (
	lockProbeSeconds  = 2
	lockProbeAttempts = 30
)

// Up applies migrations under goose's session lock, so several instances rolling
// out at once cannot race each other.
func Up(ctx context.Context, dsn string) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = db.Close() }()

	locker, err := lock.NewPostgresSessionLocker(
		lock.WithLockID(lockID),
		lock.WithLockTimeout(lockProbeSeconds, lockProbeAttempts),
		lock.WithUnlockTimeout(lockProbeSeconds, lockProbeAttempts),
	)
	if err != nil {
		return fmt.Errorf("build migration locker: %w", err)
	}

	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS,
		goose.WithSessionLocker(locker))
	if err != nil {
		return fmt.Errorf("build migration provider: %w", err)
	}

	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}
