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

	page, err := svc.CategoryPage(ctx, 1, nil, 0, false)
	require.NoError(t, err)
	assert.Equal(t, 3, page.Pages)
	assert.Len(t, page.Products, PageSize)

	page, err = svc.CategoryPage(ctx, 1, nil, 2, false)
	require.NoError(t, err)
	assert.Len(t, page.Products, 3, "the last page holds the remainder")
}

func TestCategoryPageClampsStalePage(t *testing.T) {
	svc, _ := newCatalogFixture(PageSize + 1)

	page, err := svc.CategoryPage(context.Background(), 1, nil, 7, false)
	require.NoError(t, err)
	assert.Equal(t, 1, page.Page, "a page past the end shows the last one")
	assert.Len(t, page.Products, 1)

	page, err = svc.CategoryPage(context.Background(), 1, nil, -1, false)
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

	page, err := svc.CategoryPage(context.Background(), 1, car, 0, false)
	require.NoError(t, err)
	require.Len(t, page.Products, 1)
	assert.Equal(t, int64(1), page.Products[0].ID)
	assert.Same(t, car, page.Vehicle, "the page says which car it is for")
	assert.Equal(t, []int64{7}, f.pageVehicles)
}

func TestCategoryPageEmptyCategory(t *testing.T) {
	svc, _ := newCatalogFixture(3)

	page, err := svc.CategoryPage(context.Background(), 2, nil, 0, false)
	require.NoError(t, err)
	assert.Equal(t, 1, page.Pages, "an empty category still has one page")
	assert.Empty(t, page.Products)
}

func TestCategoryPageUnknownCategory(t *testing.T) {
	svc, _ := newCatalogFixture(1)
	_, err := svc.CategoryPage(context.Background(), 99, nil, 0, false)
	require.ErrorIs(t, err, domain.ErrNotFound)
}

// A section of the tree: brakes with pads and discs, and an empty drums.
func newTreeFixture() (*CatalogService, *fakeCatalog) {
	f := &fakeCatalog{categories: []domain.Category{
		{ID: 1, Slug: "brakes", Title: "Гальмівна система"},
		{ID: 11, Slug: "brake-pads", Title: "Колодки", ParentID: 1},
		{ID: 12, Slug: "brake-discs", Title: "Диски", ParentID: 1},
		{ID: 13, Slug: "brake-drums", Title: "Барабани", ParentID: 1},
		{ID: 2, Slug: "body", Title: "Кузов"},
		{ID: 3, Slug: "other", Title: "Інше"},
	}}
	octavia := []domain.Vehicle{{ID: 7}}
	f.products = []domain.Product{
		{ID: 1, CategoryID: 11, Fits: octavia},
		{ID: 2, CategoryID: 11},
		{ID: 3, CategoryID: 12, Fits: octavia},
		{ID: 4, CategoryID: 3, Universal: true},
	}
	return NewCatalogService(f), f
}

func TestMenuCountsSectionsAndHidesEmpty(t *testing.T) {
	svc, _ := newTreeFixture()
	ctx := context.Background()

	menu, err := svc.Menu(ctx, nil)
	require.NoError(t, err)
	require.Len(t, menu, 2, "an empty section is not a menu entry")
	assert.Equal(t, "brakes", menu[0].Category.Slug)
	assert.Equal(t, 3, menu[0].Count, "a section counts its subcategories")
	assert.Equal(t, "other", menu[1].Category.Slug)

	menu, err = svc.Menu(ctx, &domain.Vehicle{ID: 7})
	require.NoError(t, err)
	assert.Equal(t, 2, menu[0].Count, "with a car, only what fits it")
}

func TestSectionOpensAsItsSubcategories(t *testing.T) {
	svc, _ := newTreeFixture()
	ctx := context.Background()

	page, err := svc.CategoryPage(ctx, 1, nil, 0, false)
	require.NoError(t, err)
	assert.Empty(t, page.Products)
	require.Len(t, page.Children, 2, "drums are empty and hidden")
	assert.Equal(t, CategoryEntry{Category: domain.Category{ID: 11, Slug: "brake-pads", Title: "Колодки", ParentID: 1}, Count: 2}, page.Children[0])
	assert.Equal(t, 3, page.Total)

	page, err = svc.CategoryPage(ctx, 1, nil, 0, true)
	require.NoError(t, err)
	assert.Len(t, page.Products, 3, "the whole section at once")
	assert.True(t, page.All)

	page, err = svc.CategoryPage(ctx, 11, nil, 0, false)
	require.NoError(t, err)
	assert.Len(t, page.Products, 2)
	require.NotNil(t, page.Parent, "a subcategory knows its section, for the back button")
	assert.Equal(t, "brakes", page.Parent.Slug)
}

func TestSectionWithEmptySubcategoriesShowsProducts(t *testing.T) {
	svc, f := newTreeFixture()
	f.products = []domain.Product{{ID: 9, CategoryID: 1}} // filed in the section itself

	page, err := svc.CategoryPage(context.Background(), 1, nil, 0, false)
	require.NoError(t, err)
	assert.Empty(t, page.Children)
	require.Len(t, page.Products, 1, "not an empty menu")
	assert.True(t, page.All, "paging stays on the whole section")
}

// fakeOrderParts are parts on order, all for vehicle 7 in category 1.
type fakeOrderParts struct{ n int }

func (f fakeOrderParts) CategoryCounts(_ context.Context, vehicleID int64) (map[int64]int, error) {
	if vehicleID != 7 {
		return map[int64]int{}, nil
	}
	return map[int64]int{1: f.n}, nil
}

func (f fakeOrderParts) ByCategory(_ context.Context, categoryID, vehicleID int64, limit, offset int) ([]domain.CatalogPart, int, error) {
	if vehicleID != 7 || categoryID != 1 {
		return nil, 0, nil
	}
	var out []domain.CatalogPart
	for i := offset; i < min(offset+limit, f.n); i++ {
		out = append(out, domain.CatalogPart{ID: int64(100 + i)})
	}
	return out, f.n, nil
}

func TestCategoryPageListsPartsOnOrderAfterTheStock(t *testing.T) {
	svc, f := newCatalogFixture(0)
	for i := range PageSize + 3 {
		f.products = append(f.products, domain.Product{ID: int64(i + 1), CategoryID: 1, Fits: []domain.Vehicle{{ID: 7}}})
	}
	svc.UseOrderParts(fakeOrderParts{n: 10})
	car := &domain.Vehicle{ID: 7}
	ctx := context.Background()

	menu, err := svc.Menu(ctx, car)
	require.NoError(t, err)
	require.Len(t, menu, 1)
	assert.Equal(t, PageSize+3+10, menu[0].Count, "the menu counts the stock and the parts on order")

	first, err := svc.CategoryPage(ctx, 1, car, 0, false)
	require.NoError(t, err)
	assert.Len(t, first.Products, PageSize)
	assert.Empty(t, first.OnOrder, "the stock comes first")
	assert.Equal(t, 3, first.Pages, "21 parts, 8 a page")
	assert.Equal(t, PageSize+13, first.Total)

	second, err := svc.CategoryPage(ctx, 1, car, 1, false)
	require.NoError(t, err)
	assert.Len(t, second.Products, 3, "the end of the stock")
	require.Len(t, second.OnOrder, PageSize-3, "and the parts on order fill the page")
	assert.Equal(t, int64(100), second.OnOrder[0].ID)

	third, err := svc.CategoryPage(ctx, 1, car, 2, false)
	require.NoError(t, err)
	assert.Empty(t, third.Products)
	require.Len(t, third.OnOrder, 5)
	assert.Equal(t, int64(105), third.OnOrder[0].ID, "picking up where the last page left off")

	all, err := svc.CategoryPage(ctx, 1, nil, 0, false)
	require.NoError(t, err)
	assert.Empty(t, all.OnOrder, "without a car there is no catalogue to order from")
}

func TestPartsOnOrderAloneFillAnEmptyShop(t *testing.T) {
	svc, _ := newCatalogFixture(0)
	svc.UseOrderParts(fakeOrderParts{n: 3})
	car := &domain.Vehicle{ID: 7}

	menu, err := svc.Menu(context.Background(), car)
	require.NoError(t, err)
	require.Len(t, menu, 1, "a shop with nothing on the shelves still has a catalogue for the car")
	page, err := svc.CategoryPage(context.Background(), 1, car, 0, false)
	require.NoError(t, err)
	assert.Len(t, page.OnOrder, 3)
	assert.Equal(t, 1, page.Pages)
}
