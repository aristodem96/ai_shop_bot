package bot

import (
	"fmt"
	"strings"

	"github.com/aristodem96/ai_shop_bot/internal/categories"
	"github.com/aristodem96/ai_shop_bot/internal/telegram"
)

const (
	msgWordHelp = "Напишіть <code>/word</code> і слово з назви товару, наприклад:\n<code>/word вал распредел</code>\n\n" +
		"Я покажу, скільки товарів з ним, і запропоную обрати категорію. Усі категорії й слова — /categories."
	msgWordPick      = "🔤 «%s». Куди віднести?"
	msgCatNewSection = "Як назвати новий розділ? Напишіть назву у відповідь."
	msgCatNewSub     = "Як назвати нову підкатегорію в розділі «%s»? Напишіть назву у відповідь."
	msgCatRename     = "Нова назва для «%s»? Напишіть у відповідь."
	msgCatAskWord    = "Яке слово відносить товари в «%s»? Напишіть у відповідь шматок назви товару, наприклад <code>колодк</code> — тоді підійдуть і «колодки», і «колодок»."
	msgCatCancelled  = "Скасовано"
	msgCatDone       = "Готово"
	msgCatExpired    = "Це вже неактуально — відкрийте /categories ще раз."
	msgCatGone       = "Цієї категорії вже немає."
)

// examplesShown bounds the product names a plan lists.
const examplesShown = 6

// destinationsShown bounds the "where they go" lines of a plan.
const destinationsShown = 5

// sectionTotal counts the products of a section with its subcategories.
func sectionTotal(tree []categories.Category, s categories.Category) int {
	n := s.Products
	for _, c := range tree {
		if c.ParentID == s.ID {
			n += c.Products
		}
	}
	return n
}

// rootView lists the sections.
func rootView(tree []categories.Category) (string, *telegram.InlineKeyboard) {
	kb := &telegram.InlineKeyboard{}
	for _, c := range tree {
		if c.ParentID == 0 {
			kb.Rows = append(kb.Rows, []telegram.InlineButton{
				button(fmt.Sprintf("%s · %s", c.Title, num(sectionTotal(tree, c))), actCatOpen, c.ID),
			})
		}
	}
	kb.Rows = append(kb.Rows, []telegram.InlineButton{button("➕ Новий розділ", actCatNew, 0)})
	return "📂 <b>Категорії магазину</b>\n\n" +
		"Товар потрапляє в категорію, коли в його назві (або в групі постачальника) є одне з її слів. " +
		"Якщо підходить кілька слів, перемагає найдовше.\n\nОберіть розділ:", kb
}

// categoryView is one category: a section's subcategories, or a
// category's words, with what can be done to it.
func categoryView(tree []categories.Category, c categories.Category) (string, *telegram.InlineKeyboard) {
	var b strings.Builder
	kb := &telegram.InlineKeyboard{}
	back := button("⬅️ Усі розділи", actCatOpen, 0)
	if c.ParentID != 0 {
		if p, ok := findCategory(tree, c.ParentID); ok {
			back = button("⬅️ "+p.Title, actCatOpen, p.ID)
		}
	}

	if hasSubcategories(tree, c.ID) {
		fmt.Fprintf(&b, "📂 <b>%s</b> — %s товарів\n\nПідкатегорії:", esc(c.Title), num(sectionTotal(tree, c)))
		for _, s := range tree {
			if s.ParentID == c.ID {
				kb.Rows = append(kb.Rows, []telegram.InlineButton{
					button(fmt.Sprintf("%s · %s", s.Title, num(s.Products)), actCatOpen, s.ID),
				})
			}
		}
		kb.Rows = append(kb.Rows,
			[]telegram.InlineButton{button("➕ Підкатегорія", actCatNew, c.ID), button("✏️ Перейменувати", actCatRename, c.ID)},
			[]telegram.InlineButton{back},
		)
		return b.String(), kb
	}

	fmt.Fprintf(&b, "📁 <b>%s</b> — %s товарів\n\n", esc(string(pathOf(tree, c))), num(c.Products))
	if len(c.Words) == 0 {
		if c.Slug == categories.Other {
			b.WriteString("Сюди потрапляють товари, які не впізнало жодне слово. Щоб розкласти їх, додайте слова іншим категоріям.")
		} else {
			b.WriteString("Слів немає. Додайте слово, і товари з ним у назві потраплять сюди.")
		}
	} else {
		b.WriteString("🔤 <b>Слова</b>: ")
		words := make([]string, len(c.Words))
		for i, w := range c.Words {
			words[i] = "«" + esc(w.Text) + "»"
		}
		b.WriteString(strings.Join(words, ", "))
		b.WriteString("\n\n✖ біля слова — прибрати його (спершу покажу, куди переїдуть товари).")
	}

	if c.Slug != categories.Other {
		kb.Rows = append(kb.Rows, []telegram.InlineButton{button("🔤 Додати слово", actCatAddWord, c.ID)})
	}
	var row []telegram.InlineButton
	for _, w := range c.Words {
		row = append(row, button("✖ "+w.Text, actCatRemoveWord, w.ID))
		if len(row) == 3 {
			kb.Rows = append(kb.Rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		kb.Rows = append(kb.Rows, row)
	}
	actions := []telegram.InlineButton{button("✏️ Перейменувати", actCatRename, c.ID)}
	if c.Slug != categories.Other {
		actions = append(actions, button("🗑 Видалити", actCatDelete, c.ID))
	}
	kb.Rows = append(kb.Rows, actions)
	if c.ParentID == 0 && c.Slug != categories.Other {
		kb.Rows = append(kb.Rows, []telegram.InlineButton{button("➕ Підкатегорія", actCatNew, c.ID)})
	}
	kb.Rows = append(kb.Rows, []telegram.InlineButton{back})
	return b.String(), kb
}

// pickKeyboard lists the sections, or a section's subcategories, for the
// category of a /word.
func pickKeyboard(job int64, tree []categories.Category, section int64) *telegram.InlineKeyboard {
	kb := &telegram.InlineKeyboard{}
	for _, c := range tree {
		if c.ParentID == section {
			kb.Rows = append(kb.Rows, []telegram.InlineButton{button(c.Title, actCatPick, job, c.ID)})
		}
	}
	if section != 0 {
		kb.Rows = append(kb.Rows, []telegram.InlineButton{button("⬅️ Розділи", actCatPick, job, 0)})
	}
	kb.Rows = append(kb.Rows, []telegram.InlineButton{button("❌ Скасувати", actCatCancel, job)})
	return kb
}

func wordStartText(info *categories.WordInfo) string {
	if info.Products == 0 {
		return fmt.Sprintf("🔤 Товарів зі словом «%s» зараз немає. Його все одно можна додати — спрацює для наступних імпортів.\n\nКуди віднести?", esc(info.Word))
	}
	return fmt.Sprintf("🔤 «%s»: у назвах %s товарів.\n%s\nКуди їх віднести?", esc(info.Word), num(info.Products), whereText(info.Where))
}

// wordTakenText answers a word the tree already has.
func wordTakenText(info *categories.WordInfo) string {
	text := fmt.Sprintf("🔤 Слово «%s» вже веде в <b>%s</b>.\n", esc(info.Word), esc(string(info.Owner)))
	if info.Products > 0 {
		text += fmt.Sprintf("\nТоварів з ним: %s.\n%s", num(info.Products), whereText(info.Where))
	}
	return text + "\nЩоб перенести слово, приберіть його там (/categories) і додайте знову."
}

func whereText(where []categories.Count) string {
	var b strings.Builder
	b.WriteString("Зараз вони в:\n")
	for _, c := range where[:min(len(where), destinationsShown)] {
		fmt.Fprintf(&b, "• %s — %s\n", esc(string(c.Path)), num(c.N))
	}
	if extra := len(where) - destinationsShown; extra > 0 {
		fmt.Fprintf(&b, "• … і ще в %s категоріях\n", num(extra))
	}
	return b.String()
}

// planView shows what a change would do, and asks.
func planView(job int64, p *categories.Plan) (string, *telegram.InlineKeyboard) {
	var b strings.Builder
	target := esc(string(p.Target))
	switch p.Op.Kind {
	case categories.AddWord:
		fmt.Fprintf(&b, "🔤 Додати слово «%s» у <b>%s</b>?\n\n", esc(p.Op.Word), target)
		if len(p.Moves) == 0 {
			b.WriteString("Зараз нічого не переїде — слово спрацює для наступних імпортів.\n")
		} else {
			fmt.Fprintf(&b, "<b>Переїде сюди товарів: %s</b>", num(len(p.Moves)))
			if p.FromOther > 0 {
				fmt.Fprintf(&b, " (з «Інше»: %s)", num(p.FromOther))
			}
			b.WriteString("\n")
			examples(&b, p.Moves)
		}
		if len(p.Stay) > 0 {
			fmt.Fprintf(&b, "\nЛишаться на місці: %s — для них є довше, точніше слово (наприклад, «%s»).\n",
				num(len(p.Stay)), esc(p.Stay[0].Name))
		}
	case categories.RemoveWord:
		fmt.Fprintf(&b, "✖ Прибрати слово «%s» з <b>%s</b>?\n\n", esc(p.Op.Word), target)
		movesText(&b, p, "Товари не переїдуть: усі, що там, туди веде й інше слово.\n")
	case categories.Delete:
		fmt.Fprintf(&b, "🗑 Видалити <b>%s</b> разом з її словами?\n\n", target)
		movesText(&b, p, "Товарів у ній немає.\n")
	case categories.Create:
		fmt.Fprintf(&b, "➕ Створити <b>%s</b>?\n\n", target)
		fmt.Fprintf(&b, "Розділ з підкатегоріями показує лише їх, тому його %s товарів переїдуть у нову підкатегорію, "+
			"разом зі словами розділу.\n", num(len(p.Moves)))
	}
	return b.String(), &telegram.InlineKeyboard{Rows: [][]telegram.InlineButton{{
		button("✅ Застосувати", actCatApply, job),
		button("❌ Скасувати", actCatCancel, job),
	}}}
}

// movesText says where the products of a removal go.
func movesText(b *strings.Builder, p *categories.Plan, none string) {
	if len(p.Moves) == 0 {
		b.WriteString(none)
		return
	}
	fmt.Fprintf(b, "<b>Переїде товарів: %s</b> — туди, куди їх відправлять інші слова:\n", num(len(p.Moves)))
	for _, c := range p.To[:min(len(p.To), destinationsShown)] {
		fmt.Fprintf(b, "• %s — %s\n", esc(string(c.Path)), num(c.N))
	}
	if extra := len(p.To) - destinationsShown; extra > 0 {
		fmt.Fprintf(b, "• … і ще в %s категорій\n", num(extra))
	}
}

func examples(b *strings.Builder, ps []categories.Product) {
	for _, p := range ps[:min(len(ps), examplesShown)] {
		fmt.Fprintf(b, "• %s\n", esc(p.Name))
	}
	if extra := len(ps) - examplesShown; extra > 0 {
		fmt.Fprintf(b, "• … і ще %s\n", num(extra))
	}
}

// doneText says what an applied change did.
func doneText(p *categories.Plan) string {
	target := esc(string(p.Target))
	switch p.Op.Kind {
	case categories.AddWord:
		return fmt.Sprintf("✅ «%s» → %s. Переїхало товарів: %s.", esc(p.Op.Word), target, num(len(p.Moves)))
	case categories.RemoveWord:
		return fmt.Sprintf("✅ Слово «%s» прибрано. Переїхало товарів: %s.", esc(p.Op.Word), num(len(p.Moves)))
	case categories.Delete:
		return fmt.Sprintf("✅ %s видалено. Переїхало товарів: %s.", target, num(len(p.Moves)))
	case categories.Rename:
		return fmt.Sprintf("✅ %s тепер називається «%s».", target, esc(p.Op.Title))
	}
	return fmt.Sprintf("✅ Створено %s. Додайте слово, щоб сюди потрапляли товари.", target)
}
