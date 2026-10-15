package bot

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"
	"unicode/utf8"

	"github.com/aristodem96/ai_shop_bot/internal/agent"
	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/telegram"
)

// Consultant answers free text. agent.Agent is the implementation.
type Consultant interface {
	Reply(ctx context.Context, userID, chatID int64, text string) (*agent.Reply, error)
	Reset(ctx context.Context, chatID int64) error
}

// Limiter caps model calls per user.
type Limiter interface {
	Allow(ctx context.Context, userID int64) (bool, time.Duration, error)
}

// UseConsultant turns on the AI consultant for free text. Without it the bot
// still works fully through buttons and answers free text with the catalogue
// menu -- the shop does not go down with the model provider.
func (b *Bot) UseConsultant(c Consultant, l Limiter) {
	b.consultant, b.limiter = c, l
}

// maxQuestionRunes bounds what one message can send to the model. Input
// tokens are billed, and a shop question does not need a page of text.
const maxQuestionRunes = 1000

// typingEvery refreshes "typing...", which Telegram clears after about five
// seconds, while a slow answer is being prepared.
const typingEvery = 4 * time.Second

func (b *Bot) consult(ctx context.Context, chat, user int64, text string) {
	if b.consultant == nil {
		b.send(ctx, chat, msgFreeTextSoon, nil)
		b.sendCategories(ctx, chat, user)
		return
	}
	if utf8.RuneCountInString(text) > maxQuestionRunes {
		b.send(ctx, chat, msgTooLong, nil)
		return
	}

	ok, retry, err := b.limiter.Allow(ctx, user)
	if err != nil {
		// Closed, not open: the limit is what protects the model budget, and
		// the buttons keep working while it is unreachable.
		b.log.ErrorContext(ctx, "rate limiter unavailable", slog.Any("error", err))
		b.send(ctx, chat, msgConsultantDown, nil)
		return
	}
	if !ok {
		minutes := int(math.Ceil(retry.Minutes()))
		b.send(ctx, chat, fmt.Sprintf(msgRateLimited, max(minutes, 1)), nil)
		return
	}

	stopTyping := b.keepTyping(ctx, chat)
	reply, err := b.consultant.Reply(ctx, user, chat, text)
	stopTyping()

	if err != nil {
		if agent.IsUnavailable(err) {
			b.log.WarnContext(ctx, "consultant unavailable", slog.Any("error", err))
			b.send(ctx, chat, msgConsultantDown, nil)
			return
		}
		b.fail(ctx, chat, "consultant reply", err)
		return
	}

	// Cards are drawn from the catalogue, not from anything the model said:
	// the name and price on a button are always the database's.
	products := make([]domain.Product, 0, len(reply.ProductIDs))
	for _, id := range reply.ProductIDs {
		p, err := b.catalog.Product(ctx, id)
		if err != nil {
			continue // taken off sale in the seconds since the agent saw it
		}
		products = append(products, *p)
	}
	msg, kb := consultantView(reply.Text, products, reply.CartChanged, reply.CarSaved)
	b.send(ctx, chat, msg, kb)
}

// keepTyping shows "typing..." until the returned function is called.
func (b *Bot) keepTyping(ctx context.Context, chat int64) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(typingEvery)
		defer t.Stop()
		for {
			// Cosmetic, so a failure is not worth a log line per tick.
			_ = b.tg.SendChatAction(ctx, chat, telegram.ChatActionTyping)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

func (b *Bot) resetConversation(ctx context.Context, chat int64) {
	if b.consultant == nil {
		b.send(ctx, chat, msgFreeTextSoon, nil)
		return
	}
	if err := b.consultant.Reset(ctx, chat); err != nil {
		b.fail(ctx, chat, "reset conversation", err)
		return
	}
	b.send(ctx, chat, msgConversationReset, nil)
}
