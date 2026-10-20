package bot

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
)

// Sources records where buyers came from. postgres.SourceRepository is
// the implementation.
type Sources interface {
	// Record keeps the mark a buyer first came with.
	Record(ctx context.Context, userID int64, source string) error
}

// UseSources has the bot remember, from the link that opened it, where each
// buyer came from -- a QR code at the till, an ad, a mechanic -- for the
// sales report to say which channel brings buyers. botName is the bot's
// username, for /link to write links with.
func (b *Bot) UseSources(s Sources, botName string) { b.sources, b.botName = s, botName }

// cmdLink writes a managers' link with a mark.
const cmdLink = "/link"

// sourceMark is what Telegram lets a start link carry: letters, digits, _
// and -, up to 64.
var sourceMark = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// recordSource keeps the mark of the link a buyer started the bot with, ""
// when there was none.
func (b *Bot) recordSource(ctx context.Context, user int64, payload string) {
	if b.sources == nil {
		return
	}
	mark := strings.ToLower(strings.TrimSpace(payload))
	if !sourceMark.MatchString(mark) {
		mark = ""
	}
	if err := b.sources.Record(ctx, user, mark); err != nil {
		b.log.WarnContext(ctx, "could not record where a buyer came from", slog.Int64("user_id", user), slog.Any("error", err))
	}
}

// handleLink answers /link: a link with a mark for one channel, to print
// on a QR code or hand a mechanic.
func (b *Bot) handleLink(ctx context.Context, chat int64, arg string) {
	mark := strings.ToLower(strings.TrimSpace(arg))
	if b.sources == nil || b.botName == "" {
		b.send(ctx, chat, msgLinkUnavailable, nil)
		return
	}
	if !sourceMark.MatchString(mark) {
		b.send(ctx, chat, msgLinkHelp, nil)
		return
	}
	b.send(ctx, chat, fmt.Sprintf(msgLinkMade, esc(mark), b.botName, mark, esc(mark)), nil)
}
