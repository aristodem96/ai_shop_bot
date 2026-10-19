package bot

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
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
