package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/telegram"
)

// Navigation that starts from a text message (the menu, a product page, the
// cart) edits that message in place, so the chat does not fill up with stale
// menus. A product card is a photo and cannot become a text message, so
// leaving it -- to a page, to similar products -- sends a new one.

func (b *Bot) sendCategories(ctx context.Context, chat int64) {
	cats, err := b.catalog.Categories(ctx)
	if err != nil {
		b.fail(ctx, chat, "list categories", err)
		return
	}
	text, kb := categoriesView(cats)
	b.send(ctx, chat, text, kb)
}

func (b *Bot) editCategories(ctx context.Context, cc callbackCtx) error {
	cats, err := b.catalog.Categories(ctx)
	if err != nil {
		return err
	}
	text, kb := categoriesView(cats)
	return b.editText(ctx, cc, text, kb)
}

func (b *Bot) editProductPage(ctx context.Context, cc callbackCtx, categoryID int64, page int) error {
	pg, err := b.catalog.CategoryPage(ctx, categoryID, page)
	if err != nil {
		return err
	}
	text, kb := productPageView(pg)
	return b.editText(ctx, cc, text, kb)
}

func (b *Bot) sendProductPage(ctx context.Context, chat, categoryID int64) error {
	pg, err := b.catalog.CategoryPage(ctx, categoryID, 0)
	if err != nil {
		return err
	}
	text, kb := productPageView(pg)
	return b.tg.SendMessage(ctx, chat, text, kb)
}

// sendProduct sends a product card. The photo is fetched by Telegram from a
// URL; if that fails -- the image host is down, the URL went stale -- the card
// still goes out as text. A missing photo should cost the picture, not the
// sale.
func (b *Bot) sendProduct(ctx context.Context, chat, productID int64) error {
	p, err := b.catalog.Product(ctx, productID)
	if err != nil {
		return err
	}
	caption, kb := productCaption(p), productKeyboard(p)

	err = b.tg.SendPhoto(ctx, chat, p.ImageURL, caption, kb)
	if err == nil || ctx.Err() != nil {
		return err
	}
	b.log.WarnContext(ctx, "could not send product photo, falling back to text",
		slog.Int64("product_id", p.ID), slog.Any("error", err))
	return b.tg.SendMessage(ctx, chat, caption, kb)
}

// editProductButtons re-reads the product before redrawing its buttons, so
// the size picker reflects stock now, not when the card was sent.
func (b *Bot) editProductButtons(ctx context.Context, cc callbackCtx, productID int64,
	keyboard func(*domain.Product) *telegram.InlineKeyboard,
) error {
	p, err := b.catalog.Product(ctx, productID)
	if err != nil {
		return err
	}
	return b.editMarkup(ctx, cc, keyboard(p))
}

func (b *Bot) addToCart(ctx context.Context, cc callbackCtx, productID, variantID int64) (string, error) {
	cart, err := b.cart.Add(ctx, cc.user, variantID)
	if err != nil {
		// The picker may be showing a size that has just sold out; redraw it
		// from fresh stock so the next tap cannot hit the same wall.
		if errors.Is(err, domain.ErrOutOfStock) {
			if rerr := b.editProductButtons(ctx, cc, productID, sizeKeyboard); rerr != nil {
				b.log.WarnContext(ctx, "could not refresh size picker", slog.Any("error", rerr))
			}
		}
		return "", err
	}

	line, _ := cart.Line(variantID)
	if err := b.editProductButtons(ctx, cc, productID, productKeyboard); err != nil {
		return "", err
	}
	return fmt.Sprintf("✅ Додано розмір %s. У кошику: %d шт.", line.Size, cart.Items()), nil
}

func (b *Bot) sendSimilar(ctx context.Context, chat, productID int64) error {
	p, similar, err := b.catalog.Similar(ctx, productID)
	if err != nil {
		return err
	}
	text, kb := similarView(p, similar)
	return b.tg.SendMessage(ctx, chat, text, kb)
}

func (b *Bot) sendCart(ctx context.Context, chat, user int64) {
	cart, err := b.cart.Get(ctx, user)
	if err != nil {
		b.fail(ctx, chat, "get cart", err)
		return
	}
	text, kb := cartView(cart)
	b.send(ctx, chat, text, kb)
}

// changeCart applies a cart change and redraws the cart message in place.
func (b *Bot) changeCart(ctx context.Context, cc callbackCtx, change func() (*domain.Cart, error)) error {
	cart, err := change()
	if err != nil {
		return err
	}
	text, kb := cartView(cart)
	return b.editText(ctx, cc, text, kb)
}

// send is for replies whose failure the user cannot be told about anyway: if
// Telegram refuses a message, a second message saying so would be refused too.
func (b *Bot) send(ctx context.Context, chat int64, html string, markup telegram.ReplyMarkup) {
	if err := b.tg.SendMessage(ctx, chat, html, markup); err != nil {
		b.log.ErrorContext(ctx, "could not send message", slog.Int64("chat_id", chat), slog.Any("error", err))
	}
}

func (b *Bot) fail(ctx context.Context, chat int64, op string, err error) {
	b.log.ErrorContext(ctx, op+" failed", slog.Int64("chat_id", chat), slog.Any("error", err))
	b.send(ctx, chat, msgInternalFail, nil)
}

// editText edits the pressed message, or sends a new one when there is no
// message to edit (Telegram omits it for very old messages).
func (b *Bot) editText(ctx context.Context, cc callbackCtx, html string, kb *telegram.InlineKeyboard) error {
	if cc.msg == 0 {
		return b.tg.SendMessage(ctx, cc.chat, html, kb)
	}
	return ignoreNotModified(b.tg.EditMessageText(ctx, cc.chat, cc.msg, html, kb))
}

func (b *Bot) editMarkup(ctx context.Context, cc callbackCtx, kb *telegram.InlineKeyboard) error {
	if cc.msg == 0 {
		return nil
	}
	return ignoreNotModified(b.tg.EditMessageReplyMarkup(ctx, cc.chat, cc.msg, kb))
}

// ignoreNotModified treats "nothing changed" as success: a double tap on "➕"
// at the stock limit redraws an identical cart, and that is fine.
func ignoreNotModified(err error) error {
	if errors.Is(err, telegram.ErrNotModified) {
		return nil
	}
	return err
}
