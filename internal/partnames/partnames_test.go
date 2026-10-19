package partnames

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/llm"
)

var cats = []domain.Category{
	{ID: 1, Slug: "brakes", Title: "Гальмівна система"},
	{ID: 2, Slug: "brake-pads", Title: "Гальмівні колодки", ParentID: 1},
	{ID: 3, Slug: "accessories", Title: "Аксесуари"},
}

type memStore struct {
	pending []string
	saved   []Name
}

func (s *memStore) Untranslated(_ context.Context, limit int) ([]string, error) {
	n := min(limit, len(s.pending))
	return s.pending[:n], nil
}

func (s *memStore) SaveNames(_ context.Context, names []Name) error {
	s.saved = append(s.saved, names...)
	s.pending = s.pending[len(names):]
	return nil
}

func newTranslator(model llm.Client, store Store) *Translator {
	return New(model, store, cats, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestTranslate(t *testing.T) {
	fake := llm.NewFake(llm.Call("c1", toolName, map[string]any{"names": []map[string]any{
		{"name": "Комплект тормозных колодок", "uk": "Гальмівні колодки", "category": "brake-pads"},
		{"name": "Ремень", "uk": "Ремінь", "category": "belts"},
		{"name": "Never asked", "uk": "x", "category": ""},
	}}))
	got, err := newTranslator(fake, &memStore{}).Translate(context.Background(), []string{"Комплект тормозных колодок", "Ремень"})
	require.NoError(t, err)
	assert.Equal(t, []Name{
		{Name: "Комплект тормозных колодок", NameUK: "Гальмівні колодки", Category: "brake-pads"},
		{Name: "Ремень", NameUK: "Ремінь"},
	}, got, "a made-up slug is dropped, and so is a name nobody asked about")

	text := fake.Requests[0].Messages[0].Text
	assert.Contains(t, text, "brake-pads | Гальмівні колодки")
	assert.Contains(t, text, "accessories | Аксесуари", "a section without subcategories takes parts")
	assert.NotContains(t, text, "brakes |", "a section with subcategories does not")
}

func TestPassTranslatesEverythingPending(t *testing.T) {
	store := &memStore{pending: []string{"Ремень"}}
	fake := llm.NewFake(llm.Call("c1", toolName, map[string]any{"names": []map[string]any{
		{"name": "Ремень", "uk": "Ремінь", "category": ""},
	}}))
	require.NoError(t, newTranslator(fake, store).Pass(context.Background()))
	assert.Len(t, store.saved, 1)
	assert.Empty(t, store.pending)
	assert.Len(t, fake.Requests, 1, "nothing pending, no request")
}

func TestPassStopsWhenTheModelSkipsEverything(t *testing.T) {
	store := &memStore{pending: []string{"Ремень"}}
	fake := llm.NewFake(llm.Say("Ремінь"))
	err := newTranslator(fake, store).Pass(context.Background())
	require.ErrorIs(t, err, ErrNoAnswer, "rather than asking about the same batch forever")
}
