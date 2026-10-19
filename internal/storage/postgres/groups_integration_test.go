package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aristodem96/ai_shop_bot/internal/categories"
	"github.com/aristodem96/ai_shop_bot/internal/grouping"
	"github.com/aristodem96/ai_shop_bot/internal/importer"
)

func TestSupplierGroups(t *testing.T) {
	skipShort(t)
	ctx := context.Background()
	repo := NewImportRepository(testPool)
	groups := NewGroupRepository(testPool)
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM products WHERE source = 'groups-test'`)
		_, _ = testPool.Exec(ctx, `DELETE FROM suppliers WHERE source = 'groups-test'`)
		_, _ = testPool.Exec(ctx, `DELETE FROM category_keywords WHERE word = 'трещітк'`)
	})
	sup := &importer.Supplier{Name: "Groups test", Source: "groups-test", Columns: importer.Columns{Name: "N", Price: "P", ExternalID: "C", Stock: "S"}}
	require.NoError(t, repo.SaveSupplier(ctx, sup))
	var ratchet int64
	require.NoError(t, testPool.QueryRow(ctx, `
		INSERT INTO products (sku, category_id, name, brand, price, stock, source, external_id, source_group)
		VALUES ('G-1', (SELECT id FROM categories WHERE slug = 'other'), 'Трещітка 1/2" 45 зубів', '', 30000, 3,
			'groups-test', '1', 'Трещітки (храповики)') RETURNING id`).Scan(&ratchet))

	require.NoError(t, repo.SavePending(ctx, sup.ID, []importer.PendingGroup{
		{GroupKey: importer.GroupKey{Kind: importer.KindGroup, Key: "Трещітки (храповики)"}, Products: 1, Examples: []string{`Трещітка 1/2" 45 зубів`}},
	}))
	open, err := groups.Open(ctx, sup.ID)
	require.NoError(t, err)
	require.Len(t, open, 1)
	g := open[0]
	assert.Equal(t, []string{`Трещітка 1/2" 45 зубів`}, g.Examples)

	require.NoError(t, groups.SaveSuggestion(ctx, g.ID, &grouping.Suggestion{NewSection: "Інструмент", Sure: true, Note: "ок"}))
	var tools int64
	require.NoError(t, testPool.QueryRow(ctx, `SELECT id FROM categories WHERE slug = 'tools'`).Scan(&tools))
	require.NoError(t, groups.Decide(ctx, g.ID, tools, "owner"))

	var cat int64
	require.NoError(t, testPool.QueryRow(ctx, `SELECT category_id FROM products WHERE id = $1`, ratchet).Scan(&cat))
	assert.Equal(t, tools, cat, "deciding the group files its products")
	mapped, err := repo.GroupCategories(ctx, sup.ID)
	require.NoError(t, err)
	assert.Equal(t, map[importer.GroupKey]string{{Kind: importer.KindGroup, Key: "Трещітки (храповики)"}: "tools"}, mapped)
	open, _ = groups.Open(ctx, sup.ID)
	assert.Empty(t, open)

	// A word elsewhere in the tree does not pull a mapped group's product.
	var pads int64
	require.NoError(t, testPool.QueryRow(ctx, `SELECT id FROM categories WHERE slug = 'brake-pads'`).Scan(&pads))
	plan, err := categories.New(NewCategoryRepository(testPool)).Plan(ctx, categories.Op{Kind: categories.AddWord, CategoryID: pads, Word: "трещітк"})
	require.NoError(t, err)
	assert.Empty(t, plan.Moves)

	found, err := groups.Find(ctx, "храповик", 10)
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.Equal(t, tools, found[0].CategoryID)
}
