package bot

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/service"
	"github.com/aristodem96/ai_shop_bot/internal/telegram"
)

var brembo = domain.CatalogPart{ID: 31, Brand: "BREMBO", Number: "P 85 075", Name: "Гальмівні колодки", Category: "brake-pads"}

type fakeCatalogParts struct{}

func (fakeCatalogParts) CatalogPart(_ context.Context, id int64) (*domain.CatalogPart, error) {
	if id != brembo.ID {
		return nil, domain.ErrNotFound
	}
	p := brembo
	return &p, nil
}

// fakeRequests keeps requests in memory; a quote's product is the pads the
// fake cart sells.
type fakeRequests struct{ byID map[int64]*domain.PartRequest }

func (f *fakeRequests) Create(_ context.Context, r domain.PartRequest) (int64, error) {
	r.ID, r.Status = int64(len(f.byID)+1), domain.RequestNew
	f.byID[r.ID] = &r
	return r.ID, nil
}

func (f *fakeRequests) Get(_ context.Context, id int64) (*domain.PartRequest, error) {
	r, ok := f.byID[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	c := *r
	return &c, nil
}

func (f *fakeRequests) SetAdminMessage(_ context.Context, id, msg int64) error {
	f.byID[id].AdminMessageID = msg
	return nil
}

func (f *fakeRequests) move(ctx context.Context, id int64, from, to domain.RequestStatus) (*domain.PartRequest, error) {
	r, ok := f.byID[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	if r.Status != from {
		return nil, domain.ErrBadTransition
	}
	r.Status = to
	return f.Get(ctx, id)
}

func (f *fakeRequests) Quote(ctx context.Context, id int64, price domain.Money, days int, ttl time.Duration) (*domain.PartRequest, error) {
	if _, err := f.move(ctx, id, domain.RequestNew, domain.RequestQuoted); err != nil {
		return nil, err
	}
	r := f.byID[id]
	r.Price, r.LeadDays, r.ProductID, r.ExpiresAt = price, days, padsID, time.Now().Add(ttl)
	return f.Get(ctx, id)
}

func (f *fakeRequests) NotFound(ctx context.Context, id int64) (*domain.PartRequest, error) {
	return f.move(ctx, id, domain.RequestNew, domain.RequestNotFound)
}

func (f *fakeRequests) Decline(ctx context.Context, id int64) (*domain.PartRequest, error) {
	return f.move(ctx, id, domain.RequestQuoted, domain.RequestDeclined)
}

func withRequests() (testBot, *fakeRequests) {
	b, _, _ := withOrders()
	clear(b.cart.qty)
	b.garage.car = &octavia
	reqs := &fakeRequests{byID: map[int64]*domain.PartRequest{}}
	b.UseRequests(reqs, fakeCatalogParts{})
	return b, reqs
}

// replyIn is a manager's answer to the bot's question in the managers' chat.
func replyIn(question, answer string) telegram.Update {
	return telegram.Update{Message: &telegram.Message{
		From: &telegram.User{ID: 5}, Chat: telegram.Chat{ID: adminChat, Type: "supergroup"}, Text: answer,
		ReplyToMessage: &telegram.Message{Text: question},
	}}
}

func TestPartRequestFromAskToCart(t *testing.T) {
	b, reqs := withRequests()
	ctx := context.Background()

	_, kb := consultantView("Можемо замовити.", nil, []domain.CatalogPart{brembo}, false, false)
	require.Equal(t, []string{encode(actRequest, 31)}, callbacks(kb))

	b.Handle(ctx, press(encode(actRequest, 31)))
	ask := b.tg.last("sendMessage")
	assert.Contains(t, ask.text, "<b>BREMBO P 85 075</b>")
	assert.Contains(t, ask.text, "Skoda Octavia A7")
	assert.Equal(t, []string{encode(actRequestQty, 31, 1), encode(actRequestQty, 31, 2), encode(actRequestQty, 31, 4)}, callbacks(ask.markup))

	b.Handle(ctx, press(encode(actRequestQty, 31, 2)))
	assert.Contains(t, b.tg.last("editMessageText").text, "Запит №1 прийнято")
	toManagers := b.tg.last("sendMessage")
	assert.Equal(t, adminChat, toManagers.chat)
	assert.Contains(t, toManagers.text, "Запит №1")
	assert.Contains(t, toManagers.text, "× 2")
	assert.Equal(t, []string{encode(actRequestFound, 1), encode(actRequestNone, 1)}, callbacks(toManagers.markup))
	assert.Equal(t, "Skoda Octavia A7 1.4 TSI (2013–2020)", reqs.byID[1].Vehicle)

	b.Handle(ctx, pressIn(adminChat, encode(actRequestFound, 1)))
	question := b.tg.last("sendMessage")
	assert.IsType(t, &telegram.ForceReply{}, question.markup)
	assert.Equal(t, int64(901), reqs.byID[1].AdminMessageID)

	b.Handle(ctx, replyIn(question.text, "дорого"))
	assert.Contains(t, b.tg.last("sendMessage").text, "Не розібрав", "a reply that is no price is asked again")

	b.Handle(ctx, replyIn(question.text, "1450 3"))
	offer := b.tg.calls[len(b.tg.calls)-3]
	assert.Equal(t, chat, offer.chat, "the buyer gets the quote")
	assert.Contains(t, offer.text, "1\u202f450 грн × 2 = <b>2\u202f900 грн</b>")
	assert.Contains(t, offer.text, "3 дні")
	assert.Equal(t, []string{encode(actRequestBuy, 1), encode(actRequestDecline, 1)}, callbacks(offer.markup))
	marked := b.tg.last("editMessageReplyMarkup")
	assert.Equal(t, int64(901), marked.msg, "the managers' buttons say it is done")
	assert.Contains(t, b.tg.last("sendMessage").text, "надіслано покупцю")

	b.Handle(ctx, replyIn(question.text, "1300 2"))
	assert.Contains(t, b.tg.last("sendMessage").text, "уже закрито", "a second quote is refused")

	b.Handle(ctx, press(encode(actRequestBuy, 1)))
	assert.Equal(t, 2, b.cart.qty[padsID], "the quoted quantity goes to the cart")
	assert.Contains(t, b.tg.last("sendMessage").text, "Кошик")
}

func TestPartRequestNotFoundAndDecline(t *testing.T) {
	b, reqs := withRequests()
	ctx := context.Background()
	b.Handle(ctx, press(encode(actRequestQty, 31, 1)))
	b.Handle(ctx, press(encode(actRequestQty, 31, 1)))

	b.Handle(ctx, pressIn(adminChat, encode(actRequestNone, 1)))
	assert.Equal(t, domain.RequestNotFound, reqs.byID[1].Status)
	assert.Contains(t, b.tg.last("sendMessage").text, "не знайшли")
	assert.Equal(t, chat, b.tg.last("sendMessage").chat)

	_, err := reqs.Quote(ctx, 2, domain.UAH(900), 1, time.Hour)
	require.NoError(t, err)
	b.Handle(ctx, press(encode(actRequestDecline, 2)))
	assert.Equal(t, domain.RequestDeclined, reqs.byID[2].Status)
	b.Handle(ctx, press(encode(actRequestBuy, 2)))
	assert.Equal(t, msgQuoteExpired, b.tg.last("editMessageText").text)
	assert.Zero(t, b.cart.qty[padsID])
}

func TestPartRequestButtonsAreGuarded(t *testing.T) {
	b, reqs := withRequests()
	ctx := context.Background()
	b.Handle(ctx, press(encode(actRequestQty, 31, 1)))

	b.Handle(ctx, press(encode(actRequestNone, 1)))
	assert.Equal(t, domain.RequestNew, reqs.byID[1].Status, "a buyer cannot press a manager's button")

	_, err := reqs.Quote(ctx, 1, domain.UAH(900), 1, time.Hour)
	require.NoError(t, err)
	reqs.byID[1].UserID = 999
	b.Handle(ctx, press(encode(actRequestBuy, 1)))
	assert.Zero(t, b.cart.qty[padsID], "nor take another buyer's quote")
}

func TestParseQuote(t *testing.T) {
	for in, want := range map[string][2]int64{
		"1450 3": {145000, 3}, "1450,50 грн 3 дні": {145050, 3}, "  99.5 0 ": {9950, 0}, "2000грн 14дн": {200000, 14},
	} {
		price, days, ok := parseQuote(in)
		require.True(t, ok, in)
		assert.Equal(t, [2]int64{int64(price), int64(days)}, want, in)
	}
	for _, in := range []string{"", "1450", "дорого", "0 3", "1450 3 5"} {
		_, _, ok := parseQuote(in)
		assert.False(t, ok, in)
	}
}

// The part's card is sent as a photo when the catalogue has one, and a photo
// has a caption, not a text: Telegram refuses to edit its text.
func TestPartRequestFromAPhotoCard(t *testing.T) {
	b, reqs := withRequests()
	ctx := context.Background()
	onPhoto := press(encode(actRequestQty, 31, 1))
	onPhoto.CallbackQuery.Message.Photo = []telegram.PhotoSize{{FileID: "p"}}

	b.Handle(ctx, onPhoto)
	require.Len(t, reqs.byID, 1)
	assert.NotContains(t, b.tg.methods(), "editMessageText", "a photo has no text to edit")
	off := b.tg.last("editMessageReplyMarkup")
	assert.Equal(t, int64(900), off.msg, "the photo loses its buttons, so the request is not sent twice")
	assert.Nil(t, off.markup.(*telegram.InlineKeyboard))
	var toBuyer sent
	for _, c := range b.tg.calls {
		if c.method == "sendMessage" && c.chat == chat {
			toBuyer = c
		}
	}
	assert.Contains(t, toBuyer.text, "Запит №1 прийнято", "the buyer is told in a new message")
}

// A private chat does not always open the reply by itself: "1450 3" sent
// as a plain message still prices the request asked about last.
func TestQuoteWithoutReplying(t *testing.T) {
	b, reqs := withRequests()
	ctx := context.Background()
	b.Handle(ctx, press(encode(actRequestQty, 31, 1)))
	b.Handle(ctx, pressIn(adminChat, encode(actRequestFound, 1)))

	b.Handle(ctx, say(adminChat, "привіт"))
	assert.Equal(t, domain.RequestNew, reqs.byID[1].Status, "text that is no price is not taken for one")

	b.Handle(ctx, say(adminChat, "1450 3"))
	assert.Equal(t, domain.RequestQuoted, reqs.byID[1].Status, "priced without a reply")
	assert.Contains(t, b.tg.last("sendMessage").text, "надіслано покупцю")

	b.Handle(ctx, say(adminChat, "1300 2"))
	assert.Equal(t, domain.UAH(1450), reqs.byID[1].Price, "the wait ends with the price: a later number is not a second quote")
}

// emptyShelfKits is a kit with nothing in stock and two parts on order.
type emptyShelfKits struct{}

func (emptyShelfKits) Kit(context.Context, int64) (*service.Kit, error) {
	return &service.Kit{
		Vehicle: octavia,
		Missing: []string{domain.PartEngineOil, domain.PartOilFilter, domain.PartSparkPlug},
		OnOrder: []service.KitOrder{
			{PartType: domain.PartOilFilter, Qty: 1, Options: []domain.CatalogPart{{ID: 71, Name: "Масляний фільтр", Brand: "MANN-FILTER", Number: "W 712/95"}}},
			{PartType: domain.PartSparkPlug, Qty: 4, Options: []domain.CatalogPart{{ID: 81, Name: "Свічка запалювання", Brand: "NGK", Number: "BKR6E"}}},
		},
	}, nil
}

func TestKitOnOrderWhenTheShelvesAreEmpty(t *testing.T) {
	b, reqs := withRequests()
	b.kits = emptyShelfKits{}
	ctx := context.Background()

	b.Handle(ctx, press(encode(actKit)))
	view := b.tg.last("sendMessage")
	assert.Contains(t, view.text, "На складі для вашого авто зараз нічого з комплекту немає.", "said plainly: nothing is in stock")
	assert.Contains(t, view.text, "Під замовлення можемо привезти:", "and what can be ordered")
	assert.Contains(t, view.text, "<b>1.</b> 🔎 Масляний фільтр · MANN-FILTER W 712/95 — 1 шт.")
	assert.Contains(t, view.text, "<b>2.</b> 🔎 Свічка запалювання · NGK BKR6E — 4 шт.")
	assert.Contains(t, view.text, "Моторна олива:</b> для цього двигуна в довіднику немає норми", "oil is missing for a reason, and says which")
	assert.NotContains(t, view.text, "Немає в наявності для вашого авто: моторна олива", "not as if the rest were in stock")
	assert.Contains(t, callbacks(view.markup), encode(actKitOrder))
	assert.Contains(t, callbacks(view.markup), encode(actRequest, 81), "or one part at a time")

	b.Handle(ctx, press(encode(actKitOrder)))
	require.Len(t, reqs.byID, 2, "a request per part on order")
	assert.Equal(t, 4, reqs.byID[2].Qty, "in the kit's quantity")
	assert.Equal(t, "NGK", reqs.byID[2].Brand)
	toBuyer := ""
	for _, c := range b.tg.calls {
		if c.method == "sendMessage" && c.chat == chat {
			toBuyer = c.text
		}
	}
	assert.Contains(t, toBuyer, "Запити на комплект прийнято")
	assert.Contains(t, toBuyer, "№1 · Масляний фільтр")
	managers := 0
	for _, c := range b.tg.calls {
		if c.method == "sendMessage" && c.chat == adminChat {
			managers++
		}
	}
	assert.Equal(t, 2, managers, "each request goes to the managers")
}

func TestCallManagerTellsTheManagersWhoAndWhy(t *testing.T) {
	b, _ := withRequests()
	ctx := context.Background()
	b.Handle(ctx, text("/start")) // the bot learns the buyer's name from their messages

	sent, err := b.CallManager(ctx, chat, "Повернення: колодки не підійшли")
	require.NoError(t, err)
	assert.True(t, sent)
	msg := b.tg.last("sendMessage")
	assert.Equal(t, adminChat, msg.chat)
	assert.Contains(t, msg.text, "Покупець просить менеджера")
	assert.Contains(t, msg.text, "Повернення: колодки не підійшли")
	assert.Contains(t, msg.text, "Skoda Octavia A7", "the buyer's car")
	assert.Contains(t, msg.text, fmt.Sprintf("tg://user?id=%d", chat), "a way to write to them")

	sent, err = b.CallManager(ctx, chat, "Ще раз")
	require.NoError(t, err)
	assert.False(t, sent, "not again within ten minutes")
}
