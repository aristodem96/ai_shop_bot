package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aristodem96/ai_shop_bot/data"
	"github.com/aristodem96/ai_shop_bot/internal/importer"
)

// SyncCategories makes the category tree in the database match
// data/categories.csv, which ships inside the binary. Every command that
// opens the database runs it after the migrations, so the file is the one
// place the tree is edited.
//
// Categories are upserted by slug and never deleted: products and old menu
// buttons point at them. One dropped from the file keeps its rows; the menu
// hides it once it is empty.
func SyncCategories(ctx context.Context, pool *pgxpool.Pool) error {
	cats, err := importer.ParseCategories(data.Categories)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		// The file lists a section before its subcategories, so the parent
		// row always exists by the time a child names it.
		for i, c := range cats {
			// A nil slice is NULL to pgx; a section has no keywords, which
			// is an empty array.
			keywords := c.Keywords
			if keywords == nil {
				keywords = []string{}
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO categories (slug, title, position, parent_id, part_type, universal, keywords)
				VALUES ($1, $2, $3, (SELECT id FROM categories WHERE slug = nullif($4, '')), $5, $6, $7)
				ON CONFLICT (slug) DO UPDATE SET
					title = EXCLUDED.title, position = EXCLUDED.position, parent_id = EXCLUDED.parent_id,
					part_type = EXCLUDED.part_type, universal = EXCLUDED.universal, keywords = EXCLUDED.keywords`,
				c.Slug, c.Title, i, c.Parent, c.PartType, c.Universal, keywords); err != nil {
				return fmt.Errorf("postgres: sync category %s: %w", c.Slug, err)
			}
		}
		return nil
	})
}
