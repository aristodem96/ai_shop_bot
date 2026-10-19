package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aristodem96/ai_shop_bot/internal/crawler"
)

var _ crawler.Store = (*CrawlRepository)(nil)

// CrawlRepository is the crawl queue and the cache of what fits each
// Auto-DB modification.
type CrawlRepository struct {
	pool *pgxpool.Pool
}

// NewCrawlRepository returns a repository backed by pool.
func NewCrawlRepository(pool *pgxpool.Pool) *CrawlRepository {
	return &CrawlRepository{pool: pool}
}

// Enqueue queues a vehicle's modification unless it is queued, running, or
// done within maxAge. A failed crawl is queued again.
func (r *CrawlRepository) Enqueue(ctx context.Context, vehicleID int64, maxAge time.Duration) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO autodb_crawls (mod_id, status)
		SELECT autodb_mod_id, 'queued' FROM vehicles WHERE id = $1 AND autodb_mod_id IS NOT NULL
		ON CONFLICT (mod_id) DO UPDATE SET status = 'queued', queued_at = now(), error = ''
		WHERE autodb_crawls.status = 'failed'
		   OR (autodb_crawls.status = 'done' AND autodb_crawls.finished_at < now() - make_interval(secs => $2))`,
		vehicleID, maxAge.Seconds())
	if err != nil {
		return fmt.Errorf("postgres: enqueue crawl of vehicle %d: %w", vehicleID, err)
	}
	return nil
}

// Claim takes the oldest queued crawl. SKIP LOCKED lets two workers take two
// crawls instead of waiting on, or both taking, the same one.
func (r *CrawlRepository) Claim(ctx context.Context) (int64, bool, error) {
	var modID int64
	err := r.pool.QueryRow(ctx, `
		UPDATE autodb_crawls SET status = 'running', sections_done = 0, parts = 0
		WHERE mod_id = (
			SELECT mod_id FROM autodb_crawls WHERE status = 'queued'
			ORDER BY queued_at LIMIT 1 FOR UPDATE SKIP LOCKED)
		RETURNING mod_id`).Scan(&modID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("postgres: claim a crawl: %w", err)
	}
	return modID, true, nil
}

// Requeue puts crawls a stopped process left running back in the queue.
func (r *CrawlRepository) Requeue(ctx context.Context) error {
	if _, err := r.pool.Exec(ctx, `UPDATE autodb_crawls SET status = 'queued' WHERE status = 'running'`); err != nil {
		return fmt.Errorf("postgres: requeue crawls: %w", err)
	}
	return nil
}

// SaveParts replaces the parts of one section, so a recrawl drops what the
// catalogue dropped.
func (r *CrawlRepository) SaveParts(ctx context.Context, modID, sectionID int64, parts []crawler.Part) error {
	sup, art, num, brand, bkey, name := make([]int64, len(parts)), make([]string, len(parts)),
		make([]string, len(parts)), make([]string, len(parts)), make([]string, len(parts)), make([]string, len(parts))
	for i, p := range parts {
		sup[i], art[i], num[i], brand[i], bkey[i], name[i] = p.SupplierID, p.ArticleKey, p.Number, p.Brand, p.BrandKey, p.Name
	}
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM autodb_mod_parts WHERE mod_id = $1 AND section_id = $2`, modID, sectionID); err != nil {
			return fmt.Errorf("postgres: clear section %d of %d: %w", sectionID, modID, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO autodb_mod_parts (mod_id, section_id, supplier_id, article_key, number, brand, brand_key, name)
			SELECT $1, $2, x.* FROM unnest($3::bigint[], $4::text[], $5::text[], $6::text[], $7::text[], $8::text[])
				AS x(supplier_id, article_key, number, brand, brand_key, name)
			ON CONFLICT DO NOTHING`, modID, sectionID, sup, art, num, brand, bkey, name); err != nil {
			return fmt.Errorf("postgres: save section %d of %d: %w", sectionID, modID, err)
		}
		// The brands' names, to tell whose a search hit is.
		if _, err := tx.Exec(ctx, `
			INSERT INTO autodb_suppliers (id, name, name_key)
			SELECT DISTINCT ON (x.id) x.* FROM unnest($1::bigint[], $2::text[], $3::text[]) AS x(id, name, name_key)
			ON CONFLICT DO NOTHING`, sup, brand, bkey); err != nil {
			return fmt.Errorf("postgres: save suppliers of section %d: %w", sectionID, err)
		}
		return nil
	})
}

// Progress records how far a crawl is.
func (r *CrawlRepository) Progress(ctx context.Context, modID int64, total, done, parts int) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE autodb_crawls SET sections_total = $2, sections_done = $3, parts = $4 WHERE mod_id = $1`,
		modID, total, done, parts)
	if err != nil {
		return fmt.Errorf("postgres: crawl progress of %d: %w", modID, err)
	}
	return nil
}

// Finish ends a crawl, done or failed.
func (r *CrawlRepository) Finish(ctx context.Context, modID int64, failure error) error {
	status, msg := "done", ""
	if failure != nil {
		status, msg = "failed", failure.Error()
	}
	if _, err := r.pool.Exec(ctx, `
		UPDATE autodb_crawls SET status = $2, error = $3, finished_at = now() WHERE mod_id = $1`,
		modID, status, msg); err != nil {
		return fmt.Errorf("postgres: finish crawl of %d: %w", modID, err)
	}
	return nil
}

// Status says how far a vehicle's crawl is; the zero Status for a vehicle
// never queued or not from Auto-DB.
func (r *CrawlRepository) Status(ctx context.Context, vehicleID int64) (crawler.Status, error) {
	var (
		s        crawler.Status
		finished *time.Time
	)
	err := r.pool.QueryRow(ctx, `
		SELECT c.status, c.sections_total, c.sections_done, c.parts, c.finished_at
		FROM autodb_crawls c JOIN vehicles v ON v.autodb_mod_id = c.mod_id
		WHERE v.id = $1`, vehicleID).Scan(&s.State, &s.SectionsTotal, &s.SectionsDone, &s.Parts, &finished)
	if errors.Is(err, pgx.ErrNoRows) {
		return crawler.Status{}, nil
	}
	if err != nil {
		return s, fmt.Errorf("postgres: crawl status of vehicle %d: %w", vehicleID, err)
	}
	if finished != nil {
		s.FinishedAtUnix = finished.Unix()
	}
	return s, nil
}
