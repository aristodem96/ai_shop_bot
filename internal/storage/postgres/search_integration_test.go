package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/service"
)

func search(t *testing.T, f service.SearchFilter) []domain.Product {
	t.Helper()
	if f.Limit == 0 {
		f.Limit = 50
	}
	got, err := NewCatalogRepository(testPool).Search(context.Background(), f)
	require.NoError(t, err)
	return got
}

func TestSearchByFilters(t *testing.T) {
	skipShort(t)

	// "A warm jacket up to 3500, size M, not black" -- the buyer from the brief.
	got := search(t, service.SearchFilter{
		Category: "jackets", Season: "winter", MaxPrice: domain.UAH(3500),
		Size: "M", ExcludeColors: []string{"black"},
	})
	require.NotEmpty(t, got)
	for _, p := range got {
		assert.Contains(t, []string{"winter", "all"}, p.Season, p.SKU)
		assert.LessOrEqual(t, p.Price, domain.UAH(3500), p.SKU)
		assert.NotEqual(t, "black", p.Color, p.SKU)

		var stockM int
		for _, v := range p.Variants {
			if v.Size == "M" {
				stockM = v.Stock
			}
		}
		assert.Positive(t, stockM, "%s must have M in stock", p.SKU)
	}
	for i := 1; i < len(got); i++ {
		assert.LessOrEqual(t, got[i-1].Price, got[i].Price, "without keywords, cheapest first")
	}
}

func TestSearchNeverReturnsSoldOut(t *testing.T) {
	skipShort(t)
	for _, p := range search(t, service.SearchFilter{Category: "accessories"}) {
		assert.NotEqual(t, "ACC-007", p.SKU, "the sold-out belt never shows up in search")
		assert.Positive(t, p.TotalStock, p.SKU)
	}
}

func TestSearchByKeywordPrefix(t *testing.T) {
	skipShort(t)

	got := search(t, service.SearchFilter{Query: "вишив"})
	require.NotEmpty(t, got)
	for _, p := range got {
		assert.Contains(t, p.Name, "Вишиванка", "a stem finds the full word")
	}

	got = search(t, service.SearchFilter{Query: "пуховик Nord", Limit: 3})
	require.NotEmpty(t, got)
	assert.Contains(t, got[0].Name, "Nord", "matching more words ranks higher")
}

func TestSearchGenderIncludesUnisex(t *testing.T) {
	skipShort(t)
	for _, p := range search(t, service.SearchFilter{Gender: "women"}) {
		assert.Contains(t, []string{"women", "unisex"}, p.Gender, p.SKU)
	}
}

func TestSearchTreatsHostileInputAsData(t *testing.T) {
	skipShort(t)
	// Neither the tsquery syntax nor SQL in a filter value can do anything.
	assert.Empty(t, search(t, service.SearchFilter{Category: "x' OR '1'='1"}))
	assert.Empty(t, search(t, service.SearchFilter{Size: "M'); DROP TABLE products; --"}))
	_ = search(t, service.SearchFilter{Query: "&|!():*'\\"})

	var n int
	require.NoError(t, testPool.QueryRow(context.Background(), `SELECT count(*) FROM products`).Scan(&n))
	assert.Positive(t, n)
}
