package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/service"
)

var _ service.CatalogRepository = (*CatalogRepository)(nil)

// CatalogRepository reads categories and products.
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

// Category finds a category.
func (r *CatalogRepository) Category(ctx context.Context, id int64) (*domain.Category, error) {
	var c domain.Category
	err := r.pool.QueryRow(ctx,
		`SELECT id, slug, title FROM categories WHERE id = $1`, id,
	).Scan(&c.ID, &c.Slug, &c.Title)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: get category %d: %w", id, err)
	}
	return &c, nil
}

// productColumns is shared by every product query, so scanProduct reads them
// all the same way.
const productColumns = `
	p.id, p.category_id, p.sku, p.name, p.brand, p.description,
	p.oem_numbers, p.specs, p.universal, p.price, p.stock, p.image_url, p.part_type`

// fitsVehicle is the condition "p fits the vehicle in placeholder ph": a
// universal part, or one fitted to it directly or through its model. Zero
// means any vehicle.
func fitsVehicle(ph string) string {
	return `(` + ph + ` = 0 OR p.universal OR EXISTS (
		SELECT 1 FROM vehicle_fitments f WHERE f.product_id = p.id AND f.vehicle_id = ` + ph + `))`
}

func scanProduct(row pgx.Row) (domain.Product, error) {
	var (
		p     domain.Product
		price int64
		specs []byte
	)
	err := row.Scan(&p.ID, &p.CategoryID, &p.SKU, &p.Name, &p.Brand, &p.Description,
		&p.OEMNumbers, &specs, &p.Universal, &price, &p.Stock, &p.ImageURL, &p.PartType)
	if err != nil {
		return p, err
	}
	p.Price = domain.Money(price)
	if err := json.Unmarshal(specs, &p.Specs); err != nil {
		return p, fmt.Errorf("decode specs of product %d: %w", p.ID, err)
	}
	return p, nil
}

func collectProducts(rows pgx.Rows) ([]domain.Product, error) {
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.Product, error) {
		return scanProduct(row)
	})
}

// ProductsByCategory returns one page of a category, in-stock products first
// and cheapest first within that. A sold-out part at the top of the list is a
// dead end; at the bottom it still tells the buyer the part exists.
func (r *CatalogRepository) ProductsByCategory(ctx context.Context, categoryID, vehicleID int64, limit, offset int) ([]domain.Product, int, error) {
	var total int
	err := r.pool.QueryRow(ctx, `
		SELECT count(*) FROM products p
		WHERE p.category_id = $1 AND p.is_active AND `+fitsVehicle("$2"),
		categoryID, vehicleID,
	).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("postgres: count products of category %d: %w", categoryID, err)
	}

	rows, err := r.pool.Query(ctx, `
		SELECT `+productColumns+` FROM products p
		WHERE p.category_id = $1 AND p.is_active AND `+fitsVehicle("$2")+`
		ORDER BY (p.stock > 0) DESC, p.price, p.id
		LIMIT $3 OFFSET $4`,
		categoryID, vehicleID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("postgres: list products of category %d: %w", categoryID, err)
	}
	products, err := collectProducts(rows)
	if err != nil {
		return nil, 0, fmt.Errorf("postgres: scan products: %w", err)
	}
	return products, total, nil
}

// Product returns an active product with the vehicles it fits and the notes
// on each fitment.
func (r *CatalogRepository) Product(ctx context.Context, id int64) (*domain.Product, error) {
	p, err := scanProduct(r.pool.QueryRow(ctx, `
		SELECT `+productColumns+` FROM products p
		WHERE p.id = $1 AND p.is_active`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: get product %d: %w", id, err)
	}

	// One row per vehicle: a part fitted to a car both directly and through
	// its model would otherwise list it twice. The direct fitment is the
	// more specific, so its note wins.
	rows, err := r.pool.Query(ctx, `
		SELECT * FROM (
			SELECT DISTINCT ON (v.id) `+vehicleColumns+`,
				concat_ws(', ', nullif(f.position, ''), nullif(f.note, ''))
			FROM vehicle_fitments f
			JOIN vehicles v ON v.id = f.vehicle_id
			WHERE f.product_id = $1
			ORDER BY v.id, f.direct DESC
		) fits
		ORDER BY make, model, year_from, engine`, id)
	if err != nil {
		return nil, fmt.Errorf("postgres: list fitments of product %d: %w", id, err)
	}
	type fit struct {
		vehicle domain.Vehicle
		note    string
	}
	fits, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (fit, error) {
		var f fit
		v := &f.vehicle
		err := row.Scan(&v.ID, &v.Make, &v.Model, &v.YearFrom, &v.YearTo, &v.Engine, &v.Fuel,
			&v.OilViscosity, &v.OilVolume, &v.Cylinders, &f.note)
		return f, err
	})
	if err != nil {
		return nil, fmt.Errorf("postgres: scan fitments: %w", err)
	}
	for _, f := range fits {
		p.Fits = append(p.Fits, f.vehicle)
		if f.note != "" {
			if p.FitNotes == nil {
				p.FitNotes = make(map[int64]string)
			}
			p.FitNotes[f.vehicle.ID] = f.note
		}
	}
	return &p, nil
}

// Analogues ranks in-stock interchangeable parts: those sharing an OEM number
// with p first -- the same part from another brand -- then the same kind of
// part for any of the same vehicles, closest in price first. No model is
// involved: "analogues" from a button is a deterministic query, which is
// cheaper, instant and gives the same answer twice.
func (r *CatalogRepository) Analogues(ctx context.Context, p *domain.Product, vehicleID int64, limit int) ([]domain.Product, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+productColumns+` FROM products p
		WHERE p.id <> $1 AND p.is_active AND p.stock > 0 AND `+fitsVehicle("$5")+` AND (
			p.oem_numbers && $2::text[]
			OR (p.category_id = $3 AND (
				($6 AND p.universal)
				OR EXISTS (
					SELECT 1 FROM vehicle_fitments a
					JOIN vehicle_fitments b ON b.vehicle_id = a.vehicle_id
					WHERE a.product_id = p.id AND b.product_id = $1)))
		)
		ORDER BY (p.oem_numbers && $2::text[]) DESC, abs(p.price - $4), p.id
		LIMIT $7`,
		p.ID, p.OEMNumbers, p.CategoryID, int64(p.Price), vehicleID, p.Universal, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: analogues of product %d: %w", p.ID, err)
	}
	products, err := collectProducts(rows)
	if err != nil {
		return nil, fmt.Errorf("postgres: scan analogues: %w", err)
	}
	return products, nil
}

// KitParts returns in-stock parts of one type that fit the vehicle, cheapest
// first. Oils are universal, so for them the grade is what decides: it is
// matched against the specs with jsonb containment. The part_type index
// narrows the rows first, so the specs need no index of their own.
func (r *CatalogRepository) KitParts(ctx context.Context, vehicleID int64, partType, viscosity string, limit int) ([]domain.Product, error) {
	grade := []byte("[]")
	if viscosity != "" {
		var err error
		grade, err = json.Marshal([]domain.Spec{{Name: "В'язкість", Value: viscosity}})
		if err != nil {
			return nil, fmt.Errorf("postgres: encode viscosity: %w", err)
		}
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+productColumns+` FROM products p
		WHERE p.is_active AND p.stock > 0 AND p.part_type = $2 AND `+fitsVehicle("$1")+`
		  AND p.specs @> $3::jsonb
		ORDER BY p.price, p.id
		LIMIT $4`,
		vehicleID, partType, grade, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: kit parts %s for vehicle %d: %w", partType, vehicleID, err)
	}
	products, err := collectProducts(rows)
	if err != nil {
		return nil, fmt.Errorf("postgres: scan kit parts: %w", err)
	}
	return products, nil
}
