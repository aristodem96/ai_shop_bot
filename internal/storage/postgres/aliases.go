package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/aristodem96/ai_shop_bot/internal/importer"
)

var _ importer.AliasStore = (*ImportRepository)(nil)

// VehicleAliases maps a supplier's confirmed vehicle names to models.
func (r *ImportRepository) VehicleAliases(ctx context.Context, source string) (map[string][]int64, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT name_key, model_id FROM vehicle_aliases WHERE source = $1 ORDER BY id`, source)
	if err != nil {
		return nil, fmt.Errorf("postgres: list vehicle aliases: %w", err)
	}
	out := make(map[string][]int64)
	var (
		key string
		id  int64
	)
	if _, err := pgx.ForEachRow(rows, []any{&key, &id}, func() error {
		out[key] = append(out[key], id)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("postgres: scan vehicle aliases: %w", err)
	}
	return out, nil
}

// AddVehicleAlias stores a confirmed mapping. Confirming the same pair twice
// changes nothing.
func (r *ImportRepository) AddVehicleAlias(ctx context.Context, source, name, key string, modelIDs []int64) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO vehicle_aliases (source, name, name_key, model_id)
		SELECT $1, $2, $3, unnest($4::bigint[])
		ON CONFLICT (source, name_key, model_id) DO NOTHING`, source, name, key, modelIDs)
	if isForeignKeyViolation(err) {
		return fmt.Errorf("postgres: alias %q names a model that is gone: %w", name, err)
	}
	if err != nil {
		return fmt.Errorf("postgres: add vehicle alias %q: %w", name, err)
	}
	return nil
}

// ListVehicleAliases lists every confirmed mapping, newest first.
func (r *ImportRepository) ListVehicleAliases(ctx context.Context) ([]importer.VehicleAlias, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT a.id, a.source, a.name, m.make || ' ' || m.model, 0
		FROM vehicle_aliases a JOIN vehicle_models m ON m.id = a.model_id
		ORDER BY a.id DESC`)
	if err != nil {
		return nil, fmt.Errorf("postgres: list vehicle aliases: %w", err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[importer.VehicleAlias])
	if err != nil {
		return nil, fmt.Errorf("postgres: scan vehicle aliases: %w", err)
	}
	return out, nil
}

// RemoveVehicleAlias deletes one mapping and, with it, the fitments it made:
// those of the supplier's products to that model that came from that name.
func (r *ImportRepository) RemoveVehicleAlias(ctx context.Context, id int64) (*importer.VehicleAlias, error) {
	var a importer.VehicleAlias
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		var (
			key     string
			modelID int64
		)
		err := tx.QueryRow(ctx, `
			WITH d AS (DELETE FROM vehicle_aliases WHERE id = $1 RETURNING id, source, name, name_key, model_id)
			SELECT d.id, d.source, d.name, m.make || ' ' || m.model, d.name_key, d.model_id
			FROM d JOIN vehicle_models m ON m.id = d.model_id`, id).
			Scan(&a.ID, &a.Source, &a.Name, &a.Model, &key, &modelID)
		if errors.Is(err, pgx.ErrNoRows) {
			return importer.ErrNoAlias
		}
		if err != nil {
			return fmt.Errorf("postgres: remove vehicle alias %d: %w", id, err)
		}
		tag, err := tx.Exec(ctx, `
			DELETE FROM product_model_fitments mf USING products p
			WHERE mf.product_id = p.id AND p.source = $1 AND mf.via = $2 AND mf.model_id = $3`,
			a.Source, key, modelID)
		if err != nil {
			return fmt.Errorf("postgres: remove fitments of alias %d: %w", id, err)
		}
		a.Fitments = int(tag.RowsAffected())
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &a, nil
}
