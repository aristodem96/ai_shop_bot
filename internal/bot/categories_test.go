package bot

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aristodem96/ai_shop_bot/internal/categories"
	"github.com/aristodem96/ai_shop_bot/internal/telegram"
)

func say(chatID int64, text string) telegram.Update {
	return telegram.Update{Message: &telegram.Message{
		From: &telegram.User{ID: 5}, Chat: telegram.Chat{ID: chatID, Type: "supergroup"}, Text: text,
	}}
}

// treeStore is the categories service's store in memory: it applies what
// the service plans, so the bot is tested against the real rules.
type treeStore struct {
	cats     []categories.Category
	products []categories.Product
	nextID   int64
}

func newTreeStore() *treeStore {
	return &treeStore{
		nextID: 100,
		cats: []categories.Category{
			{ID: 1, Slug: "brakes", Title: "Гальма"},
			{ID: 2, Slug: "brake-pads", Title: "Колодки", ParentID: 1, Words: []categories.Word{{ID: 10, Text: "колодк"}}},
			{ID: 3, Slug: "engine", Title: "Двигун"},
			{ID: 4, Slug: categories.Other, Title: "Інше"},
		},
		products: []categories.Product{
			{ID: 1, Name: "Колодки передні", CategoryID: 2},
			{ID: 2, Name: "Вал распределительный", CategoryID: 4},
		},
	}
}

func (s *treeStore) Tree(context.Context) ([]categories.Category, error) {
	out := slices.Clone(s.cats)
	for i := range out {
		out[i].Products = 0
		for _, p := range s.products {
			if p.CategoryID == out[i].ID {
				out[i].Products++
			}
		}
	}
	return out, nil
}

func (s *treeStore) ProductsIn(_ context.Context, id int64) ([]categories.Product, error) {
	var out []categories.Product
	for _, p := range s.products {
		if p.CategoryID == id {
			out = append(out, p)
		}
	}
	return out, nil
}

func (s *treeStore) ProductsWith(_ context.Context, w string) ([]categories.Product, error) {
	var out []categories.Product
	for _, p := range s.products {
		if strings.Contains(strings.ToLower(p.Name), w) {
			out = append(out, p)
		}
	}
	return out, nil
}

func (s *treeStore) Apply(_ context.Context, ch *categories.Change) error {
	var created int64
	if c := ch.Create; c != nil {
		s.nextID++
		created = s.nextID
		s.cats = append(s.cats, categories.Category{ID: created, Slug: c.Slug, Title: c.Title, ParentID: c.ParentID})
	}
	for i := range s.cats {
		c := &s.cats[i]
		if c.ID == ch.RenameID {
			c.Title = ch.Title
		}
		if c.ID == ch.AddWordTo {
			s.nextID++
			c.Words = append(c.Words, categories.Word{ID: s.nextID, Text: ch.Word})
		}
		c.Words = slices.DeleteFunc(c.Words, func(w categories.Word) bool { return w.ID == ch.RemoveWordID })
	}
	for _, m := range ch.Moves {
		for i := range s.products {
			if s.products[i].ID == m.ProductID {
				s.products[i].CategoryID = m.CategoryID
				if m.CategoryID == 0 {
					s.products[i].CategoryID = created
				}
			}
		}
	}
	s.cats = slices.DeleteFunc(s.cats, func(c categories.Category) bool { return c.ID == ch.DeleteID })
	return nil
}

func newCategoriesBot(t *testing.T) (testBot, *treeStore) {
	t.Helper()
	b, _ := newImportBot(t, "demo")
	store := newTreeStore()
	b.UseCategories(categories.New(store))
	return b, store
}

func reply(chatID int64, text string) telegram.Update {
	u := say(chatID, text)
	u.Message.ReplyToMessage = &telegram.Message{Text: "?"}
	return u
}

func TestCategoriesScreens(t *testing.T) {
	b, _ := newCategoriesBot(t)
	ctx := context.Background()

	b.Handle(ctx, say(managers, "/categories"))
	root := b.tg.last("sendMessage")
	assert.Contains(t, root.text, "перемагає найдовше")
	assert.Equal(t, []string{encode(actCatOpen, 1), encode(actCatOpen, 3), encode(actCatOpen, 4), encode(actCatNew, 0)},
		callbacks(root.markup))

	b.Handle(ctx, pressIn(managers, encode(actCatOpen, 1)))
	section := b.tg.last("editMessageText")
	assert.Contains(t, section.text, "Гальма")
	assert.Contains(t, callbacks(section.markup), encode(actCatOpen, 2), "its subcategories")
	assert.NotContains(t, callbacks(section.markup), encode(actCatDelete, 1), "a section with subcategories is not deleted")

	b.Handle(ctx, pressIn(managers, encode(actCatOpen, 2)))
	leaf := b.tg.last("editMessageText")
	assert.Contains(t, leaf.text, "Гальма › Колодки")
	assert.Contains(t, leaf.text, "«колодк»")
	assert.Contains(t, callbacks(leaf.markup), encode(actCatRemoveWord, 10))
}

func TestAddWordFromTheCategory(t *testing.T) {
	b, store := newCategoriesBot(t)
	ctx := context.Background()

	b.Handle(ctx, pressIn(managers, encode(actCatAddWord, 3)))
	assert.IsType(t, &telegram.ForceReply{}, b.tg.last("sendMessage").markup, "the word is asked by reply")

	b.Handle(ctx, reply(managers, "Вал распредел"))
	plan := b.tg.last("sendMessage")
	assert.Contains(t, plan.text, "Додати слово «вал распредел» у <b>Двигун</b>")
	assert.Contains(t, plan.text, "Переїде сюди товарів: 1</b> (з «Інше»: 1)")

	b.Handle(ctx, pressIn(managers, callbacks(plan.markup)[0]))
	assert.Equal(t, int64(3), store.products[1].CategoryID, "moved after Apply, not before")
	assert.Contains(t, b.tg.last("editMessageText").text, "✅ «вал распредел» → Двигун")
}

func TestWordCommandPicksTheCategory(t *testing.T) {
	b, store := newCategoriesBot(t)
	ctx := context.Background()

	b.Handle(ctx, say(managers, "/word вал распредел"))
	start := b.tg.last("sendMessage")
	assert.Contains(t, start.text, "у назвах 1 товарів")
	b.Handle(ctx, pressIn(managers, encode(actCatPick, 1, 1)))
	assert.Contains(t, callbacks(b.tg.last("editMessageText").markup), encode(actCatPick, 1, 2), "a section opens its subcategories")
	b.Handle(ctx, pressIn(managers, encode(actCatPick, 1, 3)))
	b.Handle(ctx, pressIn(managers, encode(actCatApply, 1)))
	assert.Equal(t, int64(3), store.products[1].CategoryID)

	b.Handle(ctx, say(managers, "/word колодк"))
	assert.Contains(t, b.tg.last("sendMessage").text, "вже веде в <b>Гальма › Колодки</b>")
}

func TestCreateRenameDelete(t *testing.T) {
	b, store := newCategoriesBot(t)
	ctx := context.Background()

	b.Handle(ctx, pressIn(managers, encode(actCatNew, 0)))
	b.Handle(ctx, reply(managers, "Тюнінг"))
	assert.Contains(t, b.tg.last("sendMessage").text, "✅ Створено Тюнінг", "nothing to move: created at once")
	created := store.cats[len(store.cats)-1]
	assert.Equal(t, "tiuninh", created.Slug)

	b.Handle(ctx, pressIn(managers, encode(actCatRename, created.ID)))
	b.Handle(ctx, reply(managers, "Тюнінг і аксесуари"))
	assert.Equal(t, "Тюнінг і аксесуари", store.cats[len(store.cats)-1].Title)

	b.Handle(ctx, pressIn(managers, encode(actCatDelete, 2)))
	plan := b.tg.last("editMessageText")
	assert.Contains(t, plan.text, "Видалити <b>Гальма › Колодки</b>")
	assert.Contains(t, plan.text, "• Інше — 1")
	b.Handle(ctx, pressIn(managers, callbacks(plan.markup)[0]))
	assert.Equal(t, int64(4), store.products[0].CategoryID, "the pads go to Other")
	_, still := findCategory(store.cats, 2)
	assert.False(t, still)

	b.Handle(ctx, pressIn(managers, encode(actCatDelete, 4)))
	assert.Contains(t, b.tg.last("sendMessage").text, "«Інше» не можна видалити")
}

func TestCategoriesAreTheManagersOnly(t *testing.T) {
	b, store := newCategoriesBot(t)
	b.Handle(context.Background(), press(encode(actCatDelete, 2)))
	_, still := findCategory(store.cats, 2)
	assert.True(t, still)
	require.True(t, b.tg.last("answerCallbackQuery").alert)
}
