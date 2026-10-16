package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aristodem96/ai_shop_bot/internal/importer"
)

func TestOwnerKeywordSurvivesSync(t *testing.T) {
	skipShort(t)
	ctx := context.Background()
	repo := NewImportRepository(testPool)
	svc := importer.NewService(repo, repo, repo, "")

	// Two products the tree does not know, sitting in "other".
	var ids [2]int64
	for i, name := range []string{"Спойлер-антикрило тюнінгове", "Антикрило карбонове"} {
		require.NoError(t, testPool.QueryRow(ctx, `
			INSERT INTO products (sku, category_id, name, brand, price, stock)
			VALUES ($1, (SELECT id FROM categories WHERE slug = 'other'), $2, 'Testbrand', 100000, 1)
			RETURNING id`, "TST-KW-"+name[:4], name).Scan(&ids[i]))
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM products WHERE brand = 'Testbrand'`)
		_, _ = testPool.Exec(ctx, `DELETE FROM category_keywords`)
		_, _ = testPool.Exec(ctx, `DELETE FROM categories WHERE custom`)
	})

	ch, err := svc.ApplyKeyword(ctx, "антикрил", "", &importer.NewCategory{Parent: "body", Title: "Тюнінг"})
	require.NoError(t, err)
	assert.Len(t, ch.Move, 2)

	var slug, parent string
	require.NoError(t, testPool.QueryRow(ctx, `
		SELECT c.slug, s.slug FROM products p
		JOIN categories c ON c.id = p.category_id JOIN categories s ON s.id = c.parent_id
		WHERE p.id = $1`, ids[0]).Scan(&slug, &parent))
	assert.Equal(t, "tiuninh", slug, "filed in the new subcategory")
	assert.Equal(t, "body", parent)

	// A deploy syncs the file's tree over the database; the owner's word and
	// category must come through it.
	require.NoError(t, SyncCategories(ctx, testPool))
	cats, err := repo.Categories(ctx)
	require.NoError(t, err)
	var found bool
	for _, c := range cats {
		if c.Slug == "tiuninh" {
			found = true
			assert.Equal(t, []string{"антикрил"}, c.Keywords, "the classifier reads the owner's words")
		}
	}
	assert.True(t, found, "the owner's category survives the sync")

	_, err = svc.PreviewKeyword(ctx, "антикрил", "other", nil)
	require.ErrorIs(t, err, importer.ErrKeywordTaken)

	kws, err := svc.OwnerKeywords(ctx)
	require.NoError(t, err)
	require.Len(t, kws, 1)
	assert.Equal(t, "Кузов і салон › Тюнінг", kws[0].Category)

	removed, err := svc.RemoveKeyword(ctx, kws[0].ID)
	require.NoError(t, err)
	assert.Equal(t, "антикрил", removed.Word)
	_, err = svc.RemoveKeyword(ctx, kws[0].ID)
	require.ErrorIs(t, err, importer.ErrBadKeyword, "a second press finds nothing")
}
