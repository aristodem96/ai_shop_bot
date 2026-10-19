package agent

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/enrich"
	"github.com/aristodem96/ai_shop_bot/internal/llm"
	"github.com/aristodem96/ai_shop_bot/internal/service"
)

var bremboPads = domain.CatalogPart{ID: 77, Brand: "BREMBO", Number: "P 85 075", Name: "Гальмівні колодки", Category: "brake-pads"}

type fakeOrderCatalog struct {
	state   string
	filters []service.SearchFilter
}

func (f *fakeOrderCatalog) ForVehicle(_ context.Context, sf service.SearchFilter) ([]domain.CatalogPart, error) {
	f.filters = append(f.filters, sf)
	return []domain.CatalogPart{bremboPads}, nil
}

func (f *fakeOrderCatalog) CatalogPart(_ context.Context, id int64) (*domain.CatalogPart, error) {
	if id != bremboPads.ID {
		return nil, domain.ErrNotFound
	}
	p := bremboPads
	return &p, nil
}

func (f *fakeOrderCatalog) CrawlState(context.Context, int64) (string, error) { return f.state, nil }

func TestSearchOffersCatalogParts(t *testing.T) {
	model := llm.NewFake(
		llm.Call("c1", "search_parts", map[string]any{"vehicle_id": octavia.ID, "query": "колодк", "max_price_uah": 900}),
		llm.Call("c2", toolShowProducts, map[string]any{"catalog_part_ids": []int{77}}),
		llm.Say("На складі таких немає, але можемо замовити Brembo."),
	)
	f := newAgent(t, model)
	parts := &fakeOrderCatalog{state: "running"}
	f.agent.UseOrderCatalog(parts)

	reply, err := f.agent.Reply(context.Background(), 1, 1, "колодки brembo")
	require.NoError(t, err)
	assert.Equal(t, []int64{77}, reply.CatalogPartIDs)
	assert.Empty(t, reply.ProductIDs)

	res := result(model, 1).Content
	assert.Contains(t, res, `"catalog_parts":[{"catalog_part_id":77,"name":"Гальмівні колодки","brand":"BREMBO","number":"P 85 075","category":"brake-pads"}]`)
	assert.Contains(t, res, `"catalog_note"`, "the model is told the car is still loading")
	require.Len(t, parts.filters, 1)
	assert.Equal(t, service.SearchFilter{VehicleID: octavia.ID, Query: "колодк", Limit: maxShown}, parts.filters[0],
		"no price filter: the parts have no price yet")
}

func TestShowRejectsUnknownCatalogParts(t *testing.T) {
	model := llm.NewFake(
		llm.Call("c1", toolShowProducts, map[string]any{"catalog_part_ids": []int{78}}),
		llm.Say("Вибачте."),
	)
	f := newAgent(t, model)
	f.agent.UseOrderCatalog(&fakeOrderCatalog{state: "done"})

	reply, err := f.agent.Reply(context.Background(), 1, 1, "колодки")
	require.NoError(t, err)
	assert.Empty(t, reply.CatalogPartIDs)
	assert.True(t, result(model, 1).IsError)
}

func TestSearchWithoutCatalogue(t *testing.T) {
	model := llm.NewFake(
		llm.Call("c1", "search_parts", map[string]any{"vehicle_id": octavia.ID}),
		llm.Say("Ось."),
	)
	f := newAgent(t, model)
	f.agent.UseOrderCatalog(&fakeOrderCatalog{state: ""})
	_, err := f.agent.Reply(context.Background(), 1, 1, "колодки")
	require.NoError(t, err)
	assert.NotContains(t, result(model, 1).Content, "catalog_parts", "a car from the shop's own list has no catalogue")
}

type fakeFits struct{}

func (fakeFits) ProductFits(_ context.Context, id int64) (*enrich.Fits, error) {
	if id != frontPads.ID {
		return nil, nil
	}
	return &enrich.Fits{Lines: []string{"Daewoo Nubira (J100) 1.6 (1997–1999)"}, Complete: false}, nil
}

func TestGetPartCarriesCatalogueFits(t *testing.T) {
	model := llm.NewFake(
		llm.Call("c1", "get_part", map[string]any{"product_id": frontPads.ID}),
		llm.Say("Так, за каталогом підходить до Nubira."),
	)
	f := newAgent(t, model)
	f.agent.UseCatalogFits(fakeFits{})
	_, err := f.agent.Reply(context.Background(), 1, 1, "а на нубіру підійде?")
	require.NoError(t, err)
	res := result(model, 1).Content
	assert.Contains(t, res, `"catalog_fits":["Daewoo Nubira (J100) 1.6 (1997–1999)"]`)
	assert.Contains(t, res, `"catalog_fits_complete":false`, "the model is told the list may be cut")
}

// treeCatalog is the catalogue with a tree the owner edits.
type treeCatalog struct {
	fakeCatalog
	cats []domain.Category
}

func (c *treeCatalog) Categories(context.Context) ([]domain.Category, error) { return c.cats, nil }

func TestToolsFollowTheCategoryTree(t *testing.T) {
	catalog := &treeCatalog{cats: []domain.Category{{ID: 1, Slug: "brakes", Title: "Гальма"}}}
	model := llm.NewFake(llm.Say("1"), llm.Say("2"), llm.Say("3"))
	a, err := New(context.Background(), model, catalog, &fakeCart{}, &fakeGarage{saved: map[int64]int64{}}, fakeKits{},
		&fakeOrders{}, newFakeHistory(), Config{MaxSteps: 3, MaxHistory: 10, Timeout: time.Second},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	ctx := context.Background()
	schema := func(n int) string {
		for _, tool := range model.Requests[n].Tools {
			if tool.Name == "search_parts" {
				return tool.Description
			}
		}
		return ""
	}

	_, err = a.Reply(ctx, 1, 1, "колодки")
	require.NoError(t, err)
	_, err = a.Reply(ctx, 1, 1, "ще")
	require.NoError(t, err)
	assert.Equal(t, schema(0), schema(1), "an unchanged tree keeps the schema byte for byte, for the cache")

	catalog.cats = append(catalog.cats, domain.Category{ID: 2, Slug: "tiuninh", Title: "Тюнінг"})
	_, err = a.Reply(ctx, 1, 1, "спойлер")
	require.NoError(t, err)
	assert.Contains(t, schema(2), "tiuninh (Тюнінг)", "a category the owner added is searchable at once")
}

type pickupOrders struct{}

func (pickupOrders) OrdersOf(context.Context, int64, int) ([]domain.Order, error) {
	return []domain.Order{{ID: 9, Status: domain.OrderConfirmed, Total: domain.UAH(900),
		Contacts: domain.Contacts{Pickup: true, Delivery: "Київ, вул. Автозаводська 1"}}}, nil
}

func TestPickupOrderIsReadyToCollect(t *testing.T) {
	model := llm.NewFake(llm.Call("c1", "order_status", map[string]any{}), llm.Say("Готове."))
	a, err := New(context.Background(), model, &fakeCatalog{}, &fakeCart{}, &fakeGarage{saved: map[int64]int64{}}, fakeKits{},
		pickupOrders{}, newFakeHistory(), Config{MaxSteps: 3, MaxHistory: 10, Timeout: time.Second},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	_, err = a.Reply(context.Background(), 1, 1, "де моє замовлення?")
	require.NoError(t, err)
	assert.Contains(t, result(model, 1).Content, "ready to collect at the shop, at the address", "not «being prepared for shipping»")
}

type fakeLoader struct{ watched [][2]int64 }

func (l *fakeLoader) Watch(_ context.Context, user, vehicle int64) error {
	l.watched = append(l.watched, [2]int64{user, vehicle})
	return nil
}

func TestSearchLoadsAnUnloadedCatalogue(t *testing.T) {
	for _, state := range []string{"none", "queued", "running", "failed"} {
		t.Run(state, func(t *testing.T) {
			model := llm.NewFake(
				llm.Call("c1", "search_parts", map[string]any{"vehicle_id": octavia.ID, "query": "рейк"}),
				llm.Say("Каталог ще завантажується, я напишу, щойно він буде готовий."),
			)
			f := newAgent(t, model)
			f.agent.UseOrderCatalog(&fakeOrderCatalog{state: state})
			loader := &fakeLoader{}
			f.agent.UseCatalogLoader(loader)

			_, err := f.agent.Reply(context.Background(), 7, 7, "рульова рейка")
			require.NoError(t, err)
			assert.Equal(t, [][2]int64{{7, octavia.ID}}, loader.watched, "queued again if it must be, and the buyer waits")
			assert.Contains(t, result(model, 1).Content, "The bot will message the buyer by itself",
				"the model promises the message the bot will send")
		})
	}
}

func TestSearchDoesNotReloadALoadedCatalogue(t *testing.T) {
	model := llm.NewFake(
		llm.Call("c1", "search_parts", map[string]any{"vehicle_id": octavia.ID}),
		llm.Say("Ось."),
	)
	f := newAgent(t, model)
	f.agent.UseOrderCatalog(&fakeOrderCatalog{state: "done"})
	loader := &fakeLoader{}
	f.agent.UseCatalogLoader(loader)
	_, err := f.agent.Reply(context.Background(), 7, 7, "колодки")
	require.NoError(t, err)
	assert.Empty(t, loader.watched)
	assert.NotContains(t, result(model, 1).Content, "catalog_note")
}
