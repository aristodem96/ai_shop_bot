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
	proposals  []importer.ProposeRequest
	added      [][]importer.Vehicle
}

func (f *fakeMatching) CanPropose() bool { return f.canSuggest }

func (f *fakeMatching) ProposeVehicles(_ context.Context, req importer.ProposeRequest) (*importer.VehicleProposal, error) {
	f.proposals = append(f.proposals, req)
	vs := []importer.Vehicle{
		{Make: "Peugeot", Model: "308 T7", YearFrom: 2007, YearTo: 2013, Engine: "1.6 VTi", Fuel: "petrol"},
		{Make: "Peugeot", Model: "308 T7", YearFrom: 2007, YearTo: 2013, Engine: "1.6 HDi", Fuel: "diesel"},
	}
	if req.Correction != "" {
		vs = vs[1:]
	}
	return &importer.VehicleProposal{Vehicles: vs, Note: "перше покоління 308"}, nil
}

func (f *fakeMatching) AddVehicles(_ context.Context, vs []importer.Vehicle) ([]int64, error) {
	f.added = append(f.added, vs)
	return []int64{31}, nil
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

func (f *fakeMatching) AddVehicleAlias(_ context.Context, supplier int64, name string, ids []int64) error {
	f.aliases = append(f.aliases, fmt.Sprint(supplier)+":"+name+fmtIDs(ids))
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
		encode(actMatchPick, 1, 0, 0), encode(actMatchPick, 1, 0, 11), encode(actMatchPick, 1, 0, 12),
		encode(actMatchMakes, 1), encode(actMatchAdd, 1, 0), encode(actMatchSkip, 1, 0), encode(actMatchStop, 1),
	}, callbacks(item.markup))

	runs := len(imports.runs)
	b.Handle(ctx, pressIn(managers, encode(actMatchPick, 1, 0, 0)))
	assert.Equal(t, []string{"1:Lada Priora→11→12"}, m.aliases, "all suggested, for this supplier")
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
	assert.Contains(t, callbacks(b.tg.last("editMessageText").markup), encode(actMatchPick, 1, 0, 21))
	b.Handle(ctx, pressIn(managers, encode(actMatchPick, 1, 0, 21)))
	assert.Equal(t, []string{"1:Lada Priora→21"}, m.aliases)

	b.Handle(ctx, pressIn(managers, encode(actMatchPick, 1, 0, 99)))
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

	b.Handle(ctx, pressIn(managers, encode(actMatchPick, 1, 0, 404)))
	b.Handle(ctx, pressIn(chat, encode(actMatchPick, 1, 0, 0)))
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

func TestDoubleTapDoesNotConfirmTheNextName(t *testing.T) {
	b, imports, m := newMatchBot(t, true)
	ctx := context.Background()
	imports.unmatched = []importer.Count{{Name: "Lada Priora", N: 3}, {Name: "Chery Tiggo", N: 2}}
	b.Handle(ctx, upload(managers, "price.xlsx", ""))
	b.Handle(ctx, pressIn(managers, encode(actMatchStart, 1)))

	b.Handle(ctx, pressIn(managers, encode(actMatchPick, 1, 0, 11)))
	b.Handle(ctx, pressIn(managers, encode(actMatchPick, 1, 0, 11)))
	assert.Equal(t, []string{"1:Lada Priora→11"}, m.aliases, "the second tap was for the first name, and is stale")
	assert.Equal(t, msgMatchStale, b.tg.last("answerCallbackQuery").text)
}

func TestAddVehicleFromMatching(t *testing.T) {
	b, _, m := newMatchBot(t, true)
	ctx := context.Background()
	b.Handle(ctx, upload(managers, "price.xlsx", ""))
	b.Handle(ctx, pressIn(managers, encode(actMatchStart, 1)))

	b.Handle(ctx, pressIn(managers, encode(actMatchAdd, 1, 0)))
	require.Len(t, m.proposals, 1)
	assert.Equal(t, "Lada Priora", m.proposals[0].Name, "the supplier's name and its products go to the model")
	draft := b.tg.last("editMessageText")
	for _, want := range []string{"У довідник</b> для «Lada Priora»", "1. Peugeot 308 T7 · 2007–2013 · 1.6 VTi · бензин", "2. Peugeot 308 T7 · 2007–2013 · 1.6 HDi · дизель", "пропозиція ШІ"} {
		assert.Contains(t, draft.text, want)
	}
	assert.Equal(t, encode(actCarAdd, 1), callbacks(draft.markup)[0])

	b.Handle(ctx, pressIn(managers, encode(actCarToggle, 1, 0)))
	assert.Contains(t, b.tg.last("editMessageText").text, "<s>Peugeot 308 T7 · 2007–2013 · 1.6 VTi · бензин</s> (прибрано)")
	assert.Equal(t, "✅ Додати (1)", b.tg.last("editMessageText").markup.(*telegram.InlineKeyboard).Rows[0][0].Text)

	b.Handle(ctx, pressIn(managers, encode(actCarAdd, 1)))
	require.Len(t, m.added, 1)
	assert.Equal(t, "1.6 HDi", m.added[0][0].Engine, "only what the owner kept is written")
	assert.Len(t, m.added[0], 1)
	assert.Equal(t, []string{"1:Lada Priora→31"}, m.aliases, "the supplier's name maps to the new vehicle")
	assert.Contains(t, b.tg.last("editMessageText").text, "зіставлено 1", "and the session moves on")
}

func TestAddVehicleCorrectedByReply(t *testing.T) {
	b, _, m := newMatchBot(t, true)
	ctx := context.Background()
	b.Handle(ctx, upload(managers, "price.xlsx", ""))
	b.Handle(ctx, pressIn(managers, encode(actMatchStart, 1)))
	b.Handle(ctx, pressIn(managers, encode(actMatchAdd, 1, 0)))

	b.Handle(ctx, pressIn(managers, encode(actCarText, 1)))
	_, isReply := b.tg.last("sendMessage").markup.(*telegram.ForceReply)
	assert.True(t, isReply)
	b.Handle(ctx, say(managers, "тільки дизель"))
	require.Len(t, m.proposals, 2)
	assert.Equal(t, "тільки дизель", m.proposals[1].Correction)
	assert.Len(t, m.proposals[1].Previous, 2, "the correction is made on the previous proposal")
	draft := b.tg.last("sendMessage")
	assert.Contains(t, draft.text, "1. Peugeot 308 T7 · 2007–2013 · 1.6 HDi · дизель")
	assert.NotContains(t, draft.text, "VTi")
}

func TestCarCommand(t *testing.T) {
	b, _, m := newMatchBot(t, true)
	ctx := context.Background()

	b.Handle(ctx, say(managers, "/car"))
	assert.Equal(t, msgCarHelp, b.tg.last("sendMessage").text)

	b.Handle(ctx, say(managers, "/car Peugeot 308 T7 дизель"))
	assert.Equal(t, "Peugeot 308 T7 дизель", m.proposals[0].Name)
	b.Handle(ctx, pressIn(managers, encode(actCarAdd, 1)))
	require.Len(t, m.added, 1)
	assert.Len(t, m.added[0], 2)
	assert.Empty(t, m.aliases, "outside an import there is no supplier name to map")
	assert.Contains(t, b.tg.last("editMessageText").text, "✅ Додано в довідник")
}

func TestCarCommandByHand(t *testing.T) {
	b, _, m := newMatchBot(t, false)
	ctx := context.Background()

	b.Handle(ctx, say(managers, "/car Peugeot, 308 T7, 2007, 2013, 1.6 HDi, дизель"))
	assert.Empty(t, m.proposals, "no model: the line is read as typed")
	draft := b.tg.last("sendMessage")
	assert.Contains(t, draft.text, "1. Peugeot 308 T7 · 2007–2013 · 1.6 HDi · дизель")

	b.Handle(ctx, say(managers, "/car Peugeot 308"))
	assert.Contains(t, b.tg.last("sendMessage").text, "через кому", "and a line it cannot read is asked again")
}
