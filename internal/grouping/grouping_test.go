package grouping

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aristodem96/ai_shop_bot/internal/categories"
)

type memStore struct{ groups []Group }

func (m *memStore) Open(_ context.Context, sup int64) ([]Group, error) {
	var out []Group
	for _, g := range m.groups {
		if g.SupplierID == sup && g.CategoryID == 0 && !g.Mixed {
			out = append(out, g)
		}
	}
	return out, nil
}

func (m *memStore) Get(_ context.Context, id int64) (*Group, error) {
	for i := range m.groups {
		if m.groups[i].ID == id {
			g := m.groups[i]
			return &g, nil
		}
	}
	return nil, assert.AnError
}

func (m *memStore) SaveSuggestion(_ context.Context, id int64, s *Suggestion) error {
	for i := range m.groups {
		if m.groups[i].ID == id {
			m.groups[i].Suggestion = s
		}
	}
	return nil
}

func (m *memStore) Decide(_ context.Context, id, cat int64, by string) error {
	for i := range m.groups {
		if m.groups[i].ID == id {
			m.groups[i].CategoryID, m.groups[i].DecidedBy = cat, by
		}
	}
	return nil
}

func (m *memStore) DecideMixed(_ context.Context, id int64, by string) error {
	for i := range m.groups {
		if m.groups[i].ID == id {
			m.groups[i].Mixed, m.groups[i].DecidedBy = true, by
		}
	}
	return nil
}

func (m *memStore) Find(_ context.Context, text string, _ int) ([]Group, error) {
	var out []Group
	for _, g := range m.groups {
		if strings.Contains(strings.ToLower(g.Key), strings.ToLower(text)) {
			out = append(out, g)
		}
	}
	return out, nil
}

// memTree is the tree, creating what it is asked to.
type memTree struct {
	cats []categories.Category
	next int64
}

func (t *memTree) Tree(context.Context) ([]categories.Category, error) {
	return slices.Clone(t.cats), nil
}

func (t *memTree) Apply(_ context.Context, op categories.Op) (*categories.Plan, error) {
	t.next++
	t.cats = append(t.cats, categories.Category{ID: 100 + t.next, Title: op.Title, ParentID: op.CategoryID})
	return &categories.Plan{}, nil
}

// fakeMatcher answers by group key.
type fakeMatcher struct {
	answers map[string]*Suggestion
	asked   []string
}

func (f *fakeMatcher) Suggest(_ context.Context, _ []Node, _ []string, groups []Group) ([]*Suggestion, error) {
	out := make([]*Suggestion, len(groups))
	for i, g := range groups {
		f.asked = append(f.asked, g.Key)
		if a, ok := f.answers[g.Key]; ok {
			c := *a
			out[i] = &c
		}
	}
	return out, nil
}

func setup() (*Service, *memStore, *memTree, *fakeMatcher) {
	store := &memStore{groups: []Group{
		{ID: 1, SupplierID: 7, Kind: "group", Key: "Колодки гальмівні", Products: 40},
		{ID: 2, SupplierID: 7, Kind: "group", Key: "Трещітки (храповики)", Products: 12},
		{ID: 3, SupplierID: 7, Kind: "group", Key: "Головки торцеві", Products: 60},
		{ID: 4, SupplierID: 7, Kind: "group", Key: "РІЗНЕ", Products: 15},
		{ID: 5, SupplierID: 7, Kind: "group", Key: "Вигадка", Products: 3},
		{ID: 6, SupplierID: 7, Kind: "group", Key: "Фарба металік", Products: 20},
		{ID: 7, SupplierID: 7, Kind: "group", Key: "Фільтр WIX", Products: 63},
	}}
	tree := &memTree{cats: []categories.Category{
		{ID: 1, Title: "Гальма"}, {ID: 2, Title: "Колодки", ParentID: 1},
		{ID: 3, Title: "Інструменти й аксесуари"}, {ID: 4, Slug: "other", Title: "Інше"},
	}}
	m := &fakeMatcher{answers: map[string]*Suggestion{
		"Колодки гальмівні":    {CategoryID: 2, Sure: true},
		"Трещітки (храповики)": {NewSection: "Інструмент", NewTitle: "Ручний інструмент", Sure: true},
		"Головки торцеві":      {NewSection: "Інструмент", NewTitle: "Ручний інструмент", Sure: true},
		"РІЗНЕ":                {CategoryID: 4, Sure: false, Note: "різні товари"},
		"Вигадка":              {CategoryID: 999, Sure: true},
		"Фарба металік":        {ParentID: 2, NewTitle: "Фарби", Sure: true},
		"Фільтр WIX":           {Mixed: true, Sure: true, Note: "фільтри різних типів"},
	}}
	return New(store, tree, m), store, tree, m
}

func TestSort(t *testing.T) {
	s, store, _, m := setup()
	sum, err := s.Sort(context.Background(), 7)
	require.NoError(t, err)

	assert.Equal(t, 1, sum.Filed, "only the sure answer into an existing leaf is applied")
	assert.Equal(t, 40, sum.FiledProducts)
	assert.Equal(t, int64(2), store.groups[0].CategoryID)
	assert.Equal(t, "model", store.groups[0].DecidedBy)

	assert.Equal(t, []Proposal{{Path: "Інструмент › Ручний інструмент", Groups: 2, Products: 72}}, sum.Proposals,
		"one new category for both tool groups, waiting for the owner")
	assert.Equal(t, 3, sum.Doubts, "unsure, an invented id, and a subcategory under a subcategory")
	assert.Equal(t, 38, sum.DoubtProducts)
	assert.Equal(t, "немає пропозиції", store.groups[4].Suggestion.Note, "the model may not name a category the tree lacks")

	_, err = s.Sort(context.Background(), 7)
	require.NoError(t, err)
	assert.Len(t, m.asked, 7, "a group is asked about once")
	assert.Equal(t, 1, sum.Mixed, "a mixed group is sorted by name, nobody asked")
	assert.True(t, store.groups[6].Mixed)
}

func TestCreateProposed(t *testing.T) {
	s, store, tree, _ := setup()
	_, err := s.Sort(context.Background(), 7)
	require.NoError(t, err)

	created, filed, err := s.CreateProposed(context.Background(), 7)
	require.NoError(t, err)
	assert.Equal(t, 2, created, "the section and its subcategory, once for both groups")
	assert.Equal(t, 2, filed)
	sub := tree.cats[len(tree.cats)-1]
	assert.Equal(t, "Ручний інструмент", sub.Title)
	assert.Equal(t, sub.ID, store.groups[1].CategoryID)
	assert.Equal(t, sub.ID, store.groups[2].CategoryID)
	assert.Equal(t, "owner", store.groups[1].DecidedBy)
}

func TestDoubtsAndDecide(t *testing.T) {
	s, store, _, _ := setup()
	ctx := context.Background()
	_, err := s.Sort(ctx, 7)
	require.NoError(t, err)

	doubts, err := s.Doubts(ctx, 7)
	require.NoError(t, err)
	keys := make([]string, len(doubts))
	for i, d := range doubts {
		keys[i] = d.Key
	}
	assert.Equal(t, []string{"РІЗНЕ", "Вигадка", "Фарба металік"}, keys)

	path, err := s.SuggestionPath(ctx, doubts[0].Suggestion)
	require.NoError(t, err)
	assert.Equal(t, "Інше", path)

	g, err := s.Decide(ctx, 4, 0)
	require.NoError(t, err, "accepting the model's guess")
	assert.Equal(t, int64(4), g.CategoryID)
	g, err = s.Decide(ctx, 5, 3)
	require.NoError(t, err, "or the owner's own pick")
	assert.Equal(t, int64(3), g.CategoryID)
	_, err = s.Decide(ctx, 6, 0)
	require.ErrorIs(t, err, ErrNoSuggestion, "an invalid suggestion was dropped: nothing to accept")
	assert.Zero(t, store.groups[5].CategoryID)
}

func TestValid(t *testing.T) {
	tree := []Node{{ID: 1, Path: "Гальма"}, {ID: 2, Path: "Гальма › Колодки", ParentID: 1, Leaf: true}}
	assert.Nil(t, valid(&Suggestion{CategoryID: 1}, tree), "a section with subcategories holds no products")
	assert.Nil(t, valid(&Suggestion{ParentID: 2, NewTitle: "X"}, tree), "two levels only")
	assert.Equal(t, &Suggestion{NewSection: "Фарби"}, valid(&Suggestion{NewTitle: "Фарби"}, tree),
		"a subcategory with no section is a new section")
	assert.NotNil(t, valid(&Suggestion{ParentID: 1, NewTitle: "Диски"}, tree))
}
