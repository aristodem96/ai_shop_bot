package bot

import (
	"fmt"
	"strings"

	"github.com/aristodem96/ai_shop_bot/internal/reports"
	"github.com/aristodem96/ai_shop_bot/internal/telegram"
)

const (
	msgReportPick      = "📊 За який період звіт?"
	msgReportAskPrefix = "📅 Період звіту"
	msgReportAsk       = msgReportAskPrefix + ": напишіть у відповідь дати, наприклад <code>01.10–15.10</code> або <code>15.10.2026</code> — один день."
	msgReportFile      = "Формую файл…"
)

// reportShown bounds the products and categories the message lists; the
// workbook has them all.
const reportShown = 5

func periodKeyboard() *telegram.InlineKeyboard {
	kb := &telegram.InlineKeyboard{}
	row := make([]telegram.InlineButton, 0, 2)
	for _, p := range reports.Presets {
		row = append(row, button(p.Label, actReport, int64(p.Preset)))
		if len(row) == 2 {
			kb.Rows = append(kb.Rows, row)
			row = make([]telegram.InlineButton, 0, 2)
		}
	}
	kb.Rows = append(kb.Rows, []telegram.InlineButton{button("📅 Свій період", actReport, customPeriod)})
	return kb
}

// reportView is the report as a message, with the workbook and the other
// periods under it.
func reportView(r *reports.Report) (string, *telegram.InlineKeyboard) {
	var b strings.Builder
	fmt.Fprintf(&b, "📊 <b>Звіт: %s</b>\n\n", esc(r.Period.Label))
	if r.Orders == 0 {
		b.WriteString("Оплачених замовлень за цей період немає.\n")
	} else {
		fmt.Fprintf(&b, "💰 Виручка: <b>%s</b> · %s · середній чек %s\n", r.Revenue,
			plural(r.Orders, "замовлення", "замовлення", "замовлень"), r.AverageCheck())
		fmt.Fprintf(&b, "🏪 Самовивіз: %s · 🚚 Нова Пошта: %s\n", num(r.Pickup), num(r.Delivery))
		if r.OnOrder > 0 {
			fmt.Fprintf(&b, "📦 Продано під замовлення на %s — їх треба замовити в постачальників\n", r.OnOrder)
		}
	}
	if r.Refunds > 0 {
		fmt.Fprintf(&b, "↩️ Повернення: %s на %s\n", num(r.Refunds), r.RefundSum)
	}
	if r.Unpaid > 0 {
		fmt.Fprintf(&b, "⏳ Створено й не оплачено: %s\n", plural(r.Unpaid, "замовлення", "замовлення", "замовлень"))
	}

	if len(r.Products) > 0 {
		b.WriteString("\n🏆 <b>Найбільше продано</b>\n")
		for i, l := range r.Products[:min(len(r.Products), reportShown)] {
			name := l.Name
			if l.Brand != "" {
				name += " · " + l.Brand
			}
			fmt.Fprintf(&b, "%d. %s — %d шт., %s\n", i+1, esc(clip(name, 60)), l.Qty, l.Sum)
		}
	}
	if len(r.Categories) > 0 {
		b.WriteString("\n🗂 <b>За категоріями</b>\n")
		for _, l := range r.Categories[:min(len(r.Categories), reportShown)] {
			fmt.Fprintf(&b, "• %s — %s\n", esc(l.Name), l.Sum)
		}
	}
	if q := r.Requests; q.Made > 0 {
		fmt.Fprintf(&b, "\n🚚 <b>Запити під замовлення: %s</b>\nзнайшли й назвали ціну %s · оплатили %s · не знайшли %s",
			num(q.Made), num(q.Quoted), num(q.Paid), num(q.NotFound))
		if q.Waiting > 0 {
			fmt.Fprintf(&b, " · ще чекають менеджера %s", num(q.Waiting))
		}
		b.WriteString("\n")
	}

	if marked(r.Sources) {
		b.WriteString("\n📍 <b>Звідки прийшли покупці</b>\n")
		for _, s := range r.Sources {
			fmt.Fprintf(&b, "• %s — нових %s · замовлень %s на %s\n", esc(sourceLabel(s.Mark)), num(s.NewBuyers), num(s.Orders), s.Revenue)
		}
	}

	kb := &telegram.InlineKeyboard{}
	if r.Orders > 0 || r.Refunds > 0 || r.Requests.Made > 0 {
		kb.Rows = append(kb.Rows, []telegram.InlineButton{
			button("📥 Excel", actReportExcel, r.Period.From.Unix(), r.Period.To.Unix()),
		})
	}
	kb.Rows = append(kb.Rows, periodKeyboard().Rows...)
	return b.String(), kb
}

// marked reports whether any buyer came by a marked link: until the shop
// hands out links, every buyer is unmarked and the section says nothing.
func marked(sources []reports.Source) bool {
	for _, s := range sources {
		if s.Mark != "" {
			return true
		}
	}
	return false
}

// sourceLabel names a channel in the report.
func sourceLabel(mark string) string {
	if mark == "" {
		return "без мітки"
	}
	return mark
}
