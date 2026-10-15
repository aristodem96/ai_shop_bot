package bot

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/aristodem96/ai_shop_bot/internal/agent"
	"github.com/aristodem96/ai_shop_bot/internal/llm"
)

type fakeConsultant struct {
	reply  *agent.Reply
	err    error
	asked  []string
	resets int
}

func (f *fakeConsultant) Reply(_ context.Context, _, _ int64, text string) (*agent.Reply, error) {
	f.asked = append(f.asked, text)
	return f.reply, f.err
}

func (f *fakeConsultant) Reset(context.Context, int64) error {
	f.resets++
	return nil
}

type fakeLimiter struct {
	allow bool
	err   error
}

func (f fakeLimiter) Allow(context.Context, int64) (bool, time.Duration, error) {
	return f.allow, 90 * time.Second, f.err
}

func withConsultant(c *fakeConsultant, l Limiter) (*Bot, *fakeTelegram) {
	b, tg, _ := newTestBot()
	b.UseConsultant(c, l)
	return b, tg
}

func TestConsultantAnswerWithCards(t *testing.T) {
	c := &fakeConsultant{reply: &agent.Reply{
		Text:       "Ось <пуховик> & ще дещо",
		ProductIDs: []int64{jacketID, 404}, // 404 left the catalogue meanwhile
	}}
	b, tg := withConsultant(c, fakeLimiter{allow: true})

	b.Handle(context.Background(), text("тепла куртка"))

	assert.Equal(t, []string{"тепла куртка"}, c.asked)
	msg := tg.last("sendMessage")
	assert.Equal(t, "Ось &lt;пуховик&gt; &amp; ще дещо", msg.text, "model output is escaped, never trusted as HTML")
	assert.Equal(t, []string{encode(actProduct, jacketID)}, callbacks(msg.markup),
		"a card per product that still exists")
	assert.Contains(t, tg.methods(), "sendChatAction", "the buyer sees typing while the model works")
}

func TestConsultantCartButtonAfterAdding(t *testing.T) {
	c := &fakeConsultant{reply: &agent.Reply{Text: "Додав.", CartChanged: true}}
	b, tg := withConsultant(c, fakeLimiter{allow: true})

	b.Handle(context.Background(), text("додай M"))
	assert.Contains(t, callbacks(tg.last("sendMessage").markup), encode(actCart))
}

func TestConsultantRateLimited(t *testing.T) {
	c := &fakeConsultant{}
	b, tg := withConsultant(c, fakeLimiter{allow: false})

	b.Handle(context.Background(), text("ще одне питання"))

	assert.Empty(t, c.asked, "the model is not called over the limit")
	assert.Contains(t, tg.last("sendMessage").text, "за 2 хв", "the wait is rounded up")
}

func TestConsultantFailsClosedWithoutLimiter(t *testing.T) {
	c := &fakeConsultant{}
	b, tg := withConsultant(c, fakeLimiter{err: errors.New("redis: connection refused")})

	b.Handle(context.Background(), text("куртка"))
	assert.Empty(t, c.asked)
	assert.Equal(t, msgConsultantDown, tg.last("sendMessage").text)
}

func TestConsultantProviderOutage(t *testing.T) {
	c := &fakeConsultant{err: llm.ErrUnavailable}
	b, tg := withConsultant(c, fakeLimiter{allow: true})

	b.Handle(context.Background(), text("куртка"))
	assert.Equal(t, msgConsultantDown, tg.last("sendMessage").text)
}

func TestConsultantRejectsLongMessages(t *testing.T) {
	c := &fakeConsultant{}
	b, tg := withConsultant(c, fakeLimiter{allow: true})

	b.Handle(context.Background(), text(strings.Repeat("я", maxQuestionRunes+1)))
	assert.Empty(t, c.asked)
	assert.Equal(t, msgTooLong, tg.last("sendMessage").text)
}

func TestNonTextMessage(t *testing.T) {
	c := &fakeConsultant{}
	b, tg := withConsultant(c, fakeLimiter{allow: true})

	b.Handle(context.Background(), text(""))
	assert.Empty(t, c.asked)
	assert.Equal(t, msgTextOnly, tg.last("sendMessage").text)
}

func TestNewConversation(t *testing.T) {
	c := &fakeConsultant{}
	b, tg := withConsultant(c, fakeLimiter{allow: true})

	b.Handle(context.Background(), text("/new"))
	assert.Equal(t, 1, c.resets)
	assert.Equal(t, msgConversationReset, tg.last("sendMessage").text)
}

func TestWithoutConsultantFreeTextShowsCatalogue(t *testing.T) {
	b, tg, _ := newTestBot()
	b.Handle(context.Background(), text("тепла куртка"))

	assert.Equal(t, msgFreeTextSoon, tg.calls[0].text)
	assert.False(t, slices.Contains(tg.methods(), "sendChatAction"))
}

func TestConsultantCardsWithoutText(t *testing.T) {
	c := &fakeConsultant{reply: &agent.Reply{Text: "  ", ProductIDs: []int64{jacketID}}}
	b, tg := withConsultant(c, fakeLimiter{allow: true})

	b.Handle(context.Background(), text("куртка"))
	assert.Equal(t, msgHereYouGo, tg.last("sendMessage").text, "Telegram rejects an empty message")
}
