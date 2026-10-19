package bot

import (
	"fmt"
	"html"
	"strconv"
	"strings"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/enrich"
	"github.com/aristodem96/ai_shop_bot/internal/service"
	"github.com/aristodem96/ai_shop_bot/internal/telegram"
)

// Views turn data into a message and its buttons. They are pure functions:
// no I/O, no state, so every screen of the bot is testable as a string.

// Every catalogue value goes through html.EscapeString before it reaches a
// message: messages are sent with parse_mode=HTML, and a product named
// "Lamp <H7>" would otherwise make Telegram reject the whole message.
var esc = html.EscapeString

func button(text string, act action, args ...int64) telegram.InlineButton {
	return telegram.InlineButton{Text: text, CallbackData: encode(act, args...)}
}

func mainMenu() *telegram.ReplyKeyboard {
	return &telegram.ReplyKeyboard{
		Rows: [][]telegram.KeyboardButton{
			{{Text: btnCatalog}, {Text: btnCart}},
			{{Text: btnCar}, {Text: btnOrders}},
			{{Text: btnHelp}},
		},
		Resize:     true,
		Persistent: true,
	}
}

func categoriesView(menu []service.CategoryEntry, car *domain.Vehicle, prog *domain.CatalogProgress) (string, *telegram.InlineKeyboard) {
	text := "<b>Каталог</b>\n"
	if car != nil {
		text += "🚗 Для: " + esc(car.Label()) + "\n"
		if line := progressLine(prog); line != "" && (!prog.Loaded() || prog.Filed < prog.Parts) {
			text += line + "\n"
		}
	}
	if len(menu) == 0 {
		text += "\nДля цього авто в каталозі поки нічого немає. Напишіть, яку деталь шукаєте, — консультант пошукає."
	} else {
		text += "\nОберіть розділ:"
	}

	kb := &telegram.InlineKeyboard{}
	// One per row: section names run to 25 characters, and with the count
	// two of them do not fit a phone's button width.
	for _, e := range menu {
		kb.Rows = append(kb.Rows, []telegram.InlineButton{button(entryLabel(e), actCategory, e.Category.ID, 0)})
	}
	if prog != nil && (!prog.Loaded() || prog.Filed < prog.Parts) {
		kb.Rows = append(kb.Rows, []telegram.InlineButton{button(btnRefresh, actCategories)})
	}
	kb.Rows = append(kb.Rows, []telegram.InlineButton{button(btnCart, actCart)})
	return text, kb
}

// btnRefresh redraws a screen that is still filling up.
const btnRefresh = "🔄 Оновити"

// progressLine says how far a car's parts catalogue has loaded, "" for a
// car with none.
func progressLine(p *domain.CatalogProgress) string {
	if p == nil {
		return ""
	}
	switch p.State {
	case "done":
		line := "✅ Каталог деталей: " + num(p.Parts)
		if p.Filed < p.Parts {
			line += fmt.Sprintf(", з них %s уже в розділах — решта з'явиться за кілька хвилин", num(p.Filed))
		}
		return line
	case "running":
		if p.SectionsTotal == 0 {
			return "⏳ Каталог деталей завантажується…"
		}
		return fmt.Sprintf("⏳ Каталог деталей завантажується: %d%% (%s деталей)",
			p.SectionsDone*100/p.SectionsTotal, num(p.Parts))
	case "failed":
		return "⚠️ Каталог деталей поки не завантажився, пробую ще раз."
	}
	return "⏳ Каталог деталей стоїть у черзі на завантаження."
}

// entryLabel is a menu button: the name and how many parts are behind it,
// so a buyer does not open a section to find one part in it.
func entryLabel(e service.CategoryEntry) string {
	return fmt.Sprintf("%s · %s", e.Category.Title, num(e.Count))
}

// productPageView renders a category: a section's subcategories, or a page
// of products.
func productPageView(pg *service.ProductPage) (string, *telegram.InlineKeyboard) {
	if len(pg.Children) > 0 {
		return sectionView(pg)
	}
	text := fmt.Sprintf("<b>%s</b>", esc(pg.Category.Title))
	if pg.All {
		text += " — усі товари"
	}
	if pg.Vehicle != nil {
		text += "\n🚗 " + esc(pg.Vehicle.Label())
	}
	switch {
	case len(pg.Products) == 0 && len(pg.OnOrder) == 0 && pg.Vehicle != nil:
		text += "\n\nДля вашого авто в цій категорії поки нічого немає."
	case len(pg.Products) == 0 && len(pg.OnOrder) == 0:
		text += "\n\nУ цій категорії поки порожньо."
	default:
		text += "\n\nОберіть товар:"
	}
	list, rows := listing(pg.Page*service.PageSize+1, pg.Products, pg.OnOrder)
	if list != "" {
		text += "\n\n" + list
	}
	if len(pg.OnOrder) > 0 {
		text += "\n\n" + msgOnOrderLegend
	}

	kb := &telegram.InlineKeyboard{Rows: rows}

	if pg.Pages > 1 {
		// Paging stays in the mode it started in: a whole section, or one
		// category.
		act := actCategory
		if pg.All {
			act = actCategoryAll
		}
		var nav []telegram.InlineButton
		if pg.Page > 0 {
			nav = append(nav, button("◀️", act, pg.Category.ID, int64(pg.Page-1)))
		}
		nav = append(nav, button(fmt.Sprintf("%d / %d", pg.Page+1, pg.Pages), actNoop))
		if pg.Page < pg.Pages-1 {
			nav = append(nav, button("▶️", act, pg.Category.ID, int64(pg.Page+1)))
		}
		kb.Rows = append(kb.Rows, nav)
	}

	kb.Rows = append(kb.Rows, []telegram.InlineButton{backButton(pg), button(btnCart, actCart)})
	return text, kb
}

// sectionView is a section's menu: its subcategories, and everything in it
// at once.
func sectionView(pg *service.ProductPage) (string, *telegram.InlineKeyboard) {
	text := fmt.Sprintf("<b>%s</b>", esc(pg.Category.Title))
	if pg.Vehicle != nil {
		text += "\n🚗 " + esc(pg.Vehicle.Label())
	}
	text += "\n\nОберіть підкатегорію:"

	kb := &telegram.InlineKeyboard{}
	for _, e := range pg.Children {
		kb.Rows = append(kb.Rows, []telegram.InlineButton{button(entryLabel(e), actCategory, e.Category.ID, 0)})
	}
	kb.Rows = append(kb.Rows,
		[]telegram.InlineButton{button("📋 Усі товари розділу · "+num(pg.Total), actCategoryAll, pg.Category.ID, 0)},
		[]telegram.InlineButton{button("⬅️ Розділи", actCategories), button(btnCart, actCart)},
	)
	return text, kb
}

// backButton leads one level up: from a subcategory to its section, from a
// section to the list of sections.
func backButton(pg *service.ProductPage) telegram.InlineButton {
	switch {
	case pg.All:
		return button("⬅️ "+pg.Category.Title, actCategory, pg.Category.ID, 0)
	case pg.Parent != nil:
		return button("⬅️ "+pg.Parent.Title, actCategory, pg.Parent.ID, 0)
	}
	return button("⬅️ Розділи", actCategories)
}

// msgOnOrderLegend explains the parts on order under a category.
const msgOnOrderLegend = "🔎 — під замовлення: на складі немає, але можемо привезти. Натисніть, і менеджер назве ціну й термін."

// listing lays products, then parts on order, out for a message: each in
// full on a numbered line of the text, and a short button with its number.
// A button holds one line, which Telegram cuts at a phone's width, so a long
// name on a button would show only its start; the text shows all of it.
// first is the number of the first item.
func listing(first int, products []domain.Product, parts []domain.CatalogPart) (string, [][]telegram.InlineButton) {
	lines := make([]string, 0, len(products)+len(parts))
	rows := make([][]telegram.InlineButton, 0, len(products)+len(parts))
	n := first
	for _, p := range products {
		lines = append(lines, fmt.Sprintf("<b>%d.</b> %s · %s — <b>%s</b> %s", n, esc(p.Name), esc(p.Brand), p.Price, stockMark(&p)))
		rows = append(rows, []telegram.InlineButton{button(productLabel(n, p), actProduct, p.ID)})
		n++
	}
	for _, p := range parts {
		lines = append(lines, fmt.Sprintf("<b>%d.</b> 🔎 %s · %s — ціна за запитом", n, esc(p.Name), esc(strings.TrimSpace(p.Brand+" "+p.Number))))
		rows = append(rows, []telegram.InlineButton{button(orderPartLabel(n, p), actRequest, p.ID)})
		n++
	}
	return strings.Join(lines, "\n"), rows
}

// stockMark is a product's availability in a list, briefly.
func stockMark(p *domain.Product) string {
	switch {
	case p.OnOrder:
		return "🚚 " + leadTime(p.LeadDays)
	case p.Stock == 0:
		return "❌ немає"
	case p.Stock <= domain.LowStock:
		return fmt.Sprintf("⚠️ останні %d шт.", p.Stock)
	default:
		return "✅"
	}
}

// orderPartLabel is the button of a listed part on order: its number and
// brand. Button text is plain, so it is not escaped.
func orderPartLabel(n int, p domain.CatalogPart) string {
	return fmt.Sprintf("%d · 🔎 %s · дізнатися ціну", n, p.Brand)
}

// productLabel is the button of a listed product: its number, brand and
// price -- short enough for any phone, the name being in the text above.
func productLabel(n int, p domain.Product) string {
	label := fmt.Sprintf("%d · %s · %s", n, p.Brand, p.Price)
	switch {
	case p.Stock > 0:
	case p.OnOrder:
		label += " · 🚚"
	default:
		label += " · ❌"
	}
	return label
}

// maxFitsListed bounds the vehicles listed on a card without a chosen car.
const maxFitsListed = 4

// productCaption is the text under a product photo. With a car chosen it says
// plainly whether the part fits it -- the question every buyer of parts has.
func productCaption(p *domain.Product, car *domain.Vehicle) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<b>%s</b>\n", esc(p.Name))
	fmt.Fprintf(&b, "%s · арт. %s\n", esc(p.Brand), esc(p.SKU))
	fmt.Fprintf(&b, "<b>%s</b> · %s\n\n", p.Price, stockLabel(p))
	if p.Description != "" {
		fmt.Fprintf(&b, "%s\n\n", esc(p.Description))
	}

	if len(p.Specs) > 0 {
		specs := make([]string, len(p.Specs))
		for i, s := range p.Specs {
			specs[i] = esc(s.Name) + ": " + esc(s.Value)
		}
		b.WriteString(strings.Join(specs, "\n") + "\n")
	}
	if len(p.OEMNumbers) > 0 {
		// The catalogue adds a dozen carmakers' numbers to some parts; a
		// photo's caption holds 1024 characters in all.
		oem := strings.Join(p.OEMNumbers[:min(len(p.OEMNumbers), oemShown)], ", ")
		if extra := len(p.OEMNumbers) - oemShown; extra > 0 {
			oem += fmt.Sprintf(" … +%d", extra)
		}
		fmt.Fprintf(&b, "OEM: <code>%s</code>\n", esc(oem))
	}
	b.WriteString("\n" + fitmentLine(p, car))
	return b.String()
}

// oemShown bounds the OEM numbers a card lists.
const oemShown = 6

// fitsShown bounds the models a card lists from the catalogue.
const fitsShown = 4

// fitsBlock lists the cars a part fits by the parts catalogue: the models
// first, and how many more there are. When the catalogue cut its list, "and
// others" says so without a number.
func fitsBlock(f *enrich.Fits) string {
	if f == nil || len(f.Lines) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n🚗 <b>За каталогом підходить до:</b>\n")
	for _, l := range f.Lines[:min(len(f.Lines), fitsShown)] {
		fmt.Fprintf(&b, "• %s\n", esc(clip(l, 70)))
	}
	switch extra := len(f.Lines) - fitsShown; {
	case !f.Complete:
		b.WriteString("• … та інші\n")
	case extra > 0:
		fmt.Fprintf(&b, "• … і ще %s\n", plural(extra, "модель", "моделі", "моделей"))
	}
	return strings.TrimRight(b.String(), "\n")
}

// clip shortens a line to n runes.
func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func stockLabel(p *domain.Product) string {
	switch {
	case p.OnOrder:
		return "🚚 " + leadTime(p.LeadDays)
	case p.Stock == 0:
		return "❌ немає в наявності"
	case p.Stock <= domain.LowStock:
		return fmt.Sprintf("⚠️ останні %d шт.", p.Stock)
	default:
		return "✅ в наявності"
	}
}

// leadTime says when a part on order arrives.
func leadTime(days int) string {
	if days == 0 {
		return "під замовлення"
	}
	return fmt.Sprintf("під замовлення, %s", plural(days, "день", "дні", "днів"))
}

// plural picks the Ukrainian form of a counted noun: 1 день, 2 дні, 5 днів,
// 11 днів, 21 день.
func plural(n int, one, few, many string) string {
	form := many
	switch {
	case n%100 >= 11 && n%100 <= 14:
	case n%10 == 1:
		form = one
	case n%10 >= 2 && n%10 <= 4:
		form = few
	}
	return fmt.Sprintf("%d %s", n, form)
}

func fitmentLine(p *domain.Product, car *domain.Vehicle) string {
	switch {
	case p.Universal:
		return "🌐 Універсальний товар — перевірте характеристики під своє авто."
	case car != nil && p.FitsVehicle(car.ID):
		line := "✅ Підходить до вашого " + esc(car.Short())
		if note := p.FitNotes[car.ID]; note != "" {
			line += " (" + esc(note) + ")"
		}
		return line
	case car != nil:
		return "⚠️ Не підходить до вашого " + esc(car.Short())
	case len(p.Fits) == 0:
		return ""
	}
	names := make([]string, 0, maxFitsListed)
	for _, v := range p.Fits[:min(len(p.Fits), maxFitsListed)] {
		names = append(names, esc(v.Short()))
	}
	line := "🚗 Підходить до: " + strings.Join(names, ", ")
	if extra := len(p.Fits) - maxFitsListed; extra > 0 {
		line += fmt.Sprintf(" та ще %d", extra)
	}
	return line
}

func productKeyboard(p *domain.Product) *telegram.InlineKeyboard {
	first := []telegram.InlineButton{button("🔁 Аналоги", actAnalogues, p.ID)}
	if p.Available() {
		first = append([]telegram.InlineButton{button("🛒 У кошик", actAdd, p.ID)}, first...)
	}
	return &telegram.InlineKeyboard{Rows: [][]telegram.InlineButton{
		first,
		{button("⬅️ До категорії", actCategoryN, p.CategoryID), button(btnCart, actCart)},
	}}
}

func analoguesView(p *domain.Product, analogues []domain.Product) (string, *telegram.InlineKeyboard) {
	if len(analogues) == 0 {
		return msgNoAnalogues, &telegram.InlineKeyboard{Rows: [][]telegram.InlineButton{
			{button("⬅️ До категорії", actCategoryN, p.CategoryID)},
		}}
	}
	list, rows := listing(1, analogues, nil)
	kb := &telegram.InlineKeyboard{Rows: rows}
	kb.Rows = append(kb.Rows, []telegram.InlineButton{button("⬅️ До категорії", actCategoryN, p.CategoryID)})
	return fmt.Sprintf("🔁 Аналоги для <b>%s</b> (%s):\n\n%s", esc(p.Name), esc(p.Brand), list), kb
}

// carView answers "🚗 My car" when one is saved.
func carView(car *domain.Vehicle, prog *domain.CatalogProgress) (string, *telegram.InlineKeyboard) {
	text := "🚗 Ваше авто: <b>" + esc(car.Label()) + "</b>\n"
	if line := progressLine(prog); line != "" {
		text += "\n" + line + "\n"
	}
	text += "\nКаталог і консультант підбирають деталі під нього. Щоб змінити — просто напишіть іншу машину."
	kb := &telegram.InlineKeyboard{Rows: [][]telegram.InlineButton{
		{button("🛍 Каталог для цього авто", actCategories)},
		{button("🔧 Комплект для ТО", actKit)},
	}}
	if prog != nil && (!prog.Loaded() || prog.Filed < prog.Parts) {
		kb.Rows = append(kb.Rows, []telegram.InlineButton{button(btnRefresh, actCarScreen)})
	}
	kb.Rows = append(kb.Rows, []telegram.InlineButton{button("✖️ Забути авто", actForgetCar)})
	return text, kb
}

var partTypeLabels = map[string]string{
	domain.PartEngineOil:   "моторна олива",
	domain.PartOilFilter:   "масляний фільтр",
	domain.PartAirFilter:   "повітряний фільтр",
	domain.PartCabinFilter: "салонний фільтр",
	domain.PartSparkPlug:   "свічки запалювання",
}

// kitView lists a maintenance kit with a button per part and one to buy it
// all.
func kitView(kit *service.Kit) (string, *telegram.InlineKeyboard) {
	var b strings.Builder
	b.WriteString("🔧 <b>Комплект для ТО</b>\n")
	b.WriteString(esc(kit.Vehicle.Label()) + "\n")
	if kit.Vehicle.OilViscosity != "" {
		fmt.Fprintf(&b, "Олива %s, %.1f л\n", esc(kit.Vehicle.OilViscosity), kit.Vehicle.OilVolume)
	}
	b.WriteString("\n")

	kb := &telegram.InlineKeyboard{}
	for n, i := range kit.Items {
		fmt.Fprintf(&b, "<b>%d.</b> %s · %s — %d × %s\n", n+1, esc(i.Product.Name), esc(i.Product.Brand), i.Qty, i.Product.Price)
		kb.Rows = append(kb.Rows, []telegram.InlineButton{
			button(fmt.Sprintf("%d · %s · %s", n+1, i.Product.Brand, i.Product.Price), actProduct, i.Product.ID),
		})
	}
	if len(kit.Missing) > 0 {
		missing := make([]string, len(kit.Missing))
		for i, t := range kit.Missing {
			missing[i] = partTypeLabels[t]
		}
		b.WriteString("\n⚠️ Немає в наявності для вашого авто: " + strings.Join(missing, ", ") + "\n")
	}

	if len(kit.Items) == 0 {
		b.WriteString("Для вашого авто зараз нічого з комплекту немає в наявності.")
		return b.String(), kb
	}
	fmt.Fprintf(&b, "\nРазом: <b>%s</b>", kit.Total())
	kb.Rows = append(kb.Rows, []telegram.InlineButton{
		button(fmt.Sprintf("🛒 Додати весь комплект — %s", kit.Total()), actKitAdd),
	})
	return b.String(), kb
}

func carForgottenView() (string, *telegram.InlineKeyboard) {
	return msgCarForgotten, nil
}

// maxReplyRunes keeps a model reply inside Telegram's 4096-character message
// limit with room for escaping and the list of the cards under it.
const maxReplyRunes = 2800

// consultantView is the consultant's answer: the model's text, escaped -- it
// is model output, so it is never trusted as markup -- and a button per
// product it chose to show, labelled from the catalogue.
func consultantView(text string, products []domain.Product, parts []domain.CatalogPart, cartChanged, carSaved bool) (string, telegram.ReplyMarkup) {
	// Telegram refuses an empty message, and the buyer would get nothing at
	// all. Cards without words still need a line above them.
	if strings.TrimSpace(text) == "" {
		text = msgHereYouGo
	}
	if r := []rune(text); len(r) > maxReplyRunes {
		text = string(r[:maxReplyRunes]) + "…"
	}
	html := esc(text)
	list, rows := listing(1, products, parts)
	if list != "" {
		html += "\n\n" + list
	}
	kb := &telegram.InlineKeyboard{Rows: rows}
	if carSaved {
		kb.Rows = append(kb.Rows, []telegram.InlineButton{
			button("🛍 Каталог для вашого авто", actCategories),
			button("🔧 Комплект для ТО", actKit),
		})
	}
	if cartChanged {
		kb.Rows = append(kb.Rows, []telegram.InlineButton{button(btnCart, actCart)})
	}
	// A plain chat reply ("how are you") carries no buttons. A nil interface,
	// not a nil *InlineKeyboard, so no reply_markup is sent at all.
	if len(kb.Rows) == 0 {
		return html, nil
	}
	return html, kb
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
		fmt.Fprintf(&b, "\n%s. %s (%s)\n", n, esc(l.ProductName), esc(l.Brand))
		fmt.Fprintf(&b, "    %d × %s = <b>%s</b>\n", l.Qty, l.Price, l.Subtotal())
		switch {
		case l.OnOrder:
			fmt.Fprintf(&b, "    🚚 %s\n", leadTime(l.LeadDays))
		case l.Unavailable():
			b.WriteString("    ⚠️ більше немає в наявності\n")
		case l.Short():
			fmt.Fprintf(&b, "    ⚠️ залишилось лише %d шт.\n", l.Stock)
		}

		kb.Rows = append(kb.Rows, []telegram.InlineButton{
			button("➖", actDec, l.ProductID),
			// The label opens the product card.
			button(fmt.Sprintf("№%s · %d шт", n, l.Qty), actProduct, l.ProductID),
			button("➕", actInc, l.ProductID),
			button("🗑", actDel, l.ProductID),
		})
	}
	fmt.Fprintf(&b, "\nРазом: <b>%s</b> (%d шт.)", c.Total(), c.Items())

	kb.Rows = append(kb.Rows,
		[]telegram.InlineButton{button("💳 Оформити замовлення", actCheckout)},
		[]telegram.InlineButton{button("🧹 Очистити", actClear), button(btnCatalog, actCategories)},
	)
	return b.String(), kb
}
