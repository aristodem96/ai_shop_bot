package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/service"
)

var _ service.CatalogRepository = (*CatalogRepository)(nil)

// CatalogRepository reads categories, products and their sizes.
type CatalogRepository struct {
	pool *pgxpool.Pool
}

// NewCatalogRepository returns a repository backed by pool.
func NewCatalogRepository(pool *pgxpool.Pool) *CatalogRepository {
	return &CatalogRepository{pool: pool}
}

// Categories lists the catalogue sections in menu order.
func (r *CatalogRepository) Categories(ctx context.Context) ([]domain.Category, error) {
	rows, err := r.pool.Query(ctx, `SELECT id, slug, title FROM categories ORDER BY position, id`)
	if err != nil {
		return nil, fmt.Errorf("postgres: list categories: %w", err)
	}
	cats, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.Category, error) {
		var c domain.Category
		err := row.Scan(&c.ID, &c.Slug, &c.Title)
		return c, err
	})
	if err != nil {
		return nil, fmt.Errorf("postgres: scan categories: %w", err)
	}
	return cats, nil
}

// CategoryBySlug finds a category.
func (r *CatalogRepository) CategoryBySlug(ctx context.Context, slug string) (*domain.Category, error) {
	var c domain.Category
	err := r.pool.QueryRow(ctx,
		`SELECT id, slug, title FROM categories WHERE slug = $1`, slug,
	).Scan(&c.ID, &c.Slug, &c.Title)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: get category %q: %w", slug, err)
	}
	return &c, nil
}

// productColumns and the stock lateral join are shared by every product
// query, so scanProduct reads them all the same way. The stock sum is a
// lateral subquery rather than a GROUP BY so that each query can add its own
// ORDER BY and WHERE on it without repeating the aggregate.
const productColumns = `
	p.id, p.category_id, p.sku, p.name, p.brand, p.description,
	p.color, p.season, p.gender, p.styles, p.price, p.image_url, s.stock`

const stockJoin = `
	CROSS JOIN LATERAL (
		SELECT coalesce(sum(v.stock), 0)::int AS stock
		FROM product_variants v WHERE v.product_id = p.id
	) s`

func scanProduct(row pgx.Row) (domain.Product, error) {
	var (
		p     domain.Product
		price int64
	)
	err := row.Scan(&p.ID, &p.CategoryID, &p.SKU, &p.Name, &p.Brand, &p.Description,
		&p.Color, &p.Season, &p.Gender, &p.Styles, &price, &p.ImageURL, &p.TotalStock)
	p.Price = domain.Money(price)
	return p, err
}

func collectProducts(rows pgx.Rows) ([]domain.Product, error) {
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.Product, error) {
		return scanProduct(row)
	})
}

// ProductsByCategory returns one page of a category, in-stock products first
// and cheapest first within that. A sold-out jacket at the top of the list is
// a dead end; at the bottom it still tells the buyer the model exists.
func (r *CatalogRepository) ProductsByCategory(ctx context.Context, categoryID int64, limit, offset int) ([]domain.Product, int, error) {
	var total int
	err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM products WHERE category_id = $1 AND is_active`, categoryID,
	).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("postgres: count products of category %d: %w", categoryID, err)
	}

	rows, err := r.pool.Query(ctx, `
		SELECT `+productColumns+`
		FROM products p`+stockJoin+`
		WHERE p.category_id = $1 AND p.is_active
		ORDER BY (s.stock > 0) DESC, p.price, p.id
		LIMIT $2 OFFSET $3`,
		categoryID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("postgres: list products of category %d: %w", categoryID, err)
	}
	products, err := collectProducts(rows)
	if err != nil {
		return nil, 0, fmt.Errorf("postgres: scan products: %w", err)
	}
	return products, total, nil
}

// Product returns an active product with its sizes.
func (r *CatalogRepository) Product(ctx context.Context, id int64) (*domain.Product, error) {
	p, err := scanProduct(r.pool.QueryRow(ctx, `
		SELECT `+productColumns+`
		FROM products p`+stockJoin+`
		WHERE p.id = $1 AND p.is_active`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: get product %d: %w", id, err)
	}

	rows, err := r.pool.Query(ctx, `
		SELECT id, product_id, size, stock
		FROM product_variants
		WHERE product_id = $1
		ORDER BY position, id`, id)
	if err != nil {
		return nil, fmt.Errorf("postgres: list variants of product %d: %w", id, err)
	}
	p.Variants, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.Variant, error) {
		var v domain.Variant
		err := row.Scan(&v.ID, &v.ProductID, &v.Size, &v.Stock)
		return v, err
	})
	if err != nil {
		return nil, fmt.Errorf("postgres: scan variants: %w", err)
	}
	return &p, nil
}

// Similar ranks in-stock products of the same category by how many style tags
// they share with p, then by how close their price is. No model is involved:
// "similar" from a button is a deterministic query, which is cheaper, instant
// and gives the same answer twice.
func (r *CatalogRepository) Similar(ctx context.Context, p *domain.Product, limit int) ([]domain.Product, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+productColumns+`
		FROM products p`+stockJoin+`
		WHERE p.category_id = $1 AND p.id <> $2 AND p.is_active AND s.stock > 0
		ORDER BY
			cardinality(ARRAY(SELECT unnest(p.styles) INTERSECT SELECT unnest($3::text[]))) DESC,
			abs(p.price - $4),
			p.id
		LIMIT $5`,
		p.CategoryID, p.ID, p.Styles, int64(p.Price), limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: similar to product %d: %w", p.ID, err)
	}
	products, err := collectProducts(rows)
	if err != nil {
		return nil, fmt.Errorf("postgres: scan similar products: %w", err)
	}
	return products, nil
}

// Variant returns one size of an active product.
func (r *CatalogRepository) Variant(ctx context.Context, id int64) (*domain.Variant, error) {
	var v domain.Variant
	err := r.pool.QueryRow(ctx, `
		SELECT v.id, v.product_id, v.size, v.stock
		FROM product_variants v
		JOIN products p ON p.id = v.product_id AND p.is_active
		WHERE v.id = $1`, id,
	).Scan(&v.ID, &v.ProductID, &v.Size, &v.Stock)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: get variant %d: %w", id, err)
	}
	return &v, nil
}
