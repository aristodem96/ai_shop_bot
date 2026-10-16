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
	profile string // "" for the vehicle directory
	opts    importer.Options
	file    string // the file's contents at the time of the call
}

// fakeImports reports what it is told to and records each run.
type fakeImports struct {
	profiles []string
	runs     []importRun
	err      error // returned by Prices
}

func (f *fakeImports) Profiles() ([]string, error) { return f.profiles, nil }

func (f *fakeImports) Prices(_ context.Context, profile, path string, opts importer.Options) (*importer.Report, importer.Result, error) {
	data, _ := os.ReadFile(path)
	f.runs = append(f.runs, importRun{profile: profile, opts: opts, file: string(data)})
	rep := &importer.Report{File: filepath.Base(path), Rows: 81654, Offers: 80942, Skipped: 1,
		Problems:          []importer.Problem{{Row: importer.RowRef{Sheet: "Chery", Line: 9}, Reason: "немає назви"}},
		NewBrands:         []string{"Mogen", "<KYB>"},
		UnmatchedVehicles: []importer.Count{{Name: "Lada Priora", N: 3}},
		Categories:        []importer.Count{{Name: "brakes", N: 80942}},
		Unsorted:          2, Unrecognised: []string{"Ящик під магнітолу", "Бігунок"}}
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

func newImportBot(t *testing.T, profiles ...string) (testBot, *fakeImports) {
	t.Helper()
	b := newTestBot()
	imports := &fakeImports{profiles: profiles}
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
	require.Len(t, imports.runs, 1, "one profile: no need to ask which")
	assert.Equal(t, importRun{profile: "chinaavto", opts: importer.Options{DryRun: true}, file: "Назва;Ціна\nКолодки;1049\n"},
		imports.runs[0], "the file is checked first, as downloaded")

	preview := b.tg.last("sendMessage")
	assert.Equal(t, managers, preview.chat)
	for _, want := range []string{
		"price.xlsx", "профіль «chinaavto»", "Рядків: 81 654", "Chery, рядок 9: немає назви",
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

func TestImportAsksWhichProfile(t *testing.T) {
	b, imports := newImportBot(t, "chinaavto", "demo-stock")
	ctx := context.Background()

	b.Handle(ctx, upload(managers, "cars.csv", ""))
	assert.Empty(t, imports.runs, "with two profiles the bot does not guess")
	ask := b.tg.last("sendMessage")
	assert.Equal(t, msgImportChoose, ask.text)
	assert.Equal(t, []string{
		encode(actImportProfile, 1, 0), encode(actImportProfile, 1, 1), encode(actImportProfile, 1, 2), encode(actImportCancel, 1),
	}, callbacks(ask.markup))

	b.Handle(ctx, pressIn(managers, encode(actImportProfile, 1, 2)))
	require.Len(t, imports.runs, 1)
	assert.Empty(t, imports.runs[0].profile, "the last button is the vehicle directory")
	assert.Contains(t, b.tg.last("sendMessage").text, "➕ 197 нових авто")
}

func TestImportCaptionPicksTarget(t *testing.T) {
	b, imports := newImportBot(t, "chinaavto", "demo-stock")
	ctx := context.Background()

	b.Handle(ctx, upload(managers, "price.csv", " Demo-Stock "))
	require.Len(t, imports.runs, 1)
	assert.Equal(t, "demo-stock", imports.runs[0].profile)

	b.Handle(ctx, upload(managers, "cars.csv", "авто"))
	require.Len(t, imports.runs, 2)
	assert.Empty(t, imports.runs[1].profile)

	b.Handle(ctx, upload(managers, "price.csv", "постачальник"))
	assert.Len(t, imports.runs, 2)
	assert.Contains(t, b.tg.last("sendMessage").text, "chinaavto, demo-stock, авто", "an unknown name lists the known ones")
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
