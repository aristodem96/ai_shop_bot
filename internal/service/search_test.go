package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
)

func TestSearchClampsLimit(t *testing.T) {
	f := &fakeCatalog{}
	svc := NewCatalogService(f)
	ctx := context.Background()

	_, err := svc.Search(ctx, SearchFilter{})
	require.NoError(t, err)
	_, err = svc.Search(ctx, SearchFilter{Limit: 500})
	require.NoError(t, err)

	assert.Equal(t, DefaultSearchLimit, f.filters[0].Limit)
	assert.Equal(t, MaxSearchLimit, f.filters[1].Limit, "the model cannot ask for the whole catalogue")
}

func TestSearchNormalizesPartNumber(t *testing.T) {
	f := &fakeCatalog{}
	_, err := NewCatalogService(f).Search(context.Background(), SearchFilter{PartNumber: "1k0-698 151"})
	require.NoError(t, err)
	assert.Equal(t, "1K0698151", f.filters[0].PartNumber)
}

func TestSearchRelaxesKeywordsButKeepsFilters(t *testing.T) {
	pads := domain.Product{ID: 1, Name: "Колодки"}
	f := &fakeCatalog{search: func(sf SearchFilter) []domain.Product {
		if sf.Query != "" {
			return nil // no product mentions the buyer's word
		}
		return []domain.Product{pads}
	}}
	svc := NewCatalogService(f)

	res, err := svc.Search(context.Background(), SearchFilter{Query: "тихі", VehicleID: 7, MaxPrice: domain.UAH(2000)})
	require.NoError(t, err)
	assert.True(t, res.Relaxed)
	assert.Equal(t, []domain.Product{pads}, res.Products)

	require.Len(t, f.filters, 2)
	assert.Empty(t, f.filters[1].Query)
	assert.Equal(t, int64(7), f.filters[1].VehicleID, "the car survives relaxation")
	assert.Equal(t, domain.UAH(2000), f.filters[1].MaxPrice)
}

func TestSearchWithoutKeywordsIsNotRelaxed(t *testing.T) {
	f := &fakeCatalog{search: func(SearchFilter) []domain.Product { return nil }}
	res, err := NewCatalogService(f).Search(context.Background(), SearchFilter{PartNumber: "000"})
	require.NoError(t, err)
	assert.False(t, res.Relaxed)
	assert.Empty(t, res.Products)
	assert.Len(t, f.filters, 1, "nothing to relax, so no second query")
}

func TestPhrases(t *testing.T) {
	assert.Equal(t, []string{"гальмівн диск", "тормозн диск"}, Phrases("Гальмівн диск,  тормозн   диск"))
	assert.Equal(t, []string{"шаров опор", "шарнир", "кульов"}, Phrases("шаров опор; шарнир | кульов ,"))
	assert.Equal(t, []string{"колодк"}, Phrases("колодк"), "one way is a query as before")
	assert.Equal(t, []string{}, Phrases(" , я "), "no way at all")
}
