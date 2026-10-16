package bot

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aristodem96/ai_shop_bot/internal/importer"
	"github.com/aristodem96/ai_shop_bot/internal/telegram"
)

type fakeMatching struct {
	canSuggest bool
	suggestErr error
	aliases    []string // "name→ids" as confirmed
	removed    []int64
}

var matchModels = []importer.Model{
	{ID: 11, Make: "Chery", Model: "Tiggo", YearFrom: 2005, YearTo: 2016},
	{ID: 12, Make: "Chery", Model: "Tiggo 4", YearFrom: 2017},
	{ID: 21, Make: "Lada", Model: "Priora", YearFrom: 2007, YearTo: 2018},
}

func (f *fakeMatching) CanSuggest() bool { return f.canSuggest }

func (f *fakeMatching) SuggestVehicles(context.Context, []importer.UnknownVehicle) (map[string]importer.VehicleSuggestion, error) {
	if f.suggestErr != nil {
		return nil, f.suggestErr
	}
	return map[string]importer.VehicleSuggestion{
		"Lada Priora": {Name: "Lada Priora", ModelIDs: []int64{11, 12}, Note: "у назвах є TIGGO 4"},
	}, nil
}

func (f *fakeMatching) VehicleModels(context.Context) ([]importer.Model, error) {
	return matchModels, nil
}

func (f *fakeMatching) AddVehicleAlias(_ context.Context, profile, name string, ids []int64) error {
	f.aliases = append(f.aliases, profile+":"+name+fmtIDs(ids))
	return nil
}

func (f *fakeMatching) VehicleAliases(context.Context) ([]importer.VehicleAlias, error) {
	return []importer.VehicleAlias{{ID: 5, Source: "chinaavto", Name: "Chery Tiggo", Model: "Chery Tiggo"}}, nil
}

func (f *fakeMatching) RemoveVehicleAlias(_ context.Context, id int64) (*importer.VehicleAlias, error) {
	f.removed = append(f.removed, id)
	return &importer.VehicleAlias{ID: id, Name: "Chery Tiggo", Model: "Chery Tiggo", Fitments: 4243}, nil
}

func fmtIDs(ids []int64) string {
	s := ""
	for _, id := range ids {
		s += fmt.Sprintf("→%d", id)
	}
	return s
}

func newMatchBot(t *testing.T, canSuggest bool) (testBot, *fakeImports, *fakeMatching) {
	t.Helper()
	b, imports := newImportBot(t, "chinaavto")
	m := &fakeMatching{canSuggest: canSuggest}
	b.UseVehicleMatching(m)
	return b, imports, m
}

func TestMatchVehiclesFromTheReport(t *testing.T) {
	b, imports, m := newMatchBot(t, true)
	ctx := context.Background()

	b.Handle(ctx, upload(managers, "price.xlsx", ""))
	preview := b.tg.last("sendMessage")
	start := encode(actMatchStart, 1)
	require.Equal(t, start, callbacks(preview.markup)[0], "mapping comes before applying")

	b.Handle(ctx, pressIn(managers, start))
	item := b.tg.last("sendMessage")
	for _, want := range []string{"1 з 1", "«Lada Priora» — 3 рядків", "💡 у назвах є TIGGO 4", "Chery Tiggo (2005–2016)", "Chery Tiggo 4 (2017–н.ч.)"} {
		assert.Contains(t, item.text, want)
	}
	assert.Equal(t, []string{
		encode(actMatchPick, 1, 0), encode(actMatchPick, 1, 11), encode(actMatchPick, 1, 12),
		encode(actMatchMakes, 1), encode(actMatchSkip, 1), encode(actMatchStop, 1),
	}, callbacks(item.markup))

	runs := len(imports.runs)
	b.Handle(ctx, pressIn(managers, encode(actMatchPick, 1, 0)))
	assert.Equal(t, []string{"chinaavto:Lada Priora→11→12"}, m.aliases, "all suggested, for this supplier")
	assert.Contains(t, b.tg.last("editMessageText").text, "зіставлено 1, пропущено 0")
	require.Len(t, imports.runs, runs+1, "the file is checked again")
	assert.True(t, imports.runs[runs].opts.DryRun)
	assert.Contains(t, callbacks(b.tg.last("sendMessage").markup), encode(actImportApply, 1, 0), "with Apply under it")
}

func TestMatchByHand(t *testing.T) {
	b, _, m := newMatchBot(t, false)
	ctx := context.Background()
	b.Handle(ctx, upload(managers, "price.xlsx", ""))
	b.Handle(ctx, pressIn(managers, encode(actMatchStart, 1)))
	assert.Contains(t, b.tg.last("sendMessage").text, "Оберіть модель з довідника", "no model configured: by hand")

	b.Handle(ctx, pressIn(managers, encode(actMatchMakes, 1)))
	makes := b.tg.last("editMessageText")
	assert.Equal(t, encode(actMatchMake, 1, 0), callbacks(makes.markup)[0])
	assert.Equal(t, "Chery", makes.markup.(*telegram.InlineKeyboard).Rows[0][0].Text)

	b.Handle(ctx, pressIn(managers, encode(actMatchMake, 1, 1)))
	assert.Contains(t, callbacks(b.tg.last("editMessageText").markup), encode(actMatchPick, 1, 21))
	b.Handle(ctx, pressIn(managers, encode(actMatchPick, 1, 21)))
	assert.Equal(t, []string{"chinaavto:Lada Priora→21"}, m.aliases)

	b.Handle(ctx, pressIn(managers, encode(actMatchPick, 1, 99)))
	assert.Len(t, m.aliases, 1, "a finished session takes no more")
}

func TestMatchSurvivesModelFailure(t *testing.T) {
	b, _, m := newMatchBot(t, true)
	m.suggestErr = errors.New("llm: provider unavailable")
	ctx := context.Background()
	b.Handle(ctx, upload(managers, "price.xlsx", ""))
	b.Handle(ctx, pressIn(managers, encode(actMatchStart, 1)))
	item := b.tg.last("sendMessage")
	assert.Contains(t, item.text, "Підказка ШІ зараз недоступна")
	assert.Contains(t, callbacks(item.markup), encode(actMatchMakes, 1), "picking by hand still works")
}

func TestMatchRejectsUnknownModelAndForeignChat(t *testing.T) {
	b, _, m := newMatchBot(t, true)
	ctx := context.Background()
	b.Handle(ctx, upload(managers, "price.xlsx", ""))
	b.Handle(ctx, pressIn(managers, encode(actMatchStart, 1)))

	b.Handle(ctx, pressIn(managers, encode(actMatchPick, 1, 404)))
	b.Handle(ctx, pressIn(chat, encode(actMatchPick, 1, 0)))
	assert.Empty(t, m.aliases, "an id outside the directory, or a button from another chat, maps nothing")
}

func TestCarsListAndDelete(t *testing.T) {
	b, _, m := newMatchBot(t, true)
	ctx := context.Background()

	b.Handle(ctx, say(managers, "/cars"))
	list := b.tg.last("sendMessage")
	assert.Contains(t, list.text, "«Chery Tiggo» → Chery Tiggo <i>(chinaavto)</i>")
	b.Handle(ctx, pressIn(managers, encode(actCarDelete, 5)))
	assert.Equal(t, []int64{5}, m.removed)
	assert.Equal(t, "Прибрано «Chery Tiggo» → Chery Tiggo. Знято сумісностей: 4243.", b.tg.last("answerCallbackQuery").text)
}
