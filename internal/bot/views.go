package bot

import (
	"fmt"
	"html"
	"strconv"
	"strings"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/service"
	"github.com/aristodem96/ai_shop_bot/internal/telegram"
)

// Views turn data into a message and its buttons. They are pure functions:
// no I/O, no state, so every screen of the bot is testable as a string.

// Every catalogue value goes through html.EscapeString before it reaches a
// message: messages are sent with parse_mode=HTML, and a product named
// "Tee <3" would otherwise make Telegram reject the whole message.
var esc = html.EscapeString

func button(text string, act action, args ...int64) telegram.InlineButton {
	return telegram.InlineButton{Text: text, CallbackData: encode(act, args...)}
}

func mainMenu() *telegram.ReplyKeyboard {
	return &telegram.ReplyKeyboard{
		Rows: [][]telegram.KeyboardButton{
			{{Text: btnCatalog}, {Text: btnCart}},
			{{Text: btnHelp}},
		},
		Resize:     true,
		Persistent: true,
	}
}

func categoriesView(cats []domain.Category) (string, *telegram.InlineKeyboard) {
	kb := &telegram.InlineKeyboard{}
	// Two per row: category names are short, and six fit without scrolling.
	for i := 0; i < len(cats); i += 2 {
		row := []telegram.InlineButton{button(cats[i].Title, actCategory, cats[i].ID, 0)}
		if i+1 < len(cats) {
			row = append(row, button(cats[i+1].Title, actCategory, cats[i+1].ID, 0))
		}
		kb.Rows = append(kb.Rows, row)
	}
	kb.Rows = append(kb.Rows, []telegram.InlineButton{button(btnCart, actCart)})
	return msgCategories, kb
}

func productPageView(pg *service.ProductPage) (string, *telegram.InlineKeyboard) {
	text := fmt.Sprintf("<b>%s</b>", esc(pg.Category.Title))
	if len(pg.Products) == 0 {
		text += "\n\nУ цій категорії поки порожньо."
	} else {
		text += "\nОберіть товар:"
	}

	kb := &telegram.InlineKeyboard{}
	for _, p := range pg.Products {
		kb.Rows = append(kb.Rows, []telegram.InlineButton{button(productLabel(p), actProduct, p.ID)})
	}

	if pg.Pages > 1 {
		var nav []telegram.InlineButton
		if pg.Page > 0 {
			nav = append(nav, button("◀️", actCategory, pg.Category.ID, int64(pg.Page-1)))
		}
		nav = append(nav, button(fmt.Sprintf("%d / %d", pg.Page+1, pg.Pages), actNoop))
		if pg.Page < pg.Pages-1 {
			nav = append(nav, button("▶️", actCategory, pg.Category.ID, int64(pg.Page+1)))
		}
		kb.Rows = append(kb.Rows, nav)
	}

	kb.Rows = append(kb.Rows, []telegram.InlineButton{
		button("⬅️ Категорії", actCategories),
		button(btnCart, actCart),
	})
	return text, kb
}

// productLabel fits a product on one button. Button text is plain, so it is
// not escaped.
func productLabel(p domain.Product) string {
	label := fmt.Sprintf("%s · %s — %s", p.Name, domain.ColorLabel(p.Color), p.Price)
	if !p.InStock() {
		label += " · немає"
	}
	return label
}

// productCaption is the text under a product photo. Sizes are all listed,
// sold-out ones struck through: a buyer looking for XL learns it exists but
// is gone, instead of wondering whether the model comes in XL at all.
func productCaption(p *domain.Product) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<b>%s</b>\n", esc(p.Name))
	fmt.Fprintf(&b, "%s · %s · %s\n", esc(p.Brand), domain.ColorLabel(p.Color), domain.SeasonLabel(p.Season))
	fmt.Fprintf(&b, "<b>%s</b>\n\n", p.Price)
	if p.Description != "" {
		fmt.Fprintf(&b, "%s\n\n", esc(p.Description))
	}

	if !p.InStock() {
		b.WriteString("❌ Немає в наявності")
		return b.String()
	}

	sizes := make([]string, 0, len(p.Variants))
	for _, v := range p.Variants {
		switch {
		case v.Stock == 0:
			sizes = append(sizes, "<s>"+esc(v.Size)+"</s>")
		case v.Stock <= domain.LowStock:
			sizes = append(sizes, fmt.Sprintf("%s (%d шт.)", esc(v.Size), v.Stock))
		default:
			sizes = append(sizes, esc(v.Size))
		}
	}
	b.WriteString("Розміри: " + strings.Join(sizes, " · "))
	return b.String()
}

func productKeyboard(p *domain.Product) *telegram.InlineKeyboard {
	first := []telegram.InlineButton{button("🔁 Схоже", actSimilar, p.ID)}
	if p.InStock() {
		first = append([]telegram.InlineButton{button("🛒 У кошик", actPickSize, p.ID)}, first...)
	}
	return &telegram.InlineKeyboard{Rows: [][]telegram.InlineButton{
		first,
		{button("⬅️ До категорії", actCategoryN, p.CategoryID), button(btnCart, actCart)},
	}}
}

// sizeKeyboard replaces the card's buttons while a size is being chosen. Only
// sizes in stock get a button: a button that can only fail is worse than none.
func sizeKeyboard(p *domain.Product) *telegram.InlineKeyboard {
	const perRow = 4
	kb := &telegram.InlineKeyboard{}
	var row []telegram.InlineButton
	for _, v := range p.Variants {
		if v.Stock == 0 {
			continue
		}
		row = append(row, button(v.Size, actAdd, p.ID, v.ID))
		if len(row) == perRow {
			kb.Rows = append(kb.Rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		kb.Rows = append(kb.Rows, row)
	}
	kb.Rows = append(kb.Rows, []telegram.InlineButton{button("✖️ Скасувати", actCardMenu, p.ID)})
	return kb
}

func similarView(p *domain.Product, similar []domain.Product) (string, *telegram.InlineKeyboard) {
	if len(similar) == 0 {
		return msgNoSimilar, &telegram.InlineKeyboard{Rows: [][]telegram.InlineButton{
			{button("⬅️ До категорії", actCategoryN, p.CategoryID)},
		}}
	}
	kb := &telegram.InlineKeyboard{}
	for _, s := range similar {
		kb.Rows = append(kb.Rows, []telegram.InlineButton{button(productLabel(s), actProduct, s.ID)})
	}
	return fmt.Sprintf("Схоже на <b>%s</b>:", esc(p.Name)), kb
}

// maxReplyRunes keeps a model reply inside Telegram's 4096-character message
// limit with room for escaping.
const maxReplyRunes = 3500

// consultantView is the consultant's answer: the model's text, escaped -- it
// is model output, so it is never trusted as markup -- and a button per
// product it chose to show, labelled from the catalogue.
func consultantView(text string, products []domain.Product, cartChanged bool) (string, *telegram.InlineKeyboard) {
	// Telegram refuses an empty message, and the buyer would get nothing at
	// all. Cards without words still need a line above them.
	if strings.TrimSpace(text) == "" {
		text = msgHereYouGo
	}
	if r := []rune(text); len(r) > maxReplyRunes {
		text = string(r[:maxReplyRunes]) + "…"
	}
	kb := &telegram.InlineKeyboard{}
	for _, p := range products {
		kb.Rows = append(kb.Rows, []telegram.InlineButton{button(productLabel(p), actProduct, p.ID)})
	}
	if cartChanged {
		kb.Rows = append(kb.Rows, []telegram.InlineButton{button(btnCart, actCart)})
	}
	return esc(text), kb
}

func cartView(c *domain.Cart) (string, *telegram.InlineKeyboard) {
	if len(c.Lines) == 0 {
		return msgCartEmpty, &telegram.InlineKeyboard{Rows: [][]telegram.InlineButton{
			{button(btnCatalog, actCategories)},
		}}
	}

	var b strings.Builder
	b.WriteString("🛒 <b>Кошик</b>\n")
	kb := &telegram.InlineKeyboard{}

	for i, l := range c.Lines {
		n := strconv.Itoa(i + 1)
		fmt.Fprintf(&b, "\n%s. %s, розмір %s\n", n, esc(l.ProductName), esc(l.Size))
		fmt.Fprintf(&b, "    %d × %s = <b>%s</b>\n", l.Qty, l.Price, l.Subtotal())
		switch {
		case l.Stock == 0:
			b.WriteString("    ⚠️ більше немає в наявності\n")
		case l.Short():
			fmt.Fprintf(&b, "    ⚠️ залишилось лише %d шт.\n", l.Stock)
		}

		kb.Rows = append(kb.Rows, []telegram.InlineButton{
			button("➖", actDec, l.VariantID),
			// The label opens the product, which is where a buyer goes to
			// pick another size instead.
			button(fmt.Sprintf("%s. %s ×%d", n, l.Size, l.Qty), actProduct, l.ProductID),
			button("➕", actInc, l.VariantID),
			button("🗑", actDel, l.VariantID),
		})
	}
	fmt.Fprintf(&b, "\nРазом: <b>%s</b> (%d шт.)", c.Total(), c.Items())

	kb.Rows = append(kb.Rows, []telegram.InlineButton{
		button("🧹 Очистити", actClear),
		button(btnCatalog, actCategories),
	})
	return b.String(), kb
}
