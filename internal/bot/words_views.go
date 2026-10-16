package bot

import (
	"fmt"
	"strings"

	"github.com/aristodem96/ai_shop_bot/internal/importer"
	"github.com/aristodem96/ai_shop_bot/internal/telegram"
)

// wordExamples bounds the product names a preview lists.
const wordExamples = 8

func wordStartText(word string, total, inDefault int) string {
	text := fmt.Sprintf("🔤 «%s»: у назвах %s товарів", esc(word), num(total))
	switch {
	case total == 0:
		text = fmt.Sprintf("🔤 Товарів зі словом «%s» зараз немає. Його все одно можна додати — спрацює на наступних імпортах.", esc(word))
	case inDefault > 0:
		text += fmt.Sprintf(", з них %s зараз в «Інше»", num(inDefault))
	}
	return text + ".\n\nКуди їх віднести?"
}

// sectionsKeyboard lists every section, empty ones too: the owner may be
// filing the first product into one. Buttons carry the category's index in
// the tree, which stays put for a dialog's half hour.
func sectionsKeyboard(job int64, cats []importer.Category) *telegram.InlineKeyboard {
	kb := &telegram.InlineKeyboard{}
	for i, c := range cats {
		if c.Parent == "" {
			kb.Rows = append(kb.Rows, []telegram.InlineButton{button(c.Title, actWordPick, job, int64(i))})
		}
	}
	kb.Rows = append(kb.Rows,
		[]telegram.InlineButton{button("➕ Новий розділ", actWordNew, job, -1)},
		[]telegram.InlineButton{button("❌ Скасувати", actWordCancel, job)},
	)
	return kb
}

func subcategoriesKeyboard(job int64, cats []importer.Category, section int64) *telegram.InlineKeyboard {
	kb := &telegram.InlineKeyboard{}
	for i, c := range cats {
		if c.Parent == cats[section].Slug {
			kb.Rows = append(kb.Rows, []telegram.InlineButton{button(c.Title, actWordPick, job, int64(i))})
		}
	}
	kb.Rows = append(kb.Rows,
		[]telegram.InlineButton{button("➕ Нова підкатегорія", actWordNew, job, section)},
		[]telegram.InlineButton{button("⬅️ Розділи", actWordBack, job), button("❌ Скасувати", actWordCancel, job)},
	)
	return kb
}

// wordPreviewView shows what a word would move, and asks.
func wordPreviewView(job int64, ch *importer.KeywordChange, path string) (string, *telegram.InlineKeyboard) {
	var b strings.Builder
	fmt.Fprintf(&b, "🔤 «%s» → <b>%s</b>", esc(ch.Word), esc(path))
	if ch.Create != nil {
		b.WriteString(" <i>(нова)</i>")
	}
	b.WriteString("\n\n")

	if len(ch.Move) == 0 {
		b.WriteString("Зараз нічого не переїде — слово спрацює на наступних імпортах.\n")
	} else {
		fmt.Fprintf(&b, "<b>Переїде товарів: %s</b>", num(len(ch.Move)))
		if ch.FromDefault > 0 {
			fmt.Fprintf(&b, " (з «Інше»: %s)", num(ch.FromDefault))
		}
		b.WriteString("\n")
		for _, r := range ch.Move[:min(len(ch.Move), wordExamples)] {
			fmt.Fprintf(&b, "• %s\n", esc(r.Name))
		}
		if extra := len(ch.Move) - wordExamples; extra > 0 {
			fmt.Fprintf(&b, "• … і ще %s\n", num(extra))
		}
	}
	if len(ch.Stay) > 0 {
		fmt.Fprintf(&b, "\nЛишаться на місці: %s — їх точніше визначає довше слово, наприклад «%s».\n",
			num(len(ch.Stay)), esc(ch.Stay[0].Name))
	}

	return b.String(), &telegram.InlineKeyboard{Rows: [][]telegram.InlineButton{{
		button("✅ Застосувати", actWordApply, job),
		button("❌ Скасувати", actWordCancel, job),
	}}}
}

// wordsListLimit bounds the /words list to a message with buttons that fit.
const wordsListLimit = 30

func wordsListView(kws []importer.OwnerKeyword) (string, *telegram.InlineKeyboard) {
	if len(kws) == 0 {
		return "Ви ще не додавали слів. <code>/word слово</code> — додати.", nil
	}
	var b strings.Builder
	b.WriteString("🔤 <b>Ваші слова</b> (новіші вгорі):\n\n")
	kb := &telegram.InlineKeyboard{}
	for _, k := range kws[:min(len(kws), wordsListLimit)] {
		fmt.Fprintf(&b, "• «%s» → %s\n", esc(k.Word), esc(k.Category))
		kb.Rows = append(kb.Rows, []telegram.InlineButton{button("🗑 "+k.Word, actWordDelete, k.ID)})
	}
	if extra := len(kws) - wordsListLimit; extra > 0 {
		fmt.Fprintf(&b, "… і ще %s\n", num(extra))
	}
	b.WriteString("\n🗑 прибирає слово; товари лишаються на місці до наступного імпорту.")
	return b.String(), kb
}
