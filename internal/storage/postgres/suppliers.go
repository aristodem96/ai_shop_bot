package postgres

import (
	"context"
	"fmt"

	"github.com/aristodem96/ai_shop_bot/internal/importer"
)

var _ importer.SupplierStore = (*ImportRepository)(nil)

// Suppliers lists the suppliers by name.
func (r *ImportRepository) Suppliers(ctx context.Context) ([]importer.Supplier, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, name, source, columns, lead_days, markup_percent::float8 FROM suppliers ORDER BY lower(name)`)
	if err != nil {
		return nil, fmt.Errorf("postgres: list suppliers: %w", err)
	}
	defer rows.Close()
	var out []importer.Supplier
	for rows.Next() {
		var s importer.Supplier
		if err := rows.Scan(&s.ID, &s.Name, &s.Source, &s.Columns, &s.LeadDays, &s.MarkupPercent); err != nil {
			return nil, fmt.Errorf("postgres: scan supplier: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// SaveSupplier creates a supplier or updates one by id. The source is set
// once, on creation.
func (r *ImportRepository) SaveSupplier(ctx context.Context, s *importer.Supplier) error {
	if s.ID == 0 {
		if err := r.pool.QueryRow(ctx, `
			INSERT INTO suppliers (name, source, columns, lead_days, markup_percent)
			VALUES ($1, $2, $3, $4, $5) RETURNING id`,
			s.Name, s.Source, s.Columns, s.LeadDays, s.MarkupPercent).Scan(&s.ID); err != nil {
			if isUniqueViolation(err) {
				return importer.ErrSupplierTaken
			}
			return fmt.Errorf("postgres: create supplier: %w", err)
		}
		return nil
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE suppliers SET name = $2, columns = $3, lead_days = $4, markup_percent = $5 WHERE id = $1`,
		s.ID, s.Name, s.Columns, s.LeadDays, s.MarkupPercent)
	if err != nil {
		if isUniqueViolation(err) {
			return importer.ErrSupplierTaken
		}
		return fmt.Errorf("postgres: update supplier %d: %w", s.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return importer.ErrNoSupplier
	}
	return nil
}

// DeleteSupplier forgets a supplier; its products stay.
func (r *ImportRepository) DeleteSupplier(ctx context.Context, id int64) error {
	if _, err := r.pool.Exec(ctx, `DELETE FROM suppliers WHERE id = $1`, id); err != nil {
		return fmt.Errorf("postgres: delete supplier %d: %w", id, err)
	}
	return nil
}
