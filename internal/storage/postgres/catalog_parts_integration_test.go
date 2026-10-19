package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aristodem96/ai_shop_bot/internal/autodb"
	"github.com/aristodem96/ai_shop_bot/internal/crawler"
	"github.com/aristodem96/ai_shop_bot/internal/directory"
	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/partnames"
	"github.com/aristodem96/ai_shop_bot/internal/service"
)

func TestCatalogParts(t *testing.T) {
	skipShort(t)
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM products WHERE source = 'cat-test'`)
		_, _ = testPool.Exec(ctx, `DELETE FROM autodb_mod_parts`)
		_, _ = testPool.Exec(ctx, `DELETE FROM autodb_part_names`)
		_, _ = testPool.Exec(ctx, `DELETE FROM autodb_crawls`)
		_, _ = testPool.Exec(ctx, `DELETE FROM vehicles WHERE autodb_mod_id IS NOT NULL`)
	})
	mod := autodb.Modification{ID: 18537, Name: "CHEVROLET LACETTI (J200) 1.6",
		Attrs: map[string][]string{"ConstructionInterval": {"03.2005 -"}}}
	vehicleID, err := NewDirectoryRepository(testPool).UpsertVehicle(ctx,
		directory.ToVehicle("CHEVROLET", autodb.Model{Name: "LACETTI (J200)"}, mod), mod.ID, 0)
	require.NoError(t, err)

	repo := NewCatalogPartRepository(testPool)
	state, err := repo.CrawlState(ctx, vehicleID)
	require.NoError(t, err)
	assert.Equal(t, "none", state)
	state, err = repo.CrawlState(ctx, vehicleByCode(t, "lanos-15"))
	require.NoError(t, err)
	assert.Empty(t, state, "a car from the shop's own list has no catalogue")

	crawls := NewCrawlRepository(testPool)
	pads := []crawler.Part{
		{SupplierID: 65, ArticleKey: "S59513", Number: "S 59 513", Brand: "BREMBO", BrandKey: "BREMBO", Name: "Комплект тормозных колодок"},
		{SupplierID: 89, ArticleKey: "LP1622", Number: "LP1622", Brand: "DELPHI", BrandKey: "DELPHI", Name: "Комплект тормозных колодок"},
	}
	require.NoError(t, crawls.SaveParts(ctx, mod.ID, 100031, pads))
	require.NoError(t, crawls.SaveParts(ctx, mod.ID, 100032, pads[:1]), "the same part in a second section")
	require.NoError(t, crawls.SaveParts(ctx, mod.ID, 100259, []crawler.Part{
		{SupplierID: 4, ArticleKey: "W71295", Number: "W 712/95", Brand: "MANN-FILTER", BrandKey: "MANNFILTER", Name: "Масляный фильтр"},
	}))
	_, err = testPool.Exec(ctx, `
		INSERT INTO products (sku, category_id, name, brand, price, stock, source, external_id, part_key)
		VALUES ('T-DELPHI', (SELECT id FROM categories WHERE slug = 'brake-pads'), 'Колодки', 'Delphi', 100000, 2, 'cat-test', 'T-DELPHI', 'LP1622')`)
	require.NoError(t, err)

	names, err := repo.Untranslated(ctx, 10)
	require.NoError(t, err)
	assert.Equal(t, []string{"Комплект тормозных колодок", "Масляный фильтр"}, names)
	require.NoError(t, repo.SaveNames(ctx, []partnames.Name{{Name: "Комплект тормозных колодок", NameUK: "Гальмівні колодки", Category: "brake-pads"}}))
	names, _ = repo.Untranslated(ctx, 10)
	assert.Equal(t, []string{"Масляный фильтр"}, names)

	all, err := repo.ForVehicle(ctx, service.SearchFilter{VehicleID: vehicleID, Limit: 10})
	require.NoError(t, err)
	require.Len(t, all, 2, "the stocked Delphi is not offered on order, the Brembo comes once")
	assert.Equal(t, domain.CatalogPart{ID: all[0].ID, Brand: "BREMBO", SupplierID: 65, Number: "S 59 513", Name: "Гальмівні колодки", Category: "brake-pads"}, all[0],
		"translated and filed first")
	assert.Equal(t, "Масляный фильтр", all[1].Name, "untranslated yet, still offered")

	for _, f := range []service.SearchFilter{
		{Category: "brakes"}, {Category: "brake-pads"}, {Query: "колодк"}, {Query: "тормозн колодок"}, {PartNumber: "s59-513"}, {Brand: "Brembo"},
	} {
		f.VehicleID, f.Limit = vehicleID, 10
		got, err := repo.ForVehicle(ctx, f)
		require.NoError(t, err)
		require.Len(t, got, 1, "%+v", f)
		assert.Equal(t, "BREMBO", got[0].Brand, "%+v", f)
	}

	one, err := repo.CatalogPart(ctx, all[0].ID)
	require.NoError(t, err)
	assert.Equal(t, all[0], *one)
	_, err = repo.CatalogPart(ctx, -1)
	require.ErrorIs(t, err, domain.ErrNotFound)

	prog, err := repo.Progress(ctx, vehicleID)
	require.NoError(t, err)
	assert.Equal(t, &domain.CatalogProgress{State: "none", Parts: 3, Filed: 2}, prog,
		"three parts, the Brembo once; the two pads are filed, the filter is not")
	prog, err = repo.Progress(ctx, vehicleByCode(t, "lanos-15"))
	require.NoError(t, err)
	assert.Nil(t, prog, "a car from the shop's own list has no catalogue")

	// The menu: only filed parts, once each, not those the shop stocks.
	padsID, brakesID := categoryBySlug(t, "brake-pads"), categoryBySlug(t, "brakes")
	counts, err := repo.CategoryCounts(ctx, vehicleID)
	require.NoError(t, err)
	assert.Equal(t, map[int64]int{padsID: 1}, counts, "the oil filter has no category yet, the Delphi is in stock")

	for _, cat := range []int64{padsID, brakesID} {
		page, total, err := repo.ByCategory(ctx, cat, vehicleID, 8, 0)
		require.NoError(t, err)
		assert.Equal(t, 1, total)
		require.Len(t, page, 1, "a section holds its subcategories' parts")
		assert.Equal(t, all[0], page[0])
	}
	page, total, err := repo.ByCategory(ctx, padsID, vehicleID, 8, 8)
	require.NoError(t, err)
	assert.Empty(t, page)
	assert.Equal(t, 1, total, "past the end, still counted for paging back")
}
