package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aristodem96/ai_shop_bot/internal/importer"
	"github.com/aristodem96/ai_shop_bot/internal/telegram"
)

// A file whose columns no supplier has is a new supplier's -- or a known one
// that changed its export. The bot reads the header, proposes which column
// is what, and asks only what the file cannot say: the delivery time when
// there is no stock column, the markup, and the supplier's name.

// cmdSuppliers lists the suppliers.
const cmdSuppliers = "/suppliers"

// supplierSetup is a supplier being made from a file.
type supplierSetup struct {
	Header   []string         `json:"header"`
	Columns  importer.Columns `json:"columns"`
	LeadDays *int             `json:"lead_days,omitempty"`
	Markup   float64          `json:"markup"`
	// Step is where the dialog is: columns, lead, markup, who, name.
	Step string `json:"step"`
}

const (
	setupColumns = "columns"
	setupLead    = "lead"
	setupMarkup  = "markup"
	setupWho     = "who"
	setupName    = "name"
)

// leadChoices and markupChoices are the buttons; anything else is rare
// enough for /suppliers.
var (
	leadChoices   = []int64{1, 2, 3, 5, 7, 14}
	markupChoices = []int64{0, 10, 15, 20, 25, 30, 40, 50}
)

// startSetup shows what the bot read of a new file's columns.
func (b *Bot) startSetup(ctx context.Context, job *importJob, in *importer.Inspection) {
	job.setup = &supplierSetup{Header: in.Header, Columns: in.Detected, Step: setupColumns}
	b.importJobs.save(ctx, job)
	text, kb := setupColumnsView(job)
	b.send(ctx, job.chat, text, kb)
}

// routeSetup dispatches the setup's buttons.
func (b *Bot) routeSetup(ctx context.Context, cc callbackCtx, cb callback) (string, error) {
	if !b.isAdmin(cc.chat) {
		return "", errBadCallback
	}
	a := cb.args
	job, ok := b.importJobs.get(ctx, a[0])
	if !ok || job.setup == nil {
		return "", b.editText(ctx, cc, msgImportExpired, nil)
	}
	st := job.setup
	switch cb.act {
	case actSetupField:
		f := int(a[1])
		if f < 0 || f >= len(importer.Fields) {
			return "", errBadCallback
		}
		return "", b.editText(ctx, cc, setupFieldText(f), setupFieldKeyboard(job, f))
	case actSetupColumn:
		f, col := int(a[1]), int(a[2])
		if f < 0 || f >= len(importer.Fields) || col < -1 || col >= len(st.Header) {
			return "", errBadCallback
		}
		header := ""
		if col >= 0 {
			header = st.Header[col]
			// One column, one field: it leaves the field it had.
			for _, fi := range importer.Fields {
				if strings.EqualFold(st.Columns.Get(fi.Field), header) {
					st.Columns.Set(fi.Field, "")
				}
			}
		}
		st.Columns.Set(importer.Fields[f].Field, header)
	case actSetupNext:
		if msg := missingColumns(st.Columns); msg != "" {
			return msg, nil
		}
		st.Step = setupLead
		if st.Columns.Stock != "" {
			st.LeadDays = nil
			st.Step = setupMarkup
		}
	case actSetupLead:
		days := int(a[1])
		st.LeadDays, st.Step = &days, setupMarkup
	case actSetupMarkup:
		st.Markup = float64(a[1])
		return "", b.askWho(ctx, cc, job)
	case actSetupSupplier:
		return b.chooseWho(ctx, cc, job, a[1])
	}
	b.importJobs.save(ctx, job)
	text, kb := setupView(job)
	return "", b.editText(ctx, cc, text, kb)
}

// missingColumns says what a supplier cannot do without, "" when nothing.
func missingColumns(c importer.Columns) string {
	switch {
	case c.Name == "":
		return "Оберіть колонку з назвою товару"
	case c.Price == "":
		return "Оберіть колонку з ціною"
	case c.ExternalID == "" && c.PartNumber == "":
		return "Оберіть колонку з кодом товару або артикулом"
	}
	return ""
}

// askWho asks whose the file is: a new supplier, or a known one that
// changed its file. With no suppliers yet, only the name is asked.
func (b *Bot) askWho(ctx context.Context, cc callbackCtx, job *importJob) error {
	all, err := b.imports.Suppliers(ctx)
	if err != nil {
		return err
	}
	if len(all) == 0 {
		return b.askSupplierName(ctx, cc, job)
	}
	job.setup.Step = setupWho
	b.importJobs.save(ctx, job)
	kb := &telegram.InlineKeyboard{}
	for _, s := range all {
		kb.Rows = append(kb.Rows, []telegram.InlineButton{button("🔁 "+s.Name+" змінив файл", actSetupSupplier, job.id, s.ID)})
	}
	kb.Rows = append(kb.Rows,
		[]telegram.InlineButton{button("➕ Новий постачальник", actSetupSupplier, job.id, 0)},
		[]telegram.InlineButton{button("❌ Скасувати", actImportCancel, job.id)},
	)
	return b.editText(ctx, cc, msgSetupWho, kb)
}

// chooseWho is the answer to askWho: zero asks the new supplier's name.
func (b *Bot) chooseWho(ctx context.Context, cc callbackCtx, job *importJob, id int64) (string, error) {
	if id == 0 {
		return "", b.askSupplierName(ctx, cc, job)
	}
	sup, err := b.imports.Supplier(ctx, id, "")
	if err != nil {
		return "", err
	}
	// The known supplier keeps its name and key; its file is read anew.
	sup.Columns, sup.LeadDays, sup.MarkupPercent = job.setup.Columns, job.setup.LeadDays, job.setup.Markup
	return "", b.finishSetup(ctx, cc, job, sup)
}

func (b *Bot) askSupplierName(ctx context.Context, cc callbackCtx, job *importJob) error {
	job.setup.Step = setupName
	b.importJobs.save(ctx, job)
	b.importJobs.await(ctx, cc.chat, job.id)
	if err := b.removeButtons(ctx, cc); err != nil {
		b.log.WarnContext(ctx, "could not remove setup buttons")
	}
	return b.tg.SendMessage(ctx, cc.chat, msgSetupName, &telegram.ForceReply{ForceReply: true, Placeholder: "Назва постачальника"})
}

// handleImportReply takes the supplier's name a setup waits for. It
// reports whether the message was one.
func (b *Bot) handleImportReply(ctx context.Context, m *telegram.Message) bool {
	if m.Text == "" || strings.HasPrefix(m.Text, "/") {
		return false
	}
	job, ok := b.importJobs.awaited(ctx, m.Chat.ID)
	if !ok || job.setup == nil || job.setup.Step != setupName {
		return false
	}
	sup := &importer.Supplier{Name: m.Text, Columns: job.setup.Columns, LeadDays: job.setup.LeadDays,
		MarkupPercent: job.setup.Markup}
	if err := b.finishSetup(ctx, callbackCtx{chat: m.Chat.ID}, job, sup); err != nil {
		b.fail(ctx, m.Chat.ID, "save a supplier", err)
	}
	return true
}

// finishSetup keeps the supplier and checks the file as its.
func (b *Bot) finishSetup(ctx context.Context, cc callbackCtx, job *importJob, sup *importer.Supplier) error {
	if err := b.imports.SaveSupplier(ctx, sup); err != nil {
		if isSupplierError(err) {
			b.send(ctx, cc.chat, "❌ "+esc(err.Error()), nil)
			if job.setup.Step == setupName {
				// Asked again: the name is the one thing typed.
				b.importJobs.await(ctx, cc.chat, job.id)
				return b.tg.SendMessage(ctx, cc.chat, msgSetupName, &telegram.ForceReply{ForceReply: true})
			}
			return nil
		}
		return err
	}
	job.setup, job.supplier, job.name = nil, sup.ID, sup.Name
	b.importJobs.save(ctx, job)
	b.send(ctx, cc.chat, fmt.Sprintf(msgSetupSaved, esc(sup.Name)), nil)
	b.previewImport(ctx, job)
	return nil
}

func isSupplierError(err error) bool {
	for _, e := range []error{importer.ErrSupplierName, importer.ErrSupplierTaken, importer.ErrNoName, importer.ErrNoPrice,
		importer.ErrNoKey, importer.ErrNoStockOrDay, importer.ErrBadMarkup} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

// --- /suppliers ----------------------------------------------------------------

// routeSuppliers dispatches the /suppliers buttons.
func (b *Bot) routeSuppliers(ctx context.Context, cc callbackCtx, cb callback) (string, error) {
	if !b.isAdmin(cc.chat) {
		return "", errBadCallback
	}
	a := cb.args
	if cb.act == actSupplierOpen && a[0] == 0 {
		text, kb, err := b.suppliersList(ctx)
		if err != nil {
			return "", err
		}
		return "", b.editText(ctx, cc, text, kb)
	}
	sup, err := b.imports.Supplier(ctx, a[0], "")
	if errors.Is(err, importer.ErrNoSupplier) {
		text, kb, err := b.suppliersList(ctx)
		if err != nil {
			return "", err
		}
		return msgSupplierGone, b.editText(ctx, cc, text, kb)
	}
	if err != nil {
		return "", err
	}
	switch cb.act {
	case actSupplierMarkup:
		if a[1] < 0 {
			return "", b.editText(ctx, cc, fmt.Sprintf(msgSupplierMarkup, esc(sup.Name)), choiceKeyboard(markupChoices, "%d%%", actSupplierMarkup, sup.ID))
		}
		sup.MarkupPercent = float64(a[1])
	case actSupplierLead:
		if a[1] < 0 {
			return "", b.editText(ctx, cc, fmt.Sprintf(msgSupplierLead, esc(sup.Name)), choiceKeyboard(leadChoices, "%d дн.", actSupplierLead, sup.ID))
		}
		days := int(a[1])
		sup.LeadDays = &days
	case actSupplierDelete:
		if a[1] == 0 {
			return "", b.editText(ctx, cc, fmt.Sprintf(msgSupplierDelete, esc(sup.Name)), &telegram.InlineKeyboard{Rows: [][]telegram.InlineButton{{
				button("🗑 Видалити", actSupplierDelete, sup.ID, 1), button("⬅️ Ні", actSupplierOpen, sup.ID),
			}}})
		}
		if err := b.imports.DeleteSupplier(ctx, sup.ID); err != nil {
			return "", err
		}
		text, kb, err := b.suppliersList(ctx)
		if err != nil {
			return "", err
		}
		return "", b.editText(ctx, cc, fmt.Sprintf(msgSupplierDeleted, esc(sup.Name))+"\n\n"+text, kb)
	}
	if cb.act != actSupplierOpen {
		if err := b.imports.SaveSupplier(ctx, sup); err != nil {
			return "", err
		}
	}
	text, kb := supplierView(sup)
	return "", b.editText(ctx, cc, text, kb)
}

func (b *Bot) suppliersList(ctx context.Context) (string, *telegram.InlineKeyboard, error) {
	all, err := b.imports.Suppliers(ctx)
	if err != nil {
		return "", nil, err
	}
	text, kb := suppliersView(all)
	return text, kb, nil
}
