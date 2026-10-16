package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/aristodem96/ai_shop_bot/internal/importer"
)

var _ importer.KeywordStore = (*ImportRepository)(nil)

// customPosition puts the owner's categories after the file's in their
// section. The file's are numbered from zero on every sync and stay far
// below it.
const customPosition = 10000

// ProductsContaining lists the active products whose name contains word,
// case aside. strpos rather than LIKE: the word is the owner's text, and a
// "%" in it must not become a wildcard.
func (r *ImportRepository) ProductsContaining(ctx context.Context, word string) ([]importer.ProductRef, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT p.id, p.name, c.slug FROM products p
		JOIN categories c ON c.id = p.category_id
		WHERE p.is_active AND strpos(lower(p.name), $1) > 0
		ORDER BY p.name, p.id`, word)
	if err != nil {
		return nil, fmt.Errorf("postgres: products containing %q: %w", word, err)
	}
	refs, err := pgx.CollectRows(rows, pgx.RowToStructByPos[importer.ProductRef])
	if err != nil {
		return nil, fmt.Errorf("postgres: scan products: %w", err)
	}
	return refs, nil
}

// AddKeyword files word under a category, creating the category first when
// asked, and moves the products the change files there -- all or nothing.
// A moved product takes the category's part type and universality, as an
// import would give them.
func (r *ImportRepository) AddKeyword(ctx context.Context, word, slug string, create *importer.NewCategory, move []int64) error {
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if create != nil {
			if _, err := tx.Exec(ctx, `
				INSERT INTO categories (slug, title, parent_id, position, custom)
				VALUES ($1, $2, (SELECT id FROM categories WHERE slug = nullif($3, '')),
					$4 + (SELECT count(*) FROM categories WHERE custom), true)`,
				create.Slug, create.Title, create.Parent, customPosition); err != nil {
				return fmt.Errorf("postgres: create category %s: %w", create.Slug, err)
			}
		}
		var categoryID int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO category_keywords (category_id, word)
			SELECT id, $2 FROM categories WHERE slug = $1
			RETURNING category_id`, slug, word).Scan(&categoryID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return importer.ErrBadCategory
			}
			if isUniqueViolation(err) {
				return importer.ErrKeywordTaken
			}
			return fmt.Errorf("postgres: add keyword %q: %w", word, err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE products p SET category_id = c.id, part_type = c.part_type, universal = c.universal,
				updated_at = now()
			FROM categories c
			WHERE c.id = $1 AND p.id = ANY($2)`, categoryID, move); err != nil {
			return fmt.Errorf("postgres: move products: %w", err)
		}
		return nil
	})
}

// OwnerKeywords lists the owner's words, newest first, with where each leads.
func (r *ImportRepository) OwnerKeywords(ctx context.Context) ([]importer.OwnerKeyword, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+ownerKeywordColumns+` ORDER BY k.id DESC`)
	if err != nil {
		return nil, fmt.Errorf("postgres: list keywords: %w", err)
	}
	kws, err := pgx.CollectRows(rows, pgx.RowToStructByPos[importer.OwnerKeyword])
	if err != nil {
		return nil, fmt.Errorf("postgres: scan keywords: %w", err)
	}
	return kws, nil
}

// RemoveKeyword deletes one of the owner's words and moves its products
// back, all or nothing. A moved product takes its new category's part type
// and universality.
func (r *ImportRepository) RemoveKeyword(ctx context.Context, id int64, moves []importer.Move) (*importer.OwnerKeyword, error) {
	var kw importer.OwnerKeyword
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			WITH k AS (DELETE FROM category_keywords WHERE id = $1 RETURNING id, word, category_id)
			SELECT k.id, k.word, coalesce(s.title || ' › ', '') || c.title, c.slug
			FROM k JOIN categories c ON c.id = k.category_id
			LEFT JOIN categories s ON s.id = c.parent_id`, id).Scan(&kw.ID, &kw.Word, &kw.Category, &kw.Slug)
		if errors.Is(err, pgx.ErrNoRows) {
			return importer.ErrBadKeyword
		}
		if err != nil {
			return fmt.Errorf("postgres: remove keyword %d: %w", id, err)
		}
		ids := make([]int64, len(moves))
		slugs := make([]string, len(moves))
		for i, m := range moves {
			ids[i], slugs[i] = m.ProductID, m.Category
		}
		if _, err := tx.Exec(ctx, `
			UPDATE products p SET category_id = c.id, part_type = c.part_type, universal = c.universal,
				updated_at = now()
			FROM unnest($1::bigint[], $2::text[]) AS x(id, slug)
			JOIN categories c ON c.slug = x.slug
			WHERE p.id = x.id`, ids, slugs); err != nil {
			return fmt.Errorf("postgres: move products back: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &kw, nil
}

const ownerKeywordColumns = `k.id, k.word, coalesce(s.title || ' › ', '') || c.title, c.slug
	FROM category_keywords k
	JOIN categories c ON c.id = k.category_id
	LEFT JOIN categories s ON s.id = c.parent_id`
