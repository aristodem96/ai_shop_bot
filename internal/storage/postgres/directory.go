package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aristodem96/ai_shop_bot/internal/autodb"
	"github.com/aristodem96/ai_shop_bot/internal/directory"
	"github.com/aristodem96/ai_shop_bot/internal/domain"
)

var _ directory.Store = (*DirectoryRepository)(nil)

// DirectoryRepository caches the Auto-DB vehicle directory.
type DirectoryRepository struct {
	pool     *pgxpool.Pool
	vehicles *VehicleRepository
}

// NewDirectoryRepository returns a repository backed by pool.
func NewDirectoryRepository(pool *pgxpool.Pool) *DirectoryRepository {
	return &DirectoryRepository{pool: pool, vehicles: NewVehicleRepository(pool)}
}

// Makes returns the cached makes, by name.
func (r *DirectoryRepository) Makes(ctx context.Context) ([]autodb.Make, error) {
	rows, err := r.pool.Query(ctx, `SELECT id, name FROM autodb_makes ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("postgres: list autodb makes: %w", err)
	}
	makes, err := pgx.CollectRows(rows, pgx.RowToStructByPos[autodb.Make])
	if err != nil {
		return nil, fmt.Errorf("postgres: scan autodb makes: %w", err)
	}
	return makes, nil
}

// SaveMakes stores the makes, replacing names that changed.
func (r *DirectoryRepository) SaveMakes(ctx context.Context, makes []autodb.Make) error {
	ids, names := make([]int64, len(makes)), make([]string, len(makes))
	for i, m := range makes {
		ids[i], names[i] = m.ID, m.Name
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO autodb_makes (id, name)
		SELECT * FROM unnest($1::bigint[], $2::text[])
		ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name, fetched_at = now()`, ids, names)
	if err != nil {
		return fmt.Errorf("postgres: save autodb makes: %w", err)
	}
	return nil
}

// Models returns a make's cached models and whether they were fetched.
func (r *DirectoryRepository) Models(ctx context.Context, makeID int64) ([]autodb.Model, bool, error) {
	var fetched bool
	if err := r.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM autodb_models_fetched WHERE make_id = $1)`,
		makeID).Scan(&fetched); err != nil {
		return nil, false, fmt.Errorf("postgres: check autodb models of %d: %w", makeID, err)
	}
	rows, err := r.pool.Query(ctx, `SELECT id, name, years FROM autodb_models WHERE make_id = $1 ORDER BY name`, makeID)
	if err != nil {
		return nil, false, fmt.Errorf("postgres: list autodb models of %d: %w", makeID, err)
	}
	models, err := pgx.CollectRows(rows, pgx.RowToStructByPos[autodb.Model])
	if err != nil {
		return nil, false, fmt.Errorf("postgres: scan autodb models: %w", err)
	}
	return models, fetched, nil
}

// SaveModels stores a make's models and marks them fetched.
func (r *DirectoryRepository) SaveModels(ctx context.Context, makeID int64, models []autodb.Model) error {
	ids, names, years := make([]int64, len(models)), make([]string, len(models)), make([]string, len(models))
	for i, m := range models {
		ids[i], names[i], years[i] = m.ID, m.Name, m.Years
	}
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO autodb_models (id, make_id, name, years)
			SELECT x.id, $1, x.name, x.years FROM unnest($2::bigint[], $3::text[], $4::text[]) AS x(id, name, years)
			ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name, years = EXCLUDED.years`,
			makeID, ids, names, years); err != nil {
			return fmt.Errorf("postgres: save autodb models of %d: %w", makeID, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO autodb_models_fetched (make_id) VALUES ($1)
			ON CONFLICT (make_id) DO UPDATE SET fetched_at = now()`, makeID); err != nil {
			return fmt.Errorf("postgres: mark autodb models of %d: %w", makeID, err)
		}
		return nil
	})
}

// Modifications returns a model's cached modifications and whether they were
// fetched.
func (r *DirectoryRepository) Modifications(ctx context.Context, modelID int64) ([]autodb.Modification, bool, error) {
	var fetched bool
	if err := r.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM autodb_modifications_fetched WHERE model_id = $1)`,
		modelID).Scan(&fetched); err != nil {
		return nil, false, fmt.Errorf("postgres: check autodb modifications of %d: %w", modelID, err)
	}
	rows, err := r.pool.Query(ctx, `SELECT id, name, attrs FROM autodb_modifications WHERE model_id = $1 ORDER BY name, id`, modelID)
	if err != nil {
		return nil, false, fmt.Errorf("postgres: list autodb modifications of %d: %w", modelID, err)
	}
	mods, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (autodb.Modification, error) {
		var (
			m     autodb.Modification
			attrs []byte
		)
		if err := row.Scan(&m.ID, &m.Name, &attrs); err != nil {
			return m, err
		}
		return m, json.Unmarshal(attrs, &m.Attrs)
	})
	if err != nil {
		return nil, false, fmt.Errorf("postgres: scan autodb modifications: %w", err)
	}
	return mods, fetched, nil
}

// SaveModifications stores a model's modifications and marks them fetched.
func (r *DirectoryRepository) SaveModifications(ctx context.Context, modelID int64, mods []autodb.Modification) error {
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		for _, m := range mods {
			attrs, err := json.Marshal(m.Attrs)
			if err != nil {
				return fmt.Errorf("postgres: encode attrs of %d: %w", m.ID, err)
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO autodb_modifications (id, model_id, name, attrs) VALUES ($1, $2, $3, $4)
				ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name, attrs = EXCLUDED.attrs`,
				m.ID, modelID, m.Name, attrs); err != nil {
				return fmt.Errorf("postgres: save autodb modification %d: %w", m.ID, err)
			}
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO autodb_modifications_fetched (model_id) VALUES ($1)
			ON CONFLICT (model_id) DO UPDATE SET fetched_at = now()`, modelID); err != nil {
			return fmt.Errorf("postgres: mark autodb modifications of %d: %w", modelID, err)
		}
		return nil
	})
}

// UpsertVehicle writes a modification as a vehicle. One already written --
// by its Auto-DB id, or as the same make, model, engine and year -- is
// reused, not doubled.
func (r *DirectoryRepository) UpsertVehicle(ctx context.Context, v domain.Vehicle, modID int64) (int64, error) {
	var id int64
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO vehicle_models (make, model) VALUES ($1, $2) ON CONFLICT DO NOTHING`, v.Make, v.Model); err != nil {
			return fmt.Errorf("postgres: model of autodb %d: %w", modID, err)
		}
		err := tx.QueryRow(ctx, `
			INSERT INTO vehicles (code, make, model, year_from, year_to, engine, fuel, cylinders, autodb_mod_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			ON CONFLICT DO NOTHING
			RETURNING id`,
			fmt.Sprintf("adb-%d", modID), v.Make, v.Model, v.YearFrom, nullable(v.YearTo), v.Engine, v.Fuel, v.Cylinders, modID,
		).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			err = tx.QueryRow(ctx, `
				SELECT id FROM vehicles
				WHERE autodb_mod_id = $1 OR (make = $2 AND model = $3 AND engine = $4 AND year_from = $5)
				ORDER BY autodb_mod_id = $1 DESC LIMIT 1`, modID, v.Make, v.Model, v.Engine, v.YearFrom).Scan(&id)
		}
		if err != nil {
			return fmt.Errorf("postgres: upsert vehicle of autodb %d: %w", modID, err)
		}
		return nil
	})
	return id, err
}

// Vehicle returns a vehicle by id.
func (r *DirectoryRepository) Vehicle(ctx context.Context, id int64) (*domain.Vehicle, error) {
	return r.vehicles.Vehicle(ctx, id)
}
