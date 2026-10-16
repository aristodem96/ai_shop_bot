package bot

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/aristodem96/ai_shop_bot/internal/importer"
	"github.com/aristodem96/ai_shop_bot/internal/telegram"
)

// importListed bounds each list in a report: a chat message holds 4096
// characters, and the first ten of 671 new brands already say what to fix.
const importListed = 10

// priceImportView renders a price list run. A dry run that went through
// offers "apply"; a run stopped by the deactivation limit offers forcing it.
// Anything else leaves nothing to decide, and no buttons.
func priceImportView(id int64, file, profile string, rep *importer.Report, res importer.Result, err error, opts importer.Options) (string, *telegram.InlineKeyboard) {
	var b strings.Builder
	fmt.Fprintf(&b, "📄 <b>%s</b> · профіль «%s»\n", esc(file), esc(profile))

	var (
		header *importer.HeaderError
		limit  *importer.DeactivationError
	)
	switch {
	case errors.As(err, &header):
		b.WriteString("\n❌ <b>Не знайшов шапку таблиці.</b>\n")
		fmt.Fprintf(&b, "Бракує колонок: %s\n", esc(quoteAll(header.Missing)))
		if header.Line > 0 {
			fmt.Fprintf(&b, "Найближчий рядок (%s, рядок %d): %s\n", esc(header.Sheet), header.Line, esc(quoteAll(header.Found)))
		}
		b.WriteString("\nСхоже, постачальник перейменував колонку або це файл іншого постачальника. Виправте назву в профілі й надішліть файл ще раз.")
		return b.String(), nil
	case err != nil && !errors.As(err, &limit):
		fmt.Fprintf(&b, "\n❌ Не вдалося: %s", esc(err.Error()))
		return b.String(), nil
	}

	if rep != nil {
		writeImportReport(&b, rep)
	}

	if limit != nil {
		fmt.Fprintf(&b, "\n⚠️ <b>Зупинено, нічого не змінено.</b> Файл зняв би з продажу %s з %s товарів — більше за %d%%.\n"+
			"Так виглядає неповний файл: один аркуш замість усіх, обірване вивантаження. "+
			"Якщо постачальник справді прибрав ці товари — застосуйте все одно.",
			num(limit.Would), num(limit.Active), limit.LimitPercent)
		return b.String(), &telegram.InlineKeyboard{Rows: [][]telegram.InlineButton{{
			button("⚠️ Застосувати все одно", actImportApply, id, 1),
			button("❌ Скасувати", actImportCancel, id),
		}}}
	}

	if opts.DryRun {
		fmt.Fprintf(&b, "\n<b>Буде:</b> ➕ %s нових · ✏️ %s оновлено · 🚫 %s знято з продажу · 🔗 %s сумісностей\n"+
			"\nЦе перевірка, каталог ще не змінено.",
			num(res.Inserted), num(res.Updated), num(res.Deactivated), num(res.Fitments))
		return b.String(), applyKeyboard(id)
	}
	fmt.Fprintf(&b, "\n✅ <b>Застосовано:</b> ➕ %s нових · ✏️ %s оновлено · 🚫 %s знято з продажу · 🔗 %s сумісностей\n"+
		"Запис №%d у журналі імпортів.",
		num(res.Inserted), num(res.Updated), num(res.Deactivated), num(res.Fitments), res.RunID)
	return b.String(), nil
}

func writeImportReport(b *strings.Builder, rep *importer.Report) {
	fmt.Fprintf(b, "Рядків: %s · товарів: %s\n", num(rep.Rows), num(rep.Offers))

	if rep.Skipped > 0 {
		fmt.Fprintf(b, "\n⚠️ <b>Пропущено рядків: %s</b>\n", num(rep.Skipped))
		for _, p := range rep.Problems[:min(len(rep.Problems), importListed)] {
			fmt.Fprintf(b, "• %s, рядок %d: %s\n", esc(p.Row.Sheet), p.Row.Line, esc(p.Reason))
		}
		if extra := rep.Skipped - min(len(rep.Problems), importListed); extra > 0 {
			fmt.Fprintf(b, "• … і ще %s\n", num(extra))
		}
	}

	if len(rep.NewBrands) > 0 {
		fmt.Fprintf(b, "\n🏷 <b>Нових брендів: %s</b>\n%s\n", num(len(rep.NewBrands)), esc(listed(rep.NewBrands)))
		b.WriteString("<i>Описку тут варто додати як аліас наявного бренду.</i>\n")
	}

	if len(rep.UnmatchedVehicles) > 0 {
		names := make([]string, 0, len(rep.UnmatchedVehicles))
		for _, c := range rep.UnmatchedVehicles {
			names = append(names, fmt.Sprintf("%s (%s)", c.Name, num(c.N)))
		}
		fmt.Fprintf(b, "\n🚗 <b>Авто, яких немає в довіднику: %s</b>\n%s\n", num(len(names)), esc(listed(names)))
		b.WriteString("<i>Додайте їх у довідник авто або в карту «vehicles» профілю.</i>\n")
	}
	if rep.WithoutFitment > 0 {
		fmt.Fprintf(b, "\n📦 Без жодного авто: %s — покупці з обраною машиною їх не побачать.\n", num(rep.WithoutFitment))
	}

	if rep.Unsorted > 0 {
		fmt.Fprintf(b, "\n❓ <b>Не впізнав категорію: %s</b> — пішли в «Інше»\n%s\n", num(rep.Unsorted), esc(listed(rep.Unrecognised)))
		b.WriteString("<i>Слово з такої назви варто додати до потрібної категорії.</i>\n")
	}

	if len(rep.Categories) > 0 {
		cats := make([]string, len(rep.Categories))
		for i, c := range rep.Categories {
			cats[i] = c.Name + " " + num(c.N)
		}
		fmt.Fprintf(b, "\n🗂 %s\n", esc(strings.Join(cats, " · ")))
	}
}

// vehicleImportView renders a vehicle directory run.
func vehicleImportView(id int64, file string, problems []importer.Problem, res importer.VehicleResult, err error, dryRun bool) (string, *telegram.InlineKeyboard) {
	var b strings.Builder
	fmt.Fprintf(&b, "🚗 <b>%s</b> · довідник авто\n", esc(file))
	if err != nil {
		fmt.Fprintf(&b, "\n❌ Не вдалося: %s", esc(err.Error()))
		return b.String(), nil
	}
	if len(problems) > 0 {
		fmt.Fprintf(&b, "\n⚠️ <b>Пропущено рядків: %s</b>\n", num(len(problems)))
		for _, p := range problems[:min(len(problems), importListed)] {
			fmt.Fprintf(&b, "• рядок %d: %s\n", p.Row.Line, esc(p.Reason))
		}
		if extra := len(problems) - importListed; extra > 0 {
			fmt.Fprintf(&b, "• … і ще %s\n", num(extra))
		}
	}
	if dryRun {
		fmt.Fprintf(&b, "\n<b>Буде:</b> ➕ %s нових авто · ✏️ %s оновлено\n\nЦе перевірка, довідник ще не змінено.",
			num(res.Inserted), num(res.Updated))
		return b.String(), applyKeyboard(id)
	}
	fmt.Fprintf(&b, "\n✅ <b>Застосовано:</b> ➕ %s нових авто · ✏️ %s оновлено", num(res.Inserted), num(res.Updated))
	return b.String(), nil
}

func applyKeyboard(id int64) *telegram.InlineKeyboard {
	return &telegram.InlineKeyboard{Rows: [][]telegram.InlineButton{{
		button("✅ Застосувати", actImportApply, id, 0),
		button("❌ Скасувати", actImportCancel, id),
	}}}
}

// chooseProfileKeyboard asks what an uploaded file is. The last button is
// the vehicle directory.
func chooseProfileKeyboard(id int64, profiles []string) *telegram.InlineKeyboard {
	kb := &telegram.InlineKeyboard{}
	for i, p := range profiles {
		kb.Rows = append(kb.Rows, []telegram.InlineButton{button("📄 "+p, actImportProfile, id, int64(i))})
	}
	kb.Rows = append(kb.Rows,
		[]telegram.InlineButton{button("🚗 Довідник авто", actImportProfile, id, int64(len(profiles)))},
		[]telegram.InlineButton{button("❌ Скасувати", actImportCancel, id)},
	)
	return kb
}

// listed joins the first importListed names and says how many more there
// are.
func listed(names []string) string {
	s := strings.Join(names[:min(len(names), importListed)], ", ")
	if extra := len(names) - importListed; extra > 0 {
		s += fmt.Sprintf(" … і ще %s", num(extra))
	}
	return s
}

func quoteAll(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = "«" + n + "»"
	}
	return strings.Join(q, ", ")
}

// num groups thousands the Ukrainian way: 80 942.
func num(n int) string {
	s := strconv.Itoa(n)
	if n < 0 {
		return "-" + num(-n)
	}
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteRune(' ')
		}
		b.WriteRune(r)
	}
	return b.String()
}
