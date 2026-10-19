package bot

import (
	"context"
	"log/slog"
	"time"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/enrich"
	"github.com/aristodem96/ai_shop_bot/internal/telegram"
)

// Photos sends photos by URL, whatever their format. photos.Photos is the
// implementation.
type Photos interface {
	Send(ctx context.Context, chatID int64, url, captionHTML string, markup telegram.ReplyMarkup) error
}

// Enricher brings what the parts catalogue knows of a part. enrich.Enricher
// is the implementation.
type Enricher interface {
	// Product matches a product to its catalogue article, if not done yet.
	Product(ctx context.Context, productID int64) error
	Article(ctx context.Context, supplierID int64, number string) (*enrich.Article, error)
}

// UsePhotos sends photos through p: converted and kept by Telegram's id.
func (b *Bot) UsePhotos(p Photos) { b.photos = p }

// UseEnrichment fills cards from the parts catalogue as they are opened.
func (b *Bot) UseEnrichment(e Enricher) { b.enricher = e }

// enrichWait bounds the catalogue lookup a card waits for. The catalogue
// answers in a second or two; past this the card goes without, and the
// background pass finishes the job for the next view.
const enrichWait = 5 * time.Second

// enrich matches a product before its card is drawn, so the first buyer to
// open it already sees the photo.
func (b *Bot) enrich(ctx context.Context, productID int64) {
	if b.enricher == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, enrichWait)
	defer cancel()
	if err := b.enricher.Product(ctx, productID); err != nil {
		b.log.WarnContext(ctx, "could not enrich a product", slog.Int64("product_id", productID), slog.Any("error", err))
	}
}

// catalogPhoto is the photo of a catalogue part, "" when there is none.
func (b *Bot) catalogPhoto(ctx context.Context, p *domain.CatalogPart) string {
	if b.enricher == nil || p.SupplierID == 0 {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, enrichWait)
	defer cancel()
	a, err := b.enricher.Article(ctx, p.SupplierID, p.Number)
	if err != nil {
		b.log.WarnContext(ctx, "could not read a catalogue part", slog.Int64("part_id", p.ID), slog.Any("error", err))
		return ""
	}
	if len(a.Images) == 0 {
		return ""
	}
	return a.Images[0]
}
