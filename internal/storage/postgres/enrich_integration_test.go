package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aristodem96/ai_shop_bot/internal/autodb"
	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/enrich"
	"github.com/aristodem96/ai_shop_bot/internal/service"
)

func TestEnrichFillsCardsAndAnalogues(t *testing.T) {
	skipShort(t)
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM products WHERE source = 'enrich-test'`)
		_, _ = testPool.Exec(ctx, `DELETE FROM autodb_crosses`)
		_, _ = testPool.Exec(ctx, `DELETE FROM autodb_articles`)
		_, _ = testPool.Exec(ctx, `DELETE FROM autodb_suppliers`)
		_, _ = testPool.Exec(ctx, `DELETE FROM photo_files`)
	})
	product := func(sku, brand, partKey, image string, oem ...string) int64 {
		var id int64
		require.NoError(t, testPool.QueryRow(ctx, `
			INSERT INTO products (sku, category_id, name, brand, price, stock, image_url, source, external_id, part_key, oem_numbers)
			VALUES ($1, (SELECT id FROM categories WHERE slug = 'oil-filters'), $1, $2, 30000, 4, $4, 'enrich-test', $1, $3, $5)
			RETURNING id`, sku, brand, partKey, image, append([]string{partKey}, oem...)).Scan(&id))
		return id
	}
	mann := product("E-MANN", "MANN", "W71295", "")
	bosch := product("E-BOSCH", "Bosch", "F026407143", "")
	knecht := product("E-KNECHT", "KNECHT", "OC9771", "https://shop/own.jpg")
	product("E-NOBRAND", "", "SOMETHINGELSE", "")
	_, err := testPool.Exec(ctx, `INSERT INTO brands (name) VALUES ('MANN') ON CONFLICT DO NOTHING`)
	require.NoError(t, err)
	_, err = testPool.Exec(ctx, `INSERT INTO brand_aliases (alias, brand_id) SELECT 'MANNFILTER', id FROM brands WHERE name = 'MANN' ON CONFLICT DO NOTHING`)
	require.NoError(t, err)

	repo := NewEnrichRepository(testPool)
	pending, err := repo.Pending(ctx, 100)
	require.NoError(t, err)
	var mine enrich.Product
	for _, p := range pending {
		if p.ID == mann {
			mine = p
		}
	}
	assert.ElementsMatch(t, []string{"MANN", "MANNFILTER"}, mine.BrandKeys, "the brand and its aliases")

	got, ok, err := repo.PendingProduct(ctx, knecht)
	require.NoError(t, err)
	require.True(t, ok)
	pending, _ = repo.Pending(ctx, 1)
	assert.Equal(t, got.ID, pending[0].ID, "a product a buyer opened goes first")

	a := &enrich.Article{SupplierID: 4, Key: "W71295", Number: "W 712/95", Brand: "MANN-FILTER", Name: "Фильтр",
		Specs: []domain.Spec{{Name: "Высота [мм]", Value: "79"}}, Images: []string{"https://img/a.webp"},
		OEMKeys: []string{"04E115561H", "W71295"}, Cross: []enrich.Cross{{BrandKey: "BOSCH", Key: "F026407143"}},
		Vehicles:      []autodb.ArticleVehicle{{ModID: 18537, Make: "CHEVROLET", Model: "LACETTI (J200)", Years: "03.2005 - ", Engine: "1.6"}},
		VehiclesKnown: true}
	require.NoError(t, repo.SaveArticle(ctx, a))
	back, ok, err := repo.Article(ctx, 4, "W71295")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, a, back)
	keys, err := repo.SupplierKeys(ctx, []int64{4, 5})
	require.NoError(t, err)
	assert.Equal(t, map[int64]string{4: "MANNFILTER"}, keys, "the card taught the supplier's name")

	require.NoError(t, repo.Matched(ctx, mann, a))
	require.NoError(t, repo.Matched(ctx, knecht, &enrich.Article{SupplierID: 7, Key: "OC9771", Images: []string{"https://img/k.webp"}}))
	require.NoError(t, repo.Unmatched(ctx, bosch))
	sup, number, ok, err := repo.ProductArticle(ctx, mann)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, [2]any{int64(4), "W 712/95"}, [2]any{sup, number}, "the matched product names its article")
	_, _, ok, _ = repo.ProductArticle(ctx, bosch)
	assert.False(t, ok)
	catalog := NewCatalogRepository(testPool)
	p, err := catalog.Product(ctx, mann)
	require.NoError(t, err)
	assert.Equal(t, "https://img/a.webp", p.ImageURL, "an empty photo is filled")
	assert.Equal(t, []string{"W71295", "04E115561H"}, p.OEMNumbers, "the carmakers' numbers after its own, once")
	p, _ = catalog.Product(ctx, knecht)
	assert.Equal(t, "https://shop/own.jpg", p.ImageURL, "the shop's own photo stays")

	found, err := catalog.Search(ctx, searchByNumber(domain.NormalizeOEM("04E 115 561 H")))
	require.NoError(t, err)
	ids := make([]int64, len(found))
	for i, f := range found {
		ids[i] = f.ID
	}
	assert.Contains(t, ids, mann, "found by the carmaker's number now")

	src, _ := catalog.Product(ctx, mann)
	analogues, err := catalog.Analogues(ctx, src, 0, 10)
	require.NoError(t, err)
	assert.Equal(t, bosch, analogues[0].ID, "the catalogue's cross list makes Bosch an analogue")
	src, _ = catalog.Product(ctx, bosch)
	analogues, err = catalog.Analogues(ctx, src, 0, 10)
	require.NoError(t, err)
	assert.Equal(t, mann, analogues[0].ID, "and the other way round")

	photos := NewPhotoRepository(testPool)
	require.NoError(t, photos.SaveFileID(ctx, "https://img/a.webp", "f1"))
	id, ok, err := photos.FileID(ctx, "https://img/a.webp")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "f1", id)
}

func searchByNumber(n string) service.SearchFilter {
	return service.SearchFilter{PartNumber: n, Limit: 5}
}
