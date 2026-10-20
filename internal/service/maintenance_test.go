package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
)

func oil(id int64, litres string, price int64) domain.Product {
	return domain.Product{ID: id, PartType: domain.PartEngineOil, Universal: true, Price: domain.UAH(price), Stock: 10,
		Specs: []domain.Spec{{Name: "В'язкість", Value: "5W-30"}, {Name: "Об'єм", Value: litres}}}
}

func TestPlanOil(t *testing.T) {
	cans := func() []can {
		return []can{{oil(1, "1 л", 380), 1}, {oil(4, "4 л", 1290), 4}}
	}
	ids := func(plan []canLine) map[int64]int {
		out := map[int64]int{}
		for _, l := range plan {
			out[l.product.ID] = l.qty
		}
		return out
	}

	assert.Equal(t, map[int64]int{4: 1}, ids(planOil(3.6, cans())), "one large can covers 3.6 l")
	assert.Equal(t, map[int64]int{4: 1}, ids(planOil(4.0, cans())))
	assert.Equal(t, map[int64]int{4: 1, 1: 1}, ids(planOil(4.6, cans())), "the rest from small cans")
	assert.Equal(t, map[int64]int{4: 2}, ids(planOil(6.5, cans())), "over half a large can: another large one")
	assert.Equal(t, map[int64]int{1: 2}, ids(planOil(1.5, []can{{oil(1, "1 л", 380), 1}})), "only small cans")
	assert.Empty(t, planOil(4, nil))
}

func newKitFixture() (*MaintenanceService, *fakeCatalog) {
	octavia := domain.Vehicle{ID: 1, Make: "Skoda", Fuel: "petrol", Cylinders: 4, OilViscosity: "5W-30", OilVolume: 4.6}
	diesel := domain.Vehicle{ID: 2, Make: "Skoda", Fuel: "diesel", Cylinders: 4, OilViscosity: "5W-30", OilVolume: 4.6}
	unknown := domain.Vehicle{ID: 3, Make: "Daewoo", Fuel: "petrol", Cylinders: 4}
	fits := []domain.Vehicle{octavia, diesel, unknown}

	catalog := &fakeCatalog{products: []domain.Product{
		oil(10, "4 л", 1290), oil(11, "1 л", 380),
		{ID: 20, PartType: domain.PartOilFilter, Price: domain.UAH(290), Stock: 5, Fits: fits},
		{ID: 21, PartType: domain.PartOilFilter, Price: domain.UAH(240), Stock: 5, Fits: fits},
		{ID: 30, PartType: domain.PartAirFilter, Price: domain.UAH(480), Stock: 5, Fits: fits[:2]},
		{ID: 40, PartType: domain.PartSparkPlug, Price: domain.UAH(260), Stock: 9, Fits: fits},
	}}
	vehicles := &fakeVehicles{list: []domain.Vehicle{octavia, diesel, unknown}}
	return NewMaintenanceService(catalog, vehicles), catalog
}

func TestKitForPetrolCar(t *testing.T) {
	svc, _ := newKitFixture()
	kit, err := svc.Kit(context.Background(), 1)
	require.NoError(t, err)

	byType := map[string][]KitItem{}
	for _, i := range kit.Items {
		byType[i.PartType] = append(byType[i.PartType], i)
	}
	require.Len(t, byType[domain.PartEngineOil], 2, "4 l + 1 l for 4.6 l")
	assert.Equal(t, int64(21), byType[domain.PartOilFilter][0].Product.ID, "the cheapest filter by default")
	assert.Equal(t, int64(20), byType[domain.PartOilFilter][0].Alternatives[0].ID)
	assert.Equal(t, 4, byType[domain.PartSparkPlug][0].Qty, "a plug per cylinder")
	assert.Equal(t, []string{domain.PartCabinFilter}, kit.Missing, "the shop has no cabin filter in stock")

	assert.Equal(t, domain.UAH(1290+380+240+480+4*260), kit.Total())
}

func TestKitForDieselHasNoPlugs(t *testing.T) {
	svc, _ := newKitFixture()
	kit, err := svc.Kit(context.Background(), 2)
	require.NoError(t, err)
	for _, i := range kit.Items {
		assert.NotEqual(t, domain.PartSparkPlug, i.PartType)
	}
}

func TestKitWithoutServiceData(t *testing.T) {
	svc, _ := newKitFixture()
	kit, err := svc.Kit(context.Background(), 3)
	require.NoError(t, err)
	assert.Contains(t, kit.Missing, domain.PartEngineOil, "no oil without knowing the grade")
}

func TestKitUnknownVehicle(t *testing.T) {
	svc, _ := newKitFixture()
	_, err := svc.Kit(context.Background(), 99)
	require.ErrorIs(t, err, domain.ErrNotFound)
}

// kitOrders are a car's catalogue parts by part type.
type kitOrders map[string][]domain.CatalogPart

func (k kitOrders) ForPartType(_ context.Context, _ int64, partType string, limit int) ([]domain.CatalogPart, error) {
	parts := k[partType]
	return parts[:min(limit, len(parts))], nil
}

func TestKitOffersWhatIsNotInStockOnOrder(t *testing.T) {
	svc, catalog := newKitFixture()
	catalog.products = nil // nothing on the shelves
	svc.UseOrderParts(kitOrders{
		domain.PartOilFilter: {{ID: 71, Name: "Масляний фільтр", Brand: "MANN-FILTER", Number: "W 712/95"}},
		domain.PartSparkPlug: {{ID: 81, Name: "Свічка запалювання", Brand: "NGK", Number: "BKR6E"}, {ID: 82, Brand: "BOSCH"}},
	})

	kit, err := svc.Kit(context.Background(), 1)
	require.NoError(t, err)
	assert.Empty(t, kit.Items)
	require.Len(t, kit.OnOrder, 2, "the car's catalogue has an oil filter and plugs")
	assert.Equal(t, domain.PartOilFilter, kit.OnOrder[0].PartType)
	assert.Equal(t, 1, kit.OnOrder[0].Qty)
	assert.Equal(t, domain.PartSparkPlug, kit.OnOrder[1].PartType)
	assert.Equal(t, 4, kit.OnOrder[1].Qty, "a plug per cylinder")
	assert.Len(t, kit.OnOrder[1].Options, 2)
	assert.Contains(t, kit.Missing, domain.PartEngineOil, "oil is planned against the engine, never offered on order")
	assert.Contains(t, kit.Missing, domain.PartAirFilter, "and what the catalogue lacks is still missing")
}
