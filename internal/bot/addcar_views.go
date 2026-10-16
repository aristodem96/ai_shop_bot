package bot

import (
	"fmt"
	"strings"

	"github.com/aristodem96/ai_shop_bot/internal/importer"
	"github.com/aristodem96/ai_shop_bot/internal/telegram"
)

// vehicleLine renders a directory entry: "Peugeot 308 T7 · 2007–2013 · 1.6
// HDi · дизель".
func vehicleLine(v importer.Vehicle) string {
	years := fmt.Sprintf("%d–н.ч.", v.YearFrom)
	if v.YearTo != 0 {
		years = fmt.Sprintf("%d–%d", v.YearFrom, v.YearTo)
	}
	return fmt.Sprintf("%s %s · %s · %s · %s", v.Make, v.Model, years, v.Engine, importer.FuelName(v.Fuel))
}

// carDraftView shows the entries to add, each with a button to drop or keep
// it, and says where they come from.
func carDraftView(d *carDraft, canPropose bool) (string, *telegram.InlineKeyboard) {
	var b strings.Builder
	fmt.Fprintf(&b, "➕ <b>У довідник</b> для «%s»:\n\n", esc(d.name))
	kb := &telegram.InlineKeyboard{}
	if len(d.vehicles) == 0 {
		b.WriteString("Записів поки немає.\n")
	}
	for i, v := range d.vehicles {
		line := esc(vehicleLine(v))
		label := fmt.Sprintf("🗑 %d", i+1)
		if d.dropped[i] {
			line = "<s>" + line + "</s> (прибрано)"
			label = fmt.Sprintf("↩️ %d", i+1)
		}
		fmt.Fprintf(&b, "%d. %s\n", i+1, line)
		kb.Rows = append(kb.Rows, []telegram.InlineButton{button(label+" "+v.Model+" "+v.Engine, actCarToggle, d.id, int64(i))})
	}
	if d.note != "" {
		fmt.Fprintf(&b, "\n💡 %s\n", esc(d.note))
	}
	if len(d.rejected) > 0 {
		fmt.Fprintf(&b, "⚠️ Відкинуто як некоректне: %s\n", esc(strings.Join(d.rejected, "; ")))
	}
	if canPropose && len(d.vehicles) > 0 {
		b.WriteString("\n<i>Роки й двигуни — пропозиція ШІ з загальних знань. Перевірте, перш ніж додавати.</i>")
	}

	var top []telegram.InlineButton
	if n := len(d.kept()); n > 0 {
		top = append(top, button(fmt.Sprintf("✅ Додати (%d)", n), actCarAdd, d.id))
	}
	edit := "✏️ Ввести вручну"
	if canPropose {
		edit = "✏️ Виправити текстом"
	}
	top = append(top, button(edit, actCarText, d.id))
	kb.Rows = append([][]telegram.InlineButton{top}, kb.Rows...)
	kb.Rows = append(kb.Rows, []telegram.InlineButton{button("⬅️ Назад", actCarBack, d.id)})
	return b.String(), kb
}

func carAddedText(vs []importer.Vehicle) string {
	var b strings.Builder
	b.WriteString("✅ Додано в довідник:\n")
	for _, v := range vs {
		fmt.Fprintf(&b, "• %s\n", esc(vehicleLine(v)))
	}
	return b.String()
}
