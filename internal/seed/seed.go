// Package seed loads the demo catalogue. It is a package rather than a SQL
// file so that the integration tests run against exactly the data a developer
// sees in the bot, and so stock levels can be generated instead of typed.
package seed

import (
	"context"
	"fmt"
	"hash/fnv"
	"net/url"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Stats reports what a run wrote.
type Stats struct {
	Categories int
	Products   int
	Variants   int
}

// Run upserts the catalogue in one transaction. It is idempotent: rows are
// matched on their natural keys (slug, chart code, SKU, product+size), so
// running it again resets the demo to its initial state -- stock included --
// without duplicating anything or breaking carts that point at existing sizes.
func Run(ctx context.Context, pool *pgxpool.Pool) (Stats, error) {
	var st Stats
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		chartIDs, chartSizes, err := upsertCharts(ctx, tx)
		if err != nil {
			return err
		}

		catIDs := make(map[string]int64, len(categories))
		catChart := make(map[string]int64, len(categories))
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
			catIDs[c.slug] = id
			catChart[c.slug] = chartIDs[c.chart]
			st.Categories++
		}

		for _, p := range products {
			chartID := catChart[p.category]
			n, err := upsertProduct(ctx, tx, p, catIDs[p.category], chartID, chartSizes[chartID])
			if err != nil {
				return err
			}
			st.Products++
			st.Variants += n
		}
		return nil
	})
	return st, err
}

func upsertCharts(ctx context.Context, tx pgx.Tx) (map[string]int64, map[int64][]string, error) {
	ids := make(map[string]int64, len(charts))
	sizes := make(map[int64][]string, len(charts))

	for _, c := range charts {
		var id int64
		err := tx.QueryRow(ctx, `
			INSERT INTO size_charts (code, title) VALUES ($1, $2)
			ON CONFLICT (code) DO UPDATE SET title = EXCLUDED.title
			RETURNING id`, c.code, c.title,
		).Scan(&id)
		if err != nil {
			return nil, nil, fmt.Errorf("seed: size chart %s: %w", c.code, err)
		}
		ids[c.code] = id

		for pos, r := range c.rows {
			_, err := tx.Exec(ctx, `
				INSERT INTO size_chart_rows (chart_id, size, position,
					chest_min_cm, chest_max_cm, waist_min_cm, waist_max_cm,
					height_min_cm, height_max_cm, foot_min_mm, foot_max_mm)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
				ON CONFLICT (chart_id, size) DO UPDATE SET
					position = EXCLUDED.position,
					chest_min_cm = EXCLUDED.chest_min_cm, chest_max_cm = EXCLUDED.chest_max_cm,
					waist_min_cm = EXCLUDED.waist_min_cm, waist_max_cm = EXCLUDED.waist_max_cm,
					height_min_cm = EXCLUDED.height_min_cm, height_max_cm = EXCLUDED.height_max_cm,
					foot_min_mm = EXCLUDED.foot_min_mm, foot_max_mm = EXCLUDED.foot_max_mm`,
				id, r.size, pos,
				nullable(r.chestMin), nullable(r.chestMax), nullable(r.waistMin), nullable(r.waistMax),
				nullable(r.heightMin), nullable(r.heightMax), nullable(r.footMin), nullable(r.footMax))
			if err != nil {
				return nil, nil, fmt.Errorf("seed: size chart %s row %s: %w", c.code, r.size, err)
			}
			sizes[id] = append(sizes[id], r.size)
		}
	}
	return ids, sizes, nil
}

func upsertProduct(ctx context.Context, tx pgx.Tx, p product, categoryID, chartID int64, chartSizes []string) (int, error) {
	var id int64
	err := tx.QueryRow(ctx, `
		INSERT INTO products (sku, category_id, size_chart_id, name, brand, description,
			color, season, gender, styles, price, image_url)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (sku) DO UPDATE SET
			category_id = EXCLUDED.category_id, size_chart_id = EXCLUDED.size_chart_id,
			name = EXCLUDED.name, brand = EXCLUDED.brand, description = EXCLUDED.description,
			color = EXCLUDED.color, season = EXCLUDED.season, gender = EXCLUDED.gender,
			styles = EXCLUDED.styles, price = EXCLUDED.price, image_url = EXCLUDED.image_url,
			is_active = true, updated_at = now()
		RETURNING id`,
		p.sku, categoryID, chartID, p.name, p.brand, p.description,
		p.color, p.season, p.gender, p.styles, p.price*100, imageURL(p),
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("seed: product %s: %w", p.sku, err)
	}

	var sizes []string
	for pos, size := range chartSizes {
		if slices.Contains(p.excludeSizes, size) {
			continue
		}
		sizes = append(sizes, size)
		_, err := tx.Exec(ctx, `
			INSERT INTO product_variants (product_id, size, position, stock)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (product_id, size) DO UPDATE SET
				position = EXCLUDED.position, stock = EXCLUDED.stock`,
			id, size, pos, stockFor(p, size))
		if err != nil {
			return 0, fmt.Errorf("seed: product %s size %s: %w", p.sku, size, err)
		}
	}

	// A size dropped from the data since the last run goes from the database
	// too, so a re-seed really converges on what the data says.
	if _, err := tx.Exec(ctx,
		`DELETE FROM product_variants WHERE product_id = $1 AND NOT (size = ANY($2))`, id, sizes,
	); err != nil {
		return 0, fmt.Errorf("seed: prune sizes of %s: %w", p.sku, err)
	}
	return len(sizes), nil
}

// stockFor derives a stable stock level from the SKU and size. A hash rather
// than math/rand: every run and every machine gets the same catalogue, so a
// screenshot in the README matches what a fresh clone shows. Roughly one size
// in ten is sold out and one in ten is down to its last unit.
func stockFor(p product, size string) int {
	if n, ok := p.stock[size]; ok {
		return n
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(p.sku + "/" + size))
	v := h.Sum32()
	switch v % 10 {
	case 0:
		return 0
	case 1:
		return 1
	default:
		return int(v/10%8) + 2
	}
}

// imageURL is a placeholder photo in the product's colour. A real shop would
// upload photos once and keep Telegram's file_id; for a demo a URL Telegram
// fetches by itself is enough.
func imageURL(p product) string {
	bg := colorHex[p.color]
	fg := "ffffff"
	if p.color == "white" || p.color == "beige" {
		fg = "333333"
	}
	return fmt.Sprintf("https://placehold.co/600x800/%s/%s/png?text=%s", bg, fg, url.QueryEscape(p.imageLabel))
}

func nullable(n int) *int {
	if n == 0 {
		return nil
	}
	return &n
}
