package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aristodem96/ai_shop_bot/internal/service"
)

// modelFitted inserts a product fitted the way a price list fits parts: to a
// whole model, from a year, with a position. It is removed when the test ends.
func modelFitted(t *testing.T, sku, makeName, model string, yearFrom, yearTo *int, position string) int64 {
	t.Helper()
	ctx := context.Background()
	var id int64
	require.NoError(t, testPool.QueryRow(ctx, `
		INSERT INTO products (sku, category_id, name, brand, price, stock)
		VALUES ($1, (SELECT id FROM categories WHERE slug = 'brakes'), 'Тестові колодки', 'Testbrand', 100000, 5)
		RETURNING id`, sku).Scan(&id))
	t.Cleanup(func() {
		_, err := testPool.Exec(context.Background(), `DELETE FROM products WHERE id = $1`, id)
		assert.NoError(t, err)
	})
	_, err := testPool.Exec(ctx, `
		INSERT INTO product_model_fitments (product_id, model_id, year_from, year_to, position)
		SELECT $1, id, $4, $5, $6 FROM vehicle_models WHERE make = $2 AND model = $3`,
		id, makeName, model, yearFrom, yearTo, position)
	require.NoError(t, err)
	return id
}

func ptr(n int) *int { return &n }

func TestModelFitmentCoversEveryEngine(t *testing.T) {
	skipShort(t)
	ctx := context.Background()
	repo := NewCatalogRepository(testPool)
	id := modelFitted(t, "TST-MODEL", "Skoda", "Octavia A7", ptr(2017), nil, "передня вісь")

	p, err := repo.Product(ctx, id)
	require.NoError(t, err)
	tsi, tdi := vehicleByCode(t, "octavia-a7-14tsi"), vehicleByCode(t, "octavia-a7-20tdi")
	assert.True(t, p.FitsVehicle(tsi), "a model fitment applies to every engine of the model")
	assert.True(t, p.FitsVehicle(tdi))
	assert.False(t, p.FitsVehicle(vehicleByCode(t, "golf-7-16tdi")))
	assert.Equal(t, "передня вісь", p.FitNotes[tsi], "the card says where on the car it goes")

	found, err := repo.Search(ctx, service.SearchFilter{VehicleID: tdi, Brand: "Testbrand", Limit: 5})
	require.NoError(t, err)
	require.Len(t, found, 1, "search filters by the model fitment too")
	assert.Equal(t, id, found[0].ID)
}

func TestModelFitmentRespectsYears(t *testing.T) {
	skipShort(t)
	ctx := context.Background()
	// Corolla E210 is produced from 2019: a part for cars up to 2018 belongs
	// to the previous generation and must not fit it.
	id := modelFitted(t, "TST-YEARS", "Toyota", "Corolla E210", nil, ptr(2018), "")

	p, err := NewCatalogRepository(testPool).Product(ctx, id)
	require.NoError(t, err)
	assert.Empty(t, p.Fits)
}

func TestDirectFitmentNoteWinsOverModel(t *testing.T) {
	skipShort(t)
	ctx := context.Background()
	id := modelFitted(t, "TST-BOTH", "Renault", "Logan II", nil, nil, "передня вісь")
	diesel := vehicleByCode(t, "logan-2-15dci")
	_, err := testPool.Exec(ctx, `
		INSERT INTO product_fitments (product_id, vehicle_id, position, note)
		VALUES ($1, $2, 'передня вісь', 'з ABS')`, id, diesel)
	require.NoError(t, err)

	p, err := NewCatalogRepository(testPool).Product(ctx, id)
	require.NoError(t, err)
	assert.Len(t, p.Fits, 2, "a vehicle fitted both ways is listed once")
	assert.Equal(t, "передня вісь, з ABS", p.FitNotes[diesel])
	assert.Equal(t, "передня вісь", p.FitNotes[vehicleByCode(t, "logan-2-16")])
}

// The menu counts must agree with the pages they open: both answer "what
// fits this car", by different queries.
func TestCategoryCountsMatchPages(t *testing.T) {
	skipShort(t)
	ctx := context.Background()
	repo := NewCatalogRepository(testPool)
	cats, err := repo.Categories(ctx)
	require.NoError(t, err)

	for _, vehicle := range []int64{0, vehicleByCode(t, "camry-xv70-25"), vehicleByCode(t, "lanos-15")} {
		counts, err := repo.CategoryCounts(ctx, vehicle)
		require.NoError(t, err)
		for _, c := range cats {
			if c.ParentID == 0 {
				continue // a section's page counts its subcategories, checked through them
			}
			_, total, err := repo.ProductsByCategory(ctx, c.ID, vehicle, 1, 0)
			require.NoError(t, err)
			assert.Equal(t, total, counts[c.ID], "%s for vehicle %d", c.Slug, vehicle)
		}
	}
}
