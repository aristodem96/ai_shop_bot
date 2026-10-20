package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SettingsRepository keeps what the bot remembers about itself between
// starts.
type SettingsRepository struct {
	pool *pgxpool.Pool
}

// NewSettingsRepository returns a repository backed by pool.
func NewSettingsRepository(pool *pgxpool.Pool) *SettingsRepository {
	return &SettingsRepository{pool: pool}
}

// Get returns a setting, "" when it was never set.
func (r *SettingsRepository) Get(ctx context.Context, key string) (string, error) {
	var v string
	err := r.pool.QueryRow(ctx, `SELECT value FROM bot_settings WHERE key = $1`, key).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("postgres: setting %s: %w", key, err)
	}
	return v, nil
}

// Set stores a setting.
func (r *SettingsRepository) Set(ctx context.Context, key, value string) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO bot_settings (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("postgres: set setting %s: %w", key, err)
	}
	return nil
}
