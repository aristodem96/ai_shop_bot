package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/telegram"
)

type fakeSessions struct{ data map[int64][]byte }

func (f *fakeSessions) Load(_ context.Context, id int64, v any) (bool, error) {
	raw, ok := f.data[id]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(raw, v)
}

func (f *fakeSessions) Save(_ context.Context, id int64, v any) error {
	raw, err := json.Marshal(v)
	f.data[id] = raw
	return err
}

func (f *fakeSessions) Delete(_ context.Context, id int64) error {
	delete(f.data, id)
	return nil
}

type fakeOrders struct {
	saved      *domain.Contacts
	placed     []domain.Contacts
	checkoutFn func() error
	moves      []domain.OrderStatus
	order      domain.Order
}

func (f *fakeOrders) Checkout(_ context.Context, user int64, c domain.Contacts) (*domain.Order, error) {
	if f.checkoutFn != nil {
		if err := f.checkoutFn(); err != nil {
			return nil, err
		}
	}
	f.placed = append(f.placed, c)
	return &domain.Order{ID: 42, UserID: user, Status: domain.OrderAwaitingPayment, Total: domain.UAH(1450),
		Contacts: c, PaymentURL: "https://pay.mbnk.biz/p2", ReservedUntil: time.Now().Add(15 * time.Minute)}, nil
}

func (f *fakeOrders) OrdersOf(context.Context, int64, int) ([]domain.Order, error) {
	return []domain.Order{f.order}, nil
}

func (f *fakeOrders) Order(context.Context, int64) (*domain.Order, error) { return &f.order, nil }

func (f *fakeOrders) SetStatus(_ context.Context, _ int64, to domain.OrderStatus) (*domain.Order, error) {
	if !f.order.Status.CanMoveTo(to) {
		return nil, domain.ErrBadTransition
	}
	f.moves = append(f.moves, to)
	f.order.Status = to
	return &f.order, nil
}

func (f *fakeOrders) Customer(context.Context, int64) (*domain.Contacts, error) { return f.saved, nil }

func (f *fakeOrders) CancelUnpaid(_ context.Context, user, id int64) (*domain.Order, error) {
	if f.order.Status != domain.OrderAwaitingPayment {
		return &f.order, domain.ErrBadTransition
	}
	f.order.Status = domain.OrderCancelled
	return &f.order, nil
}

const adminChat = int64(-100500)

func withOrders() (testBot, *fakeOrders, *fakeSessions) {
	b := newTestBot()
	orders := &fakeOrders{order: domain.Order{ID: 42, UserID: chat, Status: domain.OrderPaid, Total: domain.UAH(1450),
		Items:    []domain.OrderItem{{Name: "Колодки", Brand: "AutoLine", Price: domain.UAH(1450), Qty: 1}},
		Contacts: domain.Contacts{Name: "Тарас", Phone: "+380671234567", Delivery: "Київ, відділення 25"}}}
	sessions := &fakeSessions{data: map[int64][]byte{}}
	b.UseOrders(orders, sessions, adminChat)
	b.cart.qty[padsID] = 1
	return b, orders, sessions
}

func TestCheckoutForm(t *testing.T) {
	b, orders, sessions := withOrders()
	ctx := context.Background()

	b.Handle(ctx, press(encode(actCheckout)))
	assert.Equal(t, msgAskName, b.tg.last("sendMessage").text)

	b.Handle(ctx, text("Тарас Шевченко"))
	assert.Equal(t, msgAskPhone, b.tg.last("sendMessage").text)
	kb := b.tg.last("sendMessage").markup.(*telegram.ReplyKeyboard)
	assert.True(t, kb.Rows[0][0].RequestContact, "the phone can be shared with one tap")

	b.Handle(ctx, text("12345"))
	assert.Equal(t, msgBadPhone, b.tg.last("sendMessage").text, "a bad number is asked again")

	shared := text("")
	shared.Message.Contact = &telegram.Contact{PhoneNumber: "380671234567", UserID: chat}
	b.Handle(ctx, shared)
	assert.Equal(t, msgAskDelivery, b.tg.last("sendMessage").text)

	b.Handle(ctx, text("Київ, відділення 25"))
	confirm := b.tg.last("sendMessage")
	assert.Contains(t, confirm.text, "Перевірте замовлення")
	assert.Contains(t, confirm.text, "+380671234567")
	assert.Contains(t, callbacks(confirm.markup), encode(actPay))

	b.Handle(ctx, press(encode(actPay)))
	require.Len(t, orders.placed, 1)
	assert.Equal(t, domain.Contacts{Name: "Тарас Шевченко", Phone: "+380671234567", Delivery: "Київ, відділення 25"}, orders.placed[0])
	pay := b.tg.last("editMessageText")
	assert.Contains(t, pay.text, "Замовлення №42")
	assert.Equal(t, "https://pay.mbnk.biz/p2", pay.markup.(*telegram.InlineKeyboard).Rows[0][0].URL)
	assert.Empty(t, sessions.data, "the form is done")

	b.Handle(ctx, press(encode(actPay)))
	assert.Len(t, orders.placed, 1, "a second tap on the same confirm places nothing")
}

func TestCheckoutWithSavedContactsGoesStraightToConfirm(t *testing.T) {
	b, orders, _ := withOrders()
	orders.saved = &domain.Contacts{Name: "Тарас", Phone: "+380671234567", Delivery: "Львів, відділення 3"}

	b.Handle(context.Background(), press(encode(actCheckout)))
	confirm := b.tg.last("sendMessage")
	assert.Contains(t, confirm.text, "Львів, відділення 3")
	assert.Contains(t, callbacks(confirm.markup), encode(actEditContacts))
}

func TestCheckoutRefusesSomeoneElsesContact(t *testing.T) {
	b, _, sessions := withOrders()
	ctx := context.Background()
	b.Handle(ctx, press(encode(actCheckout)))
	b.Handle(ctx, text("Тарас Шевченко"))

	shared := text("")
	shared.Message.Contact = &telegram.Contact{PhoneNumber: "380501112233", UserID: 999}
	b.Handle(ctx, shared)
	assert.Equal(t, msgOwnPhone, b.tg.last("sendMessage").text)

	var st checkoutState
	ok, _ := sessions.Load(ctx, chat, &st)
	require.True(t, ok)
	assert.Equal(t, stepPhone, st.Step, "still waiting for the buyer's own number")
}

func TestMenuButtonLeavesCheckout(t *testing.T) {
	b, _, sessions := withOrders()
	ctx := context.Background()
	b.Handle(ctx, press(encode(actCheckout)))

	b.Handle(ctx, text(btnCatalog))
	assert.Empty(t, sessions.data)
	assert.Contains(t, b.tg.last("sendMessage").text, "Каталог", "the button does what it says")
}

func TestCheckoutOutOfStockNamesThePart(t *testing.T) {
	b, orders, sessions := withOrders()
	orders.saved = &domain.Contacts{Name: "Тарас", Phone: "+380671234567", Delivery: "Київ, відділення 25"}
	orders.checkoutFn = func() error {
		return fmt.Errorf("%w: Колодки гальмівні передні (AutoLine)", domain.ErrOutOfStock)
	}
	ctx := context.Background()
	b.Handle(ctx, press(encode(actCheckout)))
	b.Handle(ctx, press(encode(actPay)))

	answer := b.tg.last("answerCallbackQuery")
	assert.Contains(t, answer.text, "Колодки гальмівні передні (AutoLine) вже немає")
	assert.True(t, answer.alert)
	assert.NotEmpty(t, sessions.data, "the buyer can fix the cart and pay again")
}

func TestManagerButtons(t *testing.T) {
	b, orders, _ := withOrders()
	ctx := context.Background()
	inAdmin := func(data string) telegram.Update {
		u := press(data)
		u.CallbackQuery.Message.Chat.ID = adminChat
		return u
	}

	b.Handle(ctx, inAdmin(encode(actManage, 42, 1)))
	assert.Equal(t, []domain.OrderStatus{domain.OrderConfirmed}, orders.moves)
	edit := b.tg.last("editMessageText")
	assert.Contains(t, edit.text, "підтверджено")
	assert.Contains(t, callbacks(edit.markup), encode(actManage, 42, 2), "next: ship")

	b.Handle(ctx, inAdmin(encode(actManage, 42, 1)))
	assert.Equal(t, msgBadTransition, b.tg.last("answerCallbackQuery").text)
}

func TestManagerButtonsOutsideTheAdminChat(t *testing.T) {
	b, orders, _ := withOrders()
	b.Handle(context.Background(), press(encode(actManage, 42, 3)))

	assert.Empty(t, orders.moves, "a forged callback from a buyer's chat changes nothing")
	assert.Equal(t, msgStaleButton, b.tg.last("answerCallbackQuery").text)
}

func TestMyOrders(t *testing.T) {
	b, orders, _ := withOrders()
	orders.order.Status = domain.OrderAwaitingPayment
	orders.order.PaymentURL = "https://pay.mbnk.biz/p2"
	b.Handle(context.Background(), text(btnOrders))

	msg := b.tg.last("sendMessage")
	assert.Contains(t, msg.text, "№42")
	assert.Contains(t, msg.text, "очікує оплати")
	assert.Equal(t, "https://pay.mbnk.biz/p2", msg.markup.(*telegram.InlineKeyboard).Rows[0][0].URL)
}

func TestNotifier(t *testing.T) {
	tg := &fakeTelegram{}
	n := NewNotifier(tg, adminChat, slog.New(slog.NewTextHandler(io.Discard, nil)))
	o := &domain.Order{ID: 7, UserID: chat, Status: domain.OrderPaid, Total: domain.UAH(1450)}

	n.OrderChanged(context.Background(), o, domain.OrderAwaitingPayment)
	require.Len(t, tg.calls, 2)
	assert.Equal(t, chat, tg.calls[0].chat)
	assert.Contains(t, tg.calls[0].text, "Оплату отримано")
	assert.Equal(t, adminChat, tg.calls[1].chat, "managers get the paid order")
	assert.Contains(t, callbacks(tg.calls[1].markup), encode(actManage, 7, 1))

	tg.calls = nil
	o.Status = domain.OrderExpired
	n.OrderChanged(context.Background(), o, domain.OrderAwaitingPayment)
	require.Len(t, tg.calls, 1, "an expired order is the buyer's business only")
	assert.Contains(t, tg.calls[0].text, "Час на оплату")
}

const shopAddress = "Київ, вул. Автозаводська 1, пн–сб 9:00–19:00"

func TestCheckoutPickup(t *testing.T) {
	b, orders, _ := withOrders()
	b.cfg.PickupAddress = shopAddress
	ctx := context.Background()

	b.Handle(ctx, press(encode(actCheckout)))
	b.Handle(ctx, text("Тарас Шевченко"))
	b.Handle(ctx, text("067 123 45 67"))
	ask := b.tg.last("sendMessage")
	assert.Contains(t, ask.text, shopAddress, "the buyer sees where the shop is before choosing")
	rows := ask.markup.(*telegram.ReplyKeyboard).Rows
	assert.Equal(t, btnPickup, rows[0][0].Text)
	assert.Equal(t, btnNovaPoshta, rows[1][0].Text)

	b.Handle(ctx, text("щось інше"))
	assert.Equal(t, msgPickMethod, b.tg.last("sendMessage").text)

	b.Handle(ctx, text(btnPickup))
	confirm := b.tg.last("sendMessage")
	assert.Contains(t, confirm.text, "🏪 Самовивіз: "+shopAddress, "no city or branch is asked")

	b.Handle(ctx, press(encode(actPay)))
	require.Len(t, orders.placed, 1)
	assert.Equal(t, domain.Contacts{Name: "Тарас Шевченко", Phone: "+380671234567", Delivery: shopAddress, Pickup: true}, orders.placed[0])
}

func TestCheckoutNovaPoshtaWhenPickupIsOffered(t *testing.T) {
	b, orders, _ := withOrders()
	b.cfg.PickupAddress = shopAddress
	ctx := context.Background()

	b.Handle(ctx, press(encode(actCheckout)))
	b.Handle(ctx, text("Тарас Шевченко"))
	b.Handle(ctx, text("067 123 45 67"))
	b.Handle(ctx, text(btnNovaPoshta))
	assert.Equal(t, msgAskDelivery, b.tg.last("sendMessage").text)
	b.Handle(ctx, text("Київ, відділення 25"))
	b.Handle(ctx, press(encode(actPay)))
	require.Len(t, orders.placed, 1)
	assert.False(t, orders.placed[0].Pickup)
	assert.Equal(t, "Київ, відділення 25", orders.placed[0].Delivery)
}

func TestSavedPickupGetsTodaysAddress(t *testing.T) {
	b, orders, _ := withOrders()
	b.cfg.PickupAddress = shopAddress
	orders.saved = &domain.Contacts{Name: "Тарас", Phone: "+380671234567", Delivery: "стара адреса", Pickup: true}

	b.Handle(context.Background(), press(encode(actCheckout)))
	confirm := b.tg.last("sendMessage")
	assert.Contains(t, confirm.text, "🏪 Самовивіз: "+shopAddress)
	assert.NotContains(t, confirm.text, "стара адреса")
}

func TestPickupOrderForManagersAndBuyer(t *testing.T) {
	o := &domain.Order{ID: 42, Status: domain.OrderConfirmed, Total: domain.UAH(1450),
		Contacts: domain.Contacts{Name: "Тарас", Phone: "+380671234567", Delivery: shopAddress, Pickup: true}}

	text, kb := adminOrderView(o)
	assert.Contains(t, text, "🏪 Самовивіз: "+shopAddress)
	assert.Equal(t, "🏪 Видано", kb.Rows[0][0].Text, "a pickup order is handed over, not shipped")

	notice, _ := buyerNotice(o, domain.OrderPaid)
	assert.Contains(t, notice, "готове! Можна забирати: "+shopAddress)

	o.Status = domain.OrderShipped
	notice, _ = buyerNotice(o, domain.OrderConfirmed)
	assert.Contains(t, notice, "видано")
	assert.Equal(t, "🏪 видано", orderStatusLabel(o))
}

func TestBuyerCancelsBeforePaying(t *testing.T) {
	b, orders, _ := withOrders()
	ctx := context.Background()
	orders.order.Status, orders.order.PaymentURL = domain.OrderAwaitingPayment, "https://pay.mbnk.biz/p2"

	text, kb := paymentView(&orders.order)
	assert.Contains(t, text, "Оплатіть")
	assert.Contains(t, callbacks(kb), encode(actOrderCancel, 42, 0), "the payment message can be cancelled")
	_, list := ordersView([]domain.Order{orders.order})
	assert.Contains(t, callbacks(list), encode(actOrderCancel, 42, 1), "and so can the order in the list")

	b.Handle(ctx, press(encode(actOrderCancel, 42, 0)))
	assert.Equal(t, domain.OrderCancelled, orders.order.Status)
	assert.Contains(t, b.tg.last("editMessageText").text, "Замовлення №42 скасовано. Товари повернуто в кошик")
	assert.Equal(t, msgOrderCancelledToast, b.tg.last("answerCallbackQuery").text)

	b.Handle(ctx, press(encode(actOrderCancel, 42, 1)))
	assert.Contains(t, b.tg.last("answerCallbackQuery").text, "вже не можна скасувати", "a second tap says why")
}
