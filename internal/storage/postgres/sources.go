package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SourceRepository records where buyers came from.
type SourceRepository struct {
	pool *pgxpool.Pool
}

// NewSourceRepository returns a repository backed by pool.
func NewSourceRepository(pool *pgxpool.Pool) *SourceRepository {
	return &SourceRepository{pool: pool}
}

// Record keeps the mark a buyer first came with; later ones change nothing.
func (r *SourceRepository) Record(ctx context.Context, userID int64, source string) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO buyer_sources (user_id, source) VALUES ($1, $2)
		ON CONFLICT (user_id) DO NOTHING`, userID, source)
	if err != nil {
		return fmt.Errorf("postgres: record source of %d: %w", userID, err)
	}
	return nil
}
