package bot

import (
	"fmt"
	"strings"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/enrich"
	"github.com/aristodem96/ai_shop_bot/internal/telegram"
)

const (
	msgRequestSentToast = "Запит надіслано"
	msgAddedToCartToast = "Додано в кошик"
	msgAskQuote         = "Запит №%d (%d шт): напишіть у відповідь ціну за 1 шт у гривнях і термін доставки в днях, напр. <code>1450 3</code>"
	msgBadQuote         = "Не розібрав. Запит №%d: ціна за 1 шт і термін у днях, напр. <code>1450 3</code>"
	msgQuoteTooLate     = "Запит №%d уже закрито або ціну вже вказано."
	msgQuoteSent        = "✅ Ціну за запитом №%d надіслано покупцю. Після оплати прийде звичайне замовлення."
	msgQuoteUndelivered = "⚠️ Ціну за запитом №%d збережено, але покупцю не вдалося написати (можливо, він зупинив бота)."
	msgQuoteExpired     = "⌛️ Ця пропозиція вже не діє. Запитайте деталь ще раз, і менеджер уточнить ціну."
)

// leadDaysLabel is a lead time: "3 дні", or "у день оплати" for zero.
func leadDaysLabel(n int) string {
	if n == 0 {
		return "у день оплати"
	}
	return plural(n, "день", "дні", "днів")
}

// arrivalLabel says when a quoted part is at the shop.
func arrivalLabel(n int) string {
	if n == 0 {
		return "у день оплати"
	}
	return "через " + plural(n, "день", "дні", "днів") + " після оплати"
}

// partLines is a part as the messages of a request show it.
func partLines(name, brand, number string) string {
	return fmt.Sprintf("%s\n<b>%s %s</b>", esc(name), esc(brand), esc(number))
}

// requestQtyView explains ordering and asks how many.
func requestQtyView(p *domain.CatalogPart, car *domain.Vehicle, fits *enrich.Fits) (string, *telegram.InlineKeyboard) {
	var b strings.Builder
	b.WriteString("🔎 <b>Під замовлення</b>\n")
	b.WriteString(partLines(p.Name, p.Brand, p.Number))
	if car != nil {
		fmt.Fprintf(&b, "\nАвто: %s", esc(car.Label()))
	}
	b.WriteString(fitsBlock(fits))
	b.WriteString("\n\nЦієї деталі немає на складі. Менеджер знайде її в постачальників і надішле сюди ціну й термін, " +
		"а ви вирішите, чи замовляти. Запит ні до чого не зобов'язує.\n\nСкільки штук потрібно?")
	row := make([]telegram.InlineButton, 0, len(requestQtys))
	for _, n := range requestQtys {
		row = append(row, button(fmt.Sprintf("%d шт", n), actRequestQty, p.ID, n))
	}
	return b.String(), &telegram.InlineKeyboard{Rows: [][]telegram.InlineButton{row}}
}

// requestSentView is the buyer's message once the request is out.
func requestSentView(r *domain.PartRequest) string {
	return fmt.Sprintf("✅ <b>Запит №%d прийнято</b>\n%s × %d\n\nМенеджер шукає деталь у постачальників. "+
		"Ціну й термін надішлю сюди.", r.ID, partLines(r.Name, r.Brand, r.Number), r.Qty)
}

// adminRequestView puts a request before the managers.
func adminRequestView(r *domain.PartRequest) (string, *telegram.InlineKeyboard) {
	var b strings.Builder
	fmt.Fprintf(&b, "🔎 <b>Запит №%d</b> — під замовлення\n%s × %d\n", r.ID, partLines(r.Name, r.Brand, r.Number), r.Qty)
	if r.Vehicle != "" {
		fmt.Fprintf(&b, "Авто: %s\n", esc(r.Vehicle))
	}
	fmt.Fprintf(&b, `Покупець: <a href="tg://user?id=%d">написати</a>`, r.UserID)
	return b.String(), &telegram.InlineKeyboard{Rows: [][]telegram.InlineButton{{
		button("✅ Знайшли — вказати ціну", actRequestFound, r.ID),
		button("❌ Не знайшли", actRequestNone, r.ID),
	}}}
}

// quoteView offers the buyer the price.
func quoteView(r *domain.PartRequest) (string, *telegram.InlineKeyboard) {
	text := fmt.Sprintf("✅ <b>Знайшли деталь за запитом №%d</b>\n%s\n\nЦіна: %s × %d = <b>%s</b>\n"+
		"Буде в магазині: %s\n\nЦіна діє 24 години. Додайте деталь у кошик і оформіть замовлення — самовивіз з магазину або Нова Пошта.",
		r.ID, partLines(r.Name, r.Brand, r.Number), r.Price, r.Qty, r.Total(), arrivalLabel(r.LeadDays))
	return text, &telegram.InlineKeyboard{Rows: [][]telegram.InlineButton{{
		button("🛒 Додати в кошик", actRequestBuy, r.ID),
		button("✖️ Не потрібно", actRequestDecline, r.ID),
	}}}
}

// kitRequestsView is the buyer's message once a kit's parts are requested.
func kitRequestsView(reqs []*domain.PartRequest) string {
	var b strings.Builder
	b.WriteString("✅ <b>Запити на комплект прийнято</b>\n")
	for _, r := range reqs {
		fmt.Fprintf(&b, "\n№%d · %s · <b>%s %s</b> × %d", r.ID, esc(r.Name), esc(r.Brand), esc(r.Number), r.Qty)
	}
	b.WriteString("\n\nМенеджер знайде деталі в постачальників і надішле сюди ціну й термін на кожну. Ви вирішите, що брати.")
	return b.String()
}

// notFoundView tells the buyer no supplier has the part.
func notFoundView(r *domain.PartRequest) string {
	return fmt.Sprintf("😔 За запитом №%d деталь не знайшли в постачальників:\n%s\n\n"+
		"Напишіть, і я підберу аналог.", r.ID, partLines(r.Name, r.Brand, r.Number))
}
