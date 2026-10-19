package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/service"
	"github.com/aristodem96/ai_shop_bot/internal/telegram"
)

// Navigation that starts from a text message (the menu, a product page, the
// cart, the car picker) edits that message in place, so the chat does not fill
// up with stale menus. A product card is a photo and cannot become a text
// message, so leaving it -- to a page, to analogues -- sends a new one.

// car returns the buyer's car, or nil. A failure to read it degrades to "no
// car" -- the buyer is asked for it -- rather than to an
// error, since browsing still works without it.
func (b *Bot) car(ctx context.Context, user int64) *domain.Vehicle {
	v, err := b.garage.Get(ctx, user)
	if err != nil {
		b.log.WarnContext(ctx, "could not read the buyer's car", slog.Int64("user_id", user), slog.Any("error", err))
		return nil
	}
	return v
}

// start follows the greeting: a buyer without a car is asked to name it,
// because nearly every part depends on it.
func (b *Bot) start(ctx context.Context, chat, user int64) {
	if b.car(ctx, user) != nil {
		b.sendCategories(ctx, chat, user)
		return
	}
	b.send(ctx, chat, msgAskCar, nil)
}

// catalogueCar is the car the catalogue opens for, and whether it may open
// at all: with the consultant on, the catalogue is a car's -- every part of
// every car is too much to browse -- and a buyer without a car is asked to
// name one. Without the consultant there is no way to name a car, so the
// whole catalogue opens.
func (b *Bot) catalogueCar(ctx context.Context, user int64) (*domain.Vehicle, bool) {
	car := b.car(ctx, user)
	return car, car != nil || b.consultant == nil
}

func (b *Bot) sendCategories(ctx context.Context, chat, user int64) {
	car, ok := b.catalogueCar(ctx, user)
	if !ok {
		b.send(ctx, chat, msgAskCar, nil)
		return
	}
	menu, err := b.catalog.Menu(ctx, car)
	if err != nil {
		b.fail(ctx, chat, "list categories", err)
		return
	}
	text, kb := categoriesView(menu, car, b.progress(ctx, car))
	b.send(ctx, chat, text, kb)
}

func (b *Bot) editCategories(ctx context.Context, cc callbackCtx) error {
	car, ok := b.catalogueCar(ctx, cc.user)
	if !ok {
		return b.editText(ctx, cc, msgAskCar, nil)
	}
	menu, err := b.catalog.Menu(ctx, car)
	if err != nil {
		return err
	}
	text, kb := categoriesView(menu, car, b.progress(ctx, car))
	return b.editText(ctx, cc, text, kb)
}

func (b *Bot) editProductPage(ctx context.Context, cc callbackCtx, categoryID int64, page int, all bool) error {
	car, ok := b.catalogueCar(ctx, cc.user)
	if !ok {
		return b.editText(ctx, cc, msgAskCar, nil)
	}
	pg, err := b.catalog.CategoryPage(ctx, categoryID, car, page, all)
	if err != nil {
		return err
	}
	text, kb := productPageView(pg)
	return b.editText(ctx, cc, text, kb)
}

func (b *Bot) sendProductPage(ctx context.Context, cc callbackCtx, categoryID int64) error {
	car, ok := b.catalogueCar(ctx, cc.user)
	if !ok {
		return b.tg.SendMessage(ctx, cc.chat, msgAskCar, nil)
	}
	pg, err := b.catalog.CategoryPage(ctx, categoryID, car, 0, false)
	if err != nil {
		return err
	}
	text, kb := productPageView(pg)
	return b.tg.SendMessage(ctx, cc.chat, text, kb)
}

// sendProduct sends a product card. The photo is fetched by Telegram from a
// URL; if that fails -- the image host is down, the URL went stale -- the card
// still goes out as text. A missing photo should cost the picture, not the
// sale. A product imported without a photo goes out as text from the start.
func (b *Bot) sendProduct(ctx context.Context, cc callbackCtx, productID int64) error {
	b.enrich(ctx, productID)
	p, err := b.catalog.Product(ctx, productID)
	if err != nil {
		return err
	}
	caption, kb := productCaption(p, b.car(ctx, cc.user))+fitsBlock(b.productFits(ctx, p.ID)), productKeyboard(p)
	if p.ImageURL == "" {
		return b.tg.SendMessage(ctx, cc.chat, caption, kb)
	}
	return b.sendPhoto(ctx, cc.chat, p.ImageURL, caption, kb)
}

// sendPhoto sends a photo with its caption, or the caption alone when the
// photo cannot be had: the card matters more than its picture.
func (b *Bot) sendPhoto(ctx context.Context, chat int64, url, caption string, kb telegram.ReplyMarkup) error {
	var err error
	if b.photos != nil {
		err = b.photos.Send(ctx, chat, url, caption, kb)
	} else {
		err = b.tg.SendPhoto(ctx, chat, url, caption, kb)
	}
	if err == nil || ctx.Err() != nil {
		return err
	}
	b.log.WarnContext(ctx, "could not send a photo, falling back to text", slog.String("url", url), slog.Any("error", err))
	return b.tg.SendMessage(ctx, chat, caption, kb)
}

// addToCart adds one unit. A part that does not fit the buyer's car is still
// added -- they may be buying for another car -- but the toast says so.
func (b *Bot) addToCart(ctx context.Context, cc callbackCtx, productID int64) (string, error) {
	cart, err := b.cart.Add(ctx, cc.user, productID)
	if err != nil {
		return "", err
	}
	line, _ := cart.Line(productID)
	toast := fmt.Sprintf("✅ Додано. У кошику: %d шт.", line.Qty)

	if car := b.car(ctx, cc.user); car != nil {
		if p, err := b.catalog.Product(ctx, productID); err == nil && !p.FitsVehicle(car.ID) {
			toast += "\n⚠️ Ця деталь не підходить до вашого " + car.Short()
		}
	}
	return toast, nil
}

func (b *Bot) sendAnalogues(ctx context.Context, cc callbackCtx, productID int64) error {
	p, analogues, err := b.catalog.Analogues(ctx, productID, b.car(ctx, cc.user))
	if err != nil {
		return err
	}
	text, kb := analoguesView(p, analogues)
	return b.tg.SendMessage(ctx, cc.chat, text, kb)
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

// --- the buyer's car ---------------------------------------------------------

func (b *Bot) sendCar(ctx context.Context, chat, user int64) {
	if car := b.car(ctx, user); car != nil {
		text, kb := carView(car, b.progress(ctx, car))
		b.send(ctx, chat, text, kb)
		return
	}
	b.send(ctx, chat, msgAskCar, nil)
}

// editCar redraws the car screen, for how far its catalogue has got.
func (b *Bot) editCar(ctx context.Context, cc callbackCtx) error {
	car := b.car(ctx, cc.user)
	if car == nil {
		return b.editText(ctx, cc, msgAskCar, nil)
	}
	text, kb := carView(car, b.progress(ctx, car))
	return b.editText(ctx, cc, text, kb)
}

// errNoCar is a kit asked for before the buyer named a car.
var errNoCar = errors.New("bot: no car chosen")

func (b *Bot) buyerKit(ctx context.Context, user int64) (*service.Kit, error) {
	car := b.car(ctx, user)
	if car == nil {
		return nil, errNoCar
	}
	return b.kits.Kit(ctx, car.ID)
}

func (b *Bot) sendKit(ctx context.Context, cc callbackCtx) error {
	kit, err := b.buyerKit(ctx, cc.user)
	if err != nil {
		return err
	}
	text, kb := kitView(kit)
	return b.tg.SendMessage(ctx, cc.chat, text, kb)
}

// addKit puts the whole kit in the cart, unit by unit through the same rules
// as any other add. The kit is assembled again rather than taken from the
// message: prices and stock may have moved since it was shown.
func (b *Bot) addKit(ctx context.Context, cc callbackCtx) (string, error) {
	kit, err := b.buyerKit(ctx, cc.user)
	if err != nil {
		return "", err
	}
	added, wanted := 0, 0
	for _, item := range kit.Items {
		wanted += item.Qty
		for range item.Qty {
			if _, err := b.cart.Add(ctx, cc.user, item.Product.ID); err != nil {
				if !isRefusal(err) {
					return "", err
				}
				break // out of stock or at the limit: the rest of this line too
			}
			added++
		}
	}
	b.sendCart(ctx, cc.chat, cc.user)
	if added < wanted {
		return fmt.Sprintf("Додано %d з %d шт. — решти немає в наявності або досягнуто ліміту.", added, wanted), nil
	}
	return fmt.Sprintf("✅ Комплект у кошику: %d шт.", added), nil
}

// isRefusal reports whether a cart error is a business refusal rather than a
// failure. A part taken off sale between assembling the kit and adding it
// counts too: the rest of the kit should still go in.
func isRefusal(err error) bool {
	return errors.Is(err, domain.ErrOutOfStock) || errors.Is(err, domain.ErrQtyLimit) ||
		errors.Is(err, domain.ErrCartFull) || errors.Is(err, domain.ErrNotFound)
}

func (b *Bot) forgetCar(ctx context.Context, cc callbackCtx) error {
	if err := b.garage.Clear(ctx, cc.user); err != nil {
		return err
	}
	text, kb := carForgottenView()
	return b.editText(ctx, cc, text, kb)
}

// --- plumbing ----------------------------------------------------------------

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
// message to edit (Telegram omits it for very old messages) or the message
// is a photo: a photo has a caption, not a text, and editMessageText fails
// on it. The photo then loses its buttons, so they are not pressed twice.
func (b *Bot) editText(ctx context.Context, cc callbackCtx, html string, kb *telegram.InlineKeyboard) error {
	if cc.msg == 0 {
		return b.tg.SendMessage(ctx, cc.chat, html, kb)
	}
	if cc.photo {
		if err := ignoreNotModified(b.tg.EditMessageReplyMarkup(ctx, cc.chat, cc.msg, nil)); err != nil {
			b.log.WarnContext(ctx, "could not take the buttons off a photo", slog.Any("error", err))
		}
		return b.tg.SendMessage(ctx, cc.chat, html, kb)
	}
	return ignoreNotModified(b.tg.EditMessageText(ctx, cc.chat, cc.msg, html, kb))
}

// ignoreNotModified treats "nothing changed" as success: a double tap on "➕"
// at the stock limit redraws an identical cart, and that is fine.
func ignoreNotModified(err error) error {
	if errors.Is(err, telegram.ErrNotModified) {
		return nil
	}
	return err
}
