package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aristodem96/ai_shop_bot/internal/reports"
	"github.com/aristodem96/ai_shop_bot/internal/telegram"
)

// Reports sums up the shop's sales. reports.Service is the implementation.
type Reports interface {
	Period(p reports.Preset) (reports.Period, error)
	ParsePeriod(text string) (reports.Period, error)
	Report(ctx context.Context, p reports.Period) (*reports.Report, error)
	Location() *time.Location
}

// Documents sends files. telegram.Client is the implementation.
type Documents interface {
	SendDocument(ctx context.Context, chatID int64, filename string, data []byte, captionHTML string) error
}

// cmdReport opens the sales report in the managers' chat.
const cmdReport = "/report"

// customPeriod is the button that asks for dates.
const customPeriod = 0

// UseReports turns on /report in the managers' chat.
func (b *Bot) UseReports(r Reports, docs Documents) { b.reports, b.docs = r, docs }

// handleReportMessage takes /report and the dates it asks for. It reports
// whether the message was one of them.
func (b *Bot) handleReportMessage(ctx context.Context, m *telegram.Message) bool {
	chat := m.Chat.ID
	if _, ok := commandArg(m.Text, cmdReport); ok {
		b.send(ctx, chat, msgReportPick, periodKeyboard())
		return true
	}
	// The dates answer the question below; the question's text says so,
	// so the answer is recognised after a restart too.
	if m.ReplyToMessage == nil || !strings.HasPrefix(m.ReplyToMessage.Text, msgReportAskPrefix) {
		return false
	}
	p, err := b.reports.ParsePeriod(m.Text)
	if err != nil {
		if errors.Is(err, reports.ErrBadDates) {
			b.send(ctx, chat, "❌ "+esc(err.Error()), nil)
			if err := b.askDates(ctx, chat); err != nil {
				b.fail(ctx, chat, "ask for dates", err)
			}
			return true
		}
		b.fail(ctx, chat, "read a period", err)
		return true
	}
	b.sendReport(ctx, callbackCtx{chat: chat}, p)
	return true
}

// routeReports dispatches the report's buttons.
func (b *Bot) routeReports(ctx context.Context, cc callbackCtx, cb callback) (string, error) {
	if !b.isAdmin(cc.chat) {
		return "", errBadCallback
	}
	if cb.act == actReportExcel {
		loc := b.reports.Location()
		p := reports.Between(time.Unix(cb.args[0], 0).In(loc), time.Unix(cb.args[1], 0).In(loc))
		return msgReportFile, b.sendExcel(ctx, cc.chat, p)
	}
	if cb.args[0] == customPeriod {
		return "", b.askDates(ctx, cc.chat)
	}
	p, err := b.reports.Period(reports.Preset(cb.args[0]))
	if err != nil {
		return "", errBadCallback
	}
	b.typing(ctx, cc.chat)
	b.sendReport(ctx, cc, p)
	return "", nil
}

func (b *Bot) askDates(ctx context.Context, chat int64) error {
	return b.tg.SendMessage(ctx, chat, msgReportAsk, &telegram.ForceReply{ForceReply: true, Placeholder: "01.10–15.10"})
}

// sendReport shows a period's report, in the pressed message when there is
// one.
func (b *Bot) sendReport(ctx context.Context, cc callbackCtx, p reports.Period) {
	r, err := b.reports.Report(ctx, p)
	if err != nil {
		b.fail(ctx, cc.chat, "build a report", err)
		return
	}
	text, kb := reportView(r)
	b.show(ctx, cc, text, kb)
}

// sendExcel sends the period's report as a workbook.
func (b *Bot) sendExcel(ctx context.Context, chat int64, p reports.Period) error {
	b.typing(ctx, chat)
	r, err := b.reports.Report(ctx, p)
	if err != nil {
		return err
	}
	data, err := reports.Excel(r)
	if err != nil {
		return err
	}
	name := "zvit_" + strings.NewReplacer("–", "_", ".", "-").Replace(reports.Between(p.From, p.To).Label) + ".xlsx"
	return b.docs.SendDocument(ctx, chat, name, data, fmt.Sprintf("📊 Звіт за %s", esc(p.Label)))
}
