package agent

import (
	"context"
	"fmt"
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

func (f *fakeOrderCatalog) Brands(context.Context, service.SearchFilter, int) ([]domain.PartKind, error) {
	return []domain.PartKind{{Name: "BREMBO", Count: 9}, {Name: "TRW", Count: 5}}, nil
}

func (f *fakeOrderCatalog) Kinds(context.Context, service.SearchFilter, int) ([]domain.PartKind, int, error) {
	return []domain.PartKind{{Name: bremboPads.Name, Count: 14}, {Name: "Пильовик амортизатора", Count: 3}}, 17, nil
}

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
	assert.Contains(t, res, `"catalog_kinds":[{"name":"Гальмівні колодки","count":14},{"name":"Пильовик амортизатора","count":3}]`,
		"what kinds there are, so a sample is never taken for all")
	assert.Contains(t, res, `"catalog_parts_total":17`)
	assert.Equal(t, service.SearchFilter{VehicleID: octavia.ID, Query: "колодк", Limit: maxCatalogShown}, parts.filters[0],
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

type fakeLoader struct {
	watched [][2]int64
	fetched []string
}

func (l *fakeLoader) FetchNow(_ context.Context, _ int64, category, query string) {
	l.fetched = append(l.fetched, category+"|"+query)
}

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
			assert.Equal(t, []string{"|рейк"}, loader.fetched, "the sections the search names are loaded now")
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
	assert.Empty(t, loader.fetched, "a loaded catalogue has every section")
	assert.NotContains(t, result(model, 1).Content, "catalog_note")
}

func TestSearchWithoutACarOffersTheSavedCarsParts(t *testing.T) {
	model := llm.NewFake(
		llm.Call("c1", "search_parts", map[string]any{"category": "brake-discs"}),
		llm.Say("Є під замовлення."),
	)
	f := newAgent(t, model)
	f.agent.UseOrderCatalog(&fakeOrderCatalog{state: "done"})
	f.garage.saved[1] = octavia.ID

	_, err := f.agent.Reply(context.Background(), 1, 1, "гальмівні диски")
	require.NoError(t, err)
	res := result(model, 1).Content
	assert.Contains(t, res, `"catalog_parts":[`, "a search in general still offers the parts on order of the buyer's car")
	assert.Contains(t, res, fmt.Sprintf(`"catalog_parts_vehicle_id":%d`, octavia.ID), "and says whose they are")
}

func TestOtherOilGrade(t *testing.T) {
	oil := func(grade string) *domain.Product {
		return &domain.Product{PartType: domain.PartEngineOil, Specs: []domain.Spec{{Name: "В'язкість", Value: grade}}}
	}
	assert.True(t, otherOilGrade(oil("5W-30"), "0W-20", "Яке моторне масло мені взяти?"), "the Camry takes 0W-20")
	assert.False(t, otherOilGrade(oil("0W-20"), "0W-20", "Яке масло?"))
	assert.False(t, otherOilGrade(oil("0w20"), "0W-20", ""), "however it is spelt")
	assert.False(t, otherOilGrade(oil("5W-30"), "0W-20", "а 5w 30 є?"), "the buyer asked for that grade by name")
	assert.False(t, otherOilGrade(oil("5W-30"), "", "Яке масло?"), "a car of unknown grade: no rule")
	assert.False(t, otherOilGrade(&frontPads, "0W-20", ""), "only oil has a grade")
}

func TestFindVehicleListsTheMakesModelsWhenNoneMatches(t *testing.T) {
	model := llm.NewFake(
		llm.Call("c1", "find_vehicle", map[string]any{"make": "Skoda", "model": "A7"}),
		llm.Say("Уточніть модель."),
	)
	f := newAgent(t, model)
	f.garage.noMatch = true
	_, err := f.agent.Reply(context.Background(), 1, 1, "шкода а7")
	require.NoError(t, err)
	res := result(model, 1).Content
	assert.Contains(t, res, `"models_of_make":[{"name":"Octavia A7","years":"2013–2020"}`,
		"the catalogue's names, to call again with the exact one")
	assert.NotContains(t, res, "similar_makes", "the make is known")
}

type fakeHandoff struct{ reasons []string }

func (f *fakeHandoff) CallManager(_ context.Context, _ int64, reason string) (bool, error) {
	f.reasons = append(f.reasons, reason)
	return len(f.reasons) == 1, nil // the second call comes too soon
}

func TestCallManager(t *testing.T) {
	model := llm.NewFake(
		llm.Call("c1", "call_manager", map[string]any{"reason": "Повернення: колодки не підійшли"}),
		llm.Call("c2", "call_manager", map[string]any{"reason": "Ще раз"}),
		llm.Say("Менеджер напише вам у цей чат."),
	)
	f := newAgent(t, model)
	h := &fakeHandoff{}
	f.agent.UseHandoff(h)
	_, err := f.agent.Reply(context.Background(), 1, 1, "хочу повернути колодки, дайте менеджера")
	require.NoError(t, err)
	assert.Equal(t, []string{"Повернення: колодки не підійшли", "Ще раз"}, h.reasons)
	assert.Contains(t, result(model, 1).Content, `"status":"called"`)
	assert.Contains(t, result(model, 2).Content, `"status":"already_called"`, "the managers are not rung twice in a row")
}

func TestCallManagerWithoutManagers(t *testing.T) {
	model := llm.NewFake(
		llm.Call("c1", "call_manager", map[string]any{"reason": "Опт"}),
		llm.Say("Зараз не можу з'єднати."),
	)
	f := newAgent(t, model)
	_, err := f.agent.Reply(context.Background(), 1, 1, "дайте менеджера")
	require.NoError(t, err)
	assert.Contains(t, result(model, 1).Content, `"status":"unavailable"`)
}

type fakeArticles struct{}

func (fakeArticles) Article(_ context.Context, supplierID int64, number string) (*enrich.Article, error) {
	return &enrich.Article{SupplierID: supplierID, Number: number,
		Specs:   []domain.Spec{{Name: "Діаметр", Value: "280 мм"}, {Name: "Тип", Value: "вентильований"}},
		OEMKeys: []string{"1K0615301AA"}}, nil
}

func TestGetCatalogPartsReadsTheirSpecs(t *testing.T) {
	model := llm.NewFake(
		llm.Call("c1", "get_catalog_parts", map[string]any{"catalog_part_ids": []int{77, 78}}),
		llm.Say("Brembo — вентильований, 280 мм."),
	)
	f := newAgent(t, model)
	f.agent.UseOrderCatalog(&fakeOrderCatalog{state: "done"})
	f.agent.UseCatalogArticles(fakeArticles{})

	_, err := f.agent.Reply(context.Background(), 1, 1, "в чому різниця?")
	require.NoError(t, err)
	res := result(model, 1).Content
	assert.Contains(t, res, `"specs":["Діаметр: 280 мм","Тип: вентильований"]`, "the specs to tell them apart")
	assert.Contains(t, res, `"oem_numbers":["1K0615301AA"]`)
	assert.Contains(t, res, `"catalog_part_id":78,"name":"","brand":"","number":"","note":"no such catalogue part`,
		"an unknown id is said to be one, not a failed answer")
}

func TestRefusedCardsAreNotSaidTwice(t *testing.T) {
	// The first step writes its answer with a call that is refused: an id
	// that does not exist.
	first := llm.Call("c1", toolShowProducts, map[string]any{"catalog_part_ids": []int{77, 999}})
	first.Response.Message.Text = "Перша версія відповіді."
	model := llm.NewFake(
		first,
		llm.Call("c2", toolShowProducts, map[string]any{"catalog_part_ids": []int{77}}),
		llm.Say("Друга версія відповіді."),
	)
	f := newAgent(t, model)
	f.agent.UseOrderCatalog(&fakeOrderCatalog{state: "done"})

	reply, err := f.agent.Reply(context.Background(), 1, 1, "диски")
	require.NoError(t, err)
	assert.Equal(t, "Друга версія відповіді.", reply.Text, "the answer written with refused cards is not said again")
}
