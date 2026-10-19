package categories

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// memStore is a small shop: brakes with pads, engine with no subcategories,
// and Other.
type memStore struct {
	cats     []Category
	products []Product
	applied  []*Change
}

func newMemStore() *memStore {
	return &memStore{
		cats: []Category{
			{ID: 1, Slug: "brakes", Title: "Гальма"},
			{ID: 2, Slug: "brake-pads", Title: "Колодки", ParentID: 1, Words: []Word{{ID: 10, Text: "колодк"}}},
			{ID: 3, Slug: "engine", Title: "Двигун", Words: []Word{{ID: 11, Text: "поршн"}}},
			{ID: 4, Slug: Other, Title: "Інше"},
		},
		products: []Product{
			{ID: 100, Name: "Колодки передні", CategoryID: 2},
			{ID: 101, Name: "Колодки задні барабанні", CategoryID: 2},
			{ID: 102, Name: "Вал распределительный", CategoryID: 4},
			{ID: 103, Name: "Вал распределительный выпускной", CategoryID: 4},
			{ID: 104, Name: "Кільця поршневі", CategoryID: 3},
			{ID: 105, Name: "Датчик", Group: "Система гальмування", CategoryID: 4},
		},
	}
}

func (m *memStore) Tree(context.Context) ([]Category, error) {
	out := slices.Clone(m.cats)
	for i := range out {
		for _, p := range m.products {
			if p.CategoryID == out[i].ID {
				out[i].Products++
			}
		}
	}
	return out, nil
}

func (m *memStore) ProductsIn(_ context.Context, id int64) ([]Product, error) {
	var out []Product
	for _, p := range m.products {
		if p.CategoryID == id {
			out = append(out, p)
		}
	}
	return out, nil
}

func (m *memStore) ProductsWith(_ context.Context, word string) ([]Product, error) {
	var out []Product
	for _, p := range m.products {
		if strings.Contains(strings.ToLower(p.Name+" "+p.Group), word) {
			out = append(out, p)
		}
	}
	return out, nil
}

func (m *memStore) Apply(_ context.Context, ch *Change) error {
	m.applied = append(m.applied, ch)
	return nil
}

func moved(p *Plan) map[int64]int64 {
	out := map[int64]int64{}
	for _, m := range p.change.Moves {
		out[m.ProductID] = m.CategoryID
	}
	return out
}

func TestAddWord(t *testing.T) {
	s := New(newMemStore())
	p, err := s.Plan(context.Background(), Op{Kind: AddWord, CategoryID: 3, Word: "  Вал  Распредел "})
	require.NoError(t, err)
	assert.Equal(t, "вал распредел", p.Op.Word, "written the way it is matched")
	assert.Equal(t, map[int64]int64{102: 3, 103: 3}, moved(p))
	assert.Equal(t, 2, p.FromOther)
	assert.Equal(t, []Count{{Path: "Двигун", N: 2}}, p.To)
}

func TestAddWordMatchesTheSuppliersGroup(t *testing.T) {
	s := New(newMemStore())
	p, err := s.Plan(context.Background(), Op{Kind: AddWord, CategoryID: 2, Word: "гальмуван"})
	require.NoError(t, err)
	assert.Equal(t, map[int64]int64{105: 2}, moved(p), "a 1C group names what the product name does not")
}

func TestAddWordRefusals(t *testing.T) {
	s := New(newMemStore())
	ctx := context.Background()
	_, err := s.Plan(ctx, Op{Kind: AddWord, CategoryID: 3, Word: "колодк"})
	require.ErrorIs(t, err, ErrWordTaken)
	assert.ErrorContains(t, err, "Гальма › Колодки", "and says where it leads")
	_, err = s.Plan(ctx, Op{Kind: AddWord, CategoryID: 1, Word: "диск"})
	require.ErrorIs(t, err, ErrNotForWords, "a section with subcategories holds no products")
	_, err = s.Plan(ctx, Op{Kind: AddWord, CategoryID: 3, Word: "ва"})
	require.ErrorIs(t, err, ErrBadWord)
}

func TestLongerWordKeepsItsProducts(t *testing.T) {
	store := newMemStore()
	store.cats[1].Words = append(store.cats[1].Words, Word{ID: 12, Text: "колодки задні"})
	p, err := New(store).Plan(context.Background(), Op{Kind: AddWord, CategoryID: 3, Word: "колодки"})
	require.NoError(t, err)
	assert.Equal(t, map[int64]int64{100: 3}, moved(p), "the longer word beats «колодк»")
	require.Len(t, p.Stay, 1, "and «колодки задні» beats it")
	assert.Equal(t, int64(101), p.Stay[0].ID)
}

func TestRemoveWordSendsProductsWhereTheTreeWould(t *testing.T) {
	s := New(newMemStore())
	p, err := s.Plan(context.Background(), Op{Kind: RemoveWord, WordID: 10})
	require.NoError(t, err)
	assert.Equal(t, map[int64]int64{100: 4, 101: 4}, moved(p), "nothing else knows pads: Other")
	assert.Equal(t, Path("Гальма › Колодки"), p.Target)
}

func TestDelete(t *testing.T) {
	store := newMemStore()
	s := New(store)
	ctx := context.Background()

	_, err := s.Plan(ctx, Op{Kind: Delete, CategoryID: 1})
	require.ErrorIs(t, err, ErrHasChildren)
	_, err = s.Plan(ctx, Op{Kind: Delete, CategoryID: 4})
	require.ErrorIs(t, err, ErrKeepOther)

	p, err := s.Apply(ctx, Op{Kind: Delete, CategoryID: 2})
	require.NoError(t, err)
	assert.Equal(t, map[int64]int64{100: 4, 101: 4}, moved(p))
	require.Len(t, store.applied, 1)
	assert.Equal(t, int64(2), store.applied[0].DeleteID)
}

func TestCreate(t *testing.T) {
	store := newMemStore()
	s := New(store)
	ctx := context.Background()

	p, err := s.Plan(ctx, Op{Kind: Create, Title: "Тюнінг"})
	require.NoError(t, err)
	assert.Equal(t, &NewCategory{Slug: "tiuninh", Title: "Тюнінг"}, p.change.Create)
	assert.Empty(t, p.Moves, "a new section is empty until words fill it")

	_, err = s.Plan(ctx, Op{Kind: Create, CategoryID: 2, Title: "Задні"})
	require.ErrorIs(t, err, ErrTooDeep, "two levels: sections and subcategories")
	_, err = s.Plan(ctx, Op{Kind: Create, CategoryID: 1, Title: "колодки"})
	require.ErrorIs(t, err, ErrTitleTaken)

	p, err = s.Plan(ctx, Op{Kind: Create, CategoryID: 3, Title: "Поршні"})
	require.NoError(t, err)
	assert.Equal(t, Path("Двигун › Поршні"), p.Target)
	assert.Equal(t, map[int64]int64{104: 0}, moved(p), "a section's products go into its first subcategory")
	assert.Equal(t, int64(3), p.change.MoveWordsFrom, "and its words with them")
}

func TestRename(t *testing.T) {
	store := newMemStore()
	s := New(store)
	_, err := s.Apply(context.Background(), Op{Kind: Rename, CategoryID: 2, Title: " Гальмівні   колодки "})
	require.NoError(t, err)
	assert.Equal(t, "Гальмівні колодки", store.applied[0].Title)
	_, err = s.Plan(context.Background(), Op{Kind: Rename, CategoryID: 3, Title: "Гальма"})
	require.ErrorIs(t, err, ErrTitleTaken)
}
