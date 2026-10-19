package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aristodem96/ai_shop_bot/data"
	"github.com/aristodem96/ai_shop_bot/internal/categories"
	"github.com/aristodem96/ai_shop_bot/internal/importer"
)

// SeedCategories fills an empty database with the starting tree from
// data/categories.csv, which ships inside the binary. Once there is a tree
// it is the shop's, edited in the managers' chat, and the file never
// touches it again.
func SeedCategories(ctx context.Context, pool *pgxpool.Pool) error {
	cats, err := importer.ParseCategories(data.Categories)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		// Two processes starting at once seed once.
		if _, err := tx.Exec(ctx, `LOCK TABLE categories IN EXCLUSIVE MODE`); err != nil {
			return fmt.Errorf("postgres: lock categories: %w", err)
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM categories`).Scan(&n); err != nil {
			return fmt.Errorf("postgres: count categories: %w", err)
		}
		if n > 0 {
			return nil
		}
		// The file lists a section before its subcategories, so the parent
		// row always exists by the time a child names it.
		for i, c := range cats {
			if _, err := tx.Exec(ctx, `
				WITH c AS (
					INSERT INTO categories (slug, title, position, parent_id, part_type, universal)
					VALUES ($1, $2, $3, (SELECT id FROM categories WHERE slug = nullif($4, '')), $5, $6)
					RETURNING id)
				INSERT INTO category_keywords (category_id, word)
				SELECT c.id, w FROM c, unnest($7::text[]) AS w
				ON CONFLICT (word) DO NOTHING`,
				c.Slug, c.Title, i, c.Parent, c.PartType, c.Universal, nonNil(c.Keywords)); err != nil {
				return fmt.Errorf("postgres: seed category %s: %w", c.Slug, err)
			}
		}
		return nil
	})
}

var _ categories.Store = (*CategoryRepository)(nil)

// CategoryRepository is the category tree the owner edits.
type CategoryRepository struct {
	pool *pgxpool.Pool
}

// NewCategoryRepository returns a repository backed by pool.
func NewCategoryRepository(pool *pgxpool.Pool) *CategoryRepository {
	return &CategoryRepository{pool: pool}
}

// treeOrder sorts sections by position, each followed by its
// subcategories.
const treeOrder = `ORDER BY coalesce(s.position, c.position), coalesce(s.id, c.id), c.parent_id NULLS FIRST, c.position, c.id`

// Tree lists the categories in menu order, with their words and how many
// products on sale each holds.
func (r *CategoryRepository) Tree(ctx context.Context) ([]categories.Category, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT c.id, c.slug, c.title, coalesce(c.parent_id, 0), c.part_type, c.universal,
			coalesce((SELECT array_agg(k.id ORDER BY k.id) FROM category_keywords k WHERE k.category_id = c.id), '{}'),
			coalesce((SELECT array_agg(k.word ORDER BY k.id) FROM category_keywords k WHERE k.category_id = c.id), '{}'),
			(SELECT count(*) FROM products p WHERE p.category_id = c.id AND `+listed+`)
		FROM categories c LEFT JOIN categories s ON s.id = c.parent_id
		`+treeOrder)
	if err != nil {
		return nil, fmt.Errorf("postgres: category tree: %w", err)
	}
	defer rows.Close()
	var out []categories.Category
	for rows.Next() {
		var (
			c     categories.Category
			ids   []int64
			words []string
		)
		if err := rows.Scan(&c.ID, &c.Slug, &c.Title, &c.ParentID, &c.PartType, &c.Universal, &ids, &words, &c.Products); err != nil {
			return nil, fmt.Errorf("postgres: scan category: %w", err)
		}
		for i := range ids {
			c.Words = append(c.Words, categories.Word{ID: ids[i], Text: words[i]})
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ProductsIn lists every product filed in a category, on sale or not: a
// category is deleted only once none is left in it.
func (r *CategoryRepository) ProductsIn(ctx context.Context, categoryID int64) ([]categories.Product, error) {
	return r.products(ctx, `WHERE p.category_id = $1`, categoryID)
}

// ProductsWith lists every product whose name or supplier's group contains
// word, case aside. strpos rather than LIKE: the word is the owner's text,
// and a "%" in it must not become a wildcard.
func (r *CategoryRepository) ProductsWith(ctx context.Context, word string) ([]categories.Product, error) {
	return r.products(ctx, `WHERE strpos(lower(p.name || ' ' || p.source_group), $1) > 0`, word)
}

func (r *CategoryRepository) products(ctx context.Context, where string, arg any) ([]categories.Product, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT p.id, p.name, p.source_group, p.category_id, coalesce(g.category_id, n.category_id, 0)
		FROM products p
		LEFT JOIN suppliers s ON s.source = p.source
		LEFT JOIN supplier_groups g ON g.supplier_id = s.id AND g.kind = 'group' AND g.key = p.source_group
		LEFT JOIN supplier_groups n ON n.supplier_id = s.id AND n.kind = 'name' AND n.key = p.name
		`+where+` ORDER BY p.name, p.id`, arg)
	if err != nil {
		return nil, fmt.Errorf("postgres: products of the tree: %w", err)
	}
	ps, err := pgx.CollectRows(rows, pgx.RowToStructByPos[categories.Product])
	if err != nil {
		return nil, fmt.Errorf("postgres: scan products: %w", err)
	}
	return ps, nil
}

// Apply writes a change in one transaction. A product that moves takes its
// new category's part type and universality, as an import would give them.
func (r *CategoryRepository) Apply(ctx context.Context, ch *categories.Change) error {
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		var created int64
		if c := ch.Create; c != nil {
			if err := tx.QueryRow(ctx, `
				INSERT INTO categories (slug, title, parent_id, position)
				VALUES ($1, $2, nullif($3::bigint, 0),
					coalesce((SELECT max(position) + 1 FROM categories WHERE parent_id IS NOT DISTINCT FROM nullif($3::bigint, 0)), 0))
				RETURNING id`, c.Slug, c.Title, c.ParentID).Scan(&created); err != nil {
				return fmt.Errorf("postgres: create category %s: %w", c.Slug, err)
			}
			if ch.MoveWordsFrom != 0 {
				if _, err := tx.Exec(ctx, `UPDATE category_keywords SET category_id = $2 WHERE category_id = $1`,
					ch.MoveWordsFrom, created); err != nil {
					return fmt.Errorf("postgres: hand words to %s: %w", c.Slug, err)
				}
			}
		}
		if ch.RenameID != 0 {
			if _, err := tx.Exec(ctx, `UPDATE categories SET title = $2 WHERE id = $1`, ch.RenameID, ch.Title); err != nil {
				return fmt.Errorf("postgres: rename category %d: %w", ch.RenameID, err)
			}
		}
		if ch.AddWordTo != 0 {
			if _, err := tx.Exec(ctx, `INSERT INTO category_keywords (category_id, word) VALUES ($1, $2)`,
				ch.AddWordTo, ch.Word); err != nil {
				if isUniqueViolation(err) {
					return categories.ErrWordTaken
				}
				return fmt.Errorf("postgres: add word %q: %w", ch.Word, err)
			}
		}
		if ch.RemoveWordID != 0 {
			tag, err := tx.Exec(ctx, `DELETE FROM category_keywords WHERE id = $1`, ch.RemoveWordID)
			if err != nil {
				return fmt.Errorf("postgres: remove word %d: %w", ch.RemoveWordID, err)
			}
			if tag.RowsAffected() == 0 {
				return categories.ErrNoWord
			}
		}
		if len(ch.Moves) > 0 {
			ids, cats := make([]int64, len(ch.Moves)), make([]int64, len(ch.Moves))
			for i, m := range ch.Moves {
				ids[i], cats[i] = m.ProductID, m.CategoryID
				if cats[i] == 0 {
					cats[i] = created
				}
			}
			if _, err := tx.Exec(ctx, `
				UPDATE products p SET category_id = c.id, part_type = c.part_type, universal = c.universal,
					updated_at = now()
				FROM unnest($1::bigint[], $2::bigint[]) AS x(id, category_id)
				JOIN categories c ON c.id = x.category_id
				WHERE p.id = x.id`, ids, cats); err != nil {
				return fmt.Errorf("postgres: move products: %w", err)
			}
		}
		if ch.DeleteID != 0 {
			if _, err := tx.Exec(ctx, `DELETE FROM categories WHERE id = $1`, ch.DeleteID); err != nil {
				if isForeignKeyViolation(err) {
					// A product filed there since the plan was made.
					return categories.ErrNoCategory
				}
				return fmt.Errorf("postgres: delete category %d: %w", ch.DeleteID, err)
			}
		}
		return nil
	})
}
