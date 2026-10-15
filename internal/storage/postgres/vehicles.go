package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/service"
)

var (
	_ service.VehicleRepository = (*VehicleRepository)(nil)
	_ service.GarageRepository  = (*VehicleRepository)(nil)
)

// VehicleRepository reads vehicles and stores each buyer's car.
type VehicleRepository struct {
	pool *pgxpool.Pool
}

// NewVehicleRepository returns a repository backed by pool.
func NewVehicleRepository(pool *pgxpool.Pool) *VehicleRepository {
	return &VehicleRepository{pool: pool}
}

const vehicleColumns = `v.id, v.make, v.model, v.year_from, coalesce(v.year_to, 0), v.engine, v.fuel`

func scanVehicle(row pgx.Row) (domain.Vehicle, error) {
	var v domain.Vehicle
	err := row.Scan(&v.ID, &v.Make, &v.Model, &v.YearFrom, &v.YearTo, &v.Engine, &v.Fuel)
	return v, err
}

func collectVehicles(rows pgx.Rows) ([]domain.Vehicle, error) {
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.Vehicle, error) {
		return scanVehicle(row)
	})
}

// FindVehicles matches make, model and engine as case-insensitive substrings
// and a year against the production span. LIKE wildcards in the input are
// escaped, so "%" from a buyer matches a literal percent sign, not everything.
func (r *VehicleRepository) FindVehicles(ctx context.Context, q service.VehicleQuery, limit int) ([]domain.Vehicle, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+vehicleColumns+` FROM vehicles v
		WHERE v.make ILIKE '%' || $1 || '%'
		  AND v.model ILIKE '%' || $2 || '%'
		  AND v.engine ILIKE '%' || $3 || '%'
		  AND ($4 = 0 OR ($4 >= v.year_from AND (v.year_to IS NULL OR $4 <= v.year_to)))
		ORDER BY v.make, v.model, v.year_from, v.engine
		LIMIT $5`,
		escapeLike(q.Make), escapeLike(q.Model), escapeLike(q.Engine), q.Year, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: find vehicles: %w", err)
	}
	vs, err := collectVehicles(rows)
	if err != nil {
		return nil, fmt.Errorf("postgres: scan vehicles: %w", err)
	}
	return vs, nil
}

// Makes returns the distinct makes, alphabetically.
func (r *VehicleRepository) Makes(ctx context.Context) ([]string, error) {
	rows, err := r.pool.Query(ctx, `SELECT DISTINCT make FROM vehicles ORDER BY make`)
	if err != nil {
		return nil, fmt.Errorf("postgres: list makes: %w", err)
	}
	makes, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("postgres: scan makes: %w", err)
	}
	return makes, nil
}

// escapeLike makes LIKE treat %, _ and the escape character itself literally.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// Vehicle finds one vehicle.
func (r *VehicleRepository) Vehicle(ctx context.Context, id int64) (*domain.Vehicle, error) {
	v, err := scanVehicle(r.pool.QueryRow(ctx, `SELECT `+vehicleColumns+` FROM vehicles v WHERE v.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: get vehicle %d: %w", id, err)
	}
	return &v, nil
}

// Garage returns the buyer's car, or nil if none was chosen.
func (r *VehicleRepository) Garage(ctx context.Context, userID int64) (*domain.Vehicle, error) {
	v, err := scanVehicle(r.pool.QueryRow(ctx, `
		SELECT `+vehicleColumns+` FROM vehicles v
		JOIN garages g ON g.vehicle_id = v.id
		WHERE g.user_id = $1`, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: get garage of %d: %w", userID, err)
	}
	return &v, nil
}

// SetGarage chooses the buyer's car, replacing any earlier choice.
func (r *VehicleRepository) SetGarage(ctx context.Context, userID, vehicleID int64) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO garages (user_id, vehicle_id) VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET vehicle_id = EXCLUDED.vehicle_id, updated_at = now()`,
		userID, vehicleID)
	if isForeignKeyViolation(err) {
		return domain.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("postgres: set garage of %d: %w", userID, err)
	}
	return nil
}

// ClearGarage forgets the buyer's car.
func (r *VehicleRepository) ClearGarage(ctx context.Context, userID int64) error {
	if _, err := r.pool.Exec(ctx, `DELETE FROM garages WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("postgres: clear garage of %d: %w", userID, err)
	}
	return nil
}
