package directory

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aristodem96/ai_shop_bot/internal/autodb"
	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/service"
)

// The modifications below are the live API's Lacetti J200, attributes and
// all.
func lacettiMods() []autodb.Modification {
	mod := func(id int64, name, interval, kw, capacity, code, fuel string) autodb.Modification {
		return autodb.Modification{ID: id, Name: name, Attrs: map[string][]string{
			"ConstructionInterval": {interval}, "Power": {kw, "109 PS"}, "Capacity": {capacity},
			"EngineCode": {code}, "FuelType": {fuel}, "NumberOfCylinders": {"4"},
		}}
	}
	return []autodb.Modification{
		mod(18536, "CHEVROLET LACETTI (J200) 1.4 16V", "03.2005 -", "70 kW", "1.4 l", "F14D3", "бензин"),
		mod(18537, "CHEVROLET LACETTI (J200) 1.6", "03.2005 -", "80 kW", "1.6 l", "F16D3", "бензин"),
		mod(18538, "CHEVROLET LACETTI (J200) 1.8", "03.2005 -", "90 kW", "1.8 l", "T18SED", "бензин"),
		mod(106531, "CHEVROLET LACETTI (J200) 1.8", "03.2005 - 12.2009", "67 kW", "1.8 l", "F18D2", "бензин"),
		mod(24469, "CHEVROLET LACETTI (J200) 2.0 D", "01.2007 -", "89 kW", "2 l", "Z 20 DM", "Дизель"),
	}
}

type fakeAPI struct{ calls map[string]int }

func (f *fakeAPI) Makes(context.Context, autodb.Group) ([]autodb.Make, error) {
	f.calls["makes"]++
	return []autodb.Make{{ID: 138, Name: "CHEVROLET"}, {ID: 121, Name: "VW"}, {ID: 74, Name: "MERCEDES-BENZ"}}, nil
}

func (f *fakeAPI) Models(_ context.Context, _ autodb.Group, makeID int64) ([]autodb.Model, error) {
	f.calls["models"]++
	if makeID != 138 {
		return nil, nil
	}
	return []autodb.Model{
		{ID: 5403, Name: "LACETTI (J200)", Years: "03.2003 -"},
		{ID: 1, Name: "AVEO (T250)", Years: "03.2005 - 12.2011"},
	}, nil
}

func (f *fakeAPI) Modifications(_ context.Context, _ autodb.Group, modelID int64) ([]autodb.Modification, error) {
	f.calls["mods"]++
	if modelID == 5403 {
		return lacettiMods(), nil
	}
	return nil, nil
}

// memStore is the cache in memory.
type memStore struct {
	makes    []autodb.Make
	models   map[int64][]autodb.Model
	mods     map[int64][]autodb.Modification
	vehicles map[int64]domain.Vehicle // by mod id
}

func newMemStore() *memStore {
	return &memStore{models: map[int64][]autodb.Model{}, mods: map[int64][]autodb.Modification{}, vehicles: map[int64]domain.Vehicle{}}
}

func (s *memStore) Makes(context.Context) ([]autodb.Make, error) { return s.makes, nil }
func (s *memStore) SaveMakes(_ context.Context, m []autodb.Make) error {
	s.makes = m
	return nil
}
func (s *memStore) Models(_ context.Context, id int64) ([]autodb.Model, bool, error) {
	m, ok := s.models[id]
	return m, ok, nil
}
func (s *memStore) SaveModels(_ context.Context, id int64, m []autodb.Model) error {
	s.models[id] = m
	return nil
}
func (s *memStore) Modifications(_ context.Context, id int64) ([]autodb.Modification, bool, error) {
	m, ok := s.mods[id]
	return m, ok, nil
}
func (s *memStore) SaveModifications(_ context.Context, id int64, m []autodb.Modification) error {
	s.mods[id] = m
	return nil
}
func (s *memStore) UpsertVehicle(_ context.Context, v domain.Vehicle, modID int64) (int64, error) {
	s.vehicles[modID] = v
	return modID, nil
}
func (s *memStore) Vehicle(_ context.Context, id int64) (*domain.Vehicle, error) {
	v, ok := s.vehicles[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return &v, nil
}

func newDirectory() (*Directory, *fakeAPI, *memStore) {
	api, store := &fakeAPI{calls: map[string]int{}}, newMemStore()
	return New(api, store, slog.New(slog.NewTextHandler(io.Discard, nil))), api, store
}

func TestToVehicle(t *testing.T) {
	v := ToVehicle("CHEVROLET", autodb.Model{Name: "LACETTI (J200)"}, lacettiMods()[3])
	assert.Equal(t, domain.Vehicle{Make: "Chevrolet", Model: "Lacetti (J200)", YearFrom: 2005, YearTo: 2009,
		Engine: "1.8 F18D2, 67 kW", Fuel: "petrol", Cylinders: 4}, v)
	assert.Equal(t, "Chevrolet Lacetti (J200) 1.8 F18D2, 67 kW (2005–2009)", v.Label(),
		"three 1.8s of one model are told apart by code and power")

	diesel := ToVehicle("CHEVROLET", autodb.Model{Name: "LACETTI (J200)"}, lacettiMods()[4])
	assert.Equal(t, "diesel", diesel.Fuel)
	assert.Zero(t, diesel.YearTo, "still produced")
}

func TestFindVehicles(t *testing.T) {
	d, api, _ := newDirectory()
	ctx := context.Background()

	got, err := d.FindVehicles(ctx, service.VehicleQuery{Make: "Chevrolet", Model: "Lacetti", Engine: "1.6"}, 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, int64(18537), got[0].ID, "the vehicle keeps the modification it is")
	assert.Equal(t, "1.6 F16D3, 80 kW", got[0].Engine)

	got, err = d.FindVehicles(ctx, service.VehicleQuery{Make: "шевроле", Model: "lacetti", Engine: "1.8"}, 10)
	require.NoError(t, err)
	assert.Len(t, got, 2, "a Cyrillic make, and both 1.8s")

	got, err = d.FindVehicles(ctx, service.VehicleQuery{Make: "Chevrolet", Model: "Lacetti", Engine: "1.8", Year: 2011}, 10)
	require.NoError(t, err)
	require.Len(t, got, 1, "the one 1.8 still made in 2011")
	assert.Equal(t, "1.8 T18SED, 90 kW", got[0].Engine)

	got, err = d.FindVehicles(ctx, service.VehicleQuery{Make: "Chevrolet", Model: "Lacetti", Engine: "дизель"}, 10)
	require.NoError(t, err)
	require.Len(t, got, 1, "a fuel word picks by fuel")
	assert.Equal(t, "diesel", got[0].Fuel)

	assert.Equal(t, 1, api.calls["makes"], "the directory asks the API once and keeps the answer")
	assert.Equal(t, 1, api.calls["models"])
	assert.Equal(t, 1, api.calls["mods"])
}

func TestFindVehiclesNeedsAMake(t *testing.T) {
	d, api, _ := newDirectory()
	got, err := d.FindVehicles(context.Background(), service.VehicleQuery{Model: "Lacetti"}, 10)
	require.NoError(t, err)
	assert.Empty(t, got)
	assert.Empty(t, api.calls, "no make, no calls")
}

func TestFindMakeSynonymsAndPrefixes(t *testing.T) {
	d, _, _ := newDirectory()
	ctx := context.Background()
	for name, want := range map[string]string{
		"Volkswagen": "VW", "фольксваген": "VW", "Mercedes": "MERCEDES-BENZ", "merc": "MERCEDES-BENZ", "chev": "CHEVROLET",
	} {
		m, ok, err := d.findMake(ctx, name)
		require.NoError(t, err)
		require.True(t, ok, name)
		assert.Equal(t, want, m.Name, name)
	}
	_, ok, _ := d.findMake(ctx, "m")
	assert.False(t, ok, "one letter matches too much")
}

func TestTitleCase(t *testing.T) {
	assert.Equal(t, "Chevrolet", titleCase("CHEVROLET"))
	assert.Equal(t, "VW", titleCase("VW"))
	assert.Equal(t, "Golf V (1K1)", titleCase("GOLF V (1K1)"))
	assert.Equal(t, "Lacetti Универсал (J200)", titleCase("LACETTI универсал (J200)"))
}
