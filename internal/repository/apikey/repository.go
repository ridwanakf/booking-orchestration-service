package apikey

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/ridwanakf/booking-orchestration-service/internal/constant"
	"github.com/ridwanakf/booking-orchestration-service/internal/model"
	"github.com/ridwanakf/booking-orchestration-service/internal/repository"
)

// nomock: satisfied by *pgxpool.Pool, and the repository above it is what tests mock.
type DB interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

var _ repository.APIKeyRepository = (*Repo)(nil)

type Repo struct {
	db DB
}

func New(db DB) *Repo { return &Repo{db: db} }

// FindActive returns a key only while it is unrevoked. Rotation is additive, so
// a distributor holds two active keys across a rollover.
func (r *Repo) FindActive(ctx context.Context, keyID string) (*model.APIKey, error) {
	var k model.APIKey
	err := r.db.QueryRow(ctx,
		`SELECT key_id, distributor_id, secret_hash FROM distributor_api_keys
		 WHERE key_id = $1 AND revoked_at IS NULL`, keyID).
		Scan(&k.KeyID, &k.DistributorID, &k.SecretHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, constant.ErrUnauthorized
	}
	if err != nil {
		return nil, fmt.Errorf("find api key: %w", err)
	}
	return &k, nil
}
