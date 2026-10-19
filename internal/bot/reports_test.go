package bot

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/reports"
	"github.com/aristodem96/ai_shop_bot/internal/telegram"
)

type reportStore struct{ asked []reports.Period }

func (s *reportStore) Report(_ context.Context, from, to time.Time) (*reports.Report, error) {
	s.asked = append(s.asked, reports.Period{From: from, To: to})
	return &reports.Report{Orders: 3, Revenue: domain.UAH(4350), Pickup: 1, Delivery: 2, OnOrder: domain.UAH(1450),
		Products:   []reports.Line{{Name: "Колодки", Brand: "BREMBO", Qty: 2, Sum: domain.UAH(2900)}},
		Categories: []reports.Line{{Name: "Гальмівна система › Колодки", Qty: 2, Sum: domain.UAH(2900)}},
		Requests:   reports.Requests{Made: 4, Quoted: 3, Paid: 1, NotFound: 1}}, nil
}

type fakeDocs struct {
	name    string
	caption string
	size    int
}

func (f *fakeDocs) SendDocument(_ context.Context, _ int64, name string, data []byte, caption string) error {
	f.name, f.caption, f.size = name, caption, len(data)
	return nil
}

func newReportsBot(t *testing.T) (testBot, *reportStore, *fakeDocs) {
	t.Helper()
	b, _ := newImportBot(t)
	loc, err := time.LoadLocation("Europe/Kyiv")
	require.NoError(t, err)
	store, docs := &reportStore{}, &fakeDocs{}
	b.UseReports(reports.New(store, loc), docs)
	return b, store, docs
}

func TestReportByPreset(t *testing.T) {
	b, store, docs := newReportsBot(t)
	ctx := context.Background()

	b.Handle(ctx, say(managers, "/report"))
	pick := b.tg.last("sendMessage")
	assert.Contains(t, callbacks(pick.markup), encode(actReport, int64(reports.ThisWeek)))

	b.Handle(ctx, pressIn(managers, encode(actReport, int64(reports.ThisWeek))))
	require.Len(t, store.asked, 1)
	rep := b.tg.last("editMessageText")
	for _, want := range []string{"Звіт: Цей тиждень", "Виручка: <b>4 350 грн</b> · 3 замовлення · середній чек 1 450 грн",
		"Самовивіз: 1 · 🚚 Нова Пошта: 2", "Продано під замовлення на 1 450 грн", "1. Колодки · BREMBO — 2 шт.",
		"Запити під замовлення: 4", "оплатили 1"} {
		assert.Contains(t, rep.text, want)
	}

	excel := callbacks(rep.markup)[0]
	b.Handle(ctx, pressIn(managers, excel))
	assert.Equal(t, store.asked[0], store.asked[1], "the file is the same period")
	assert.Contains(t, docs.name, "zvit_")
	assert.Positive(t, docs.size)
}

func TestReportByTypedDates(t *testing.T) {
	b, store, _ := newReportsBot(t)
	ctx := context.Background()

	b.Handle(ctx, pressIn(managers, encode(actReport, customPeriod)))
	ask := b.tg.last("sendMessage")
	assert.IsType(t, &telegram.ForceReply{}, ask.markup)

	answer := func(text string) {
		u := say(managers, text)
		u.Message.ReplyToMessage = &telegram.Message{Text: "📅 Період звіту: напишіть у відповідь дати"}
		b.Handle(ctx, u)
	}
	answer("вчора")
	assert.Contains(t, b.tg.calls[len(b.tg.calls)-2].text, "не розібрав дати")
	assert.Empty(t, store.asked)

	answer("01.10–15.10.2026")
	require.Len(t, store.asked, 1)
	assert.Equal(t, 1, store.asked[0].From.Day())
	assert.Equal(t, 16, store.asked[0].To.Day(), "the last day included")
	assert.Contains(t, b.tg.last("sendMessage").text, "Звіт: 01.10–15.10.2026")
}

func TestReportIsTheManagersOnly(t *testing.T) {
	b, store, _ := newReportsBot(t)
	b.Handle(context.Background(), press(encode(actReport, int64(reports.Today))))
	assert.Empty(t, store.asked)
}
