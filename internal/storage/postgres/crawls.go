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
		if _, err := tx.Exec(ctx, `
			INSERT INTO autodb_mod_sections (mod_id, section_id) VALUES ($1, $2)
			ON CONFLICT (mod_id, section_id) DO UPDATE SET fetched_at = now()`, modID, sectionID); err != nil {
			return fmt.Errorf("postgres: mark section %d of %d stored: %w", sectionID, modID, err)
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

// SaveSections records the paths of sections, the same for every vehicle.
func (r *CrawlRepository) SaveSections(ctx context.Context, sections []crawler.Section) error {
	ids, paths := make([]int64, len(sections)), make([]string, len(sections))
	for i, s := range sections {
		ids[i], paths[i] = s.ID, s.Path
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO autodb_sections (id, path)
		SELECT DISTINCT ON (x.id) x.id, x.path FROM unnest($1::bigint[], $2::text[]) AS x(id, path)
		ON CONFLICT (id) DO UPDATE SET path = excluded.path`, ids, paths)
	if err != nil {
		return fmt.Errorf("postgres: save section paths: %w", err)
	}
	return nil
}

// Fetched returns the sections of a modification stored since then.
func (r *CrawlRepository) Fetched(ctx context.Context, modID int64, since time.Time) (map[int64]bool, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT section_id FROM autodb_mod_sections WHERE mod_id = $1 AND fetched_at >= $2`, modID, since)
	if err != nil {
		return nil, fmt.Errorf("postgres: stored sections of %d: %w", modID, err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, fmt.Errorf("postgres: stored sections of %d: %w", modID, err)
	}
	out := make(map[int64]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

// Matching finds the sections a buyer's search names for their vehicle:
// those whose paths hold a word of the category or of its subcategories --
// the shop's words are Ukrainian and Russian, and the catalogue's paths
// Russian -- or one of the search's own words. Sections stored since then
// are left out; the more specific ones, deeper in the tree, come first.
func (r *CrawlRepository) Matching(ctx context.Context, vehicleID int64, category string, words []string, since time.Time, limit int) (int64, []int64, error) {
	var modID int64
	err := r.pool.QueryRow(ctx, `SELECT autodb_mod_id FROM vehicles WHERE id = $1 AND autodb_mod_id IS NOT NULL`, vehicleID).Scan(&modID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil, nil
	}
	if err != nil {
		return 0, nil, fmt.Errorf("postgres: modification of vehicle %d: %w", vehicleID, err)
	}
	if words == nil {
		words = []string{}
	}
	rows, err := r.pool.Query(ctx, `
		WITH terms AS (
			SELECT lower(k.word) AS term FROM category_keywords k JOIN categories c ON c.id = k.category_id
			WHERE $2 <> '' AND (c.slug = $2 OR c.parent_id = (SELECT id FROM categories WHERE slug = $2))
			UNION
			SELECT lower(w) FROM unnest($3::text[]) w WHERE char_length(w) >= 4
		)
		SELECT s.id FROM autodb_sections s
		WHERE EXISTS (SELECT 1 FROM terms WHERE lower(s.path) LIKE '%' || terms.term || '%')
		  AND NOT EXISTS (SELECT 1 FROM autodb_mod_sections f
		                  WHERE f.mod_id = $1 AND f.section_id = s.id AND f.fetched_at >= $4)
		ORDER BY char_length(s.path) - char_length(replace(s.path, '›', '')) DESC, s.id
		LIMIT $5`, modID, category, words, since, limit)
	if err != nil {
		return 0, nil, fmt.Errorf("postgres: sections matching a search: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return 0, nil, fmt.Errorf("postgres: sections matching a search: %w", err)
	}
	return modID, ids, nil
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

// Watch records that a buyer waits for the vehicle's crawl. The crawl's row
// is locked while it is read, so a crawl finishing at the same moment either
// sees the buyer among its waiters or is seen done, and no one waits for a
// crawl that has already ended. A buyer waits for one vehicle's crawl once.
func (r *CrawlRepository) Watch(ctx context.Context, userID, vehicleID int64) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO autodb_crawl_waiters (mod_id, user_id, vehicle_id)
		SELECT c.mod_id, $1, v.id FROM vehicles v JOIN autodb_crawls c ON c.mod_id = v.autodb_mod_id
		WHERE v.id = $2 AND c.status <> 'done'
		FOR SHARE OF c
		ON CONFLICT (mod_id, user_id) DO UPDATE SET vehicle_id = excluded.vehicle_id`, userID, vehicleID)
	if err != nil {
		return fmt.Errorf("postgres: watch crawl of vehicle %d: %w", vehicleID, err)
	}
	return nil
}

// Waiters takes the buyers waiting for a modification's crawl.
func (r *CrawlRepository) Waiters(ctx context.Context, modID int64) ([]crawler.Waiter, error) {
	rows, err := r.pool.Query(ctx, `
		DELETE FROM autodb_crawl_waiters WHERE mod_id = $1 RETURNING user_id, vehicle_id`, modID)
	if err != nil {
		return nil, fmt.Errorf("postgres: waiters of crawl %d: %w", modID, err)
	}
	ws, err := pgx.CollectRows(rows, pgx.RowToStructByPos[crawler.Waiter])
	if err != nil {
		return nil, fmt.Errorf("postgres: waiters of crawl %d: %w", modID, err)
	}
	return ws, nil
}

// RetryFailed queues again the failed crawls someone waits for that failed
// more than after ago, and forgets waits older than keep.
func (r *CrawlRepository) RetryFailed(ctx context.Context, after, keep time.Duration) error {
	if _, err := r.pool.Exec(ctx, `
		DELETE FROM autodb_crawl_waiters WHERE created_at < now() - make_interval(secs => $1)`, keep.Seconds()); err != nil {
		return fmt.Errorf("postgres: forget old crawl waiters: %w", err)
	}
	if _, err := r.pool.Exec(ctx, `
		UPDATE autodb_crawls c SET status = 'queued', queued_at = now(), error = ''
		WHERE c.status = 'failed' AND c.finished_at < now() - make_interval(secs => $1)
		  AND EXISTS (SELECT 1 FROM autodb_crawl_waiters w WHERE w.mod_id = c.mod_id)`, after.Seconds()); err != nil {
		return fmt.Errorf("postgres: retry failed crawls: %w", err)
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
