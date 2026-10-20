package postgres

import (
	"context"
	"errors"
	"fmt"

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

// notStocked keeps a catalogue part the shop has no product for: the same
// article, from the same brand or one the shop's product names no brand.
const notStocked = `NOT EXISTS (
	SELECT 1 FROM products p WHERE p.part_key = mp.article_key AND ` + listed + `
	AND (p.brand = '' OR p.brand_key = mp.brand_key OR EXISTS (
		SELECT 1 FROM brand_aliases a JOIN brands b ON b.id = a.brand_id
		WHERE a.alias = mp.brand_key AND b.name = p.brand)))`

// filedParts are a vehicle's catalogue parts the shop does not stock that
// have a category, once each: a part only gets one when its name is
// translated, and the menu has nowhere to put the rest.
const filedParts = `
	SELECT DISTINCT ON (mp.article_key, mp.brand_key) ` + catalogPartColumns + `
	FROM vehicles v
	JOIN autodb_mod_parts mp ON mp.mod_id = v.autodb_mod_id
	JOIN autodb_part_names n ON n.name = mp.name AND n.category <> ''
	WHERE v.id = $1 AND ` + notStocked + `
	ORDER BY mp.article_key, mp.brand_key, mp.id`

// CategoryCounts counts a vehicle's catalogue parts on order in each
// category, for the menu.
func (r *CatalogPartRepository) CategoryCounts(ctx context.Context, vehicleID int64) (map[int64]int, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT c.id, count(*) FROM (`+filedParts+`) x JOIN categories c ON c.slug = x.category
		GROUP BY c.id`, vehicleID)
	if err != nil {
		return nil, fmt.Errorf("postgres: catalogue parts per category of vehicle %d: %w", vehicleID, err)
	}
	out := make(map[int64]int)
	var id int64
	var n int
	if _, err := pgx.ForEachRow(rows, []any{&id, &n}, func() error {
		out[id] = n
		return nil
	}); err != nil {
		return nil, fmt.Errorf("postgres: scan catalogue part counts: %w", err)
	}
	return out, nil
}

// ByCategory returns one page of a vehicle's catalogue parts on order in a
// category and its subcategories, by name, and how many there are.
func (r *CatalogPartRepository) ByCategory(ctx context.Context, categoryID, vehicleID int64, limit, offset int) ([]domain.CatalogPart, int, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, brand, supplier_id, number, name, category, count(*) OVER () FROM (`+filedParts+`) x
		WHERE x.category IN (SELECT slug FROM categories WHERE id = $2 OR parent_id = $2)
		ORDER BY name, brand, number
		LIMIT $3 OFFSET $4`, vehicleID, categoryID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("postgres: catalogue parts of category %d: %w", categoryID, err)
	}
	var (
		parts []domain.CatalogPart
		total int
	)
	var p domain.CatalogPart
	if _, err := pgx.ForEachRow(rows, []any{&p.ID, &p.Brand, &p.SupplierID, &p.Number, &p.Name, &p.Category, &total}, func() error {
		parts = append(parts, p)
		return nil
	}); err != nil {
		return nil, 0, fmt.Errorf("postgres: scan catalogue parts: %w", err)
	}
	if len(parts) == 0 && offset > 0 {
		// Past the end: the total still matters for paging back.
		if err := r.pool.QueryRow(ctx, `
			SELECT count(*) FROM (`+filedParts+`) x
			WHERE x.category IN (SELECT slug FROM categories WHERE id = $2 OR parent_id = $2)`,
			vehicleID, categoryID).Scan(&total); err != nil {
			return nil, 0, fmt.Errorf("postgres: count catalogue parts of category %d: %w", categoryID, err)
		}
	}
	return parts, total, nil
}

// ForVehicle finds the catalogue's parts for a vehicle that the shop has no
// product for. Query words match the Ukrainian name or the Russian one, a
// category its subcategories too -- and a part not yet filed, its name not
// translated, by its section's path holding a word of the category, so a
// search right after a crawl does not miss what the crawl brought. Of a
// query's ways of naming the part, any one matches, with all its words in
// the Ukrainian name or the Russian one. A part listed in several sections
// comes once.
func (r *CatalogPartRepository) ForVehicle(ctx context.Context, f service.SearchFilter) ([]domain.CatalogPart, error) {
	phrases := service.Phrases(f.Query)
	rows, err := r.pool.Query(ctx, `
		SELECT id, brand, supplier_id, number, name, category FROM (
			SELECT DISTINCT ON (mp.article_key, mp.brand_key) `+catalogPartColumns+`
			FROM vehicles v
			JOIN autodb_mod_parts mp ON mp.mod_id = v.autodb_mod_id
			LEFT JOIN autodb_part_names n ON n.name = mp.name
			WHERE v.id = $1
			  AND ($2 = '' OR n.category = $2 OR n.category IN (
			      SELECT c.slug FROM categories c JOIN categories s ON s.id = c.parent_id WHERE s.slug = $2)
			      OR (coalesce(n.category, '') = '' AND EXISTS (
			          SELECT 1 FROM autodb_sections sec
			          JOIN category_keywords k ON lower(sec.path) LIKE '%' || lower(k.word) || '%'
			          JOIN categories c ON c.id = k.category_id
			          WHERE sec.id = mp.section_id
			            AND (c.slug = $2 OR c.parent_id = (SELECT id FROM categories WHERE slug = $2)))))
			  AND (cardinality($3::text[]) = 0 OR EXISTS (
			      SELECT 1 FROM unnest($3::text[]) ph
			      WHERE (SELECT bool_and(lower(coalesce(n.name_uk, '') || ' ' || mp.name) LIKE '%' || w || '%')
			             FROM unnest(string_to_array(ph, ' ')) w)))
			  AND ($4 = '' OR mp.article_key = $4)
			  AND ($5 = '' OR mp.brand_key = $5)
			  AND `+notStocked+`
			ORDER BY mp.article_key, mp.brand_key, mp.id
		) x
		ORDER BY category = '', brand, number
		LIMIT $6`,
		f.VehicleID, f.Category, phrases, domain.NormalizeOEM(f.PartNumber), domain.NormalizeOEM(f.Brand), f.Limit)
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

// Progress says how far a vehicle's catalogue has loaded; nil for a
// vehicle not from the catalogue.
func (r *CatalogPartRepository) Progress(ctx context.Context, vehicleID int64) (*domain.CatalogProgress, error) {
	var (
		p     domain.CatalogProgress
		modID int64
	)
	err := r.pool.QueryRow(ctx, `
		SELECT v.autodb_mod_id, coalesce(c.status, 'none'), coalesce(c.sections_done, 0), coalesce(c.sections_total, 0)
		FROM vehicles v LEFT JOIN autodb_crawls c ON c.mod_id = v.autodb_mod_id
		WHERE v.id = $1 AND v.autodb_mod_id IS NOT NULL`, vehicleID).Scan(&modID, &p.State, &p.SectionsDone, &p.SectionsTotal)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: catalogue progress of vehicle %d: %w", vehicleID, err)
	}
	err = r.pool.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE filed) FROM (
			SELECT mp.article_key, mp.brand_key, bool_or(coalesce(n.category, '') <> '') AS filed
			FROM autodb_mod_parts mp LEFT JOIN autodb_part_names n ON n.name = mp.name
			WHERE mp.mod_id = $1
			GROUP BY mp.article_key, mp.brand_key) x`, modID).Scan(&p.Parts, &p.Filed)
	if err != nil {
		return nil, fmt.Errorf("postgres: count catalogue parts of vehicle %d: %w", vehicleID, err)
	}
	return &p, nil
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

// Missing returns those of names with no translation yet.
func (r *CatalogPartRepository) Missing(ctx context.Context, names []string) ([]string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT x FROM unnest($1::text[]) x
		WHERE NOT EXISTS (SELECT 1 FROM autodb_part_names n WHERE n.name = x)`, names)
	if err != nil {
		return nil, fmt.Errorf("postgres: untranslated of %d names: %w", len(names), err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("postgres: scan part names: %w", err)
	}
	return out, nil
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
