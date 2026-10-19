package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aristodem96/ai_shop_bot/internal/autodb"
	"github.com/aristodem96/ai_shop_bot/internal/directory"
)

func TestDirectoryCache(t *testing.T) {
	skipShort(t)
	ctx := context.Background()
	repo := NewDirectoryRepository(testPool)
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM vehicles WHERE autodb_mod_id IS NOT NULL`)
		_, _ = testPool.Exec(ctx, `DELETE FROM autodb_makes`)
	})

	require.NoError(t, repo.SaveMakes(ctx, []autodb.Make{{ID: 138, Name: "CHEVROLET"}}))
	makes, err := repo.Makes(ctx)
	require.NoError(t, err)
	assert.Equal(t, []autodb.Make{{ID: 138, Name: "CHEVROLET"}}, makes)

	_, fetched, err := repo.Models(ctx, 138)
	require.NoError(t, err)
	assert.False(t, fetched)
	require.NoError(t, repo.SaveModels(ctx, 138, nil))
	_, fetched, err = repo.Models(ctx, 138)
	require.NoError(t, err)
	assert.True(t, fetched, "an empty list fetched is remembered, not asked again")

	model := autodb.Model{ID: 5403, Name: "LACETTI (J200)", Years: "03.2003 -"}
	require.NoError(t, repo.SaveModels(ctx, 138, []autodb.Model{model}))
	mod := autodb.Modification{ID: 18537, Name: "CHEVROLET LACETTI (J200) 1.6",
		Attrs: map[string][]string{"Power": {"80 kW", "109 PS"}, "Capacity": {"1.6 l"}, "FuelType": {"бензин"},
			"ConstructionInterval": {"03.2005 -"}, "EngineCode": {"F16D3"}}}
	require.NoError(t, repo.SaveModifications(ctx, 5403, []autodb.Modification{mod}))
	mods, fetched, err := repo.Modifications(ctx, 5403)
	require.NoError(t, err)
	assert.True(t, fetched)
	assert.Equal(t, []autodb.Modification{mod}, mods, "attributes survive the round trip")

	v := directory.ToVehicle("CHEVROLET", model, mod)
	id, err := repo.UpsertVehicle(ctx, v, mod.ID)
	require.NoError(t, err)
	again, err := repo.UpsertVehicle(ctx, v, mod.ID)
	require.NoError(t, err)
	assert.Equal(t, id, again, "a modification is one vehicle however often it is found")

	got, err := repo.Vehicle(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "Chevrolet Lacetti (J200) 1.6 F16D3, 80 kW (2005–н.ч.)", got.Label())

	// The garage takes it like any vehicle.
	require.NoError(t, NewVehicleRepository(testPool).SetGarage(ctx, 7001, id))
	t.Cleanup(func() { _ = NewVehicleRepository(testPool).ClearGarage(ctx, 7001) })
	car, err := NewVehicleRepository(testPool).Garage(ctx, 7001)
	require.NoError(t, err)
	assert.Equal(t, id, car.ID)
}
