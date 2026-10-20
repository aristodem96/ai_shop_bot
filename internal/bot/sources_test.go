package bot

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/reports"
)

type fakeSources struct{ marks map[int64]string }

func (f *fakeSources) Record(_ context.Context, user int64, mark string) error {
	if _, ok := f.marks[user]; !ok {
		f.marks[user] = mark
	}
	return nil
}

func TestStartRecordsWhereTheBuyerCameFrom(t *testing.T) {
	b, _, _ := withOrders()
	src := &fakeSources{marks: map[int64]string{}}
	b.UseSources(src, "zviahel_parts_bot")
	ctx := context.Background()

	b.Handle(ctx, text("/start STO_Ivan"))
	assert.Equal(t, "sto_ivan", src.marks[chat], "the link's mark, in one spelling")
	assert.Equal(t, msgWelcomeButtons, b.tg.calls[0].text, "and the buyer is greeted as with a plain /start")

	b.Handle(ctx, text("/start olx"))
	assert.Equal(t, "sto_ivan", src.marks[chat], "the first channel keeps the buyer")

	other := text("/start <script>")
	other.Message.From.ID, other.Message.Chat.ID = 7, 7
	b.Handle(ctx, other)
	assert.Equal(t, "", src.marks[7], "a mark Telegram would not carry counts as none")
}

func TestLinkMakesAMarkedLink(t *testing.T) {
	b, _, _ := withOrders()
	b.UseSources(&fakeSources{marks: map[int64]string{}}, "zviahel_parts_bot")
	ctx := context.Background()

	b.Handle(ctx, say(adminChat, "/link QR_kasa"))
	assert.Contains(t, b.tg.last("sendMessage").text, "https://t.me/zviahel_parts_bot?start=qr_kasa")
	b.Handle(ctx, say(adminChat, "/link майстер Іван"))
	assert.Equal(t, msgLinkHelp, b.tg.last("sendMessage").text, "Latin letters, digits, _ and - only")

	b.Handle(ctx, text("/link qr_kasa"))
	assert.Equal(t, msgUnknownCommand, b.tg.last("sendMessage").text, "a managers' command, nothing to a buyer")
}

func TestReportSaysWhichChannelBrings(t *testing.T) {
	r := &reports.Report{Period: reports.Period{Label: "Цей тиждень"}, Sources: []reports.Source{
		{Mark: "sto_ivan", NewBuyers: 5, Orders: 3, Revenue: domain.UAH(4500)},
		{Mark: "", NewBuyers: 2, Orders: 1, Revenue: domain.UAH(900)},
	}}
	text, _ := reportView(r)
	assert.Contains(t, text, "Звідки прийшли покупці")
	assert.Contains(t, text, "• sto_ivan — нових 5 · замовлень 3 на 4 500 грн")
	assert.Contains(t, text, "• без мітки — нових 2")

	r.Sources = []reports.Source{{Mark: "", NewBuyers: 9}}
	text, _ = reportView(r)
	assert.NotContains(t, text, "Звідки прийшли", "until links are handed out, there is nothing to say")
}
