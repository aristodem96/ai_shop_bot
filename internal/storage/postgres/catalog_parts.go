package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/partnames"
	"github.com/aristodem96/ai_shop_bot/internal/service"
)

var _ partnames.Store = (*CatalogPartRepository)(nil)

// CatalogPartRepository reads the crawled catalogue as parts a buyer can
// ask the shop to order, and keeps their names' translations.
type CatalogPartRepository struct {
	pool *pgxpool.Pool
}

// NewCatalogPartRepository returns a repository backed by pool.
func NewCatalogPartRepository(pool *pgxpool.Pool) *CatalogPartRepository {
	return &CatalogPartRepository{pool: pool}
}

// catalogPartColumns read a part with its translation, when there is one.
const catalogPartColumns = `mp.id, mp.brand, mp.supplier_id, mp.number, coalesce(n.name_uk, mp.name) AS name, coalesce(n.category, '') AS category`

// ForVehicle finds the catalogue's parts for a vehicle that the shop has no
// product for. Query words match the Ukrainian name or the Russian one, a
// category its subcategories too. A part listed in several sections comes
// once.
func (r *CatalogPartRepository) ForVehicle(ctx context.Context, f service.SearchFilter) ([]domain.CatalogPart, error) {
	words := strings.Fields(strings.ToLower(f.Query))
	if words == nil {
		words = []string{}
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, brand, supplier_id, number, name, category FROM (
			SELECT DISTINCT ON (mp.article_key, mp.brand_key) `+catalogPartColumns+`
			FROM vehicles v
			JOIN autodb_mod_parts mp ON mp.mod_id = v.autodb_mod_id
			LEFT JOIN autodb_part_names n ON n.name = mp.name
			WHERE v.id = $1
			  AND ($2 = '' OR n.category = $2 OR n.category IN (
			      SELECT c.slug FROM categories c JOIN categories s ON s.id = c.parent_id WHERE s.slug = $2))
			  AND (SELECT coalesce(bool_and(lower(coalesce(n.name_uk, '') || ' ' || mp.name) LIKE '%' || w || '%'), true)
			       FROM unnest($3::text[]) w)
			  AND ($4 = '' OR mp.article_key = $4)
			  AND ($5 = '' OR mp.brand_key = $5)
			  AND NOT EXISTS (
			      SELECT 1 FROM products p WHERE p.part_key = mp.article_key AND p.is_active AND p.request_id IS NULL
			      AND (p.brand = '' OR p.brand_key = mp.brand_key OR EXISTS (
			          SELECT 1 FROM brand_aliases a JOIN brands b ON b.id = a.brand_id
			          WHERE a.alias = mp.brand_key AND b.name = p.brand)))
			ORDER BY mp.article_key, mp.brand_key, mp.id
		) x
		ORDER BY category = '', brand, number
		LIMIT $6`,
		f.VehicleID, f.Category, words, domain.NormalizeOEM(f.PartNumber), domain.NormalizeOEM(f.Brand), f.Limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: catalogue parts of vehicle %d: %w", f.VehicleID, err)
	}
	parts, err := pgx.CollectRows(rows, pgx.RowToStructByPos[domain.CatalogPart])
	if err != nil {
		return nil, fmt.Errorf("postgres: scan catalogue parts: %w", err)
	}
	return parts, nil
}

// CatalogPart returns one part by the id its button carries; ErrNotFound
// once a recrawl has replaced it.
func (r *CatalogPartRepository) CatalogPart(ctx context.Context, id int64) (*domain.CatalogPart, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+catalogPartColumns+` FROM autodb_mod_parts mp
		LEFT JOIN autodb_part_names n ON n.name = mp.name WHERE mp.id = $1`, id)
	if err != nil {
		return nil, fmt.Errorf("postgres: catalogue part %d: %w", id, err)
	}
	p, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[domain.CatalogPart])
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: scan catalogue part %d: %w", id, err)
	}
	return &p, nil
}

// CrawlState says how complete a vehicle's catalogue is: "" for a vehicle
// not from Auto-DB, "none" for one never crawled, else the crawl's status.
func (r *CatalogPartRepository) CrawlState(ctx context.Context, vehicleID int64) (string, error) {
	var state string
	err := r.pool.QueryRow(ctx, `
		SELECT coalesce(c.status, 'none') FROM vehicles v
		LEFT JOIN autodb_crawls c ON c.mod_id = v.autodb_mod_id
		WHERE v.id = $1 AND v.autodb_mod_id IS NOT NULL`, vehicleID).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("postgres: crawl state of vehicle %d: %w", vehicleID, err)
	}
	return state, nil
}

// Untranslated returns crawled names that have no translation yet.
func (r *CatalogPartRepository) Untranslated(ctx context.Context, limit int) ([]string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT mp.name FROM autodb_mod_parts mp
		WHERE mp.name <> '' AND NOT EXISTS (SELECT 1 FROM autodb_part_names n WHERE n.name = mp.name)
		ORDER BY mp.name LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: untranslated part names: %w", err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("postgres: scan part names: %w", err)
	}
	return names, nil
}

// SaveNames stores translations; a name translated again is replaced.
func (r *CatalogPartRepository) SaveNames(ctx context.Context, names []partnames.Name) error {
	name, uk, cat := make([]string, len(names)), make([]string, len(names)), make([]string, len(names))
	for i, n := range names {
		name[i], uk[i], cat[i] = n.Name, n.NameUK, n.Category
	}
	if _, err := r.pool.Exec(ctx, `
		INSERT INTO autodb_part_names (name, name_uk, category)
		SELECT * FROM unnest($1::text[], $2::text[], $3::text[])
		ON CONFLICT (name) DO UPDATE SET name_uk = excluded.name_uk, category = excluded.category`,
		name, uk, cat); err != nil {
		return fmt.Errorf("postgres: save part names: %w", err)
	}
	return nil
}
