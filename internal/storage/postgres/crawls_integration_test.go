package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aristodem96/ai_shop_bot/internal/autodb"
	"github.com/aristodem96/ai_shop_bot/internal/crawler"
	"github.com/aristodem96/ai_shop_bot/internal/directory"
	"github.com/aristodem96/ai_shop_bot/internal/service"
)

func TestAutoDBFitments(t *testing.T) {
	skipShort(t)
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM products WHERE source = 'fit-test'`)
		_, _ = testPool.Exec(ctx, `DELETE FROM autodb_mod_parts`)
		_, _ = testPool.Exec(ctx, `DELETE FROM autodb_crawls`)
		_, _ = testPool.Exec(ctx, `DELETE FROM vehicles WHERE autodb_mod_id IS NOT NULL`)
		_, _ = testPool.Exec(ctx, `DELETE FROM brands WHERE name IN ('MANN', 'BREMBO')`)
	})

	// A Lacetti 1.6 a buyer chose from Auto-DB.
	model := autodb.Model{ID: 5403, Name: "LACETTI (J200)"}
	mod := autodb.Modification{ID: 18537, Name: "CHEVROLET LACETTI (J200) 1.6",
		Attrs: map[string][]string{"ConstructionInterval": {"03.2005 -"}, "Capacity": {"1.6 l"}, "FuelType": {"бензин"}}}
	vehicleID, err := NewDirectoryRepository(testPool).UpsertVehicle(ctx, directory.ToVehicle("CHEVROLET", model, mod), mod.ID, 0)
	require.NoError(t, err)

	// What Auto-DB lists for it.
	crawls := NewCrawlRepository(testPool)
	require.NoError(t, crawls.SaveParts(ctx, mod.ID, 100031, []crawler.Part{
		{SupplierID: 65, ArticleKey: "S59513", Number: "S 59 513", Brand: "BREMBO", BrandKey: "BREMBO"},
		{SupplierID: 4, ArticleKey: "W71295", Number: "W 712/95", Brand: "MANN-FILTER", BrandKey: "MANNFILTER"},
		{SupplierID: 89, ArticleKey: "LS1622", Number: "LS1622", Brand: "DELPHI", BrandKey: "DELPHI"},
	}))

	// The shop's stock, from price lists.
	product := func(sku, brand, partKey string) int64 {
		var id int64
		require.NoError(t, testPool.QueryRow(ctx, `
			INSERT INTO products (sku, category_id, name, brand, price, stock, source, external_id, part_key)
			VALUES ($1, (SELECT id FROM categories WHERE slug = 'brake-pads'), $1, $2, 100000, 3, 'fit-test', $1, $3)
			RETURNING id`, sku, brand, partKey).Scan(&id))
		return id
	}
	brembo := product("T-BREMBO", "Brembo", "S59513")          // same brand, written otherwise
	mann := product("T-MANN", "MANN", "W71295")                // a brand the catalogue spells MANN-FILTER
	noBrand := product("T-NOBRAND", "", "LS1622")              // a price list with no brand
	otherBrand := product("T-OTHER", "Ferodo", "LS1622")       // the same number from another brand
	elsewhere := product("T-ELSEWHERE", "BOSCH", "0986494000") // not for this car

	_, err = testPool.Exec(ctx, `INSERT INTO brands (name) VALUES ('MANN') ON CONFLICT DO NOTHING`)
	require.NoError(t, err)
	_, err = testPool.Exec(ctx, `
		INSERT INTO brand_aliases (alias, brand_id) SELECT 'MANNFILTER', id FROM brands WHERE name = 'MANN'
		ON CONFLICT DO NOTHING`)
	require.NoError(t, err)

	found, err := NewCatalogRepository(testPool).Search(ctx, service.SearchFilter{VehicleID: vehicleID, Limit: 20})
	require.NoError(t, err)
	got := map[int64]bool{}
	for _, p := range found {
		got[p.ID] = true
	}
	assert.True(t, got[brembo], "same brand in other letters")
	assert.True(t, got[mann], "a brand alias joins MANN-FILTER to MANN")
	assert.True(t, got[noBrand], "a price list with no brand matches by the article alone")
	assert.False(t, got[otherBrand], "the same number from another brand is another part")
	assert.False(t, got[elsewhere], "a part the catalogue does not list for the car")

	counts, err := NewCatalogRepository(testPool).CategoryCounts(ctx, vehicleID)
	require.NoError(t, err)
	assert.Equal(t, 3, counts[categoryBySlug(t, "brake-pads")], "the menu counts what the catalogue says fits")
}

func TestCrawlQueue(t *testing.T) {
	skipShort(t)
	ctx := context.Background()
	repo := NewCrawlRepository(testPool)
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM autodb_crawls`)
		_, _ = testPool.Exec(ctx, `DELETE FROM vehicles WHERE autodb_mod_id IS NOT NULL`)
	})
	vehicleID, err := NewDirectoryRepository(testPool).UpsertVehicle(ctx,
		directory.ToVehicle("VW", autodb.Model{Name: "GOLF V (1K1)"}, autodb.Modification{ID: 9001, Name: "VW GOLF V (1K1) 1.6",
			Attrs: map[string][]string{"ConstructionInterval": {"10.2003 - 02.2009"}}}), 9001, 0)
	require.NoError(t, err)

	require.NoError(t, repo.Enqueue(ctx, vehicleID, time.Hour))
	require.NoError(t, repo.Enqueue(ctx, vehicleID, time.Hour), "a car chosen twice is queued once")
	require.NoError(t, repo.Enqueue(ctx, vehicleByCode(t, "lanos-15"), time.Hour), "a car not from Auto-DB is no crawl")

	s, err := repo.Status(ctx, vehicleID)
	require.NoError(t, err)
	assert.Equal(t, "queued", s.State)

	modID, ok, err := repo.Claim(ctx)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, int64(9001), modID)
	_, ok, err = repo.Claim(ctx)
	require.NoError(t, err)
	assert.False(t, ok, "only one crawl was queued")

	require.NoError(t, repo.Progress(ctx, modID, 300, 120, 2400))
	s, _ = repo.Status(ctx, vehicleID)
	assert.Equal(t, crawler.Status{State: "running", SectionsTotal: 300, SectionsDone: 120, Parts: 2400}, s)

	require.NoError(t, repo.Finish(ctx, modID, nil))
	s, _ = repo.Status(ctx, vehicleID)
	assert.True(t, s.Done())
	require.NoError(t, repo.Enqueue(ctx, vehicleID, time.Hour))
	_, ok, _ = repo.Claim(ctx)
	assert.False(t, ok, "a fresh crawl is not repeated")
}
