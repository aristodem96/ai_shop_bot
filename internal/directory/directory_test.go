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
	links    map[int64]int64          // mod id -> shop model
	shop     []ShopModel
}

func newMemStore() *memStore {
	return &memStore{models: map[int64][]autodb.Model{}, mods: map[int64][]autodb.Modification{}, vehicles: map[int64]domain.Vehicle{},
		links: map[int64]int64{}, shop: []ShopModel{{ID: 153, Make: "Chevrolet", Model: "Lacetti"}}}
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
func (s *memStore) ShopModels(context.Context) ([]ShopModel, error) { return s.shop, nil }
func (s *memStore) UpsertVehicle(_ context.Context, v domain.Vehicle, modID, shopModel int64) (int64, error) {
	s.vehicles[modID] = v
	s.links[modID] = shopModel
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

func TestFoundVehiclesLinkToTheShopsModel(t *testing.T) {
	d, _, store := newDirectory()
	_, err := d.FindVehicles(context.Background(), service.VehicleQuery{Make: "Chevrolet", Model: "Lacetti", Engine: "1.6"}, 10)
	require.NoError(t, err)
	assert.Equal(t, int64(153), store.links[18537], "LACETTI (J200) is the shop's Lacetti, so price list fitments reach it")
}

func TestLinkModel(t *testing.T) {
	shop := []ShopModel{
		{ID: 1, Make: "Chevrolet", Model: "Aveo T250"}, {ID: 2, Make: "Chevrolet", Model: "Aveo T200"},
		{ID: 3, Make: "Chevrolet", Model: "Lacetti"}, {ID: 4, Make: "Volkswagen", Model: "Golf"},
		{ID: 5, Make: "Geely", Model: "CK"}, {ID: 6, Make: "Geely", Model: "CK 2"},
	}
	for name, want := range map[[2]string]int64{
		{"CHEVROLET", "LACETTI (J200)"}:                3,
		{"CHEVROLET", "AVEO / KALOS Hatchback (T200)"}: 2,
		{"CHEVROLET", "AVEO Saloon (T250, T255)"}:      1,
		{"VW", "GOLF V (1K1)"}:                         4,
		{"GEELY", "CK (..)"}:                           5,
		{"CHEVROLET", "EPICA (KL1_)"}:                  0,
		{"DAEWOO", "LACETTI"}:                          0,
	} {
		assert.Equal(t, want, LinkModel(name[0], name[1], shop), name)
	}
}

type fakeOwnList struct{}

func (fakeOwnList) FindVehicles(_ context.Context, q service.VehicleQuery, _ int) ([]domain.Vehicle, error) {
	if q.Make == "Chery" {
		return []domain.Vehicle{{ID: 7, Make: "Chery", Model: "Tiggo"}}, nil
	}
	return nil, nil
}
func (fakeOwnList) Makes(context.Context) ([]string, error) {
	return []string{"Chery", "Chevrolet", "Volkswagen"}, nil
}
func (fakeOwnList) Models(_ context.Context, makeName string, _ int) ([]service.ModelName, error) {
	if makeName == "Chery" {
		return []service.ModelName{{Name: "Tiggo", Years: "2005–н.ч."}}, nil
	}
	return nil, nil
}
func (fakeOwnList) Vehicle(context.Context, int64) (*domain.Vehicle, error) {
	return nil, domain.ErrNotFound
}

func TestFallbackToTheShopsList(t *testing.T) {
	d, _, _ := newDirectory()
	d.WithFallback(fakeOwnList{})
	ctx := context.Background()

	got, err := d.FindVehicles(ctx, service.VehicleQuery{Make: "Chery", Model: "Tiggo"}, 10)
	require.NoError(t, err)
	require.Len(t, got, 1, "a make the catalogue lacks comes from the shop's list")
	assert.Equal(t, int64(7), got[0].ID)

	got, err = d.FindVehicles(ctx, service.VehicleQuery{Make: "Chevrolet", Model: "Lacetti", Engine: "1.6"}, 10)
	require.NoError(t, err)
	assert.Equal(t, int64(18537), got[0].ID, "one the catalogue has comes from the catalogue")

	makes, err := d.Makes(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"Chery", "Chevrolet", "Mercedes-Benz", "VW"}, makes, "Volkswagen is VW, said once")
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
	assert.Equal(t, "Mercedes-Benz", titleCase("MERCEDES-BENZ"))
	assert.Equal(t, "Lacetti Универсал (J200)", titleCase("LACETTI универсал (J200)"))
}

func TestModelsOfAMake(t *testing.T) {
	d, _, _ := newDirectory()
	d.WithFallback(fakeOwnList{})
	ctx := context.Background()

	all, err := d.Models(ctx, "шевроле", 0)
	require.NoError(t, err)
	assert.Equal(t, []service.ModelName{{Name: "Lacetti (J200)", Years: "2003–н.ч."}, {Name: "Aveo (T250)", Years: "2005–2011"}}, all,
		"as the catalogue names them, for a buyer's word to be matched against")

	in2013, err := d.Models(ctx, "Chevrolet", 2013)
	require.NoError(t, err)
	assert.Equal(t, []service.ModelName{{Name: "Lacetti (J200)", Years: "2003–н.ч."}}, in2013, "the Aveo T250 ended in 2011")

	own, err := d.Models(ctx, "Chery", 0)
	require.NoError(t, err)
	assert.Equal(t, []service.ModelName{{Name: "Tiggo", Years: "2005–н.ч."}}, own, "a make the catalogue lacks: the shop's list")
}
