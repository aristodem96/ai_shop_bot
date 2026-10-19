package bot

import (
	"fmt"
	"strings"

	"github.com/aristodem96/ai_shop_bot/internal/categories"
	"github.com/aristodem96/ai_shop_bot/internal/grouping"
	"github.com/aristodem96/ai_shop_bot/internal/telegram"
)

const (
	msgGroupsSorting  = "🤖 У файлі є групи товарів, яким ще не обрано категорію. Розкладаю їх по категоріях…"
	msgGroupsFailed   = "⚠️ ШІ зараз недоступний — групи розкладу за словами з назв. Розібрати їх можна пізніше: /groups"
	msgGroupsCreated  = "✅ Створено категорій: %s. Розкладено груп: %s."
	msgGroupsReviewed = "✅ Сумнівні групи розібрано."
	msgGroupFiled     = "Готово"
	msgGroupsExpired  = "Це вже неактуально — /groups покаже, що ще не розібрано."
	msgGroupFiledText = "✅ «%s» → <b>%s</b> (%s товарів)."
	msgGroupMixedText = "🔀 «%s» (%s товарів): товари різні — розкладаю кожен за його назвою."
	msgGroupPick      = "📂 Куди віднести групу «%s» (%s товарів)?"
	msgGroupsNone     = "Усі групи постачальників розкладено по категоріях."
	msgGroupHelp      = "Напишіть <code>/group</code> і частину назви групи, наприклад <code>/group трещітки</code> — я покажу, куди вона віднесена, і дам змінити."
	msgGroupNotFound  = "Груп зі словом «%s» немає."
)

// groupsSummaryView sums up a sorting pass and offers what is left.
func groupsSummaryView(r *review, sum *grouping.Summary) (string, *telegram.InlineKeyboard) {
	var b strings.Builder
	if sum.Filed > 0 {
		fmt.Fprintf(&b, "🤖 Розклав по наявних категоріях: <b>%s груп</b> (%s товарів).\n", num(sum.Filed), num(sum.FiledProducts))
	}
	if sum.Mixed > 0 {
		fmt.Fprintf(&b, "🔀 У %s групах (%s товарів) товари різні — їх розкладено поштучно за назвами.\n",
			num(sum.Mixed), num(sum.MixedProducts))
	}
	kb := &telegram.InlineKeyboard{}
	if len(sum.Proposals) > 0 {
		b.WriteString("\n➕ <b>Пропоную створити категорії</b> — для товарів, яким у дереві немає місця:\n")
		for _, p := range sum.Proposals[:min(len(sum.Proposals), 12)] {
			fmt.Fprintf(&b, "• %s — %s груп, %s товарів\n", esc(p.Path), num(p.Groups), num(p.Products))
		}
		if extra := len(sum.Proposals) - 12; extra > 0 {
			fmt.Fprintf(&b, "• … і ще %s\n", num(extra))
		}
		kb.Rows = append(kb.Rows, []telegram.InlineButton{button("➕ Створити ці категорії", actGroupsCreate, r.id)})
	}
	if sum.Doubts > 0 {
		fmt.Fprintf(&b, "\n❓ <b>Сумніваюся щодо %s груп</b> (%s товарів): покажу по одній, ви оберете.\n",
			num(sum.Doubts), num(sum.DoubtProducts))
		kb.Rows = append(kb.Rows, []telegram.InlineButton{button(fmt.Sprintf("❓ Розібрати сумнівні (%s)", num(sum.Doubts)), actGroupsDoubts, r.id, 0)})
	}
	if r.job != 0 {
		b.WriteString("\nМожна й пізніше (/groups): поки товари цих груп розкладено за словами з назв.")
		kb.Rows = append(kb.Rows, []telegram.InlineButton{button("➡️ Далі — до звіту", actGroupsNext, r.id)})
	}
	return strings.TrimLeft(b.String(), "\n"), kb
}

// groupsDoneText says the model filed every group.
func groupsDoneText(sum *grouping.Summary) string {
	if sum.Filed == 0 {
		return "🤖 Нових груп немає."
	}
	return fmt.Sprintf("🤖 Розклав %s груп (%s товарів) по категоріях. Перевірити й змінити: <code>/group назва</code>.",
		num(sum.Filed), num(sum.FiledProducts))
}

// doubtView puts one doubtful group to the owner.
func doubtView(r *review, total int, g *grouping.Group, path string) (string, *telegram.InlineKeyboard) {
	i := r.index
	var b strings.Builder
	kind := "Група"
	if g.Kind == "name" {
		kind = "Товар"
	}
	fmt.Fprintf(&b, "❓ %s <b>«%s»</b> — %s товарів <i>(%d з %d)</i>\n", kind, esc(g.Key), num(g.Products), i+1, total)
	if g.Kind != "name" {
		for _, e := range g.Examples {
			fmt.Fprintf(&b, "• %s\n", esc(e))
		}
	}
	kb := &telegram.InlineKeyboard{}
	if path != "" {
		fmt.Fprintf(&b, "\n🤖 Моя здогадка: <b>%s</b>", esc(path))
		if g.Suggestion != nil && g.Suggestion.Note != "" {
			fmt.Fprintf(&b, " — %s", esc(g.Suggestion.Note))
		}
		b.WriteString("\n")
		kb.Rows = append(kb.Rows, []telegram.InlineButton{button("✅ Так: "+clip(path, 40), actGroupAccept, r.id, g.ID)})
	} else if g.Suggestion != nil && g.Suggestion.Note != "" {
		fmt.Fprintf(&b, "\n🤖 %s\n", esc(g.Suggestion.Note))
	}
	kb.Rows = append(kb.Rows,
		[]telegram.InlineButton{button("📂 Обрати категорію", actGroupPick, r.id, g.ID, 0)},
		[]telegram.InlineButton{button("🔀 Різні товари — за назвами", actGroupMixed, r.id, g.ID)},
		[]telegram.InlineButton{button("⏭ Пропустити", actGroupsDoubts, r.id, int64(i+1))},
	)
	return b.String(), kb
}

// foundGroupsView lists groups found by /group, each with where it goes.
func foundGroupsView(r *review, found []grouping.Group, tree []categories.Category) (string, *telegram.InlineKeyboard) {
	var b strings.Builder
	b.WriteString("🔎 Знайдені групи — натисніть, щоб змінити категорію:\n\n")
	kb := &telegram.InlineKeyboard{}
	for _, g := range found {
		where := "не розкладено"
		if c, ok := findCategory(tree, g.CategoryID); ok {
			where = string(pathOf(tree, c))
		}
		fmt.Fprintf(&b, "• «%s» (%s товарів) → %s\n", esc(g.Key), num(g.Products), esc(where))
		kb.Rows = append(kb.Rows, []telegram.InlineButton{button(clip(g.Key, 50), actGroupPick, r.id, g.ID, 0)})
	}
	return b.String(), kb
}
