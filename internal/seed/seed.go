// Package seed loads the demo catalogue. It is a package rather than a SQL
// file so that the integration tests run against exactly the data a developer
// sees in the bot, and so stock levels can be generated instead of typed.
package seed

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/url"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
)

// Stats reports what a run wrote.
type Stats struct {
	Categories int
	Vehicles   int
	Products   int
	Fitments   int
}

// Run upserts the catalogue in one transaction. It is idempotent: rows are
// matched on their natural keys (category slug, vehicle code, SKU), so running
// it again resets the demo to its initial state -- stock included -- without
// duplicating anything or breaking carts and garages that point at it.
func Run(ctx context.Context, pool *pgxpool.Pool) (Stats, error) {
	var st Stats
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		catIDs := make(map[string]int64, len(categories))
		catColor := make(map[string]string, len(categories))
		for i, c := range categories {
			var id int64
			err := tx.QueryRow(ctx, `
				INSERT INTO categories (slug, title, position) VALUES ($1, $2, $3)
				ON CONFLICT (slug) DO UPDATE SET title = EXCLUDED.title, position = EXCLUDED.position
				RETURNING id`, c.slug, c.title, i,
			).Scan(&id)
			if err != nil {
				return fmt.Errorf("seed: category %s: %w", c.slug, err)
			}
			catIDs[c.slug], catColor[c.slug] = id, c.imageColor
			st.Categories++
		}

		vehicleIDs := make(map[string]int64, len(vehicles))
		for _, v := range vehicles {
			if _, err := tx.Exec(ctx, `
				INSERT INTO vehicle_models (make, model) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
				v.make, v.model); err != nil {
				return fmt.Errorf("seed: model of %s: %w", v.code, err)
			}
			var id int64
			err := tx.QueryRow(ctx, `
				INSERT INTO vehicles (code, make, model, year_from, year_to, engine, fuel, oil_viscosity, oil_volume_l)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
				ON CONFLICT (code) DO UPDATE SET
					make = EXCLUDED.make, model = EXCLUDED.model,
					year_from = EXCLUDED.year_from, year_to = EXCLUDED.year_to,
					engine = EXCLUDED.engine, fuel = EXCLUDED.fuel,
					oil_viscosity = EXCLUDED.oil_viscosity, oil_volume_l = EXCLUDED.oil_volume_l
				RETURNING id`,
				v.code, v.make, v.model, v.yearFrom, nullable(v.yearTo), v.engine, v.fuel, v.oilViscosity, v.oilVolume,
			).Scan(&id)
			if err != nil {
				return fmt.Errorf("seed: vehicle %s: %w", v.code, err)
			}
			vehicleIDs[v.code] = id
			st.Vehicles++
		}

		for _, p := range products {
			n, err := upsertProduct(ctx, tx, p, catIDs[p.category], catColor[p.category], vehicleIDs)
			if err != nil {
				return err
			}
			st.Products++
			st.Fitments += n
		}
		return nil
	})
	return st, err
}

func upsertProduct(ctx context.Context, tx pgx.Tx, p product, categoryID int64, color string, vehicleIDs map[string]int64) (int, error) {
	oem := make([]string, len(p.oem))
	for i, n := range p.oem {
		oem[i] = domain.NormalizeOEM(n)
	}
	specs := make([]domain.Spec, len(p.specs))
	for i, s := range p.specs {
		specs[i] = domain.Spec{Name: s[0], Value: s[1]}
	}
	specsJSON, err := json.Marshal(specs)
	if err != nil {
		return 0, fmt.Errorf("seed: specs of %s: %w", p.sku, err)
	}

	// The brand goes into the directory too, so an import that spells it
	// "Auto Line" finds the shop's "AutoLine" instead of creating a twin.
	if _, err := tx.Exec(ctx, `
		WITH b AS (
			INSERT INTO brands (name) VALUES ($1)
			ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name
			RETURNING id)
		INSERT INTO brand_aliases (alias, brand_id) SELECT $2, id FROM b
		ON CONFLICT (alias) DO NOTHING`, p.brand, domain.NormalizeOEM(p.brand)); err != nil {
		return 0, fmt.Errorf("seed: brand of %s: %w", p.sku, err)
	}

	var id int64
	err = tx.QueryRow(ctx, `
		INSERT INTO products (sku, category_id, name, brand, description, oem_numbers, specs,
			universal, price, stock, image_url, part_type)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (sku) DO UPDATE SET
			category_id = EXCLUDED.category_id, name = EXCLUDED.name, brand = EXCLUDED.brand,
			description = EXCLUDED.description, oem_numbers = EXCLUDED.oem_numbers,
			specs = EXCLUDED.specs, universal = EXCLUDED.universal, price = EXCLUDED.price,
			stock = EXCLUDED.stock, image_url = EXCLUDED.image_url, part_type = EXCLUDED.part_type,
			is_active = true, updated_at = now()
		RETURNING id`,
		p.sku, categoryID, p.name, p.brand, p.description, oem, specsJSON,
		p.universal, p.price*100, stockFor(p), imageURL(p, color), p.partType,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("seed: product %s: %w", p.sku, err)
	}

	// Fitments are replaced wholesale, so a re-seed converges on what the
	// data says, removals included.
	if _, err := tx.Exec(ctx, `DELETE FROM product_fitments WHERE product_id = $1`, id); err != nil {
		return 0, fmt.Errorf("seed: clear fitments of %s: %w", p.sku, err)
	}
	ids := make([]int64, 0, len(p.fits))
	for _, code := range p.fits {
		vid, ok := vehicleIDs[code]
		if !ok {
			return 0, fmt.Errorf("seed: product %s fits unknown vehicle %q", p.sku, code)
		}
		ids = append(ids, vid)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO product_fitments (product_id, vehicle_id)
		SELECT $1, unnest($2::bigint[])`, id, ids,
	); err != nil {
		return 0, fmt.Errorf("seed: fitments of %s: %w", p.sku, err)
	}
	return len(ids), nil
}

// stockFor derives a stable stock level from the SKU. A hash rather than
// math/rand: every run and every machine gets the same catalogue, so a
// screenshot in the README matches what a fresh clone shows.
func stockFor(p product) int {
	switch {
	case p.stock == stockSoldOut:
		return 0
	case p.stock > 0:
		return p.stock
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(p.sku))
	v := h.Sum32()
	// Mostly healthy stock, with roughly one product in eight down to its
	// last two units.
	if v%8 == 0 {
		return int(v/8%2) + 1
	}
	return int(v/8%14) + 3
}

// imageURL is a placeholder photo tinted by category. A real shop would
// upload photos once and keep Telegram's file_id; for a demo a URL Telegram
// fetches by itself is enough.
func imageURL(p product, color string) string {
	return fmt.Sprintf("https://placehold.co/800x600/%s/ffffff/png?text=%s", color, url.QueryEscape(p.imageLabel))
}

func nullable(n int) *int {
	if n == 0 {
		return nil
	}
	return &n
}
