package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/aristodem96/ai_shop_bot/internal/agent"
	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/llm"
	"github.com/aristodem96/ai_shop_bot/internal/telegram"
)

// Consultant answers free text and photos. agent.Agent is the implementation.
type Consultant interface {
	Reply(ctx context.Context, userID, chatID int64, text string, images ...llm.Image) (*agent.Reply, error)
	Reset(ctx context.Context, chatID int64) error
}

// Limiter caps model calls per user.
type Limiter interface {
	Allow(ctx context.Context, userID int64) (bool, time.Duration, error)
}

// Files downloads what buyers send. telegram.Client is the implementation.
type Files interface {
	GetFile(ctx context.Context, fileID string) (*telegram.File, error)
	DownloadFile(ctx context.Context, filePath string, maxBytes int64) ([]byte, error)
}

// UseConsultant turns on the AI consultant for free text and photos. Without
// it the bot still works fully through buttons and answers free text with the
// catalogue menu -- the shop does not go down with the model provider.
func (b *Bot) UseConsultant(c Consultant, l Limiter, f Files) {
	b.consultant, b.limiter, b.files = c, l, f
}

// maxQuestionRunes bounds what one message can send to the model. Input
// tokens are billed, and a parts question does not need a page of text.
const maxQuestionRunes = 1000

// maxPhotoBytes is the largest photo passed to the model; the API takes up to
// 5 MB per image. Telegram compresses photos far below that, so the limit
// only bites on something unusual.
const maxPhotoBytes = 5 << 20

// photoQuestion stands in for a photo sent without a caption.
const photoQuestion = "Що це за деталь? Підбери нову або аналог для мого авто."

// typingEvery refreshes "typing...", which Telegram clears after about five
// seconds, while a slow answer is being prepared.
const typingEvery = 4 * time.Second

// consult answers free text.
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
	if !b.allow(ctx, chat, user) {
		return
	}
	stopTyping := b.keepTyping(ctx, chat)
	reply, err := b.consultant.Reply(ctx, user, chat, text)
	stopTyping()
	b.answer(ctx, chat, reply, err)
}

// consultPhoto answers a photo: usually a worn or broken part the buyer wants
// a new one of.
func (b *Bot) consultPhoto(ctx context.Context, chat, user int64, m *telegram.Message) {
	if b.consultant == nil {
		b.send(ctx, chat, msgPhotoNeedsConsultant, nil)
		return
	}
	question := m.Caption
	if question == "" {
		question = photoQuestion
	}
	if utf8.RuneCountInString(question) > maxQuestionRunes {
		b.send(ctx, chat, msgTooLong, nil)
		return
	}
	// The limit is checked before downloading: a photo costs model tokens
	// like a message does, and a rejected one should not cost bandwidth.
	if !b.allow(ctx, chat, user) {
		return
	}

	stopTyping := b.keepTyping(ctx, chat)
	defer stopTyping()

	img, err := b.downloadPhoto(ctx, m.Photo)
	if err != nil {
		stopTyping()
		if errors.Is(err, errNotAnImage) || errors.Is(err, telegram.ErrFileTooLarge) {
			b.send(ctx, chat, msgBadPhoto, nil)
			return
		}
		b.fail(ctx, chat, "download photo", err)
		return
	}

	reply, err := b.consultant.Reply(ctx, user, chat, question, *img)
	stopTyping()
	b.answer(ctx, chat, reply, err)
}

var errNotAnImage = errors.New("bot: not a supported image")

// downloadPhoto fetches the largest size of a photo that fits the limit.
// Telegram lists sizes smallest first; the biggest gives the model the best
// chance of reading a number stamped on the part.
func (b *Bot) downloadPhoto(ctx context.Context, sizes []telegram.PhotoSize) (*llm.Image, error) {
	var best *telegram.PhotoSize
	for i := range sizes {
		s := &sizes[i]
		if s.FileSize > maxPhotoBytes {
			continue
		}
		if best == nil || s.Width*s.Height > best.Width*best.Height {
			best = s
		}
	}
	if best == nil {
		return nil, telegram.ErrFileTooLarge
	}

	f, err := b.files.GetFile(ctx, best.FileID)
	if err != nil {
		return nil, err
	}
	data, err := b.files.DownloadFile(ctx, f.FilePath, maxPhotoBytes)
	if err != nil {
		return nil, err
	}

	// The media type is read from the bytes, not taken from the file name:
	// the API rejects an image whose declared type does not match it.
	switch mt := http.DetectContentType(data); mt {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return &llm.Image{MediaType: mt, Data: data}, nil
	default:
		return nil, fmt.Errorf("%w: %s", errNotAnImage, mt)
	}
}

// allow applies the per-user limit and tells the buyer when it is reached.
func (b *Bot) allow(ctx context.Context, chat, user int64) bool {
	ok, retry, err := b.limiter.Allow(ctx, user)
	if err != nil {
		// Closed, not open: the limit is what protects the model budget, and
		// the buttons keep working while it is unreachable.
		b.log.ErrorContext(ctx, "rate limiter unavailable", slog.Any("error", err))
		b.send(ctx, chat, msgConsultantDown, nil)
		return false
	}
	if !ok {
		minutes := int(math.Ceil(retry.Minutes()))
		b.send(ctx, chat, fmt.Sprintf(msgRateLimited, max(minutes, 1)), nil)
		return false
	}
	return true
}

// answer sends the consultant's reply, or explains why there is none.
func (b *Bot) answer(ctx context.Context, chat int64, reply *agent.Reply, err error) {
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
	var parts []domain.CatalogPart
	for _, id := range reply.CatalogPartIDs {
		if b.parts == nil {
			break
		}
		p, err := b.parts.CatalogPart(ctx, id)
		if err != nil {
			continue // replaced by a recrawl since
		}
		parts = append(parts, *p)
	}
	msg, kb := consultantView(reply.Text, products, parts, reply.CartChanged, reply.CarSaved)
	b.send(ctx, chat, msg, kb)
}

// keepTyping shows "typing..." until the returned function is called. It is
// safe to call the returned function more than once.
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
