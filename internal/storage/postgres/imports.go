package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aristodem96/ai_shop_bot/internal/importer"
)

var _ importer.Store = (*ImportRepository)(nil)

// ImportRepository writes supplier price lists into the catalogue.
type ImportRepository struct {
	pool *pgxpool.Pool
}

// NewImportRepository returns a repository backed by pool.
func NewImportRepository(pool *pgxpool.Pool) *ImportRepository {
	return &ImportRepository{pool: pool}
}

// BrandAliases maps every known spelling key to its brand.
func (r *ImportRepository) BrandAliases(ctx context.Context) (map[string]string, error) {
	rows, err := r.pool.Query(ctx, `SELECT a.alias, b.name FROM brand_aliases a JOIN brands b ON b.id = a.brand_id`)
	if err != nil {
		return nil, fmt.Errorf("postgres: list brand aliases: %w", err)
	}
	out := make(map[string]string)
	var alias, name string
	_, err = pgx.ForEachRow(rows, []any{&alias, &name}, func() error {
		out[alias] = name
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("postgres: scan brand aliases: %w", err)
	}
	return out, nil
}

// VehicleModels lists the models of the catalogue.
func (r *ImportRepository) VehicleModels(ctx context.Context) ([]importer.Model, error) {
	rows, err := r.pool.Query(ctx, `SELECT id, make, model FROM vehicle_models ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("postgres: list vehicle models: %w", err)
	}
	models, err := pgx.CollectRows(rows, pgx.RowToStructByPos[importer.Model])
	if err != nil {
		return nil, fmt.Errorf("postgres: scan vehicle models: %w", err)
	}
	return models, nil
}

// Categories lists the category tree in menu order, sections before their
// subcategories, each with the file's keywords and then the owner's.
func (r *ImportRepository) Categories(ctx context.Context) ([]importer.Category, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT c.slug, coalesce(p.slug, ''), c.title, c.part_type, c.universal,
			c.keywords || coalesce((SELECT array_agg(k.word ORDER BY k.id)
				FROM category_keywords k WHERE k.category_id = c.id), '{}')
		FROM categories c
		LEFT JOIN categories p ON p.id = c.parent_id
		ORDER BY c.position, c.id`)
	if err != nil {
		return nil, fmt.Errorf("postgres: list categories: %w", err)
	}
	cats, err := pgx.CollectRows(rows, pgx.RowToStructByPos[importer.Category])
	if err != nil {
		return nil, fmt.Errorf("postgres: scan categories: %w", err)
	}
	return cats, nil
}

// errDryRun rolls a dry run back once its counts are taken.
var errDryRun = errors.New("dry run")

// Apply writes a batch in one transaction: the shop sees the old price list
// or the new one, never half of each, and a failure changes nothing.
//
// Rows travel by COPY into temporary tables and are merged from there with a
// few set-based statements. Eighty thousand single-row upserts would take
// minutes; this takes seconds.
func (r *ImportRepository) Apply(ctx context.Context, b *importer.Batch, dryRun bool) (importer.Result, error) {
	var res importer.Result
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if err := createCategories(ctx, tx, b.CreateCategories); err != nil {
			return err
		}
		if err := createBrands(ctx, tx, b.NewBrands); err != nil {
			return err
		}
		if err := stage(ctx, tx, b.Products); err != nil {
			return err
		}

		// Lock the supplier's products in id order, as checkout does, before
		// reading what open orders hold. Checkout of these products waits
		// for the import, and the stock computed below cannot miss an order
		// placed while it runs.
		if _, err := tx.Exec(ctx, `SELECT 1 FROM products WHERE source = $1 ORDER BY id FOR UPDATE`, b.Source); err != nil {
			return fmt.Errorf("postgres: lock supplier products: %w", err)
		}
		// What is on sale before the file is applied: the base the
		// deactivation limit is a share of.
		var active int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM products WHERE source = $1 AND is_active`,
			b.Source).Scan(&active); err != nil {
			return fmt.Errorf("postgres: count active products: %w", err)
		}

		// A stock column counts what is physically on the shelf, and units
		// held by unpaid or not yet shipped orders are on it still. The
		// catalogue's stock is what is free to sell, so they are subtracted:
		// otherwise an order that later expires would give its units back a
		// second time.
		rows, err := tx.Query(ctx, `
			INSERT INTO products (source, external_id, sku, category_id, name, brand, description,
				oem_numbers, price, stock, lead_time_days, image_url, part_type, universal)
			SELECT $1, i.external_id, i.sku, c.id, i.name, i.brand, i.description,
				i.oem, i.price, coalesce(i.stock, 0), i.lead_days, i.image, i.part_type, i.universal
			FROM import_products i
			JOIN categories c ON c.slug = i.category
			ON CONFLICT (source, external_id) DO UPDATE SET
				category_id = EXCLUDED.category_id, name = EXCLUDED.name, brand = EXCLUDED.brand,
				description = coalesce(nullif(EXCLUDED.description, ''), products.description),
				oem_numbers = EXCLUDED.oem_numbers, price = EXCLUDED.price,
				stock = greatest(EXCLUDED.stock - (
					SELECT coalesce(sum(oi.qty), 0) FROM order_items oi
					JOIN orders o ON o.id = oi.order_id
					WHERE oi.product_id = products.id AND oi.from_stock
					  AND o.status IN ('awaiting_payment', 'paid', 'confirmed')), 0),
				lead_time_days = EXCLUDED.lead_time_days,
				image_url = coalesce(nullif(EXCLUDED.image_url, ''), products.image_url),
				part_type = EXCLUDED.part_type, universal = EXCLUDED.universal,
				is_active = true, updated_at = now()
			RETURNING xmax = 0`, b.Source)
		if err != nil {
			return fmt.Errorf("postgres: upsert products: %w", err)
		}
		var inserted bool
		_, err = pgx.ForEachRow(rows, []any{&inserted}, func() error {
			if inserted {
				res.Inserted++
			} else {
				res.Updated++
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("postgres: upsert products: %w", err)
		}

		// The file is the whole truth about its fitments: they are replaced,
		// so a vehicle the supplier dropped stops matching.
		if _, err := tx.Exec(ctx, `
			DELETE FROM product_model_fitments mf
			USING products p, import_products i
			WHERE mf.product_id = p.id AND p.source = $1 AND p.external_id = i.external_id`, b.Source); err != nil {
			return fmt.Errorf("postgres: clear fitments: %w", err)
		}
		tag, err := tx.Exec(ctx, `
			INSERT INTO product_model_fitments (product_id, model_id, year_from, year_to, position, note)
			SELECT p.id, f.model_id, nullif(f.year_from, 0), nullif(f.year_to, 0), f.position, f.note
			FROM import_fits f
			JOIN products p ON p.source = $1 AND p.external_id = f.external_id
			ON CONFLICT DO NOTHING`, b.Source)
		if err != nil {
			return fmt.Errorf("postgres: insert fitments: %w", err)
		}
		res.Fitments = int(tag.RowsAffected())

		if b.DeactivateMissing {
			tag, err := tx.Exec(ctx, `
				UPDATE products p SET is_active = false, updated_at = now()
				WHERE p.source = $1 AND p.is_active
				  AND NOT EXISTS (SELECT 1 FROM import_products i WHERE i.external_id = p.external_id)`, b.Source)
			if err != nil {
				return fmt.Errorf("postgres: deactivate missing products: %w", err)
			}
			res.Deactivated = int(tag.RowsAffected())
			if !b.Force && importer.Exceeded(res.Deactivated, active, b.MaxDeactivatePercent) {
				return &importer.DeactivationError{Would: res.Deactivated, Active: active, LimitPercent: b.MaxDeactivatePercent}
			}
		}

		if dryRun {
			return errDryRun
		}
		report, err := json.Marshal(b.Report)
		if err != nil {
			return fmt.Errorf("postgres: encode import report: %w", err)
		}
		return tx.QueryRow(ctx, `
			INSERT INTO import_runs (source, file_name, started_at, inserted, updated, deactivated, fitments, report)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			RETURNING id`,
			b.Source, b.Report.File, b.StartedAt, res.Inserted, res.Updated, res.Deactivated, res.Fitments, report,
		).Scan(&res.RunID)
	})
	var limit *importer.DeactivationError
	switch {
	case errors.As(err, &limit):
		// Rolled back; the counts still tell the operator what the file
		// would have done.
		res.RunID = 0
		return res, err
	case err != nil && !errors.Is(err, errDryRun):
		return importer.Result{}, err
	}
	return res, nil
}

func createCategories(ctx context.Context, tx pgx.Tx, cats map[string]string) error {
	for slug, title := range cats {
		if _, err := tx.Exec(ctx, `
			INSERT INTO categories (slug, title, position)
			VALUES ($1, $2, (SELECT coalesce(max(position), 0) + 1 FROM categories))
			ON CONFLICT (slug) DO NOTHING`, slug, title); err != nil {
			return fmt.Errorf("postgres: create category %s: %w", slug, err)
		}
	}
	return nil
}

func createBrands(ctx context.Context, tx pgx.Tx, brands []importer.NewBrand) error {
	if len(brands) == 0 {
		return nil
	}
	names := make([]string, len(brands))
	aliases := make([]string, len(brands))
	for i, b := range brands {
		names[i], aliases[i] = b.Name, b.Alias
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO brands (name) SELECT unnest($1::text[]) ON CONFLICT (name) DO NOTHING`, names); err != nil {
		return fmt.Errorf("postgres: create brands: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO brand_aliases (alias, brand_id)
		SELECT x.alias, b.id FROM unnest($1::text[], $2::text[]) AS x(name, alias)
		JOIN brands b ON b.name = x.name
		ON CONFLICT (alias) DO NOTHING`, names, aliases); err != nil {
		return fmt.Errorf("postgres: create brand aliases: %w", err)
	}
	return nil
}

// stage copies the batch into temporary tables that vanish with the
// transaction.
func stage(ctx context.Context, tx pgx.Tx, products []importer.Product) error {
	if _, err := tx.Exec(ctx, `
		CREATE TEMP TABLE import_products (
			external_id text PRIMARY KEY,
			sku text, name text, brand text, description text, oem text[], price bigint,
			stock int, lead_days int, image text, category text, part_type text, universal boolean
		) ON COMMIT DROP;
		CREATE TEMP TABLE import_fits (
			external_id text, model_id bigint, year_from int, year_to int, position text, note text
		) ON COMMIT DROP`); err != nil {
		return fmt.Errorf("postgres: create staging tables: %w", err)
	}

	_, err := tx.CopyFrom(ctx, pgx.Identifier{"import_products"},
		[]string{"external_id", "sku", "name", "brand", "description", "oem", "price",
			"stock", "lead_days", "image", "category", "part_type", "universal"},
		pgx.CopyFromSlice(len(products), func(i int) ([]any, error) {
			p := products[i]
			oem := p.OEM
			if oem == nil {
				oem = []string{}
			}
			return []any{p.ExternalID, p.SKU, p.Name, p.Brand, p.Description, oem, int64(p.Price),
				p.Stock, p.LeadDays, p.Image, p.Category, p.PartType, p.Universal}, nil
		}))
	if err != nil {
		return fmt.Errorf("postgres: copy products: %w", err)
	}

	var fits [][]any
	for _, p := range products {
		for _, f := range p.Fits {
			fits = append(fits, []any{p.ExternalID, f.ModelID, f.YearFrom, f.YearTo, f.Position, f.Note})
		}
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"import_fits"},
		[]string{"external_id", "model_id", "year_from", "year_to", "position", "note"},
		pgx.CopyFromRows(fits)); err != nil {
		return fmt.Errorf("postgres: copy fitments: %w", err)
	}
	// Fresh tables have no statistics; without them the merges below are
	// planned for an empty table.
	if _, err := tx.Exec(ctx, `ANALYZE import_products; ANALYZE import_fits`); err != nil {
		return fmt.Errorf("postgres: analyze staging tables: %w", err)
	}
	return nil
}

var _ importer.VehicleStore = (*ImportRepository)(nil)

// ApplyVehicles upserts a vehicle directory in one transaction. A vehicle is
// matched on make, model, engine and first year, so the seed's cars and the
// same cars in a file meet instead of doubling. Service data the file leaves
// empty does not erase what is known.
func (r *ImportRepository) ApplyVehicles(ctx context.Context, vs []importer.Vehicle, dryRun bool) (importer.VehicleResult, error) {
	var res importer.VehicleResult
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		for _, v := range vs {
			if _, err := tx.Exec(ctx, `
				INSERT INTO vehicle_models (make, model) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
				v.Make, v.Model); err != nil {
				return fmt.Errorf("postgres: model of %s: %w", v.Code(), err)
			}
			var inserted bool
			err := tx.QueryRow(ctx, `
				INSERT INTO vehicles (code, make, model, year_from, year_to, engine, fuel, oil_viscosity, oil_volume_l)
				VALUES ($1, $2, $3, $4, $5, $6, $7, nullif($8, ''), nullif($9, 0::numeric))
				ON CONFLICT (make, model, engine, year_from) DO UPDATE SET
					year_to = EXCLUDED.year_to, fuel = EXCLUDED.fuel,
					oil_viscosity = coalesce(EXCLUDED.oil_viscosity, vehicles.oil_viscosity),
					oil_volume_l = coalesce(EXCLUDED.oil_volume_l, vehicles.oil_volume_l)
				RETURNING xmax = 0`,
				v.Code(), v.Make, v.Model, v.YearFrom, nullable(v.YearTo), v.Engine, v.Fuel,
				v.OilViscosity, v.OilVolume,
			).Scan(&inserted)
			if err != nil {
				return fmt.Errorf("postgres: upsert vehicle %s: %w", v.Code(), err)
			}
			if inserted {
				res.Inserted++
			} else {
				res.Updated++
			}
		}
		if dryRun {
			return errDryRun
		}
		return nil
	})
	if err != nil && !errors.Is(err, errDryRun) {
		return importer.VehicleResult{}, err
	}
	return res, nil
}

func nullable(n int) *int {
	if n == 0 {
		return nil
	}
	return &n
}
