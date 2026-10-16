package importer

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeKeywords struct {
	cats     []Category
	products []ProductRef
	added    []string
	moved    []int64
	created  *NewCategory
}

func (f *fakeKeywords) Categories(context.Context) ([]Category, error) {
	out := make([]Category, len(f.cats))
	for i, c := range f.cats {
		c.Keywords = append([]string(nil), c.Keywords...)
		out[i] = c
	}
	return out, nil
}

func (f *fakeKeywords) ProductsContaining(_ context.Context, word string) ([]ProductRef, error) {
	var out []ProductRef
	for _, p := range f.products {
		if containsFold(p.Name, word) {
			out = append(out, p)
		}
	}
	return out, nil
}

func (f *fakeKeywords) AddKeyword(_ context.Context, word, slug string, create *NewCategory, move []int64) error {
	f.added, f.moved, f.created = append(f.added, word+"→"+slug), move, create
	return nil
}
func (f *fakeKeywords) OwnerKeywords(context.Context) ([]OwnerKeyword, error)       { return nil, nil }
func (f *fakeKeywords) RemoveKeyword(context.Context, int64) (*OwnerKeyword, error) { return nil, nil }

func containsFold(s, sub string) bool {
	return NewClassifier([]Category{{Slug: "x", Keywords: []string{sub}}}).Classify(s) != nil
}

func keywordService() (*Service, *fakeKeywords) {
	f := &fakeKeywords{
		cats: []Category{
			{Slug: "engine", Title: "Двигун"},
			{Slug: "engine-parts", Parent: "engine", Title: "Деталі двигуна", Keywords: []string{"распредвал"}},
			{Slug: "timing", Parent: "engine", Title: "Ремені й ГРМ", Keywords: []string{"шестерня распредвала"}},
			{Slug: "other", Title: "Інше"},
		},
		products: []ProductRef{
			{ID: 1, Name: "Вал распределительный TIGGO 2", Category: "other"},
			{ID: 2, Name: "Вал распределительный выпускной", Category: "other"},
			{ID: 3, Name: "Шестерня распредвала вал распределительный", Category: "timing"},
			{ID: 4, Name: "Вал распределительный (оригинал)", Category: "engine-parts"},
			{ID: 5, Name: "Колодки", Category: "other"},
		},
	}
	return NewService(nil, nil, f, ""), f
}

func TestPreviewKeyword(t *testing.T) {
	s, _ := keywordService()
	ch, err := s.PreviewKeyword(context.Background(), "  Вал  РАСПРЕДЕЛ ", "engine-parts", nil)
	require.NoError(t, err)

	assert.Equal(t, "вал распредел", ch.Word, "written as the classifier compares")
	assert.Equal(t, 4, ch.Matching)
	require.Len(t, ch.Move, 2)
	assert.Equal(t, []int64{1, 2}, []int64{ch.Move[0].ID, ch.Move[1].ID})
	assert.Equal(t, 2, ch.FromDefault)
	require.Len(t, ch.Stay, 1, "a longer word files the gear elsewhere, as an import would")
	assert.Equal(t, int64(3), ch.Stay[0].ID)
}

func TestApplyKeywordMovesWhatThePreviewSaid(t *testing.T) {
	s, f := keywordService()
	_, err := s.ApplyKeyword(context.Background(), "вал распредел", "engine-parts", nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"вал распредел→engine-parts"}, f.added)
	assert.Equal(t, []int64{1, 2}, f.moved)
}

func TestPreviewKeywordNewCategory(t *testing.T) {
	s, _ := keywordService()
	ch, err := s.PreviewKeyword(context.Background(), "колодк", "", &NewCategory{Parent: "", Title: " Тюнінг "})
	require.NoError(t, err)
	require.NotNil(t, ch.Create)
	assert.Equal(t, "tiuninh", ch.Create.Slug, "a Latin key from the Ukrainian title")
	assert.Equal(t, "Тюнінг", ch.Target.Title)
	assert.Len(t, ch.Move, 1)

	_, err = s.PreviewKeyword(context.Background(), "колодк", "", &NewCategory{Title: "Я"})
	require.ErrorIs(t, err, ErrBadCategoryNm)
}

func TestPreviewKeywordRefuses(t *testing.T) {
	s, _ := keywordService()
	ctx := context.Background()

	_, err := s.PreviewKeyword(ctx, "распредвал", "timing", nil)
	require.ErrorIs(t, err, ErrKeywordTaken, "one word, one category")
	assert.ErrorContains(t, err, "Деталі двигуна", "and the owner is told where it leads")

	for _, bad := range []string{"ва", "колодк|диск", "a,b"} {
		_, err = s.PreviewKeyword(ctx, bad, "timing", nil)
		require.ErrorIs(t, err, ErrBadKeyword, bad)
	}
	_, err = s.PreviewKeyword(ctx, "вал распредел", "nowhere", nil)
	require.ErrorIs(t, err, ErrBadCategory)
}

func TestUniqueSlug(t *testing.T) {
	cats := []Category{{Slug: "tiuninh"}}
	assert.Equal(t, "tiuninh-2", uniqueSlug("Тюнінг", cats))
	assert.Equal(t, "shchytky-ta-shchyty", uniqueSlug("Щитки та щити", nil))
	assert.Equal(t, "custom-4x4", uniqueSlug("4x4", nil), "a slug starts with a letter")
}

func TestKeywordInfo(t *testing.T) {
	s, _ := keywordService()
	ctx := context.Background()

	info, err := s.KeywordInfo(ctx, "вал распредел")
	require.NoError(t, err)
	assert.Empty(t, info.TakenBy)
	assert.Equal(t, 4, info.Total)
	assert.Equal(t, 2, info.InDefault)
	assert.Equal(t, []Count{{Name: "Інше", N: 2}, {Name: "Двигун › Деталі двигуна", N: 1}, {Name: "Двигун › Ремені й ГРМ", N: 1}}, info.Where)

	info, err = s.KeywordInfo(ctx, "распредвал")
	require.NoError(t, err)
	assert.Equal(t, "Двигун › Деталі двигуна", info.TakenBy, "a word the tree has is said so at once")
}
