package bot

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/service"
	"github.com/aristodem96/ai_shop_bot/internal/telegram"
)

// sent is one call the bot made to Telegram.
type sent struct {
	method string
	chat   int64
	msg    int64
	text   string
	markup telegram.ReplyMarkup
	alert  bool
}

// fakeTelegram records every call instead of making it.
type fakeTelegram struct {
	mu       sync.Mutex
	calls    []sent
	photoErr error
}

func (f *fakeTelegram) record(s sent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
	return nil
}

func (f *fakeTelegram) SendMessage(_ context.Context, chat int64, html string, m telegram.ReplyMarkup) error {
	return f.record(sent{method: "sendMessage", chat: chat, text: html, markup: m})
}

func (f *fakeTelegram) SendPhoto(_ context.Context, chat int64, _, caption string, m telegram.ReplyMarkup) error {
	if f.photoErr != nil {
		return f.photoErr
	}
	return f.record(sent{method: "sendPhoto", chat: chat, text: caption, markup: m})
}

func (f *fakeTelegram) EditMessageText(_ context.Context, chat, msg int64, html string, kb *telegram.InlineKeyboard) error {
	return f.record(sent{method: "editMessageText", chat: chat, msg: msg, text: html, markup: kb})
}

func (f *fakeTelegram) EditMessageReplyMarkup(_ context.Context, chat, msg int64, kb *telegram.InlineKeyboard) error {
	return f.record(sent{method: "editMessageReplyMarkup", chat: chat, msg: msg, markup: kb})
}

func (f *fakeTelegram) AnswerCallbackQuery(_ context.Context, _, text string, alert bool) error {
	return f.record(sent{method: "answerCallbackQuery", text: text, alert: alert})
}

func (f *fakeTelegram) methods() []string {
	out := make([]string, 0, len(f.calls))
	for _, c := range f.calls {
		out = append(out, c.method)
	}
	return out
}

func (f *fakeTelegram) last(method string) sent {
	for i := len(f.calls) - 1; i >= 0; i-- {
		if f.calls[i].method == method {
			return f.calls[i]
		}
	}
	return sent{}
}

// fakeCatalog serves one category with one jacket.
type fakeCatalog struct{ jacket domain.Product }

func (f *fakeCatalog) Categories(context.Context) ([]domain.Category, error) {
	return []domain.Category{{ID: 1, Slug: "jackets", Title: "Куртки"}}, nil
}

func (f *fakeCatalog) CategoryPage(_ context.Context, id int64, _ int) (*service.ProductPage, error) {
	if id != 1 {
		return nil, domain.ErrNotFound
	}
	return &service.ProductPage{Category: domain.Category{ID: 1, Title: "Куртки"},
		Products: []domain.Product{f.jacket}, Pages: 1}, nil
}

func (f *fakeCatalog) Product(_ context.Context, id int64) (*domain.Product, error) {
	if id != f.jacket.ID {
		return nil, domain.ErrNotFound
	}
	p := f.jacket
	return &p, nil
}

func (f *fakeCatalog) Similar(ctx context.Context, id int64) (*domain.Product, []domain.Product, error) {
	p, err := f.Product(ctx, id)
	return p, nil, err
}

// fakeCart is backed by the jacket's stock, enough to exercise the bot's
// handling of both success and refusal.
type fakeCart struct {
	catalog *fakeCatalog
	qty     map[int64]int
	err     error
}

func (f *fakeCart) Get(_ context.Context, user int64) (*domain.Cart, error) {
	c := &domain.Cart{UserID: user}
	for _, v := range f.catalog.jacket.Variants {
		if n := f.qty[v.ID]; n > 0 {
			c.Lines = append(c.Lines, domain.CartLine{VariantID: v.ID, ProductID: v.ProductID,
				ProductName: f.catalog.jacket.Name, Size: v.Size, Price: f.catalog.jacket.Price, Qty: n, Stock: v.Stock})
		}
	}
	return c, nil
}

func (f *fakeCart) Add(ctx context.Context, user, variant int64) (*domain.Cart, error) {
	if f.err != nil {
		return nil, f.err
	}
	v, ok := f.catalog.jacket.Variant(variant)
	if !ok {
		return nil, domain.ErrNotFound
	}
	if f.qty[variant]+1 > v.Stock {
		return nil, domain.ErrOutOfStock
	}
	f.qty[variant]++
	return f.Get(ctx, user)
}

func (f *fakeCart) Decrement(ctx context.Context, user, variant int64) (*domain.Cart, error) {
	f.qty[variant] = max(f.qty[variant]-1, 0)
	return f.Get(ctx, user)
}

func (f *fakeCart) Remove(ctx context.Context, user, variant int64) (*domain.Cart, error) {
	delete(f.qty, variant)
	return f.Get(ctx, user)
}

func (f *fakeCart) Clear(context.Context, int64) error {
	clear(f.qty)
	return nil
}

const (
	chat     = int64(77)
	jacketID = int64(5)
	sizeM    = int64(51)
	sizeL    = int64(52) // sold out
)

func newTestBot() (*Bot, *fakeTelegram, *fakeCart) {
	catalog := &fakeCatalog{jacket: domain.Product{
		ID: jacketID, CategoryID: 1, Name: "Пуховик «Nord»", Brand: "Nord Line",
		Color: "olive", Season: "winter", Price: domain.UAH(3499), TotalStock: 3,
		Variants: []domain.Variant{
			{ID: sizeM, ProductID: jacketID, Size: "M", Stock: 3},
			{ID: sizeL, ProductID: jacketID, Size: "L", Stock: 0},
		},
	}}
	cart := &fakeCart{catalog: catalog, qty: map[int64]int{}}
	tg := &fakeTelegram{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(nil, tg, catalog, cart, Config{}, log), tg, cart
}

func text(s string) telegram.Update {
	return telegram.Update{Message: &telegram.Message{
		From: &telegram.User{ID: chat}, Chat: telegram.Chat{ID: chat, Type: telegram.ChatPrivate}, Text: s,
	}}
}

func press(data string) telegram.Update {
	return telegram.Update{CallbackQuery: &telegram.CallbackQuery{
		ID: "cb", From: telegram.User{ID: chat}, Data: data,
		Message: &telegram.Message{MessageID: 900, Chat: telegram.Chat{ID: chat, Type: telegram.ChatPrivate}},
	}}
}

// callbacks returns the callback data of every inline button.
func callbacks(m telegram.ReplyMarkup) []string {
	kb, ok := m.(*telegram.InlineKeyboard)
	if !ok {
		return nil
	}
	var out []string
	for _, row := range kb.Rows {
		for _, b := range row {
			out = append(out, b.CallbackData)
		}
	}
	return out
}

func TestStartShowsMenuAndCategories(t *testing.T) {
	b, tg, _ := newTestBot()
	b.Handle(context.Background(), text("/start"))

	require.Equal(t, []string{"sendMessage", "sendMessage"}, tg.methods())
	assert.IsType(t, &telegram.ReplyKeyboard{}, tg.calls[0].markup, "the main menu goes with the greeting")
	assert.Contains(t, callbacks(tg.calls[1].markup), encode(actCategory, 1, 0))
}

func TestFreeTextPointsToTheCatalogue(t *testing.T) {
	b, tg, _ := newTestBot()
	b.Handle(context.Background(), text("тепла куртка до 3000"))

	require.Len(t, tg.calls, 2)
	assert.Equal(t, msgFreeTextSoon, tg.calls[0].text)
}

func TestGroupChatsAreRefused(t *testing.T) {
	b, tg, _ := newTestBot()
	u := text("/start")
	u.Message.Chat.Type = "group"
	b.Handle(context.Background(), u)

	require.Len(t, tg.calls, 1)
	assert.Equal(t, msgPrivateOnly, tg.calls[0].text)
}

func TestProductCardFallsBackToTextWithoutPhoto(t *testing.T) {
	b, tg, _ := newTestBot()
	tg.photoErr = errors.New("telegram: sendPhoto failed (400): wrong file identifier")

	b.Handle(context.Background(), press(encode(actProduct, jacketID)))

	require.Equal(t, []string{"sendMessage", "answerCallbackQuery"}, tg.methods())
	assert.Contains(t, tg.calls[0].text, "Пуховик")
	assert.Contains(t, callbacks(tg.calls[0].markup), encode(actPickSize, jacketID))
}

func TestSizePickerOffersOnlySizesInStock(t *testing.T) {
	b, tg, _ := newTestBot()
	b.Handle(context.Background(), press(encode(actPickSize, jacketID)))

	edit := tg.last("editMessageReplyMarkup")
	assert.Equal(t, int64(900), edit.msg, "the card itself is edited")
	data := callbacks(edit.markup)
	assert.Contains(t, data, encode(actAdd, jacketID, sizeM))
	assert.NotContains(t, data, encode(actAdd, jacketID, sizeL), "a sold-out size gets no button")
	assert.Contains(t, data, encode(actCardMenu, jacketID))
}

func TestAddToCart(t *testing.T) {
	b, tg, cart := newTestBot()
	b.Handle(context.Background(), press(encode(actAdd, jacketID, sizeM)))

	assert.Equal(t, 1, cart.qty[sizeM])
	answer := tg.last("answerCallbackQuery")
	assert.Contains(t, answer.text, "Додано розмір M")
	assert.False(t, answer.alert, "success is a toast, not a dialog")
	assert.Contains(t, callbacks(tg.last("editMessageReplyMarkup").markup), encode(actPickSize, jacketID),
		"the card gets its normal buttons back")
}

func TestAddSoldOutSizeAlertsAndRefreshesPicker(t *testing.T) {
	b, tg, _ := newTestBot()
	b.Handle(context.Background(), press(encode(actAdd, jacketID, sizeL)))

	answer := tg.last("answerCallbackQuery")
	assert.Equal(t, msgOutOfStock, answer.text)
	assert.True(t, answer.alert)
	assert.NotContains(t, callbacks(tg.last("editMessageReplyMarkup").markup), encode(actAdd, jacketID, sizeL))
}

func TestUnexpectedErrorsAreNotShownToTheUser(t *testing.T) {
	b, tg, cart := newTestBot()
	cart.err = errors.New("postgres: connection refused")
	b.Handle(context.Background(), press(encode(actAdd, jacketID, sizeM)))

	assert.Equal(t, msgInternalFail, tg.last("answerCallbackQuery").text)
}

func TestCartButtonsEditTheCartInPlace(t *testing.T) {
	b, tg, cart := newTestBot()
	ctx := context.Background()

	b.Handle(ctx, press(encode(actInc, sizeM)))
	b.Handle(ctx, press(encode(actInc, sizeM)))
	assert.Equal(t, 2, cart.qty[sizeM])

	edit := tg.last("editMessageText")
	assert.Equal(t, int64(900), edit.msg)
	assert.Contains(t, edit.text, "2 × ")

	b.Handle(ctx, press(encode(actClear)))
	assert.Equal(t, msgCartEmpty, tg.last("editMessageText").text)
	assert.Equal(t, msgCartCleared, tg.last("answerCallbackQuery").text)
}

func TestEveryCallbackIsAnsweredOnce(t *testing.T) {
	for _, data := range []string{
		encode(actCategories), encode(actCategory, 1, 0), encode(actCategory, 99, 0),
		encode(actProduct, jacketID), encode(actProduct, 404), encode(actSimilar, jacketID),
		encode(actCart), encode(actDec, sizeM), encode(actNoop), "garbage", "p:999999999999999999999",
	} {
		b, tg, _ := newTestBot()
		b.Handle(context.Background(), press(data))

		answers := slices.DeleteFunc(tg.methods(), func(m string) bool { return m != "answerCallbackQuery" })
		assert.Len(t, answers, 1, "%q", data)
	}
}

func TestStaleButtonsSaySo(t *testing.T) {
	b, tg, _ := newTestBot()
	b.Handle(context.Background(), press("garbage"))
	assert.Equal(t, msgStaleButton, tg.last("answerCallbackQuery").text)

	b.Handle(context.Background(), press(encode(actProduct, 404)))
	assert.True(t, strings.Contains(tg.last("answerCallbackQuery").text, "недоступний"))
}
