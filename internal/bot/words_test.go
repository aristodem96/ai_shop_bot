package bot

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aristodem96/ai_shop_bot/internal/importer"
	"github.com/aristodem96/ai_shop_bot/internal/telegram"
)

// fakeWords answers from a three-node tree and records what it is asked.
type fakeWords struct {
	applied  []string
	created  *importer.NewCategory
	removed  []int64
	keywords []importer.OwnerKeyword
}

var wordTree = []importer.Category{
	{Slug: "engine", Title: "Двигун"},
	{Slug: "engine-parts", Parent: "engine", Title: "Деталі двигуна"},
	{Slug: "other", Title: "Інше"},
}

func (f *fakeWords) Categories(context.Context) ([]importer.Category, error) { return wordTree, nil }

func (f *fakeWords) KeywordMatches(context.Context, string) (int, int, error) { return 39, 37, nil }

func (f *fakeWords) PreviewKeyword(_ context.Context, word, slug string, create *importer.NewCategory) (*importer.KeywordChange, error) {
	target := importer.Category{Slug: slug, Parent: "engine", Title: "Деталі двигуна"}
	if create != nil {
		if len([]rune(create.Title)) < 2 {
			return nil, importer.ErrBadCategoryNm
		}
		target = importer.Category{Slug: "tiuninh", Parent: create.Parent, Title: create.Title}
	}
	return &importer.KeywordChange{Word: word, Target: target, Create: create, Matching: 39, FromDefault: 37,
		Move: []importer.ProductRef{{ID: 1, Name: "Вал распределительный TIGGO 2"}, {ID: 2, Name: "Вал <выпускной>"}},
		Stay: []importer.ProductRef{{ID: 3, Name: "Шестерня распредвала"}}}, nil
}

func (f *fakeWords) ApplyKeyword(ctx context.Context, word, slug string, create *importer.NewCategory) (*importer.KeywordChange, error) {
	f.applied = append(f.applied, word+"→"+slug)
	f.created = create
	return f.PreviewKeyword(ctx, word, slug, create)
}

func (f *fakeWords) OwnerKeywords(context.Context) ([]importer.OwnerKeyword, error) {
	return f.keywords, nil
}

func (f *fakeWords) RemoveKeyword(_ context.Context, id int64) (*importer.OwnerKeyword, error) {
	f.removed = append(f.removed, id)
	return &importer.OwnerKeyword{ID: id, Word: "вал распредел"}, nil
}

func newWordsBot(t *testing.T) (testBot, *fakeWords) {
	t.Helper()
	b, _ := newImportBot(t, "chinaavto")
	w := &fakeWords{}
	b.UseWords(w)
	return b, w
}

func say(chatID int64, text string) telegram.Update {
	return telegram.Update{Message: &telegram.Message{
		From: &telegram.User{ID: 5}, Chat: telegram.Chat{ID: chatID, Type: "supergroup"}, Text: text,
	}}
}

func TestWordIntoExistingSubcategory(t *testing.T) {
	b, w := newWordsBot(t)
	ctx := context.Background()

	b.Handle(ctx, say(managers, "/word@shop_bot Вал Распредел"))
	start := b.tg.last("sendMessage")
	assert.Contains(t, start.text, "«вал распредел»: у назвах 39 товарів, з них 37 зараз в «Інше»")
	assert.Equal(t, []string{
		encode(actWordPick, 1, 0), encode(actWordPick, 1, 2), encode(actWordNew, 1, -1), encode(actWordCancel, 1),
	}, callbacks(start.markup), "every section, a new one, cancel")

	b.Handle(ctx, pressIn(managers, encode(actWordPick, 1, 0)))
	sub := b.tg.last("editMessageText")
	assert.Contains(t, sub.text, "Оберіть підкатегорію")
	assert.Equal(t, []string{
		encode(actWordPick, 1, 1), encode(actWordNew, 1, 0), encode(actWordBack, 1), encode(actWordCancel, 1),
	}, callbacks(sub.markup))

	b.Handle(ctx, pressIn(managers, encode(actWordPick, 1, 1)))
	preview := b.tg.last("editMessageText")
	for _, want := range []string{"Двигун › Деталі двигуна", "Переїде товарів: 2", "з «Інше»: 37", "Вал &lt;выпускной&gt;", "Лишаться на місці: 1"} {
		assert.Contains(t, preview.text, want)
	}
	assert.Empty(t, w.applied, "nothing changes before Apply")

	b.Handle(ctx, pressIn(managers, encode(actWordApply, 1)))
	assert.Equal(t, []string{"вал распредел→engine-parts"}, w.applied)
	assert.Contains(t, b.tg.last("editMessageText").text, "✅ Готово")

	b.Handle(ctx, pressIn(managers, encode(actWordApply, 1)))
	assert.Len(t, w.applied, 1, "applied once")
}

func TestWordIntoNewCategory(t *testing.T) {
	b, w := newWordsBot(t)
	ctx := context.Background()
	b.Handle(ctx, say(managers, "/word антикрил"))

	b.Handle(ctx, pressIn(managers, encode(actWordNew, 1, 0)))
	ask := b.tg.last("sendMessage")
	assert.Contains(t, ask.text, "нову підкатегорію в розділі «Двигун»")
	_, isReply := ask.markup.(*telegram.ForceReply)
	assert.True(t, isReply, "a reply is what a group in privacy mode delivers")

	b.Handle(ctx, say(managers, "Тюнінг"))
	assert.Contains(t, b.tg.last("sendMessage").text, "Двигун › Тюнінг")
	assert.Contains(t, b.tg.last("sendMessage").text, "(нова)")

	b.Handle(ctx, say(managers, "Тюнінг"))
	assert.Equal(t, 1, countSent(b, "sendMessage", "Двигун › Тюнінг"), "the title is taken once; the next message is just a message")

	b.Handle(ctx, pressIn(managers, encode(actWordApply, 1)))
	require.NotNil(t, w.created)
	assert.Equal(t, importer.NewCategory{Parent: "engine", Title: "Тюнінг"}, *w.created)
}

func TestWordsListAndDelete(t *testing.T) {
	b, w := newWordsBot(t)
	ctx := context.Background()
	w.keywords = []importer.OwnerKeyword{{ID: 7, Word: "вал распредел", Category: "Двигун › Деталі двигуна"}}

	b.Handle(ctx, say(managers, "/words"))
	list := b.tg.last("sendMessage")
	assert.Contains(t, list.text, "«вал распредел» → Двигун › Деталі двигуна")
	assert.Equal(t, []string{encode(actWordDelete, 7)}, callbacks(list.markup))

	b.Handle(ctx, pressIn(managers, encode(actWordDelete, 7)))
	assert.Equal(t, []int64{7}, w.removed)
	assert.Contains(t, b.tg.last("answerCallbackQuery").text, "Видалено «вал распредел»")
}

func TestWordsOnlyInManagersChat(t *testing.T) {
	b, w := newWordsBot(t)
	ctx := context.Background()

	u := say(chat, "/word вал распредел")
	u.Message.Chat.Type = telegram.ChatPrivate
	b.Handle(ctx, u)
	assert.NotContains(t, b.tg.last("sendMessage").text, "Куди їх віднести", "a buyer's /word is just text")

	b.Handle(ctx, say(managers, "/word вал распредел"))
	b.Handle(ctx, pressIn(chat, encode(actWordApply, 1)))
	b.Handle(ctx, pressIn(chat, encode(actWordDelete, 7)))
	assert.Empty(t, w.applied, "a forged button from another chat changes nothing")
	assert.Empty(t, w.removed)
}

func TestWordHelpAndBadWord(t *testing.T) {
	b, _ := newWordsBot(t)
	ctx := context.Background()
	b.Handle(ctx, say(managers, "/word"))
	assert.Equal(t, msgWordHelp, b.tg.last("sendMessage").text)
	b.Handle(ctx, say(managers, "/word ва"))
	assert.Contains(t, b.tg.last("sendMessage").text, "від 3 до 40 символів")
}

func countSent(b testBot, method, contains string) int {
	n := 0
	for _, c := range b.tg.calls {
		if c.method == method && strings.Contains(c.text, contains) {
			n++
		}
	}
	return n
}
