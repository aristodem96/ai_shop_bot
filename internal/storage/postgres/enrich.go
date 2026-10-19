package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aristodem96/ai_shop_bot/internal/autodb"
	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/enrich"
)

var _ enrich.Store = (*EnrichRepository)(nil)

// EnrichRepository caches Auto-DB articles and matches products to them.
type EnrichRepository struct {
	pool *pgxpool.Pool
}

// NewEnrichRepository returns a repository backed by pool.
func NewEnrichRepository(pool *pgxpool.Pool) *EnrichRepository {
	return &EnrichRepository{pool: pool}
}

// Article returns a cached article with its crosses.
func (r *EnrichRepository) Article(ctx context.Context, supplierID int64, key string) (*enrich.Article, bool, error) {
	a := enrich.Article{SupplierID: supplierID, Key: key}
	var specs, vehicles []byte
	err := r.pool.QueryRow(ctx, `
		SELECT a.number, coalesce(s.name, ''), a.name, a.specs, a.images, a.oem_keys, a.vehicles
		FROM autodb_articles a LEFT JOIN autodb_suppliers s ON s.id = a.supplier_id
		WHERE a.supplier_id = $1 AND a.article_key = $2`, supplierID, key).
		Scan(&a.Number, &a.Brand, &a.Name, &specs, &a.Images, &a.OEMKeys, &vehicles)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("postgres: article %s of %d: %w", key, supplierID, err)
	}
	if err := json.Unmarshal(specs, &a.Specs); err != nil {
		return nil, false, fmt.Errorf("postgres: specs of article %s: %w", key, err)
	}
	if vehicles != nil {
		a.VehiclesKnown = true
		if err := json.Unmarshal(vehicles, &a.Vehicles); err != nil {
			return nil, false, fmt.Errorf("postgres: vehicles of article %s: %w", key, err)
		}
	}
	rows, err := r.pool.Query(ctx, `
		SELECT brand_key, cross_key FROM autodb_crosses WHERE supplier_id = $1 AND article_key = $2`, supplierID, key)
	if err != nil {
		return nil, false, fmt.Errorf("postgres: crosses of %s: %w", key, err)
	}
	if a.Cross, err = pgx.CollectRows(rows, pgx.RowToStructByPos[enrich.Cross]); err != nil {
		return nil, false, fmt.Errorf("postgres: scan crosses of %s: %w", key, err)
	}
	return &a, true, nil
}

// SaveArticle stores an article and its crosses, and its brand's name when
// the card gave one.
func (r *EnrichRepository) SaveArticle(ctx context.Context, a *enrich.Article) error {
	specs, err := json.Marshal(a.Specs)
	if err != nil {
		return fmt.Errorf("postgres: encode specs: %w", err)
	}
	vehicles, err := json.Marshal(nonNilVehicles(a.Vehicles))
	if err != nil {
		return fmt.Errorf("postgres: encode vehicles: %w", err)
	}
	brands, keys := make([]string, len(a.Cross)), make([]string, len(a.Cross))
	for i, c := range a.Cross {
		brands[i], keys[i] = c.BrandKey, c.Key
	}
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO autodb_articles (supplier_id, article_key, number, name, specs, images, oem_keys, vehicles)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (supplier_id, article_key) DO UPDATE SET number = excluded.number, name = excluded.name,
				specs = excluded.specs, images = excluded.images, oem_keys = excluded.oem_keys,
				vehicles = excluded.vehicles, fetched_at = now()`,
			a.SupplierID, a.Key, a.Number, a.Name, specs, nonNil(a.Images), nonNil(a.OEMKeys), vehicles); err != nil {
			return fmt.Errorf("postgres: save article %s: %w", a.Key, err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM autodb_crosses WHERE supplier_id = $1 AND article_key = $2`, a.SupplierID, a.Key); err != nil {
			return fmt.Errorf("postgres: clear crosses of %s: %w", a.Key, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO autodb_crosses (supplier_id, article_key, brand_key, cross_key)
			SELECT $1, $2, x.* FROM unnest($3::text[], $4::text[]) AS x
			ON CONFLICT DO NOTHING`, a.SupplierID, a.Key, brands, keys); err != nil {
			return fmt.Errorf("postgres: save crosses of %s: %w", a.Key, err)
		}
		if a.Brand != "" {
			if _, err := tx.Exec(ctx, `
				INSERT INTO autodb_suppliers (id, name, name_key) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
				a.SupplierID, a.Brand, domain.NormalizeOEM(a.Brand)); err != nil {
				return fmt.Errorf("postgres: save supplier %d: %w", a.SupplierID, err)
			}
		}
		return nil
	})
}

// pendingSelect reads a product to match with the keys its brand goes by:
// one never looked for, or one not found a month ago, when crawls have
// named more brands. A product made for a part request is matched by its
// request already.
const pendingSelect = `
	SELECT p.id, p.part_key, CASE WHEN p.brand_key = '' THEN '{}'::text[] ELSE array(
		SELECT p.brand_key UNION
		SELECT a.alias FROM brand_aliases a JOIN brands b ON b.id = a.brand_id WHERE b.name = p.brand) END
	FROM products p
	WHERE (p.autodb_checked_at IS NULL
	       OR (p.autodb_supplier_id IS NULL AND p.autodb_checked_at < now() - interval '30 days'))
	  AND p.part_key <> '' AND p.request_id IS NULL`

// Pending returns products not looked for yet, those buyers opened first.
func (r *EnrichRepository) Pending(ctx context.Context, limit int) ([]enrich.Product, error) {
	rows, err := r.pool.Query(ctx, pendingSelect+`
		ORDER BY p.autodb_wanted_at DESC NULLS LAST, p.id LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: products to enrich: %w", err)
	}
	ps, err := pgx.CollectRows(rows, pgx.RowToStructByPos[enrich.Product])
	if err != nil {
		return nil, fmt.Errorf("postgres: scan products to enrich: %w", err)
	}
	return ps, nil
}

// PendingProduct returns one product if it was not looked for yet, and
// marks it wanted, so the background pass takes it first should this look
// fail.
func (r *EnrichRepository) PendingProduct(ctx context.Context, id int64) (*enrich.Product, bool, error) {
	rows, err := r.pool.Query(ctx, pendingSelect+` AND p.id = $1`, id)
	if err != nil {
		return nil, false, fmt.Errorf("postgres: product %d to enrich: %w", id, err)
	}
	p, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[enrich.Product])
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("postgres: scan product %d to enrich: %w", id, err)
	}
	if _, err := r.pool.Exec(ctx, `UPDATE products SET autodb_wanted_at = now() WHERE id = $1`, id); err != nil {
		return nil, false, fmt.Errorf("postgres: want product %d: %w", id, err)
	}
	return &p, true, nil
}

// SupplierKeys returns the brand keys of the supplier ids it knows.
func (r *EnrichRepository) SupplierKeys(ctx context.Context, ids []int64) (map[int64]string, error) {
	out := make(map[int64]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT id, name_key FROM autodb_suppliers WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("postgres: suppliers: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id  int64
			key string
		)
		if err := rows.Scan(&id, &key); err != nil {
			return nil, fmt.Errorf("postgres: scan supplier: %w", err)
		}
		out[id] = key
	}
	return out, rows.Err()
}

// Matched links the product to its article and fills what the product
// lacks: a photo, and the carmakers' numbers after its own, which search by
// number and analogues match on.
func (r *EnrichRepository) Matched(ctx context.Context, productID int64, a *enrich.Article) error {
	if _, err := r.pool.Exec(ctx, `
		UPDATE products SET autodb_supplier_id = $2, autodb_checked_at = now(), updated_at = now(),
			image_url = CASE WHEN image_url = '' AND cardinality($3::text[]) > 0 THEN ($3::text[])[1] ELSE image_url END,
			oem_numbers = oem_numbers || array(SELECT k FROM unnest($4::text[]) k WHERE k <> ALL(oem_numbers))
		WHERE id = $1`, productID, a.SupplierID, nonNil(a.Images), nonNil(a.OEMKeys)); err != nil {
		return fmt.Errorf("postgres: enrich product %d: %w", productID, err)
	}
	return nil
}

// Unmatched records that the product has no article to be found.
func (r *EnrichRepository) Unmatched(ctx context.Context, productID int64) error {
	if _, err := r.pool.Exec(ctx, `UPDATE products SET autodb_checked_at = now() WHERE id = $1`, productID); err != nil {
		return fmt.Errorf("postgres: mark product %d: %w", productID, err)
	}
	return nil
}

// ProductArticle names the article a product was matched to.
func (r *EnrichRepository) ProductArticle(ctx context.Context, productID int64) (int64, string, bool, error) {
	var (
		supplierID int64
		number     string
	)
	err := r.pool.QueryRow(ctx, `
		SELECT a.supplier_id, a.number FROM products p
		JOIN autodb_articles a ON a.supplier_id = p.autodb_supplier_id AND a.article_key = p.part_key
		WHERE p.id = $1`, productID).Scan(&supplierID, &number)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", false, nil
	}
	if err != nil {
		return 0, "", false, fmt.Errorf("postgres: article of product %d: %w", productID, err)
	}
	return supplierID, number, true, nil
}

// nonNilVehicles writes an empty list, not null: null means "not fetched".
func nonNilVehicles(vs []autodb.ArticleVehicle) []autodb.ArticleVehicle {
	if vs == nil {
		return []autodb.ArticleVehicle{}
	}
	return vs
}

// nonNil writes an empty array, not NULL, for nothing.
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
