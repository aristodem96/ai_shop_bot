// Package reports sums up the shop's sales over a period for its managers:
// a day, a week, a month, or any dates they name.
//
// A sale is an order paid within the period, counted by when it was paid,
// not placed: an order placed on the 31st and paid on the 1st is the new
// month's money. A paid order cancelled since is a refund, not a sale. The
// days are the shop's, in its time zone: "today" in Kyiv starts at
// midnight in Kyiv, not in UTC.
package reports

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
)

// Period is a span of days, To excluded.
type Period struct {
	From, To time.Time
	// Label names it for people: "Цей тиждень", "01.10–15.10.2026".
	Label string
}

// Report is the shop's sales over a period.
type Report struct {
	Period Period
	// Paid orders: those paid within the period and not cancelled since.
	Orders  int
	Revenue domain.Money
	// Pickup and Delivery split Orders by how buyers receive them.
	Pickup, Delivery int
	// Refunds are orders paid within the period and cancelled since.
	Refunds   int
	RefundSum domain.Money
	// Unpaid are orders placed within the period and never paid.
	Unpaid int
	// OnOrder is the revenue of parts sold on order, a supplier's to buy.
	OnOrder domain.Money
	// Products and Categories are what sold, most money first.
	Products   []Line
	Categories []Line
	Requests   Requests
	// Sales are the paid orders, one by one, for the spreadsheet.
	Sales []Sale
}

// AverageCheck is the revenue per paid order.
func (r *Report) AverageCheck() domain.Money {
	if r.Orders == 0 {
		return 0
	}
	return r.Revenue / domain.Money(r.Orders)
}

// Line is a product or a category and what it sold.
type Line struct {
	Name  string
	Brand string
	Qty   int
	Sum   domain.Money
}

// Requests sums up the buyers' requests for parts on order made in the
// period.
type Requests struct {
	Made, Quoted, NotFound, Declined, Expired, Waiting int
	// Paid are requests whose quoted part was paid for.
	Paid int
}

// Sale is one paid order.
type Sale struct {
	ID       int64
	PaidAt   time.Time
	Customer string
	Phone    string
	Pickup   bool
	Delivery string
	Status   domain.OrderStatus
	Total    domain.Money
	Items    int
}

// Store reads the numbers.
type Store interface {
	Report(ctx context.Context, from, to time.Time) (*Report, error)
}

// Service builds reports.
type Service struct {
	store Store
	loc   *time.Location
	now   func() time.Time
}

// New returns a Service counting days in loc.
func New(store Store, loc *time.Location) *Service {
	return &Service{store: store, loc: loc, now: time.Now}
}

// Report builds the report of a period.
func (s *Service) Report(ctx context.Context, p Period) (*Report, error) {
	r, err := s.store.Report(ctx, p.From, p.To)
	if err != nil {
		return nil, err
	}
	r.Period = p
	return r, nil
}

// Preset is a period the managers pick by a button.
type Preset int

// Presets, in the order the buttons show them.
const (
	Today Preset = iota + 1
	Yesterday
	ThisWeek
	LastWeek
	ThisMonth
	LastMonth
)

// Presets lists them with their labels.
var Presets = []struct {
	Preset Preset
	Label  string
}{
	{Today, "Сьогодні"}, {Yesterday, "Вчора"}, {ThisWeek, "Цей тиждень"}, {LastWeek, "Минулий тиждень"},
	{ThisMonth, "Цей місяць"}, {LastMonth, "Минулий місяць"},
}

// ErrBadPreset means no such preset.
var ErrBadPreset = errors.New("reports: no such period")

// Period resolves a preset against now, in the shop's time zone. A week
// starts on Monday. A running period ends now; the rest end at midnight.
func (s *Service) Period(p Preset) (Period, error) {
	now := s.now().In(s.loc)
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, s.loc)
	monday := day.AddDate(0, 0, -((int(day.Weekday()) + 6) % 7))
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, s.loc)
	var from, to time.Time
	switch p {
	case Today:
		from, to = day, day.AddDate(0, 0, 1)
	case Yesterday:
		from, to = day.AddDate(0, 0, -1), day
	case ThisWeek:
		from, to = monday, monday.AddDate(0, 0, 7)
	case LastWeek:
		from, to = monday.AddDate(0, 0, -7), monday
	case ThisMonth:
		from, to = month, month.AddDate(0, 1, 0)
	case LastMonth:
		from, to = month.AddDate(0, -1, 0), month
	default:
		return Period{}, ErrBadPreset
	}
	for _, x := range Presets {
		if x.Preset == p {
			return Period{From: from, To: to, Label: x.Label + " (" + span(from, to) + ")"}, nil
		}
	}
	return Period{}, ErrBadPreset
}

// span writes a period's days: "20.10.2026", "13.10–19.10.2026".
func span(from, to time.Time) string {
	last := to.AddDate(0, 0, -1)
	if !last.After(from) {
		return from.Format("02.01.2006")
	}
	if from.Year() == last.Year() {
		return from.Format("02.01") + "–" + last.Format("02.01.2006")
	}
	return from.Format("02.01.2006") + "–" + last.Format("02.01.2006")
}

// ErrBadDates is a period the managers wrote that cannot be read.
var ErrBadDates = errors.New("не розібрав дати: напишіть, наприклад, 01.10–15.10 або 01.10.2026-15.10.2026, або одну дату")

// maxDays bounds a period one report covers.
const maxDays = 366

// datePattern is a day, a month and optionally a year.
var datePattern = regexp.MustCompile(`(\d{1,2})\.(\d{1,2})(?:\.(\d{2,4}))?`)

// ParsePeriod reads the dates a manager typed: one day, or two, both
// included, "01.10–15.10" in the current year unless it says otherwise.
func (s *Service) ParsePeriod(text string) (Period, error) {
	now := s.now().In(s.loc)
	ms := datePattern.FindAllStringSubmatch(text, 3)
	if len(ms) == 0 || len(ms) > 2 {
		return Period{}, ErrBadDates
	}
	days := make([]time.Time, len(ms))
	for i, m := range ms {
		d, _ := strconv.Atoi(m[1])
		mo, _ := strconv.Atoi(m[2])
		y := now.Year()
		if m[3] != "" {
			y, _ = strconv.Atoi(m[3])
			if y < 100 {
				y += 2000
			}
		}
		t := time.Date(y, time.Month(mo), d, 0, 0, 0, 0, s.loc)
		if t.Day() != d || int(t.Month()) != mo {
			return Period{}, ErrBadDates // 31.02 and the like
		}
		days[i] = t
	}
	from, last := days[0], days[len(days)-1]
	if last.Before(from) {
		from, last = last, from
	}
	to := last.AddDate(0, 0, 1)
	if to.Sub(from) > maxDays*24*time.Hour {
		return Period{}, fmt.Errorf("%w (не більше року за раз)", ErrBadDates)
	}
	return Period{From: from, To: to, Label: span(from, to)}, nil
}

// Between is the period from one day to another, To excluded, as a button
// carries it.
func Between(from, to time.Time) Period {
	return Period{From: from, To: to, Label: span(from, to)}
}

// Location is the time zone the shop's days are counted in.
func (s *Service) Location() *time.Location { return s.loc }
