package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
)

func newCatalogFixture(products int) (*CatalogService, *fakeCatalog) {
	f := &fakeCatalog{categories: []domain.Category{
		{ID: 1, Slug: "brakes", Title: "Гальмівна система"},
		{ID: 2, Slug: "empty", Title: "Порожня"},
	}}
	for i := range products {
		f.products = append(f.products, domain.Product{ID: int64(i + 1), CategoryID: 1, Universal: true})
	}
	return NewCatalogService(f), f
}

func TestCategoryPagePagination(t *testing.T) {
	svc, _ := newCatalogFixture(PageSize*2 + 3)
	ctx := context.Background()

	page, err := svc.CategoryPage(ctx, 1, nil, 0)
	require.NoError(t, err)
	assert.Equal(t, 3, page.Pages)
	assert.Len(t, page.Products, PageSize)

	page, err = svc.CategoryPage(ctx, 1, nil, 2)
	require.NoError(t, err)
	assert.Len(t, page.Products, 3, "the last page holds the remainder")
}

func TestCategoryPageClampsStalePage(t *testing.T) {
	svc, _ := newCatalogFixture(PageSize + 1)

	page, err := svc.CategoryPage(context.Background(), 1, nil, 7)
	require.NoError(t, err)
	assert.Equal(t, 1, page.Page, "a page past the end shows the last one")
	assert.Len(t, page.Products, 1)

	page, err = svc.CategoryPage(context.Background(), 1, nil, -1)
	require.NoError(t, err)
	assert.Equal(t, 0, page.Page)
}

func TestCategoryPageFiltersByVehicle(t *testing.T) {
	svc, f := newCatalogFixture(0)
	f.products = []domain.Product{
		{ID: 1, CategoryID: 1, Fits: []domain.Vehicle{{ID: 7}}},
		{ID: 2, CategoryID: 1, Fits: []domain.Vehicle{{ID: 8}}},
	}
	car := &domain.Vehicle{ID: 7}

	page, err := svc.CategoryPage(context.Background(), 1, car, 0)
	require.NoError(t, err)
	require.Len(t, page.Products, 1)
	assert.Equal(t, int64(1), page.Products[0].ID)
	assert.Same(t, car, page.Vehicle, "the page says which car it is for")
	assert.Equal(t, []int64{7}, f.pageVehicles)
}

func TestCategoryPageEmptyCategory(t *testing.T) {
	svc, _ := newCatalogFixture(3)

	page, err := svc.CategoryPage(context.Background(), 2, nil, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, page.Pages, "an empty category still has one page")
	assert.Empty(t, page.Products)
}

func TestCategoryPageUnknownCategory(t *testing.T) {
	svc, _ := newCatalogFixture(1)
	_, err := svc.CategoryPage(context.Background(), 99, nil, 0)
	require.ErrorIs(t, err, domain.ErrNotFound)
}
