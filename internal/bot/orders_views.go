package bot

import (
	"fmt"
	"strings"
	"time"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/telegram"
)

// kyiv is the zone order times are shown in. Falls back to UTC on a system
// without zone data, which only a misbuilt image would be.
var kyiv = func() *time.Location {
	loc, err := time.LoadLocation("Europe/Kyiv")
	if err != nil {
		return time.UTC
	}
	return loc
}()

var statusLabels = map[domain.OrderStatus]string{
	domain.OrderAwaitingPayment: "⏳ очікує оплати",
	domain.OrderPaid:            "💰 оплачено",
	domain.OrderConfirmed:       "✅ підтверджено",
	domain.OrderShipped:         "📦 відправлено",
	domain.OrderCancelled:       "✖️ скасовано",
	domain.OrderExpired:         "⌛ час оплати минув",
	domain.OrderNeedsRefund:     "↩️ повернення коштів",
}

func statusLabel(s domain.OrderStatus) string {
	if l, ok := statusLabels[s]; ok {
		return l
	}
	return string(s)
}

// adminMoves are the manager buttons; the number travels in the callback.
var adminMoves = map[int64]domain.OrderStatus{
	1: domain.OrderConfirmed,
	2: domain.OrderShipped,
	3: domain.OrderCancelled,
}

func contactsBlock(c domain.Contacts) string {
	return fmt.Sprintf("👤 %s\n📱 %s\n🚚 Нова Пошта: %s", esc(c.Name), esc(c.Phone), esc(c.Delivery))
}

func confirmView(cart *domain.Cart, c domain.Contacts) (string, *telegram.InlineKeyboard) {
	var b strings.Builder
	b.WriteString("🧾 <b>Перевірте замовлення</b>\n\n")
	for _, l := range cart.Lines {
		fmt.Fprintf(&b, "• %s (%s) — %d × %s\n", esc(l.ProductName), esc(l.Brand), l.Qty, l.Price)
	}
	fmt.Fprintf(&b, "\nРазом: <b>%s</b>\n\n%s\n\n", cart.Total(), contactsBlock(c))
	fmt.Fprintf(&b, "Після підтвердження товар резервується на %d хв, поки ви оплачуєте.", int(domain.ReservationTTL.Minutes()))
	return b.String(), &telegram.InlineKeyboard{Rows: [][]telegram.InlineButton{
		{button(fmt.Sprintf("💳 Оплатити %s", cart.Total()), actPay)},
		{button("✏️ Інші контакти", actEditContacts), button("✖️ Скасувати", actCancelCheckout)},
	}}
}

func paymentView(o *domain.Order) (string, *telegram.InlineKeyboard) {
	text := fmt.Sprintf("🧾 <b>Замовлення №%d</b> на суму <b>%s</b>\n\nТовар зарезервовано до %s. Оплатіть карткою, Apple Pay або Google Pay на сторінці monobank — і ми одразу повідомимо, що оплата пройшла.",
		o.ID, o.Total, o.ReservedUntil.In(kyiv).Format("15:04"))
	return text, &telegram.InlineKeyboard{Rows: [][]telegram.InlineButton{
		{{Text: fmt.Sprintf("💳 Оплатити %s", o.Total), URL: o.PaymentURL}},
	}}
}

func ordersView(orders []domain.Order) (string, telegram.ReplyMarkup) {
	if len(orders) == 0 {
		return msgNoOrders, nil
	}
	var b strings.Builder
	b.WriteString("📦 <b>Ваші замовлення</b>\n")
	kb := &telegram.InlineKeyboard{}
	for _, o := range orders {
		fmt.Fprintf(&b, "\n<b>№%d</b> від %s — %s\n%s\n", o.ID, o.CreatedAt.In(kyiv).Format("02.01 15:04"), o.Total, statusLabel(o.Status))
		for _, i := range o.Items {
			fmt.Fprintf(&b, "   • %s ×%d\n", esc(i.Name), i.Qty)
		}
		if o.Status == domain.OrderAwaitingPayment && o.PaymentURL != "" {
			kb.Rows = append(kb.Rows, []telegram.InlineButton{{Text: fmt.Sprintf("💳 Оплатити №%d", o.ID), URL: o.PaymentURL}})
		}
	}
	if len(kb.Rows) == 0 {
		return b.String(), nil
	}
	return b.String(), kb
}

// adminOrderView is an order as managers see it, with the moves its status
// allows.
func adminOrderView(o *domain.Order) (string, *telegram.InlineKeyboard) {
	var b strings.Builder
	fmt.Fprintf(&b, "🛒 <b>Замовлення №%d</b> — %s\n%s\n\n", o.ID, o.Total, statusLabel(o.Status))
	if o.Status == domain.OrderNeedsRefund {
		b.WriteString("❗ Оплата отримана, але товару вже немає або сума не збіглася. Поверніть кошти в кабінеті monobank.\n\n")
	}
	for _, i := range o.Items {
		fmt.Fprintf(&b, "• %s (%s) — %d × %s\n", esc(i.Name), esc(i.Brand), i.Qty, i.Price)
	}
	b.WriteString("\n" + contactsBlock(o.Contacts))
	if o.InvoiceID != "" {
		fmt.Fprintf(&b, "\n\nРахунок monobank: <code>%s</code>", esc(o.InvoiceID))
	}

	kb := &telegram.InlineKeyboard{}
	var row []telegram.InlineButton
	if o.Status.CanMoveTo(domain.OrderConfirmed) {
		row = append(row, button("✅ Підтвердити", actManage, o.ID, 1))
	}
	if o.Status.CanMoveTo(domain.OrderShipped) {
		row = append(row, button("📦 Відправлено", actManage, o.ID, 2))
	}
	if o.Status.CanMoveTo(domain.OrderCancelled) {
		row = append(row, button("✖️ Скасувати", actManage, o.ID, 3))
	}
	if len(row) > 0 {
		kb.Rows = append(kb.Rows, row)
	}
	return b.String(), kb
}

// buyerNotice is what the buyer is told when their order moves; empty for
// moves they need not hear about.
func buyerNotice(o *domain.Order, from domain.OrderStatus) (string, telegram.ReplyMarkup) {
	cartButton := &telegram.InlineKeyboard{Rows: [][]telegram.InlineButton{{button("🛒 Кошик", actCart)}}}
	switch o.Status {
	case domain.OrderPaid:
		return fmt.Sprintf("✅ Оплату отримано! Замовлення №%d на %s прийнято. Менеджер підтвердить його найближчим часом.", o.ID, o.Total), nil
	case domain.OrderConfirmed:
		return fmt.Sprintf("✅ Замовлення №%d підтверджено і готується до відправки.", o.ID), nil
	case domain.OrderShipped:
		return fmt.Sprintf("📦 Замовлення №%d відправлено Новою Поштою: %s.", o.ID, esc(o.Contacts.Delivery)), nil
	case domain.OrderExpired:
		return fmt.Sprintf("⌛ Час на оплату замовлення №%d минув. Товари повернуто в кошик — оформіть ще раз, коли будете готові.", o.ID), cartButton
	case domain.OrderCancelled:
		if from == domain.OrderAwaitingPayment {
			return fmt.Sprintf("Оплата замовлення №%d не пройшла. Товари повернуто в кошик — спробуйте ще раз.", o.ID), cartButton
		}
		return fmt.Sprintf("✖️ Замовлення №%d скасовано. Якщо ви його оплатили, кошти повернуться на картку.", o.ID), nil
	case domain.OrderNeedsRefund:
		return fmt.Sprintf("Оплату за замовлення №%d отримано, але товару вже немає в наявності. Кошти повернемо на вашу картку — менеджер зв'яжеться з вами.", o.ID), nil
	}
	return "", nil
}
