package agent

import (
	"context"
	"testing"

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
