package bot

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aristodem96/ai_shop_bot/internal/categories"
	"github.com/aristodem96/ai_shop_bot/internal/grouping"
	"github.com/aristodem96/ai_shop_bot/internal/importer"
)

// fakeGrouping has two doubtful groups and one proposal until created.
type fakeGrouping struct {
	doubts   []grouping.Group
	proposed bool
	decided  map[int64]int64
	sorted   int
}

func newFakeGrouping() *fakeGrouping {
	return &fakeGrouping{proposed: true, decided: map[int64]int64{}, doubts: []grouping.Group{
		{ID: 11, Key: "РІЗНЕ", Products: 15, Examples: []string{"Болт", "Наклейка"},
			Suggestion: &grouping.Suggestion{CategoryID: 4, Note: "різні товари"}},
		{ID: 12, Key: "Шумоізоляція", Products: 9},
	}}
}

func (f *fakeGrouping) CanSuggest() bool { return true }
func (f *fakeGrouping) Sort(ctx context.Context, s int64) (*grouping.Summary, error) {
	f.sorted++
	sum, _ := f.Summary(ctx, s)
	sum.Filed, sum.FiledProducts = 380, 2310
	return sum, nil
}
func (f *fakeGrouping) Summary(context.Context, int64) (*grouping.Summary, error) {
	sum := &grouping.Summary{Doubts: len(f.doubts)}
	for _, d := range f.doubts {
		sum.DoubtProducts += d.Products
	}
	if f.proposed {
		sum.Proposals = []grouping.Proposal{{Path: "Інструмент › Ручний інструмент", Groups: 14, Products: 160}}
	}
	return sum, nil
}
func (f *fakeGrouping) CreateProposed(context.Context, int64) (int, int, error) {
	f.proposed = false
	return 2, 14, nil
}
func (f *fakeGrouping) Doubts(context.Context, int64) ([]grouping.Group, error) { return f.doubts, nil }
func (f *fakeGrouping) Decide(_ context.Context, id, cat int64) (*grouping.Group, error) {
	for i, d := range f.doubts {
		if d.ID == id {
			if cat == 0 {
				cat = d.Suggestion.CategoryID
			}
			f.decided[id] = cat
			f.doubts = append(f.doubts[:i], f.doubts[i+1:]...)
			d.CategoryID = cat
			return &d, nil
		}
	}
	return nil, assert.AnError
}
func (f *fakeGrouping) SortByName(ctx context.Context, id int64) (*grouping.Group, error) {
	return f.Decide(ctx, id, -1)
}

func (f *fakeGrouping) Get(_ context.Context, id int64) (*grouping.Group, error) {
	for _, d := range f.doubts {
		if d.ID == id {
			return &d, nil
		}
	}
	return nil, assert.AnError
}
func (f *fakeGrouping) SuggestionPath(_ context.Context, s *grouping.Suggestion) (string, error) {
	if s == nil {
		return "", nil
	}
	return "Інше", nil
}
func (f *fakeGrouping) Find(context.Context, string) ([]grouping.Group, error) { return f.doubts, nil }

func TestFileWithNewGroupsIsSortedFirst(t *testing.T) {
	b, imports := newImportBot(t, "Наша 1С")
	b.UseCategories(categories.New(newTreeStore()))
	g := newFakeGrouping()
	b.UseGrouping(g)
	imports.pending = []importer.PendingGroup{{GroupKey: importer.GroupKey{Kind: "group", Key: "РІЗНЕ"}, Products: 15}}
	ctx := context.Background()

	b.Handle(ctx, upload(managers, "zalyshky.xlsx", ""))
	assert.Equal(t, 1, g.sorted, "the model files the groups before the report")
	sum := b.tg.last("sendMessage")
	for _, want := range []string{"380 груп</b> (2 310 товарів)", "Інструмент › Ручний інструмент — 14 груп, 160 товарів",
		"Сумніваюся щодо 2 груп"} {
		assert.Contains(t, sum.text, want)
	}
	cbs := callbacks(sum.markup)
	require.Len(t, cbs, 3)

	b.Handle(ctx, pressIn(managers, cbs[0]))
	assert.False(t, g.proposed, "the owner's yes creates them")
	created := b.tg.last("editMessageText")
	assert.Contains(t, created.text, "Створено категорій: 2")

	b.Handle(ctx, pressIn(managers, callbacks(created.markup)[0]))
	doubt := b.tg.last("editMessageText")
	assert.Contains(t, doubt.text, "«РІЗНЕ»</b> — 15 товарів <i>(1 з 2)</i>")
	assert.Contains(t, doubt.text, "Моя здогадка: <b>Інше</b> — різні товари")

	b.Handle(ctx, pressIn(managers, callbacks(doubt.markup)[0]))
	assert.Equal(t, int64(4), g.decided[11], "accepting the guess")
	next := b.tg.last("editMessageText")
	assert.Contains(t, next.text, "«Шумоізоляція»", "the next doubt takes its place")
	assert.NotContains(t, next.text, "здогадка", "no guess, no accept button")

	runs := len(imports.runs)
	b.Handle(ctx, pressIn(managers, callbacks(next.markup)[1]))
	assert.Contains(t, b.tg.last("editMessageText").text, "Сумнівні групи розібрано", "skipping the last one ends the review")
	assert.Greater(t, len(imports.runs), runs, "and the file is checked again")
	assert.Contains(t, b.tg.last("sendMessage").text, "каталог ще не змінено", "with its report")
}

// The model files hundreds of groups for minutes; the handler that started
// it has a deadline of a minute. The work, and the message at its end, must
// outlive the handler.
func TestSlowWorkOutlivesItsHandler(t *testing.T) {
	b := newTestBot()
	handler, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	<-handler.Done() // the handler's deadline has passed

	got := make(chan error, 1)
	b.inBackground(handler, func(ctx context.Context) {
		got <- ctx.Err()
	})
	select {
	case err := <-got:
		assert.NoError(t, err, "the background has a deadline of its own")
	case <-time.After(5 * time.Second):
		t.Fatal("the background work never ran")
	}
}
