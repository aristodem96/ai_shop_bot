package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aristodem96/ai_shop_bot/internal/categories"
)

func TestCategoryTree(t *testing.T) {
	skipShort(t)
	ctx := context.Background()
	repo := NewCategoryRepository(testPool)
	svc := categories.New(repo)
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM products WHERE source = 'tree-test'`)
		_, _ = testPool.Exec(ctx, `DELETE FROM category_keywords WHERE word IN ('розподтест', 'шестерни тест')`)
		_, _ = testPool.Exec(ctx, `DELETE FROM categories WHERE slug IN ('test-section', 'shesterni', 'tiuninh-test')`)
	})

	tree, err := repo.Tree(ctx)
	require.NoError(t, err)
	byslug := map[string]categories.Category{}
	for _, c := range tree {
		byslug[c.Slug] = c
	}
	pads := byslug["brake-pads"]
	assert.Equal(t, byslug["brakes"].ID, pads.ParentID)
	assert.NotEmpty(t, pads.Words, "the seed file's words are in the table")
	assert.Equal(t, "brakes", tree[0].Slug, "sections in file order")
	assert.Equal(t, "brake-pads", tree[1].Slug, "each followed by its subcategories")

	require.NoError(t, SeedCategories(ctx, testPool))
	again, _ := repo.Tree(ctx)
	assert.Len(t, again, len(tree), "seeding a tree that exists adds nothing")

	product := func(sku, name, group string) int64 {
		var id int64
		require.NoError(t, testPool.QueryRow(ctx, `
			INSERT INTO products (sku, category_id, name, brand, price, stock, source, external_id, source_group)
			VALUES ($1, (SELECT id FROM categories WHERE slug = 'other'), $2, '', 100000, 1, 'tree-test', $1, $3)
			RETURNING id`, sku, name, group).Scan(&id))
		return id
	}
	camshaft := product("T-CAM", "Тестова деталь розподтест", "")
	gear := product("T-GEAR", "Деталь 7", "Шестерни тест")

	// A section of our own, with a word.
	_, err = svc.Apply(ctx, categories.Op{Kind: categories.Create, Title: "Test section"})
	require.NoError(t, err)
	tree, _ = repo.Tree(ctx)
	var section categories.Category
	for _, c := range tree {
		if c.Slug == "test-section" {
			section = c
		}
	}
	require.NotZero(t, section.ID)
	assert.Equal(t, "test-section", tree[len(tree)-1].Slug, "a new section goes last")

	p, err := svc.Apply(ctx, categories.Op{Kind: categories.AddWord, CategoryID: section.ID, Word: "розподтест"})
	require.NoError(t, err)
	assert.Len(t, p.Moves, 1)
	p, err = svc.Apply(ctx, categories.Op{Kind: categories.AddWord, CategoryID: section.ID, Word: "шестерни тест"})
	require.NoError(t, err)
	assert.Len(t, p.Moves, 1, "the supplier's group counts")

	// Its first subcategory takes its products and words.
	_, err = svc.Apply(ctx, categories.Op{Kind: categories.Create, CategoryID: section.ID, Title: "Шестерні"})
	require.NoError(t, err)
	var sub int64
	require.NoError(t, testPool.QueryRow(ctx, `SELECT id FROM categories WHERE slug = 'shesterni'`).Scan(&sub))
	for _, id := range []int64{camshaft, gear} {
		var cat int64
		require.NoError(t, testPool.QueryRow(ctx, `SELECT category_id FROM products WHERE id = $1`, id).Scan(&cat))
		assert.Equal(t, sub, cat)
	}
	var words int
	require.NoError(t, testPool.QueryRow(ctx, `SELECT count(*) FROM category_keywords WHERE category_id = $1`, sub).Scan(&words))
	assert.Equal(t, 2, words)

	// Deleting it sends its products where the rest of the tree files them.
	p, err = svc.Apply(ctx, categories.Op{Kind: categories.Delete, CategoryID: sub})
	require.NoError(t, err)
	assert.ElementsMatch(t, []categories.Count{{Path: "Інше", N: 1}, {Path: "Трансмісія › Коробка передач", N: 1}}, p.To,
		"the gear to the gearbox's «шестерн», the rest to Other")
	var left int
	require.NoError(t, testPool.QueryRow(ctx, `SELECT count(*) FROM category_keywords WHERE category_id = $1`, sub).Scan(&left))
	assert.Zero(t, left, "its words went with it")

	_, err = svc.Apply(ctx, categories.Op{Kind: categories.AddWord, CategoryID: pads.ID, Word: pads.Words[0].Text})
	require.ErrorIs(t, err, categories.ErrWordTaken)
}
