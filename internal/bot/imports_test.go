package bot

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aristodem96/ai_shop_bot/internal/importer"
	"github.com/aristodem96/ai_shop_bot/internal/telegram"
)

// managers is the managers' group in these tests.
const managers = int64(-100500)

// importRun is one call the bot made to the importer.
type importRun struct {
	supplier string // "" for the vehicle directory
	opts     importer.Options
	file     string // the file's contents at the time of the call
}

// fakeImports reports what it is told to and records each run. A file is
// recognised as the first supplier's when there is one; otherwise its
// header is header.
type fakeImports struct {
	suppliers []importer.Supplier
	header    []string
	runs      []importRun
	err       error // returned by Prices
	// unmatched replaces the report's unknown vehicle names when set.
	unmatched []importer.Count
}

func (f *fakeImports) Suppliers(context.Context) ([]importer.Supplier, error) {
	return f.suppliers, nil
}

func (f *fakeImports) Supplier(_ context.Context, id int64, _ string) (*importer.Supplier, error) {
	for i := range f.suppliers {
		if f.suppliers[i].ID == id {
			s := f.suppliers[i]
			return &s, nil
		}
	}
	return nil, importer.ErrNoSupplier
}

func (f *fakeImports) Inspect(context.Context, string) (*importer.Inspection, error) {
	if len(f.suppliers) > 0 {
		s := f.suppliers[0]
		return &importer.Inspection{Supplier: &s}, nil
	}
	return &importer.Inspection{Header: f.header, Detected: importer.Detect(f.header)}, nil
}

func (f *fakeImports) SaveSupplier(_ context.Context, s *importer.Supplier) error {
	for i := range f.suppliers {
		if f.suppliers[i].ID == s.ID {
			f.suppliers[i] = *s
			return nil
		}
		if f.suppliers[i].Name == s.Name {
			return importer.ErrSupplierTaken
		}
	}
	s.ID = int64(len(f.suppliers) + 1)
	f.suppliers = append(f.suppliers, *s)
	return nil
}

func (f *fakeImports) DeleteSupplier(_ context.Context, id int64) error {
	for i := range f.suppliers {
		if f.suppliers[i].ID == id {
			f.suppliers = append(f.suppliers[:i], f.suppliers[i+1:]...)
			return nil
		}
	}
	return nil
}

func (f *fakeImports) Prices(ctx context.Context, id int64, path string, opts importer.Options) (*importer.Report, importer.Result, error) {
	data, _ := os.ReadFile(path)
	name := ""
	if s, err := f.Supplier(ctx, id, ""); err == nil {
		name = s.Name
	}
	f.runs = append(f.runs, importRun{supplier: name, opts: opts, file: string(data)})
	rep := &importer.Report{File: filepath.Base(path), Rows: 81654, Offers: 80942, Skipped: 1,
		Problems:          []importer.Problem{{Row: importer.RowRef{Sheet: "Chery", Line: 9}, Reason: "немає назви"}},
		NewBrands:         []string{"Mogen", "<KYB>"},
		UnmatchedVehicles: []importer.Count{{Name: "Lada Priora", N: 3}},
		Categories:        []importer.Count{{Name: "brakes", N: 80942}},
		Unsorted:          2, Unrecognised: []string{"Ящик під магнітолу", "Бігунок"}}
	if f.unmatched != nil {
		rep.UnmatchedVehicles = f.unmatched
	}
	res := importer.Result{Inserted: 12, Updated: 80930, Fitments: 88341}
	if !opts.DryRun {
		res.RunID = 7
	}
	return rep, res, f.err
}

func (f *fakeImports) Vehicles(_ context.Context, path string, dryRun bool) ([]importer.Problem, importer.VehicleResult, error) {
	data, _ := os.ReadFile(path)
	f.runs = append(f.runs, importRun{opts: importer.Options{DryRun: dryRun}, file: string(data)})
	return nil, importer.VehicleResult{Inserted: 197, Updated: 12}, nil
}

// newImportBot is a managers' chat with the named suppliers.
func newImportBot(t *testing.T, suppliers ...string) (testBot, *fakeImports) {
	t.Helper()
	b := newTestBot()
	imports := &fakeImports{}
	for i, name := range suppliers {
		imports.suppliers = append(imports.suppliers, importer.Supplier{ID: int64(i + 1), Name: name,
			Columns: importer.Columns{Name: "Назва", Price: "Ціна"}})
	}
	files := &fakeFiles{data: map[string][]byte{"doc": []byte("Назва;Ціна\nКолодки;1049\n")}}
	require.NoError(t, b.UseImports(imports, files, filepath.Join(t.TempDir(), "imports"), managers))
	return b, imports
}

func upload(chatID int64, name, caption string) telegram.Update {
	return telegram.Update{Message: &telegram.Message{
		From:     &telegram.User{ID: 5},
		Chat:     telegram.Chat{ID: chatID, Type: "supergroup"},
		Caption:  caption,
		Document: &telegram.Document{FileID: "doc", FileName: name, FileSize: 30},
	}}
}

func pressIn(chatID int64, data string) telegram.Update {
	return telegram.Update{CallbackQuery: &telegram.CallbackQuery{
		ID: "cb", From: telegram.User{ID: 5}, Data: data,
		Message: &telegram.Message{MessageID: 901, Chat: telegram.Chat{ID: chatID, Type: "supergroup"}},
	}}
}

func TestImportPreviewThenApply(t *testing.T) {
	b, imports := newImportBot(t, "chinaavto")
	ctx := context.Background()

	b.Handle(ctx, upload(managers, "price.xlsx", ""))
	require.Len(t, imports.runs, 1, "a known supplier's file is checked at once, no caption needed")
	assert.Equal(t, importRun{supplier: "chinaavto", opts: importer.Options{DryRun: true}, file: "Назва;Ціна\nКолодки;1049\n"},
		imports.runs[0], "the file is checked first, as downloaded")

	preview := b.tg.last("sendMessage")
	assert.Equal(t, managers, preview.chat)
	for _, want := range []string{
		"price.xlsx", "постачальник «chinaavto»", "Рядків: 81 654", "Chery, рядок 9: немає назви",
		"&lt;KYB&gt;", "Lada Priora (3)", "Не впізнав категорію: 2", "Ящик під магнітолу, Бігунок", "➕ 12 нових", "✏️ 80 930 оновлено", "каталог ще не змінено",
	} {
		assert.Contains(t, preview.text, want)
	}
	apply := encode(actImportApply, 1, 0)
	require.Equal(t, []string{apply, encode(actImportCancel, 1)}, callbacks(preview.markup))

	b.Handle(ctx, pressIn(managers, apply))
	require.Len(t, imports.runs, 2)
	assert.Equal(t, importer.Options{}, imports.runs[1].opts, "applied for real, not forced")
	result := b.tg.last("sendMessage")
	assert.Contains(t, result.text, "✅ <b>Застосовано:</b>")
	assert.Contains(t, result.text, "Запис №7")
	assert.Nil(t, result.markup, "nothing left to decide")
	assert.Equal(t, int64(901), b.tg.last("editMessageReplyMarkup").msg, "the preview's buttons are gone")

	b.Handle(ctx, pressIn(managers, apply))
	assert.Len(t, imports.runs, 2, "a second press applies nothing")
	assert.Equal(t, msgImportGone, b.tg.last("answerCallbackQuery").text)
	assert.Empty(t, importFiles(t, b), "the file is deleted once applied")
}

func TestNewFileSetsUpASupplier(t *testing.T) {
	b, imports := newImportBot(t)
	imports.header = []string{"Код", "Номенклатура", "Производитель", "Цена"}
	ctx := context.Background()

	b.Handle(ctx, upload(managers, "export.xlsx", ""))
	assert.Empty(t, imports.runs, "a new file is set up before it is checked")
	setup := b.tg.last("sendMessage")
	for _, want := range []string{"новий для мене файл", "✅ Назва: «Номенклатура»", "✅ Ціна: «Цена»",
		"✅ Код товару: «Код»", "✅ Бренд: «Производитель»", "▫️ Залишок: —"} {
		assert.Contains(t, setup.text, want)
	}

	// The brand is in fact something else: no such column.
	b.Handle(ctx, pressIn(managers, encode(actSetupField, 1, 4)))
	assert.Contains(t, callbacks(b.tg.last("editMessageText").markup), encode(actSetupColumn, 1, 4, 2))
	b.Handle(ctx, pressIn(managers, encode(actSetupColumn, 1, 4, -1)))
	assert.Contains(t, b.tg.last("editMessageText").text, "▫️ Бренд: —")

	b.Handle(ctx, pressIn(managers, encode(actSetupNext, 1)))
	assert.Contains(t, b.tg.last("editMessageText").text, "немає колонки із залишками", "no stock: asks the delivery time")
	b.Handle(ctx, pressIn(managers, encode(actSetupLead, 1, 2)))
	assert.Contains(t, b.tg.last("editMessageText").text, "націнку")
	b.Handle(ctx, pressIn(managers, encode(actSetupMarkup, 1, 20)))
	assert.IsType(t, &telegram.ForceReply{}, b.tg.last("sendMessage").markup, "the name is asked by reply")

	b.Handle(ctx, reply(managers, "Наша 1С"))
	require.Len(t, imports.suppliers, 1)
	s := imports.suppliers[0]
	assert.Equal(t, "Наша 1С", s.Name)
	assert.Equal(t, importer.Columns{ExternalID: "Код", Name: "Номенклатура", Price: "Цена"}, s.Columns)
	assert.Equal(t, 2, *s.LeadDays)
	assert.InDelta(t, 20.0, s.MarkupPercent, 0.001)
	require.Len(t, imports.runs, 1, "and the file is checked as its")
	assert.Equal(t, "Наша 1С", imports.runs[0].supplier)
}

func TestChangedFileUpdatesTheKnownSupplier(t *testing.T) {
	b, imports := newImportBot(t)
	imports.header = []string{"Артикул", "Назва", "Ціна", "Залишок"}
	imports.suppliers = []importer.Supplier{{ID: 7, Name: "China Avto", Source: "china-avto",
		Columns: importer.Columns{PartNumber: "Кат. номер", Name: "Назва", Price: "Ціна"}}}
	ctx := context.Background()

	// The supplier renamed a column: the file no longer reads as its.
	in := imports.suppliers
	imports.suppliers = nil
	b.Handle(ctx, upload(managers, "price.xlsx", ""))
	imports.suppliers = in
	b.Handle(ctx, pressIn(managers, encode(actSetupNext, 1)))
	assert.Contains(t, b.tg.last("editMessageText").text, "націнку", "a stock column: no delivery time to ask")
	b.Handle(ctx, pressIn(managers, encode(actSetupMarkup, 1, 0)))
	assert.Contains(t, callbacks(b.tg.last("editMessageText").markup), encode(actSetupSupplier, 1, 7))
	b.Handle(ctx, pressIn(managers, encode(actSetupSupplier, 1, 7)))

	require.Len(t, imports.suppliers, 1, "not a second supplier")
	assert.Equal(t, "Артикул", imports.suppliers[0].Columns.PartNumber)
	assert.Equal(t, "china-avto", imports.suppliers[0].Source, "its products are matched by the same key")
	assert.Equal(t, "China Avto", imports.runs[0].supplier)
}

func TestVehicleDirectoryByCaption(t *testing.T) {
	b, imports := newImportBot(t, "chinaavto")
	b.Handle(context.Background(), upload(managers, "cars.csv", "Авто"))
	require.Len(t, imports.runs, 1)
	assert.Empty(t, imports.runs[0].supplier)
	assert.Contains(t, b.tg.last("sendMessage").text, "➕ 197 нових авто")
}

func TestSuppliersList(t *testing.T) {
	b, imports := newImportBot(t, "China Avto")
	ctx := context.Background()

	b.Handle(ctx, say(managers, "/suppliers"))
	assert.Contains(t, b.tg.last("sendMessage").text, "<b>China Avto</b> — залишки з файлу")
	b.Handle(ctx, pressIn(managers, encode(actSupplierOpen, 1)))
	assert.Contains(t, b.tg.last("editMessageText").text, "• Назва — «Назва»")
	b.Handle(ctx, pressIn(managers, encode(actSupplierMarkup, 1, 15)))
	assert.InDelta(t, 15.0, imports.suppliers[0].MarkupPercent, 0.001)
	assert.Contains(t, b.tg.last("editMessageText").text, "націнка 15%")

	b.Handle(ctx, pressIn(managers, encode(actSupplierDelete, 1, 1)))
	assert.Empty(t, imports.suppliers)
}

func TestImportStoppedByLimitOffersForce(t *testing.T) {
	b, imports := newImportBot(t, "chinaavto")
	ctx := context.Background()
	imports.err = &importer.DeactivationError{Would: 60000, Active: 80942, LimitPercent: 20}

	b.Handle(ctx, upload(managers, "price.xlsx", ""))
	stopped := b.tg.last("sendMessage")
	assert.Contains(t, stopped.text, "зняв би з продажу 60 000 з 80 942")
	force := encode(actImportApply, 1, 1)
	require.Equal(t, []string{force, encode(actImportCancel, 1)}, callbacks(stopped.markup))

	imports.err = nil
	b.Handle(ctx, pressIn(managers, force))
	assert.Equal(t, importer.Options{Force: true}, imports.runs[1].opts, "forcing is the second, deliberate press")
}

func TestImportHeaderErrorExplains(t *testing.T) {
	b, imports := newImportBot(t, "chinaavto")
	imports.err = &importer.HeaderError{Missing: []string{"Назва"}, Sheet: "Chery", Line: 6, Found: []string{"Авто", "Найменування"}}

	b.Handle(context.Background(), upload(managers, "price.xlsx", ""))
	msg := b.tg.last("sendMessage")
	assert.Contains(t, msg.text, "Бракує колонок: «Назва»")
	assert.Contains(t, msg.text, "«Найменування»", "the closest row shows the new name")
	assert.Nil(t, msg.markup, "nothing to apply")
	assert.Empty(t, importFiles(t, b), "and nothing kept")
}

func TestImportOnlyInManagersChat(t *testing.T) {
	b, imports := newImportBot(t, "chinaavto")
	ctx := context.Background()

	u := upload(chat, "price.xlsx", "")
	u.Message.Chat.Type = telegram.ChatPrivate
	b.Handle(ctx, u)
	assert.Empty(t, imports.runs, "a buyer's file is not a price list")

	b.Handle(ctx, upload(managers, "price.xlsx", ""))
	b.Handle(ctx, pressIn(chat, encode(actImportApply, 1, 1)))
	assert.Len(t, imports.runs, 1, "a forged button from another chat applies nothing")
}

func TestImportRefusesOtherFiles(t *testing.T) {
	b, imports := newImportBot(t, "chinaavto")
	ctx := context.Background()

	b.Handle(ctx, upload(managers, "price.xls", ""))
	assert.Equal(t, msgImportBadType, b.tg.last("sendMessage").text)

	big := upload(managers, "price.xlsx", "")
	big.Message.Document.FileSize = maxImportBytes + 1
	b.Handle(ctx, big)
	assert.Equal(t, msgImportTooBig, b.tg.last("sendMessage").text)
	assert.Empty(t, imports.runs)
}

func TestImportCancel(t *testing.T) {
	b, imports := newImportBot(t, "chinaavto")
	ctx := context.Background()
	b.Handle(ctx, upload(managers, "price.xlsx", ""))

	b.Handle(ctx, pressIn(managers, encode(actImportCancel, 1)))
	assert.Equal(t, msgImportCancelled, b.tg.last("answerCallbackQuery").text)
	assert.Empty(t, importFiles(t, b))
	b.Handle(ctx, pressIn(managers, encode(actImportApply, 1, 0)))
	assert.Len(t, imports.runs, 1, "a cancelled file cannot be applied")
}

func TestNum(t *testing.T) {
	for n, want := range map[int]string{0: "0", 999: "999", 1000: "1 000", 80942: "80 942", 1234567: "1 234 567", -1500: "-1 500"} {
		assert.Equal(t, want, num(n))
	}
}

// importFiles lists the job directories left in the import directory.
func importFiles(t *testing.T, b testBot) []string {
	t.Helper()
	entries, err := os.ReadDir(b.importDir)
	require.NoError(t, err)
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	return names
}
