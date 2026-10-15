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

func TestSearchRelaxesKeywordsButKeepsFilters(t *testing.T) {
	coat := domain.Product{ID: 1, Name: "Пальто"}
	f := &fakeCatalog{search: func(sf SearchFilter) []domain.Product {
		if sf.Query != "" {
			return nil // no product mentions the buyer's word
		}
		return []domain.Product{coat}
	}}
	svc := NewCatalogService(f)

	res, err := svc.Search(context.Background(), SearchFilter{Query: "кашемір", MaxPrice: domain.UAH(5000), Size: "M"})
	require.NoError(t, err)
	assert.True(t, res.Relaxed)
	assert.Equal(t, []domain.Product{coat}, res.Products)

	require.Len(t, f.filters, 2)
	assert.Empty(t, f.filters[1].Query)
	assert.Equal(t, domain.UAH(5000), f.filters[1].MaxPrice, "the price limit survives relaxation")
	assert.Equal(t, "M", f.filters[1].Size)
}

func TestSearchWithoutKeywordsIsNotRelaxed(t *testing.T) {
	f := &fakeCatalog{search: func(SearchFilter) []domain.Product { return nil }}
	res, err := NewCatalogService(f).Search(context.Background(), SearchFilter{Size: "XXXL"})
	require.NoError(t, err)
	assert.False(t, res.Relaxed)
	assert.Empty(t, res.Products)
	assert.Len(t, f.filters, 1, "nothing to relax, so no second query")
}
