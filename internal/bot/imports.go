package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/aristodem96/ai_shop_bot/internal/importer"
	"github.com/aristodem96/ai_shop_bot/internal/telegram"
)

// Imports runs catalogue imports for the managers' chat.
type Imports interface {
	Suppliers(ctx context.Context) ([]importer.Supplier, error)
	Supplier(ctx context.Context, id int64, name string) (*importer.Supplier, error)
	Inspect(ctx context.Context, path string) (*importer.Inspection, error)
	SaveSupplier(ctx context.Context, s *importer.Supplier) error
	DeleteSupplier(ctx context.Context, id int64) error
	Prices(ctx context.Context, supplierID int64, path string, opts importer.Options) (*importer.Report, importer.Result, error)
	Vehicles(ctx context.Context, path string, dryRun bool) ([]importer.Problem, importer.VehicleResult, error)
}

const (
	// maxImportBytes is what the Bot API lets a bot download at all.
	maxImportBytes = 20 << 20
	// importTTL is how long an uploaded file waits for "apply". A preview
	// older than this describes a catalogue that may have moved on.
	importTTL = time.Hour
	// vehiclesCaption marks a file as the vehicle directory.
	vehiclesCaption = "авто"
)

// importJob is a file a manager uploaded: being set up as a new supplier's,
// or checked and waiting to be applied.
type importJob struct {
	id       int64
	chat     int64
	dir      string // the job's own directory; removed with it
	path     string
	supplier int64  // zero while the file's supplier is being set up
	name     string // the supplier's, for the messages
	cars     bool   // the vehicle directory rather than a price list
	created  time.Time
	// setup is a new supplier being made from this file.
	setup *supplierSetup
	// grouped is set once the file's new groups were put to the model, so
	// checking it again goes straight to the report.
	grouped bool
	// report is the last preview's, for mapping its unknown vehicles.
	report *importer.Report
}

// UseImports lets the managers' chat load price lists and the vehicle
// directory by sending the file. dir holds uploads while they wait; anything
// left in it from an earlier run is removed.
func (b *Bot) UseImports(imports Imports, files Files, dir string, adminChat int64) error {
	if adminChat == 0 {
		return errors.New("bot: imports need the managers' chat")
	}
	b.adminChat = adminChat
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("bot: create import directory: %w", err)
	}
	// Uploads outlive a restart while their preview may still be applied;
	// older ones are left from runs that never came back to them.
	if err := removeStale(dir, importTTL); err != nil {
		b.log.Warn("could not clear old uploads", slog.Any("error", err))
	}
	b.imports, b.importFiles, b.importDir = imports, files, dir
	b.importJobs = newJobs[importJob](importTTL, b.dialogs("dialog:import:", importTTL),
		b.dialogs("dialog:import-reply:", importTTL), b.log)
	b.importJobs.drop = func(j *importJob) { j.remove(b.log) }
	return nil
}

// isAdmin reports whether a chat is the managers' chat.
func (b *Bot) isAdmin(chat int64) bool { return b.adminChat != 0 && chat == b.adminChat }

// receiveImport takes a file sent to the managers' chat.
func (b *Bot) receiveImport(ctx context.Context, m *telegram.Message) {
	chat, doc := m.Chat.ID, m.Document
	ext := strings.ToLower(filepath.Ext(doc.FileName))
	switch {
	case !slices.Contains([]string{".csv", ".txt", ".xlsx", ".xlsm"}, ext):
		b.send(ctx, chat, msgImportBadType, nil)
		return
	case doc.FileSize > maxImportBytes:
		b.send(ctx, chat, msgImportTooBig, nil)
		return
	}

	job := &importJob{chat: chat, created: time.Now(),
		cars: strings.EqualFold(strings.TrimSpace(m.Caption), vehiclesCaption)}
	if err := b.saveUpload(ctx, job, doc); err != nil {
		b.fail(ctx, chat, "download import file", err)
		return
	}
	if job.cars {
		b.previewImport(ctx, job)
		return
	}
	in, err := b.imports.Inspect(ctx, job.path)
	if err != nil {
		b.importJobs.remove(ctx, job.id)
		job.remove(b.log)
		b.send(ctx, chat, "❌ "+esc(err.Error()), nil)
		return
	}
	if in.Supplier != nil {
		// A file this supplier has sent before: its columns, its terms.
		job.supplier, job.name = in.Supplier.ID, in.Supplier.Name
		b.send(ctx, chat, fmt.Sprintf(msgImportChecking, esc(job.name)), nil)
		b.previewImport(ctx, job)
		return
	}
	b.startSetup(ctx, job, in)
}

// saveUpload downloads the file into a directory of its own, under the name
// the manager gave it: the reports quote it.
func (b *Bot) saveUpload(ctx context.Context, job *importJob, doc *telegram.Document) error {
	f, err := b.importFiles.GetFile(ctx, doc.FileID)
	if err != nil {
		return err
	}
	data, err := b.importFiles.DownloadFile(ctx, f.FilePath, maxImportBytes)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp(b.importDir, "job-*")
	if err != nil {
		return fmt.Errorf("bot: create job directory: %w", err)
	}
	// Base drops any path a client put in the name.
	path := filepath.Join(dir, filepath.Base(doc.FileName))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return fmt.Errorf("bot: save import file: %w", err)
	}
	job.dir, job.path = dir, path
	b.importJobs.add(ctx, job)
	return nil
}

// previewImport runs a dry run and shows its report with the buttons that
// apply it.
func (b *Bot) previewImport(ctx context.Context, job *importJob) {
	b.typing(ctx, job.chat)
	// A file with groups no category is decided for: they are filed first,
	// with the owner where the model is unsure, and the report follows.
	if b.grouping != nil && b.cats != nil && !job.cars && !job.grouped {
		rep, _, err := b.imports.Prices(ctx, job.supplier, job.path, importer.Options{DryRun: true})
		if err == nil && rep != nil && len(rep.Pending) > 0 {
			job.grouped = true
			b.importJobs.save(ctx, job)
			b.sortGroups(ctx, job)
			return
		}
	}
	text, kb := b.runImport(ctx, job, importer.Options{DryRun: true})
	if kb == nil {
		// Nothing to apply: the file did not parse.
		if j, ok := b.importJobs.take(ctx, job.id); ok {
			j.remove(b.log)
		}
	} else {
		// The report, for applying and mapping.
		b.importJobs.save(ctx, job)
	}
	b.send(ctx, job.chat, text, kb)
}

// runImport runs a job and renders the outcome.
func (b *Bot) runImport(ctx context.Context, job *importJob, opts importer.Options) (string, *telegram.InlineKeyboard) {
	name := filepath.Base(job.path)
	if job.cars {
		problems, res, err := b.imports.Vehicles(ctx, job.path, opts.DryRun)
		return vehicleImportView(job.id, name, problems, res, err, opts.DryRun)
	}
	rep, res, err := b.imports.Prices(ctx, job.supplier, job.path, opts)
	if opts.DryRun && rep != nil {
		job.report = rep
	}
	return priceImportView(job.id, name, job.name, rep, res, err, opts, b.matching != nil)
}

// applyImport is the "apply" button under a preview. force is the second,
// deliberate press past the deactivation limit.
func (b *Bot) applyImport(ctx context.Context, cc callbackCtx, id, force int64) (string, error) {
	if !b.isAdmin(cc.chat) {
		return "", errBadCallback
	}
	// Taken out before it runs: a second press of the same button, or a
	// press while the first is still running, finds nothing to apply.
	job, ok := b.importJobs.take(ctx, id)
	if !ok {
		return msgImportGone, b.removeButtons(ctx, cc)
	}
	b.typing(ctx, cc.chat)
	text, kb := b.runImport(ctx, job, importer.Options{Force: force == 1})
	if kb != nil {
		// Stopped again (the limit, a file that no longer parses): the job
		// stays, so the manager can still decide.
		b.importJobs.put(ctx, job)
	} else {
		job.remove(b.log)
	}
	if err := b.removeButtons(ctx, cc); err != nil {
		b.log.WarnContext(ctx, "could not remove import buttons", slog.Any("error", err))
	}
	b.send(ctx, cc.chat, text, kb)
	return "", nil
}

// cancelImport drops an uploaded file.
func (b *Bot) cancelImport(ctx context.Context, cc callbackCtx, id int64) (string, error) {
	if !b.isAdmin(cc.chat) {
		return "", errBadCallback
	}
	if job, ok := b.importJobs.take(ctx, id); ok {
		job.remove(b.log)
	}
	return msgImportCancelled, b.removeButtons(ctx, cc)
}

func (b *Bot) removeButtons(ctx context.Context, cc callbackCtx) error {
	if cc.msg == 0 {
		return nil
	}
	return b.tg.EditMessageReplyMarkup(ctx, cc.chat, cc.msg, &telegram.InlineKeyboard{})
}

func (b *Bot) typing(ctx context.Context, chat int64) {
	if err := b.tg.SendChatAction(ctx, chat, "typing"); err != nil {
		b.log.DebugContext(ctx, "could not send chat action", slog.Any("error", err))
	}
}

func (j *importJob) remove(log *slog.Logger) {
	if err := os.RemoveAll(j.dir); err != nil {
		log.Warn("could not remove an import file", slog.String("dir", j.dir), slog.Any("error", err))
	}
}

// removeStale removes the job directories older than ttl.
func removeStale(dir string, ttl time.Duration) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("bot: list uploads: %w", err)
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || time.Since(info.ModTime()) < ttl {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return fmt.Errorf("bot: remove an old upload: %w", err)
		}
	}
	return nil
}
