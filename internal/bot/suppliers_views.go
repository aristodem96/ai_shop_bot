package bot

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/aristodem96/ai_shop_bot/internal/importer"
	"github.com/aristodem96/ai_shop_bot/internal/telegram"
)

const (
	msgSetupWho        = "Чий це файл?\n\nЯкщо хтось із ваших постачальників змінив вигляд файлу (перейменував колонки, додав нові) — оберіть його, і його товари оновляться, а не задвояться." //nolint:gosec // a question, not a credential
	msgSetupName       = "Як назвати цього постачальника? Напишіть у відповідь, наприклад <code>China Avto</code> або <code>Наша 1С</code>."
	msgSetupSaved      = "✅ Постачальника «%s» збережено. Наступні його файли я впізнаватиму сам — підпис не потрібен.\n\n⏳ Перевіряю файл…"
	msgSupplierMarkup  = "Націнка для «%s» — скільки додати до цін з файлу:"
	msgSupplierLead    = "За скільки днів «%s» привозить товар?"
	msgSupplierDelete  = "Видалити постачальника «%s»?\n\nЙого товари лишаться в каталозі. Наступний його файл я спитаю як новий."
	msgSupplierDeleted = "🗑 «%s» видалено."
	msgSupplierGone    = "Цього постачальника вже немає"
)

// setupView draws the setup at its step.
func setupView(job *importJob) (string, *telegram.InlineKeyboard) {
	st := job.setup
	switch st.Step {
	case setupLead:
		return "📦 У файлі немає колонки із залишками, тож це прайс постачальника: усі товари будуть <b>під замовлення</b>.\n\n" +
				"За скільки днів постачальник привозить товар? Покупець побачить цей термін на картці.",
			withCancel(choiceKeyboard(leadChoices, "%d дн.", actSetupLead, job.id), job.id)
	case setupMarkup:
		return "💰 Ціни у файлі — це ціни для покупців, чи до них треба додати націнку?\n\n" +
				"Наприклад, якщо у файлі закупівельні ціни, а ви продаєте на 20% дорожче — оберіть 20%.",
			withCancel(markupKeyboard(job.id), job.id)
	}
	return setupColumnsView(job)
}

// setupColumnsView shows which column the bot reads each field from.
func setupColumnsView(job *importJob) (string, *telegram.InlineKeyboard) {
	st := job.setup
	var b strings.Builder
	fmt.Fprintf(&b, "📄 <b>%s</b> — новий для мене файл.\n\nЯ прочитав колонки так:\n", esc(filepath.Base(job.path)))
	kb := &telegram.InlineKeyboard{}
	for i, f := range importer.Fields {
		col := st.Columns.Get(f.Field)
		mark := "▫️"
		if col != "" {
			mark = "✅"
		}
		shown := "—"
		if col != "" {
			shown = "«" + col + "»"
		}
		fmt.Fprintf(&b, "%s %s: %s\n", mark, f.Label, esc(shown))
		label := f.Label + ": " + strings.Trim(shown, "«»")
		kb.Rows = append(kb.Rows, []telegram.InlineButton{button(label, actSetupField, job.id, int64(i))})
	}
	b.WriteString("\nЯкщо щось не так — натисніть поле й оберіть колонку. " +
		"Потрібні щонайменше назва, ціна і код товару або артикул.")
	kb.Rows = append(kb.Rows, []telegram.InlineButton{
		button("✅ Все правильно, далі", actSetupNext, job.id),
		button("❌ Скасувати", actImportCancel, job.id),
	})
	return b.String(), kb
}

// setupFieldText asks which column holds a field.
func setupFieldText(f int) string {
	fi := importer.Fields[f]
	text := fmt.Sprintf("З якої колонки брати <b>%s</b>?", esc(fi.Label))
	if fi.Why != "" {
		text += "\n<i>" + esc(fi.Why) + "</i>"
	}
	return text
}

// setupFieldKeyboard lists the file's columns for a field, and "none".
func setupFieldKeyboard(job *importJob, f int) *telegram.InlineKeyboard {
	kb := &telegram.InlineKeyboard{}
	current := job.setup.Columns.Get(importer.Fields[f].Field)
	row := make([]telegram.InlineButton, 0, 2)
	for i, h := range job.setup.Header {
		label := h
		if strings.EqualFold(h, current) {
			label = "✅ " + h
		}
		row = append(row, button(label, actSetupColumn, job.id, int64(f), int64(i)))
		if len(row) == 2 {
			kb.Rows = append(kb.Rows, row)
			row = make([]telegram.InlineButton, 0, 2)
		}
	}
	if len(row) > 0 {
		kb.Rows = append(kb.Rows, row)
	}
	kb.Rows = append(kb.Rows, []telegram.InlineButton{button("— немає такої колонки", actSetupColumn, job.id, int64(f), -1)})
	return kb
}

// choiceKeyboard is a row of numbers to pick from.
func choiceKeyboard(choices []int64, format string, act action, id int64) *telegram.InlineKeyboard {
	kb := &telegram.InlineKeyboard{}
	row := make([]telegram.InlineButton, 0, 4)
	for _, c := range choices {
		row = append(row, button(fmt.Sprintf(format, c), act, id, c))
		if len(row) == 4 {
			kb.Rows = append(kb.Rows, row)
			row = make([]telegram.InlineButton, 0, 4)
		}
	}
	if len(row) > 0 {
		kb.Rows = append(kb.Rows, row)
	}
	return kb
}

func markupKeyboard(job int64) *telegram.InlineKeyboard {
	kb := &telegram.InlineKeyboard{Rows: [][]telegram.InlineButton{{
		button("Ціни вже для покупців — без націнки", actSetupMarkup, job, 0),
	}}}
	more := choiceKeyboard(markupChoices[1:], "+%d%%", actSetupMarkup, job)
	kb.Rows = append(kb.Rows, more.Rows...)
	return kb
}

func withCancel(kb *telegram.InlineKeyboard, job int64) *telegram.InlineKeyboard {
	kb.Rows = append(kb.Rows, []telegram.InlineButton{button("❌ Скасувати", actImportCancel, job)})
	return kb
}

// suppliersView lists the suppliers.
func suppliersView(all []importer.Supplier) (string, *telegram.InlineKeyboard) {
	if len(all) == 0 {
		return "🏭 <b>Постачальників ще немає.</b>\n\nНадішліть сюди прайс (.xlsx або .csv) — з 1С чи від постачальника. " +
			"Я розпізнаю колонки, ви перевірите, і постачальника буде збережено.", nil
	}
	var b strings.Builder
	b.WriteString("🏭 <b>Постачальники</b>\n\n")
	kb := &telegram.InlineKeyboard{}
	for _, s := range all {
		fmt.Fprintf(&b, "• <b>%s</b> — %s\n", esc(s.Name), esc(supplierTerms(&s)))
		kb.Rows = append(kb.Rows, []telegram.InlineButton{button(s.Name, actSupplierOpen, s.ID)})
	}
	b.WriteString("\nНовий постачальник з'являється сам, щойно ви надішлете його перший файл.")
	return b.String(), kb
}

// supplierTerms says how a supplier's goods are sold.
func supplierTerms(s *importer.Supplier) string {
	terms := "залишки з файлу"
	if s.LeadDays != nil {
		terms = fmt.Sprintf("під замовлення, %s", leadDaysLabel(*s.LeadDays))
	}
	if s.MarkupPercent > 0 {
		terms += fmt.Sprintf(", націнка %g%%", s.MarkupPercent)
	}
	return terms
}

// supplierView is one supplier with what can be changed.
func supplierView(s *importer.Supplier) (string, *telegram.InlineKeyboard) {
	var b strings.Builder
	fmt.Fprintf(&b, "🏭 <b>%s</b>\n%s\n\n<b>Колонки файлу:</b>\n", esc(s.Name), esc(supplierTerms(s)))
	for _, f := range importer.Fields {
		if col := s.Columns.Get(f.Field); col != "" {
			fmt.Fprintf(&b, "• %s — «%s»\n", esc(f.Label), esc(col))
		}
	}
	b.WriteString("\n<i>Якщо постачальник змінить файл, я помічу це на наступному прайсі й спитаю про колонки.</i>")
	kb := &telegram.InlineKeyboard{}
	row := []telegram.InlineButton{button("💰 Націнка", actSupplierMarkup, s.ID, -1)}
	if s.LeadDays != nil {
		row = append(row, button("📦 Термін", actSupplierLead, s.ID, -1))
	}
	kb.Rows = append(kb.Rows, row,
		[]telegram.InlineButton{button("🗑 Видалити", actSupplierDelete, s.ID, 0)},
		[]telegram.InlineButton{button("⬅️ Усі постачальники", actSupplierOpen, 0)},
	)
	return b.String(), kb
}
