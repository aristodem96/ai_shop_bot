package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/service"
	"github.com/aristodem96/ai_shop_bot/internal/telegram"
)

// Orders is what the bot needs from the order service.
type Orders interface {
	Checkout(ctx context.Context, userID int64, c domain.Contacts) (*domain.Order, error)
	OrdersOf(ctx context.Context, userID int64, limit int) ([]domain.Order, error)
	Order(ctx context.Context, id int64) (*domain.Order, error)
	SetStatus(ctx context.Context, orderID int64, to domain.OrderStatus) (*domain.Order, error)
	Customer(ctx context.Context, userID int64) (*domain.Contacts, error)
}

// Sessions keeps per-chat state between messages.
type Sessions interface {
	Load(ctx context.Context, id int64, v any) (bool, error)
	Save(ctx context.Context, id int64, v any) error
	Delete(ctx context.Context, id int64) error
}

// UseOrders turns on checkout, the buyer's order list and the admin chat.
// adminChat is where managers get paid orders; zero turns that off.
func (b *Bot) UseOrders(orders Orders, sessions Sessions, adminChat int64) {
	b.orders, b.sessions, b.adminChat = orders, sessions, adminChat
}

// Checkout is deterministic, not conversational: name, phone, how to get
// the order (pickup or Nova Poshta), the branch for Nova Poshta, confirm. A form is what a payment needs -- exact fields, validated -- and a
// model in that path would only add cost and a way to get the fields wrong.
type checkoutStep string

const (
	stepName     checkoutStep = "name"
	stepPhone    checkoutStep = "phone"
	stepMethod   checkoutStep = "method"
	stepDelivery checkoutStep = "delivery"
	stepConfirm  checkoutStep = "confirm"
)

type checkoutState struct {
	Step     checkoutStep    `json:"step"`
	Contacts domain.Contacts `json:"contacts"`
}

// btnCancelCheckout leaves the checkout from the reply keyboard.
const btnCancelCheckout = "✖️ Скасувати оформлення"

// The two ways to get an order.
const (
	btnPickup     = "🏪 Самовивіз з магазину"
	btnNovaPoshta = "🚚 Нова Пошта"
)

func methodKeyboard() *telegram.ReplyKeyboard {
	return &telegram.ReplyKeyboard{Resize: true, Rows: [][]telegram.KeyboardButton{
		{{Text: btnPickup}}, {{Text: btnNovaPoshta}}, {{Text: btnCancelCheckout}},
	}}
}

func checkoutKeyboard(askPhone bool) *telegram.ReplyKeyboard {
	rows := [][]telegram.KeyboardButton{{{Text: btnCancelCheckout}}}
	if askPhone {
		rows = append([][]telegram.KeyboardButton{{{Text: "📱 Поділитися номером", RequestContact: true}}}, rows...)
	}
	return &telegram.ReplyKeyboard{Rows: rows, Resize: true}
}

// startCheckout begins the form, or skips it for a buyer whose contacts are
// already known.
func (b *Bot) startCheckout(ctx context.Context, cc callbackCtx) (string, error) {
	cart, err := b.cart.Get(ctx, cc.user)
	if err != nil {
		return "", err
	}
	if len(cart.Lines) == 0 {
		return "", domain.ErrCartEmpty
	}

	saved, err := b.orders.Customer(ctx, cc.user)
	if err != nil {
		return "", err
	}
	if saved != nil && saved.Pickup && b.cfg.PickupAddress == "" {
		saved = nil // pickup is no longer offered: ask again
	}
	if saved != nil {
		if saved.Pickup {
			// The address the buyer is sent to is today's, not the one
			// from their last order.
			saved.Delivery = b.cfg.PickupAddress
		}
		st := checkoutState{Step: stepConfirm, Contacts: *saved}
		if err := b.sessions.Save(ctx, cc.chat, st); err != nil {
			return "", err
		}
		text, kb := confirmView(cart, st.Contacts)
		return "", b.tg.SendMessage(ctx, cc.chat, text, kb)
	}
	return "", b.askContacts(ctx, cc.chat)
}

func (b *Bot) askContacts(ctx context.Context, chat int64) error {
	if err := b.sessions.Save(ctx, chat, checkoutState{Step: stepName}); err != nil {
		return err
	}
	return b.tg.SendMessage(ctx, chat, msgAskName, checkoutKeyboard(false))
}

// inCheckout reports whether the chat is filling the checkout form. A command
// or a main menu button leaves the form: the buyer has moved on.
func (b *Bot) inCheckout(ctx context.Context, chat int64, m *telegram.Message) (*checkoutState, bool) {
	if b.orders == nil {
		return nil, false
	}
	var st checkoutState
	ok, err := b.sessions.Load(ctx, chat, &st)
	if err != nil {
		b.log.WarnContext(ctx, "could not read checkout state", slog.Any("error", err))
		return nil, false
	}
	if !ok || st.Step == stepConfirm {
		return nil, false
	}
	switch m.Text {
	case btnCatalog, btnCart, btnCar, btnHelp, btnOrders:
		_ = b.sessions.Delete(ctx, chat)
		return nil, false
	}
	if strings.HasPrefix(m.Text, "/") && m.Text != "/cancel" {
		_ = b.sessions.Delete(ctx, chat)
		return nil, false
	}
	return &st, true
}

// checkoutInput takes one answer of the form.
func (b *Bot) checkoutInput(ctx context.Context, chat, user int64, m *telegram.Message, st *checkoutState) {
	if m.Text == btnCancelCheckout || m.Text == "/cancel" {
		_ = b.sessions.Delete(ctx, chat)
		b.send(ctx, chat, msgCheckoutCancelled, b.mainMenu())
		return
	}
	text := strings.TrimSpace(m.Text)

	switch st.Step {
	case stepName:
		c, err := service.ValidateContacts(domain.Contacts{Name: text, Phone: "0000000000", Delivery: "xxxxx"})
		if err != nil || text == "" {
			b.send(ctx, chat, msgBadName, nil)
			return
		}
		st.Contacts.Name, st.Step = c.Name, stepPhone
		b.saveCheckout(ctx, chat, st, msgAskPhone, checkoutKeyboard(true))

	case stepPhone:
		raw := text
		if m.Contact != nil {
			// A shared contact must be the buyer's own: anyone can share
			// someone else's card from their address book.
			if m.Contact.UserID != 0 && m.Contact.UserID != user {
				b.send(ctx, chat, msgOwnPhone, nil)
				return
			}
			raw = m.Contact.PhoneNumber
		}
		phone, ok := domain.NormalizePhone(raw)
		if !ok {
			b.send(ctx, chat, msgBadPhone, nil)
			return
		}
		st.Contacts.Phone = phone
		if b.cfg.PickupAddress == "" {
			st.Step = stepDelivery
			b.saveCheckout(ctx, chat, st, msgAskDelivery, checkoutKeyboard(false))
			return
		}
		st.Step = stepMethod
		b.saveCheckout(ctx, chat, st, fmt.Sprintf(msgAskMethod, esc(b.cfg.PickupAddress)), methodKeyboard())

	case stepMethod:
		switch text {
		case btnPickup:
			st.Contacts.Pickup, st.Contacts.Delivery = true, b.cfg.PickupAddress
			b.confirmCheckout(ctx, chat, user, st)
		case btnNovaPoshta:
			st.Contacts.Pickup, st.Step = false, stepDelivery
			b.saveCheckout(ctx, chat, st, msgAskDelivery, checkoutKeyboard(false))
		default:
			b.send(ctx, chat, msgPickMethod, methodKeyboard())
		}

	case stepDelivery:
		st.Contacts.Delivery = text
		if _, err := service.ValidateContacts(st.Contacts); err != nil {
			b.send(ctx, chat, msgBadDelivery, nil)
			return
		}
		b.confirmCheckout(ctx, chat, user, st)
	}
}

// confirmCheckout ends the form: the contacts are checked and saved with the
// session, and the order is shown for confirmation.
func (b *Bot) confirmCheckout(ctx context.Context, chat, user int64, st *checkoutState) {
	c, err := service.ValidateContacts(st.Contacts)
	if err != nil {
		b.fail(ctx, chat, "validate contacts", err)
		return
	}
	st.Contacts, st.Step = c, stepConfirm
	cart, err := b.cart.Get(ctx, user)
	if err != nil {
		b.fail(ctx, chat, "get cart", err)
		return
	}
	if err := b.sessions.Save(ctx, chat, st); err != nil {
		b.fail(ctx, chat, "save checkout", err)
		return
	}
	// The reply keyboard goes back to the menu before the inline confirm.
	b.send(ctx, chat, msgContactsSaved, b.mainMenu())
	confirm, kb := confirmView(cart, st.Contacts)
	b.send(ctx, chat, confirm, kb)
}

func (b *Bot) saveCheckout(ctx context.Context, chat int64, st *checkoutState, ask string, kb telegram.ReplyMarkup) {
	if err := b.sessions.Save(ctx, chat, st); err != nil {
		b.fail(ctx, chat, "save checkout", err)
		return
	}
	b.send(ctx, chat, ask, kb)
}

// pay places the order and sends its payment link.
func (b *Bot) pay(ctx context.Context, cc callbackCtx) (string, error) {
	var st checkoutState
	ok, err := b.sessions.Load(ctx, cc.chat, &st)
	if err != nil {
		return "", err
	}
	if !ok || st.Step != stepConfirm {
		return "", errBadCallback // a confirm button from a checkout that ended
	}

	order, err := b.orders.Checkout(ctx, cc.user, st.Contacts)
	switch {
	case errors.Is(err, domain.ErrOutOfStock):
		// The error names the part; the buyer fixes the cart and retries.
		return "", err
	case err != nil && !errors.Is(err, domain.ErrCartEmpty) && !errors.Is(err, domain.ErrInvalidContacts):
		b.log.ErrorContext(ctx, "checkout failed", slog.Int64("user_id", cc.user), slog.Any("error", err))
		return "", errPaymentUnavailable
	case err != nil:
		return "", err
	}
	_ = b.sessions.Delete(ctx, cc.chat)

	text, kb := paymentView(order)
	return "", b.editText(ctx, cc, text, kb)
}

var errPaymentUnavailable = errors.New("bot: payment provider unavailable")

func (b *Bot) cancelCheckout(ctx context.Context, cc callbackCtx) error {
	_ = b.sessions.Delete(ctx, cc.chat)
	return b.editText(ctx, cc, msgCheckoutCancelled, &telegram.InlineKeyboard{Rows: [][]telegram.InlineButton{
		{button(btnCart, actCart)},
	}})
}

// maxOrdersListed is how many recent orders "my orders" shows.
const maxOrdersListed = 5

func (b *Bot) sendOrders(ctx context.Context, chat, user int64) {
	if b.orders == nil {
		b.send(ctx, chat, msgNoOrders, nil)
		return
	}
	orders, err := b.orders.OrdersOf(ctx, user, maxOrdersListed)
	if err != nil {
		b.fail(ctx, chat, "list orders", err)
		return
	}
	text, kb := ordersView(orders)
	b.send(ctx, chat, text, kb)
}

// manage is a manager's button in the admin chat. The chat is checked: the
// buttons only ever appear there, but callback data is client-supplied, and
// an order's status must not be one forged callback away from anyone.
func (b *Bot) manage(ctx context.Context, cc callbackCtx, orderID int64, move int64) (string, error) {
	if b.adminChat == 0 || cc.chat != b.adminChat {
		return "", errBadCallback
	}
	to, ok := adminMoves[move]
	if !ok {
		return "", errBadCallback
	}
	o, err := b.orders.SetStatus(ctx, orderID, to)
	if err != nil {
		return "", err
	}
	text, kb := adminOrderView(o)
	if err := b.editText(ctx, cc, text, kb); err != nil {
		return "", err
	}
	return "Статус: " + orderStatusLabel(o), nil
}
