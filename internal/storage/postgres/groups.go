package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aristodem96/ai_shop_bot/internal/grouping"
	"github.com/aristodem96/ai_shop_bot/internal/importer"
)

// GroupCategories maps the supplier's decided groups to their categories'
// slugs; a mixed group maps to "", its products sorted by name.
func (r *ImportRepository) GroupCategories(ctx context.Context, supplierID int64) (map[importer.GroupKey]string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT g.kind, g.key, coalesce(c.slug, '') FROM supplier_groups g LEFT JOIN categories c ON c.id = g.category_id
		WHERE g.supplier_id = $1 AND (g.category_id IS NOT NULL OR g.mixed)`, supplierID)
	if err != nil {
		return nil, fmt.Errorf("postgres: groups of supplier %d: %w", supplierID, err)
	}
	defer rows.Close()
	out := map[importer.GroupKey]string{}
	for rows.Next() {
		var (
			k    importer.GroupKey
			slug string
		)
		if err := rows.Scan(&k.Kind, &k.Key, &slug); err != nil {
			return nil, fmt.Errorf("postgres: scan group: %w", err)
		}
		out[k] = slug
	}
	return out, rows.Err()
}

// SavePending records the supplier's groups waiting for a category, with
// the counts and examples of the latest file; what was decided or proposed
// for them stays.
func (r *ImportRepository) SavePending(ctx context.Context, supplierID int64, groups []importer.PendingGroup) error {
	kinds, keys, counts := make([]string, len(groups)), make([]string, len(groups)), make([]int32, len(groups))
	examples := make([]string, len(groups))
	for i, g := range groups {
		kinds[i], keys[i], counts[i] = g.Kind, g.Key, int32(min(g.Products, 1<<30)) //nolint:gosec // bounded
		raw, err := json.Marshal(nonNil(g.Examples))
		if err != nil {
			return fmt.Errorf("postgres: encode examples: %w", err)
		}
		examples[i] = string(raw)
	}
	if _, err := r.pool.Exec(ctx, `
		INSERT INTO supplier_groups (supplier_id, kind, key, products, examples)
		SELECT $1, x.kind, x.key, x.products, array(SELECT jsonb_array_elements_text(x.examples::jsonb))
		FROM unnest($2::text[], $3::text[], $4::int[], $5::text[]) AS x(kind, key, products, examples)
		ON CONFLICT (supplier_id, kind, key) DO UPDATE SET
			products = excluded.products, examples = excluded.examples, updated_at = now()`,
		supplierID, kinds, keys, counts, examples); err != nil {
		return fmt.Errorf("postgres: save groups of supplier %d: %w", supplierID, err)
	}
	return nil
}

var _ grouping.Store = (*GroupRepository)(nil)

// GroupRepository keeps the suppliers' groups for grouping.
type GroupRepository struct {
	pool *pgxpool.Pool
}

// NewGroupRepository returns a repository backed by pool.
func NewGroupRepository(pool *pgxpool.Pool) *GroupRepository { return &GroupRepository{pool: pool} }

const groupColumns = `g.id, g.supplier_id, g.kind, g.key, g.products, g.examples, coalesce(g.category_id, 0),
	g.decided_by, g.mixed, g.suggestion`

func scanGroups(rows pgx.Rows) ([]grouping.Group, error) {
	defer rows.Close()
	var out []grouping.Group
	for rows.Next() {
		var (
			g   grouping.Group
			sug []byte
		)
		if err := rows.Scan(&g.ID, &g.SupplierID, &g.Kind, &g.Key, &g.Products, &g.Examples, &g.CategoryID,
			&g.DecidedBy, &g.Mixed, &sug); err != nil {
			return nil, fmt.Errorf("postgres: scan group: %w", err)
		}
		if sug != nil {
			g.Suggestion = &grouping.Suggestion{}
			if err := json.Unmarshal(sug, g.Suggestion); err != nil {
				return nil, fmt.Errorf("postgres: decode suggestion: %w", err)
			}
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// Open lists the supplier's undecided groups, most products first.
func (r *GroupRepository) Open(ctx context.Context, supplierID int64) ([]grouping.Group, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+groupColumns+` FROM supplier_groups g
		WHERE g.supplier_id = $1 AND g.category_id IS NULL AND NOT g.mixed ORDER BY g.products DESC, g.id`, supplierID)
	if err != nil {
		return nil, fmt.Errorf("postgres: open groups of supplier %d: %w", supplierID, err)
	}
	return scanGroups(rows)
}

// Get returns a group.
func (r *GroupRepository) Get(ctx context.Context, id int64) (*grouping.Group, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+groupColumns+` FROM supplier_groups g WHERE g.id = $1`, id)
	if err != nil {
		return nil, fmt.Errorf("postgres: group %d: %w", id, err)
	}
	gs, err := scanGroups(rows)
	if err != nil {
		return nil, err
	}
	if len(gs) == 0 {
		return nil, errNoGroup
	}
	return &gs[0], nil
}

var errNoGroup = errors.New("postgres: no such group")

// SaveSuggestion keeps what the model proposed for a group.
func (r *GroupRepository) SaveSuggestion(ctx context.Context, id int64, s *grouping.Suggestion) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("postgres: encode suggestion: %w", err)
	}
	if _, err := r.pool.Exec(ctx, `UPDATE supplier_groups SET suggestion = $2, updated_at = now() WHERE id = $1`, id, raw); err != nil {
		return fmt.Errorf("postgres: save suggestion of group %d: %w", id, err)
	}
	return nil
}

// Decide maps a group and files the supplier's products of it there. A
// product moved takes its category's part type and universality, as an
// import would give them.
func (r *GroupRepository) Decide(ctx context.Context, id, categoryID int64, by string) error {
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE supplier_groups SET category_id = $2, decided_by = $3, mixed = false, updated_at = now() WHERE id = $1`,
			id, categoryID, by)
		if err != nil {
			return fmt.Errorf("postgres: decide group %d: %w", id, err)
		}
		if tag.RowsAffected() == 0 {
			return errNoGroup
		}
		if _, err := tx.Exec(ctx, `
			UPDATE products p SET category_id = c.id, part_type = c.part_type, universal = c.universal, updated_at = now()
			FROM supplier_groups g, suppliers s, categories c
			WHERE g.id = $1 AND s.id = g.supplier_id AND c.id = g.category_id AND p.source = s.source
			  AND ((g.kind = 'group' AND p.source_group = g.key) OR (g.kind = 'name' AND p.name = g.key))`, id); err != nil {
			return fmt.Errorf("postgres: file products of group %d: %w", id, err)
		}
		return nil
	})
}

// DecideMixed marks a group as of mixed products. Its products stay where
// they are: the words of their names put them there, and will again.
func (r *GroupRepository) DecideMixed(ctx context.Context, id int64, by string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE supplier_groups SET category_id = NULL, mixed = true, decided_by = $2, updated_at = now() WHERE id = $1`, id, by)
	if err != nil {
		return fmt.Errorf("postgres: mark group %d mixed: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return errNoGroup
	}
	return nil
}

// Find lists groups whose key contains text, case aside.
func (r *GroupRepository) Find(ctx context.Context, text string, limit int) ([]grouping.Group, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+groupColumns+` FROM supplier_groups g
		WHERE strpos(lower(g.key), lower($1)) > 0 ORDER BY g.products DESC, g.id LIMIT $2`, text, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: find groups: %w", err)
	}
	return scanGroups(rows)
}
