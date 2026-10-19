package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aristodem96/ai_shop_bot/internal/photos"
)

var _ photos.Cache = (*PhotoRepository)(nil)

// PhotoRepository keeps the file ids of photos Telegram already has.
type PhotoRepository struct {
	pool *pgxpool.Pool
}

// NewPhotoRepository returns a repository backed by pool.
func NewPhotoRepository(pool *pgxpool.Pool) *PhotoRepository {
	return &PhotoRepository{pool: pool}
}

// FileID returns the file id a photo was sent under.
func (r *PhotoRepository) FileID(ctx context.Context, url string) (string, bool, error) {
	var id string
	err := r.pool.QueryRow(ctx, `SELECT file_id FROM photo_files WHERE url = $1`, url).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("postgres: photo file id: %w", err)
	}
	return id, true, nil
}

// SaveFileID keeps a photo's file id.
func (r *PhotoRepository) SaveFileID(ctx context.Context, url, fileID string) error {
	if _, err := r.pool.Exec(ctx, `
		INSERT INTO photo_files (url, file_id) VALUES ($1, $2)
		ON CONFLICT (url) DO UPDATE SET file_id = excluded.file_id, created_at = now()`, url, fileID); err != nil {
		return fmt.Errorf("postgres: save photo file id: %w", err)
	}
	return nil
}
