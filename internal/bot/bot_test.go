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

func (f *fakeTelegram) SendChatAction(context.Context, int64, string) error {
	return f.record(sent{method: "sendChatAction"})
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

// fakeCatalog serves one category with one pair of brake pads.
type fakeCatalog struct{ pads domain.Product }

func (f *fakeCatalog) Menu(context.Context, *domain.Vehicle) ([]service.CategoryEntry, error) {
	return []service.CategoryEntry{{Category: domain.Category{ID: 1, Slug: "brakes", Title: "Гальмівна система"}, Count: 1}}, nil
}

func (f *fakeCatalog) CategoryPage(_ context.Context, id int64, car *domain.Vehicle, _ int, _ bool) (*service.ProductPage, error) {
	if id != 1 {
		return nil, domain.ErrNotFound
	}
	return &service.ProductPage{Category: domain.Category{ID: 1, Title: "Гальмівна система"},
		Products: []domain.Product{f.pads}, Vehicle: car, Pages: 1}, nil
}

func (f *fakeCatalog) Product(_ context.Context, id int64) (*domain.Product, error) {
	if id != f.pads.ID {
		return nil, domain.ErrNotFound
	}
	p := f.pads
	return &p, nil
}

func (f *fakeCatalog) Analogues(ctx context.Context, id int64, _ *domain.Vehicle) (*domain.Product, []domain.Product, error) {
	p, err := f.Product(ctx, id)
	return p, nil, err
}

// fakeCart is backed by the pads' stock, enough to exercise the bot's
// handling of both success and refusal.
type fakeCart struct {
	catalog *fakeCatalog
	qty     map[int64]int
	err     error
}

func (f *fakeCart) Get(_ context.Context, user int64) (*domain.Cart, error) {
	c := &domain.Cart{UserID: user}
	p := f.catalog.pads
	if n := f.qty[p.ID]; n > 0 {
		c.Lines = append(c.Lines, domain.CartLine{ProductID: p.ID, ProductName: p.Name, Brand: p.Brand,
			Price: p.Price, Qty: n, Stock: p.Stock})
	}
	return c, nil
}

func (f *fakeCart) Add(ctx context.Context, user, productID int64) (*domain.Cart, error) {
	if f.err != nil {
		return nil, f.err
	}
	if productID != f.catalog.pads.ID {
		return nil, domain.ErrNotFound
	}
	if f.qty[productID]+1 > f.catalog.pads.Stock {
		return nil, domain.ErrOutOfStock
	}
	f.qty[productID]++
	return f.Get(ctx, user)
}

func (f *fakeCart) Decrement(ctx context.Context, user, productID int64) (*domain.Cart, error) {
	f.qty[productID] = max(f.qty[productID]-1, 0)
	return f.Get(ctx, user)
}

func (f *fakeCart) Remove(ctx context.Context, user, productID int64) (*domain.Cart, error) {
	delete(f.qty, productID)
	return f.Get(ctx, user)
}

func (f *fakeCart) Clear(context.Context, int64) error {
	clear(f.qty)
	return nil
}

var (
	octavia = domain.Vehicle{ID: 50, Make: "Skoda", Model: "Octavia A7", YearFrom: 2013, YearTo: 2020, Engine: "1.4 TSI"}
	lanos   = domain.Vehicle{ID: 60, Make: "Daewoo", Model: "Lanos", YearFrom: 1997, YearTo: 2009, Engine: "1.5"}
)

type fakeGarage struct {
	car *domain.Vehicle
}

func (f *fakeGarage) Get(context.Context, int64) (*domain.Vehicle, error) { return f.car, nil }

func (f *fakeGarage) Clear(context.Context, int64) error {
	f.car = nil
	return nil
}

// fakeKits builds a two-line kit from the pads: two units of them, and a
// line the cart will refuse.
type fakeKits struct{ catalog *fakeCatalog }

func (f fakeKits) Kit(_ context.Context, vehicleID int64) (*service.Kit, error) {
	if vehicleID != octavia.ID {
		return nil, domain.ErrNotFound
	}
	return &service.Kit{
		Vehicle: octavia,
		Items: []service.KitItem{
			{PartType: domain.PartOilFilter, Product: f.catalog.pads, Qty: 2},
			{PartType: domain.PartSparkPlug, Product: domain.Product{ID: 404, Name: "Свічка", Brand: "Iskra", Price: domain.UAH(260)}, Qty: 4},
		},
		Missing: []string{domain.PartCabinFilter},
	}, nil
}

const (
	chat   = int64(77)
	padsID = int64(5)
)

type testBot struct {
	*Bot
	catalog *fakeCatalog
	tg      *fakeTelegram
	cart    *fakeCart
	garage  *fakeGarage
}

func newTestBot() testBot {
	catalog := &fakeCatalog{pads: domain.Product{
		ID: padsID, CategoryID: 1, SKU: "BRK-001", Name: "Колодки гальмівні передні", Brand: "AutoLine",
		Price: domain.UAH(1450), Stock: 2, OEMNumbers: []string{"5Q0698151"},
		ImageURL: "https://example.com/pads.png",
		Fits:     []domain.Vehicle{octavia},
	}}
	cart := &fakeCart{catalog: catalog, qty: map[int64]int{}}
	garage := &fakeGarage{}
	tg := &fakeTelegram{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bot := New(nil, tg, catalog, cart, garage, fakeKits{catalog}, Config{}, log)
	// Slow work runs before the handler returns, for tests to see its end.
	bot.later = func(ctx context.Context, f func(context.Context)) { f(ctx) }
	return testBot{
		Bot:     bot,
		catalog: catalog, tg: tg, cart: cart, garage: garage,
	}
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

func TestStartAsksForTheCarInWords(t *testing.T) {
	b := newTestBot()
	b.Handle(context.Background(), text("/start"))

	require.Equal(t, []string{"sendMessage", "sendMessage"}, b.tg.methods())
	assert.IsType(t, &telegram.ReplyKeyboard{}, b.tg.calls[0].markup, "the main menu goes with the greeting")
	assert.Equal(t, msgAskCar, b.tg.calls[1].text, "no menu of every make: the buyer writes the car")
}

func TestStartWithACarGoesToTheCatalogue(t *testing.T) {
	b := newTestBot()
	b.garage.car = &octavia
	b.Handle(context.Background(), text("/start"))

	assert.Contains(t, b.tg.calls[1].text, "Для: Skoda Octavia A7 1.4 TSI")
	assert.Contains(t, callbacks(b.tg.calls[1].markup), encode(actCategory, 1, 0))
}

func TestMyCar(t *testing.T) {
	b := newTestBot()
	ctx := context.Background()

	b.Handle(ctx, text(btnCar))
	assert.Equal(t, msgAskCar, b.tg.last("sendMessage").text)

	b.garage.car = &octavia
	b.Handle(ctx, text(btnCar))
	msg := b.tg.last("sendMessage")
	assert.Contains(t, msg.text, "Skoda Octavia A7 1.4 TSI")
	assert.Contains(t, callbacks(msg.markup), encode(actForgetCar))

	b.Handle(ctx, press(encode(actForgetCar)))
	assert.Nil(t, b.garage.car)
	assert.Equal(t, msgCarForgotten, b.tg.last("editMessageText").text)
}

func TestMaintenanceKitButton(t *testing.T) {
	b := newTestBot()
	b.garage.car = &octavia
	b.Handle(context.Background(), press(encode(actKit)))

	msg := b.tg.last("sendMessage")
	assert.Contains(t, msg.text, "Комплект для ТО")
	assert.Contains(t, msg.text, "2 × 1\u202f450 грн")
	assert.Contains(t, msg.text, "Немає в наявності для вашого авто: салонний фільтр")
	assert.Contains(t, callbacks(msg.markup), encode(actKitAdd))
}

func TestAddWholeKit(t *testing.T) {
	b := newTestBot()
	b.garage.car = &octavia
	b.Handle(context.Background(), press(encode(actKitAdd)))

	assert.Equal(t, 2, b.cart.qty[padsID], "both units of the first line")
	assert.Equal(t, "Додано 2 з 6 шт. — решти немає в наявності або досягнуто ліміту.",
		b.tg.last("answerCallbackQuery").text, "the buyer is told what did not go in")
	assert.Contains(t, b.tg.last("sendMessage").text, "Кошик", "and sees the cart")
}

func TestKitWithoutACar(t *testing.T) {
	b := newTestBot()
	b.Handle(context.Background(), press(encode(actKit)))

	answer := b.tg.last("answerCallbackQuery")
	assert.Equal(t, msgNoCarForKit, answer.text)
	assert.True(t, answer.alert)
}

func TestIDWorksInGroups(t *testing.T) {
	b := newTestBot()
	u := text("/id@shopexampleaibot")
	u.Message.Chat = telegram.Chat{ID: -1001234567890, Type: "supergroup"}
	b.Handle(context.Background(), u)

	msg := b.tg.last("sendMessage")
	assert.Equal(t, int64(-1001234567890), msg.chat)
	assert.Contains(t, msg.text, "-1001234567890", "a managers' group can find its id for ADMIN_CHAT_ID")
}

func TestGroupChatsAreRefused(t *testing.T) {
	b := newTestBot()
	u := text("/start")
	u.Message.Chat.Type = "group"
	b.Handle(context.Background(), u)

	require.Len(t, b.tg.calls, 1)
	assert.Equal(t, msgPrivateOnly, b.tg.calls[0].text)
}

func TestProductCardShowsFit(t *testing.T) {
	b := newTestBot()
	b.garage.car = &lanos
	b.Handle(context.Background(), press(encode(actProduct, padsID)))

	card := b.tg.last("sendPhoto")
	assert.Contains(t, card.text, "Не підходить до вашого Daewoo Lanos 1.5")
	assert.Contains(t, callbacks(card.markup), encode(actAdd, padsID))
}

func TestProductCardFallsBackToTextWhenPhotoFails(t *testing.T) {
	b := newTestBot()
	b.tg.photoErr = errors.New("telegram: sendPhoto failed (400): wrong file identifier")

	b.Handle(context.Background(), press(encode(actProduct, padsID)))

	require.Equal(t, []string{"sendMessage", "answerCallbackQuery"}, b.tg.methods())
	assert.Contains(t, b.tg.calls[0].text, "Колодки")
	assert.Contains(t, callbacks(b.tg.calls[0].markup), encode(actAdd, padsID))
}

func TestProductCardWithoutPhotoIsText(t *testing.T) {
	b := newTestBot()
	b.catalog.pads.ImageURL = "" // imported from a price list without photos

	b.Handle(context.Background(), press(encode(actProduct, padsID)))

	require.Equal(t, []string{"sendMessage", "answerCallbackQuery"}, b.tg.methods(), "no doomed sendPhoto first")
	assert.Contains(t, b.tg.calls[0].text, "Колодки")
}

func TestAddToCart(t *testing.T) {
	b := newTestBot()
	b.Handle(context.Background(), press(encode(actAdd, padsID)))

	assert.Equal(t, 1, b.cart.qty[padsID])
	answer := b.tg.last("answerCallbackQuery")
	assert.Equal(t, "✅ Додано. У кошику: 1 шт.", answer.text)
	assert.False(t, answer.alert, "success is a toast, not a dialog")
}

func TestAddToCartWarnsWhenItDoesNotFit(t *testing.T) {
	b := newTestBot()
	b.garage.car = &lanos
	b.Handle(context.Background(), press(encode(actAdd, padsID)))

	assert.Equal(t, 1, b.cart.qty[padsID], "still added: it may be for another car")
	assert.Contains(t, b.tg.last("answerCallbackQuery").text, "не підходить до вашого Daewoo Lanos")
}

func TestAddBeyondStockAlerts(t *testing.T) {
	b := newTestBot()
	ctx := context.Background()
	b.Handle(ctx, press(encode(actAdd, padsID)))
	b.Handle(ctx, press(encode(actAdd, padsID)))
	b.Handle(ctx, press(encode(actAdd, padsID)))

	answer := b.tg.last("answerCallbackQuery")
	assert.Equal(t, msgOutOfStock, answer.text)
	assert.True(t, answer.alert)
}

func TestUnexpectedErrorsAreNotShownToTheUser(t *testing.T) {
	b := newTestBot()
	b.cart.err = errors.New("postgres: connection refused")
	b.Handle(context.Background(), press(encode(actAdd, padsID)))

	assert.Equal(t, msgInternalFail, b.tg.last("answerCallbackQuery").text)
}

func TestCartButtonsEditTheCartInPlace(t *testing.T) {
	b := newTestBot()
	ctx := context.Background()

	b.Handle(ctx, press(encode(actInc, padsID)))
	b.Handle(ctx, press(encode(actInc, padsID)))
	assert.Equal(t, 2, b.cart.qty[padsID])

	edit := b.tg.last("editMessageText")
	assert.Equal(t, int64(900), edit.msg)
	assert.Contains(t, edit.text, "2 × ")

	b.Handle(ctx, press(encode(actClear)))
	assert.Equal(t, msgCartEmpty, b.tg.last("editMessageText").text)
	assert.Equal(t, msgCartCleared, b.tg.last("answerCallbackQuery").text)
}

func TestEveryCallbackIsAnsweredOnce(t *testing.T) {
	for _, data := range []string{
		encode(actCategories), encode(actCategory, 1, 0), encode(actCategory, 99, 0),
		encode(actProduct, padsID), encode(actProduct, 404), encode(actAnalogues, padsID),
		encode(actCart), encode(actDec, padsID), encode(actForgetCar), encode(actKit), encode(actKitAdd),
		encode(actCheckout), encode(actPay), encode(actCancelCheckout), encode(actManage, 1, 1),
		encode(actNoop), "garbage", "gs:5", "p:999999999999999999999",
	} {
		b := newTestBot()
		b.Handle(context.Background(), press(data))

		answers := slices.DeleteFunc(b.tg.methods(), func(m string) bool { return m != "answerCallbackQuery" })
		assert.Len(t, answers, 1, "%q", data)
	}
}

func TestStaleButtonsSaySo(t *testing.T) {
	b := newTestBot()
	b.Handle(context.Background(), press("garbage"))
	assert.Equal(t, msgStaleButton, b.tg.last("answerCallbackQuery").text)

	b.Handle(context.Background(), press(encode(actProduct, 404)))
	assert.True(t, strings.Contains(b.tg.last("answerCallbackQuery").text, "недоступний"))
}

// Dialog ids start at 1 in tests, so a test can name the button it presses.
func init() { firstJobID = func() int64 { return 0 } }
