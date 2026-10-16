package bot

import (
	"fmt"
	"strings"

	"github.com/aristodem96/ai_shop_bot/internal/importer"
	"github.com/aristodem96/ai_shop_bot/internal/telegram"
)

// matchItemView asks about the current name: the model's proposal, with a
// button per model and one for all of them, or picking by hand.
func matchItemView(job *matchJob, canSuggest bool) (string, *telegram.InlineKeyboard) {
	item := job.current()
	var b strings.Builder
	fmt.Fprintf(&b, "🚗 <b>%d з %d</b> · «%s» — %s рядків\n", job.idx+1, len(job.items), esc(item.Name), num(item.Rows))
	if len(item.Examples) > 0 {
		fmt.Fprintf(&b, "<i>Приклади: %s</i>\n", esc(strings.Join(item.Examples, "; ")))
	}
	b.WriteString("\n")

	kb := &telegram.InlineKeyboard{}
	switch {
	case len(item.suggested) > 0:
		if item.note != "" {
			fmt.Fprintf(&b, "💡 %s\n", esc(item.note))
		}
		b.WriteString("Пропоную:\n")
		for _, id := range item.suggested {
			if m, ok := job.model(id); ok {
				fmt.Fprintf(&b, "• %s\n", esc(m.Label()))
			}
		}
		if len(item.suggested) > 1 {
			kb.Rows = append(kb.Rows, []telegram.InlineButton{button(fmt.Sprintf("✅ Усі запропоновані (%d)", len(item.suggested)), actMatchPick, job.id, 0)})
		}
		for _, id := range item.suggested {
			if m, ok := job.model(id); ok {
				kb.Rows = append(kb.Rows, []telegram.InlineButton{button("✅ "+m.Label(), actMatchPick, job.id, id)})
			}
		}
	case job.aiError:
		b.WriteString("Підказка ШІ зараз недоступна — оберіть модель вручну або пропустіть.\n")
	case canSuggest:
		if item.note != "" {
			fmt.Fprintf(&b, "💡 %s\n", esc(item.note))
		}
		b.WriteString("Відповідника в довіднику не знайшов. Оберіть вручну, або додайте авто в довідник і пропустіть.\n")
	default:
		b.WriteString("Оберіть модель з довідника або пропустіть.\n")
	}

	kb.Rows = append(kb.Rows,
		[]telegram.InlineButton{button("✏️ Інша модель", actMatchMakes, job.id), button("⏭ Пропустити", actMatchSkip, job.id)},
		[]telegram.InlineButton{button("⏹ Завершити", actMatchStop, job.id)},
	)
	return b.String(), kb
}

// makesKeyboard lists the directory's makes, two to a row: they are short.
func makesKeyboard(job *matchJob) *telegram.InlineKeyboard {
	kb := &telegram.InlineKeyboard{}
	makes := modelMakes(job.models)
	for i := 0; i < len(makes); i += 2 {
		row := []telegram.InlineButton{button(makes[i], actMatchMake, job.id, int64(i))}
		if i+1 < len(makes) {
			row = append(row, button(makes[i+1], actMatchMake, job.id, int64(i+1)))
		}
		kb.Rows = append(kb.Rows, row)
	}
	kb.Rows = append(kb.Rows, []telegram.InlineButton{button("⬅️ Назад", actMatchBack, job.id)})
	return kb
}

func modelsKeyboard(job *matchJob, mk string) *telegram.InlineKeyboard {
	kb := &telegram.InlineKeyboard{}
	for _, m := range job.models {
		if m.Make == mk {
			kb.Rows = append(kb.Rows, []telegram.InlineButton{button(m.Label(), actMatchPick, job.id, m.ID)})
		}
	}
	kb.Rows = append(kb.Rows, []telegram.InlineButton{button("⬅️ Марки", actMatchMakes, job.id), button("⬅️ До назви", actMatchBack, job.id)})
	return kb
}

// carsListLimit bounds /cars to a message with buttons that fit.
const carsListLimit = 30

func carsListView(aliases []importer.VehicleAlias) (string, *telegram.InlineKeyboard) {
	if len(aliases) == 0 {
		return "Відповідностей авто ще немає. Вони з'являються, коли під звітом прайсу натиснути «🚗 Зіставити авто».", nil
	}
	var b strings.Builder
	b.WriteString("🚗 <b>Відповідності авто</b> (новіші вгорі):\n\n")
	kb := &telegram.InlineKeyboard{}
	for _, a := range aliases[:min(len(aliases), carsListLimit)] {
		fmt.Fprintf(&b, "• «%s» → %s <i>(%s)</i>\n", esc(a.Name), esc(a.Model), esc(a.Source))
		kb.Rows = append(kb.Rows, []telegram.InlineButton{button("🗑 "+a.Name+" → "+a.Model, actCarDelete, a.ID)})
	}
	if extra := len(aliases) - carsListLimit; extra > 0 {
		fmt.Fprintf(&b, "… і ще %s\n", num(extra))
	}
	b.WriteString("\n🗑 прибирає відповідність разом із сумісностями, які вона дала.")
	return b.String(), kb
}
