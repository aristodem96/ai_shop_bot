package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
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

func TestCrawlWaiters(t *testing.T) {
	skipShort(t)
	ctx := context.Background()
	repo := NewCrawlRepository(testPool)
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM autodb_crawls`)
		_, _ = testPool.Exec(ctx, `DELETE FROM vehicles WHERE autodb_mod_id IS NOT NULL`)
	})
	vehicleID, err := NewDirectoryRepository(testPool).UpsertVehicle(ctx,
		directory.ToVehicle("BMW", autodb.Model{Name: "3 (E46)"}, autodb.Modification{ID: 9101, Name: "BMW 3 (E46) 320 i",
			Attrs: map[string][]string{"ConstructionInterval": {"02.2000 - 12.2005"}}}), 9101, 0)
	require.NoError(t, err)

	require.NoError(t, repo.Watch(ctx, 5, vehicleID), "nothing queued yet: nothing to wait for")
	require.NoError(t, repo.Enqueue(ctx, vehicleID, time.Hour))
	require.NoError(t, repo.Watch(ctx, 5, vehicleID))
	require.NoError(t, repo.Watch(ctx, 5, vehicleID), "a buyer asking twice is told once")
	require.NoError(t, repo.Watch(ctx, 6, vehicleID))
	require.NoError(t, repo.Watch(ctx, 5, vehicleByCode(t, "lanos-15")), "a car not from Auto-DB has no catalogue to wait for")

	modID, ok, err := repo.Claim(ctx)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, repo.Finish(ctx, modID, assert.AnError))

	require.NoError(t, repo.RetryFailed(ctx, time.Hour, 24*time.Hour))
	_, ok, _ = repo.Claim(ctx)
	assert.False(t, ok, "failed just now: not yet")
	require.NoError(t, repo.RetryFailed(ctx, 0, 24*time.Hour))
	modID, ok, err = repo.Claim(ctx)
	require.NoError(t, err)
	require.True(t, ok, "failed, and someone waits: queued again")

	require.NoError(t, repo.Finish(ctx, modID, nil))
	require.NoError(t, repo.Watch(ctx, 7, vehicleID), "done: nothing to wait for")
	ws, err := repo.Waiters(ctx, modID)
	require.NoError(t, err)
	assert.ElementsMatch(t, []crawler.Waiter{{UserID: 5, VehicleID: vehicleID}, {UserID: 6, VehicleID: vehicleID}}, ws)
	ws, err = repo.Waiters(ctx, modID)
	require.NoError(t, err)
	assert.Empty(t, ws, "each is told once")

	// A failed crawl nobody waits for stays failed, and old waits are let go.
	require.NoError(t, repo.Finish(ctx, modID, assert.AnError))
	_, err = testPool.Exec(ctx, `INSERT INTO autodb_crawl_waiters (mod_id, user_id, vehicle_id, created_at)
		VALUES ($1, 8, $2, now() - interval '8 days')`, modID, vehicleID)
	require.NoError(t, err)
	require.NoError(t, repo.RetryFailed(ctx, 0, 7*24*time.Hour))
	_, ok, _ = repo.Claim(ctx)
	assert.False(t, ok)
}

func TestSectionsForASearch(t *testing.T) {
	skipShort(t)
	ctx := context.Background()
	repo := NewCrawlRepository(testPool)
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM autodb_sections`)
		_, _ = testPool.Exec(ctx, `DELETE FROM autodb_mod_sections`)
		_, _ = testPool.Exec(ctx, `DELETE FROM autodb_mod_parts`)
		_, _ = testPool.Exec(ctx, `DELETE FROM vehicles WHERE autodb_mod_id IS NOT NULL`)
	})
	vehicleID, err := NewDirectoryRepository(testPool).UpsertVehicle(ctx,
		directory.ToVehicle("DAEWOO", autodb.Model{Name: "LANOS (KLAT)"}, autodb.Modification{ID: 9201, Name: "DAEWOO LANOS 1.5",
			Attrs: map[string][]string{"ConstructionInterval": {"05.1997 -"}}}), 9201, 0)
	require.NoError(t, err)

	require.NoError(t, repo.SaveSections(ctx, []crawler.Section{
		{ID: 100030, Path: "тормозная система › Тормозной механизм › Комплект тормозных колодок"},
		{ID: 100300, Path: "Подвеска оси / система подвески / колеса › Рычаг независимой подвески колеса"},
		{ID: 100310, Path: "Подвеска оси › Несущий / направляющий шарнир"},
		{ID: 100770, Path: "Внутренняя отделка › Ручка двери"},
	}))
	since := time.Now().Add(-time.Hour)

	modID, secs, err := repo.Matching(ctx, vehicleID, "brake-pads", nil, since, 6)
	require.NoError(t, err)
	assert.Equal(t, int64(9201), modID)
	assert.Equal(t, []int64{100030}, secs, "by the category's words: \"колодок\"")

	_, secs, err = repo.Matching(ctx, vehicleID, "arms", nil, since, 6)
	require.NoError(t, err)
	assert.ElementsMatch(t, []int64{100300, 100310}, secs, "the shop's Russian words find the catalogue's sections: \"рычаг\", \"направляющий шарнир\"")

	_, secs, err = repo.Matching(ctx, vehicleID, "", []string{"кульов опор", "направляющ шарнир", "на"}, since, 6)
	require.NoError(t, err)
	assert.Equal(t, []int64{100310}, secs, "by any of the search's ways of naming it; a short one is no evidence")
	_, secs, err = repo.Matching(ctx, vehicleID, "", []string{"шарнир двери"}, since, 6)
	require.NoError(t, err)
	assert.Empty(t, secs, "all the words of a way must be there")

	require.NoError(t, repo.SaveParts(ctx, modID, 100300, nil))
	require.NoError(t, repo.SaveParts(ctx, modID, 100030, []crawler.Part{
		{SupplierID: 65, ArticleKey: "P85075", Number: "P 85 075", Brand: "BREMBO", BrandKey: "BREMBO", Name: "Колодки"},
	}))
	fetched, err := repo.Fetched(ctx, modID, since)
	require.NoError(t, err)
	assert.Equal(t, map[int64]bool{100030: true}, fetched,
		"a section with parts is stored; an empty one is asked again, the catalogue answering an overloaded caller with empty lists")
	_, secs, err = repo.Matching(ctx, vehicleID, "brake-pads", nil, since, 6)
	require.NoError(t, err)
	assert.Empty(t, secs, "and not loaded again")
	_, secs, err = repo.Matching(ctx, vehicleID, "brake-pads", nil, time.Now().Add(time.Hour), 6)
	require.NoError(t, err)
	assert.Equal(t, []int64{100030}, secs, "unless it is older than wanted")

	modID, secs, err = repo.Matching(ctx, vehicleByCode(t, "lanos-15"), "brake-pads", nil, since, 6)
	require.NoError(t, err)
	assert.Zero(t, modID)
	assert.Empty(t, secs, "a car from the shop's own list has no catalogue")
}

func TestSectionsAreFiledByTheShopsWords(t *testing.T) {
	skipShort(t)
	ctx := context.Background()
	repo := NewCrawlRepository(testPool)
	t.Cleanup(func() { _, _ = testPool.Exec(ctx, `DELETE FROM autodb_sections`) })

	require.NoError(t, repo.SaveSections(ctx, []crawler.Section{
		{ID: 1, Path: "тормозная система › Тормозной механизм › Тормозной диск"},
		{ID: 2, Path: "фильтр › Масляный фильтр"},
		{ID: 3, Path: "Подвеска оси › Несущий / направляющий шарнир"},
		{ID: 4, Path: "Комплектующие › Шплинт"},
	}))
	got := map[int64]string{}
	rows, err := testPool.Query(ctx, `SELECT id, category FROM autodb_sections`)
	require.NoError(t, err)
	var (
		id  int64
		cat string
	)
	_, err = pgx.ForEachRow(rows, []any{&id, &cat}, func() error { got[id] = cat; return nil })
	require.NoError(t, err)
	assert.Equal(t, map[int64]string{1: "brake-discs", 2: "oil-filters", 3: "arms", 4: ""}, got,
		"by the shop's words in the path; none fits a split pin")
}

func TestModelsOfTheShopsList(t *testing.T) {
	skipShort(t)
	models, err := NewVehicleRepository(testPool).Models(context.Background(), "daewoo", 0)
	require.NoError(t, err)
	names := make([]string, len(models))
	for i, m := range models {
		names[i] = m.Name
	}
	assert.Contains(t, names, "Lanos", "any case of the make")
	for _, m := range models {
		assert.NotEmpty(t, m.Years)
	}
	none, err := NewVehicleRepository(testPool).Models(context.Background(), "daewoo", 1950)
	require.NoError(t, err)
	assert.Empty(t, none, "nothing made in 1950")
}
