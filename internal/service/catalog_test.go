package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
)

func newCatalogFixture(products int) *CatalogService {
	f := &fakeCatalog{categories: []domain.Category{
		{ID: 1, Slug: "jackets", Title: "Куртки"},
		{ID: 2, Slug: "empty", Title: "Порожня"},
	}}
	for i := range products {
		f.products = append(f.products, domain.Product{ID: int64(i + 1), CategoryID: 1})
	}
	return NewCatalogService(f)
}

func TestCategoryPagePagination(t *testing.T) {
	svc := newCatalogFixture(PageSize*2 + 3)
	ctx := context.Background()

	page, err := svc.CategoryPage(ctx, "jackets", 0)
	require.NoError(t, err)
	assert.Equal(t, 3, page.Pages)
	assert.Len(t, page.Products, PageSize)

	page, err = svc.CategoryPage(ctx, "jackets", 2)
	require.NoError(t, err)
	assert.Len(t, page.Products, 3, "the last page holds the remainder")
}

func TestCategoryPageClampsStalePage(t *testing.T) {
	svc := newCatalogFixture(PageSize + 1)

	page, err := svc.CategoryPage(context.Background(), "jackets", 7)
	require.NoError(t, err)
	assert.Equal(t, 1, page.Page, "a page past the end shows the last one")
	assert.Len(t, page.Products, 1)

	page, err = svc.CategoryPage(context.Background(), "jackets", -1)
	require.NoError(t, err)
	assert.Equal(t, 0, page.Page)
}

func TestCategoryPageEmptyCategory(t *testing.T) {
	svc := newCatalogFixture(3)

	page, err := svc.CategoryPage(context.Background(), "empty", 0)
	require.NoError(t, err)
	assert.Equal(t, 1, page.Pages, "an empty category still has one page")
	assert.Empty(t, page.Products)
}

func TestCategoryPageUnknownSlug(t *testing.T) {
	svc := newCatalogFixture(1)
	_, err := svc.CategoryPage(context.Background(), "nope", 0)
	require.ErrorIs(t, err, domain.ErrNotFound)
}
