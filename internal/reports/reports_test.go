package reports

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xuri/excelize/v2"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
)

func kyiv(t *testing.T) *time.Location {
	loc, err := time.LoadLocation("Europe/Kyiv")
	require.NoError(t, err)
	return loc
}

func service(t *testing.T, now time.Time) *Service {
	s := New(nil, kyiv(t))
	s.now = func() time.Time { return now }
	return s
}

func TestPresets(t *testing.T) {
	loc := kyiv(t)
	// Tuesday 20 October 2026, 00:30 in Kyiv: still Monday in UTC.
	s := service(t, time.Date(2026, 10, 20, 0, 30, 0, 0, loc))
	day := func(m time.Month, d int) time.Time { return time.Date(2026, m, d, 0, 0, 0, 0, loc) }
	for p, want := range map[Preset][2]time.Time{
		Today:     {day(10, 20), day(10, 21)},
		Yesterday: {day(10, 19), day(10, 20)},
		ThisWeek:  {day(10, 19), day(10, 26)},
		LastWeek:  {day(10, 12), day(10, 19)},
		ThisMonth: {day(10, 1), day(11, 1)},
		LastMonth: {day(9, 1), day(10, 1)},
	} {
		got, err := s.Period(p)
		require.NoError(t, err)
		assert.Equal(t, want, [2]time.Time{got.From, got.To}, "preset %d: the shop's days, not UTC's", p)
	}
	p, _ := s.Period(LastWeek)
	assert.Equal(t, "Минулий тиждень (12.10–18.10.2026)", p.Label)
	p, _ = s.Period(Today)
	assert.Equal(t, "Сьогодні (20.10.2026)", p.Label)
	_, err := s.Period(Preset(99))
	require.ErrorIs(t, err, ErrBadPreset)
}

func TestParsePeriod(t *testing.T) {
	loc := kyiv(t)
	s := service(t, time.Date(2026, 10, 20, 12, 0, 0, 0, loc))
	for in, want := range map[string]string{
		"01.10–15.10":            "01.10–15.10.2026",
		"1.10 - 15.10.2026":      "01.10–15.10.2026",
		"з 15.09.26 по 01.10.26": "15.09–01.10.2026",
		"15.10.2026":             "15.10.2026",
		"15.10–01.10":            "01.10–15.10.2026",
		"20.12.2025-10.01.2026":  "20.12.2025–10.01.2026",
	} {
		p, err := s.ParsePeriod(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, p.Label, in)
	}
	p, _ := s.ParsePeriod("01.10–15.10")
	assert.Equal(t, time.Date(2026, 10, 16, 0, 0, 0, 0, loc), p.To, "the last day is included")
	for _, bad := range []string{"вчора", "31.02", "01.01.2024-01.01.2026", "1.1 2.2 3.3"} {
		_, err := s.ParsePeriod(bad)
		require.ErrorIs(t, err, ErrBadDates, bad)
	}
}

func TestExcel(t *testing.T) {
	r := &Report{Period: Period{Label: "Цей тиждень"}, Orders: 2, Revenue: domain.UAH(3000), Pickup: 1, Delivery: 1,
		Products: []Line{{Name: "Колодки", Brand: "BREMBO", Qty: 2, Sum: domain.UAH(2900)}},
		Sales: []Sale{{ID: 7, PaidAt: time.Date(2026, 10, 20, 10, 5, 0, 0, time.UTC), Customer: "Тарас", Pickup: true,
			Status: domain.OrderPaid, Total: domain.UAH(1450), Items: 1}}}
	data, err := Excel(r)
	require.NoError(t, err)
	f, err := excelize.OpenReader(bytes.NewReader(data))
	require.NoError(t, err)
	assert.Equal(t, []string{"Підсумок", "Замовлення", "Товари", "Категорії"}, f.GetSheetList())
	v, _ := f.GetCellValue("Підсумок", "B5")
	assert.Equal(t, "1500", v, "the average check")
	v, _ = f.GetCellValue("Замовлення", "E2")
	assert.Equal(t, "Самовивіз", v)
	v, _ = f.GetCellValue("Товари", "A2")
	assert.Equal(t, "Колодки", v)
}

type fakeStore struct{ from, to time.Time }

func (f *fakeStore) Report(_ context.Context, from, to time.Time) (*Report, error) {
	f.from, f.to = from, to
	return &Report{Orders: 1}, nil
}

func TestReportKeepsThePeriod(t *testing.T) {
	store := &fakeStore{}
	s := New(store, kyiv(t))
	p := Period{From: time.Unix(0, 0), To: time.Unix(86400, 0), Label: "x"}
	r, err := s.Report(context.Background(), p)
	require.NoError(t, err)
	assert.Equal(t, p, r.Period)
	assert.Equal(t, p.From, store.from)
}
