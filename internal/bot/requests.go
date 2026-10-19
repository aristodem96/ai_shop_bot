package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/telegram"
)

// PartRequests stores requests for parts on order.
type PartRequests interface {
	Create(ctx context.Context, r domain.PartRequest) (int64, error)
	Get(ctx context.Context, id int64) (*domain.PartRequest, error)
	SetAdminMessage(ctx context.Context, id, messageID int64) error
	Quote(ctx context.Context, id int64, price domain.Money, leadDays int, ttl time.Duration) (*domain.PartRequest, error)
	NotFound(ctx context.Context, id int64) (*domain.PartRequest, error)
	Decline(ctx context.Context, id int64) (*domain.PartRequest, error)
}

// CatalogParts reads the catalogue parts the consultant offers.
type CatalogParts interface {
	CatalogPart(ctx context.Context, id int64) (*domain.CatalogPart, error)
}

// UseRequests lets buyers request catalogue parts the shop does not stock.
// The managers' chat answers them, so it needs UseOrders first: the quote is
// paid through the usual checkout.
func (b *Bot) UseRequests(r PartRequests, parts CatalogParts) {
	b.requests, b.parts = r, parts
}

// requestQtys are the quantities a buyer picks from: one part, a pair for
// an axle, a set of four.
var requestQtys = []int64{1, 2, 4}

func (b *Bot) routeRequest(ctx context.Context, cc callbackCtx, cb callback) (string, error) {
	a := cb.args
	switch cb.act {
	case actRequest:
		return "", b.askRequestQty(ctx, cc, a[0])
	case actRequestQty:
		return b.sendRequest(ctx, cc, a[0], a[1])
	case actRequestBuy:
		return b.buyQuote(ctx, cc, a[0])
	case actRequestDecline:
		return b.declineQuote(ctx, cc, a[0])
	}
	// The rest are the managers' buttons.
	if !b.isAdmin(cc.chat) {
		return "", errBadCallback
	}
	if cb.act == actRequestFound {
		return "", b.askQuote(ctx, cc, a[0])
	}
	return b.requestNotFound(ctx, cc, a[0])
}

// askRequestQty explains what ordering means and asks how many.
func (b *Bot) askRequestQty(ctx context.Context, cc callbackCtx, partID int64) error {
	p, err := b.parts.CatalogPart(ctx, partID)
	if err != nil {
		return err
	}
	text, kb := requestQtyView(p, b.car(ctx, cc.user))
	if photo := b.catalogPhoto(ctx, p); photo != "" {
		return b.sendPhoto(ctx, cc.chat, photo, text, kb)
	}
	return b.tg.SendMessage(ctx, cc.chat, text, kb)
}

// sendRequest stores the request and puts it before the managers.
func (b *Bot) sendRequest(ctx context.Context, cc callbackCtx, partID, qty int64) (string, error) {
	if qty < 1 || qty > domain.MaxQtyPerLine {
		return "", errBadCallback
	}
	if b.adminChat == 0 {
		return "", errPaymentUnavailable
	}
	p, err := b.parts.CatalogPart(ctx, partID)
	if err != nil {
		return "", err
	}
	req := domain.PartRequest{UserID: cc.user, Brand: p.Brand, Number: p.Number, PartKey: domain.NormalizeOEM(p.Number),
		Name: p.Name, Category: p.Category, Qty: int(qty)}
	if car := b.car(ctx, cc.user); car != nil {
		req.Vehicle = car.Label()
	}
	if req.ID, err = b.requests.Create(ctx, req); err != nil {
		return "", err
	}
	text, kb := adminRequestView(&req)
	if err := b.tg.SendMessage(ctx, b.adminChat, text, kb); err != nil {
		return "", fmt.Errorf("bot: request %d to the managers: %w", req.ID, err)
	}
	return msgRequestSentToast, b.editText(ctx, cc, requestSentView(&req), nil)
}

// askQuote asks the manager for the price and lead time, as a reply: the
// question names the request, so the answer finds it after a restart too.
func (b *Bot) askQuote(ctx context.Context, cc callbackCtx, id int64) error {
	req, err := b.requests.Get(ctx, id)
	if err != nil {
		return err
	}
	if req.Status != domain.RequestNew {
		return domain.ErrBadTransition
	}
	if err := b.requests.SetAdminMessage(ctx, id, cc.msg); err != nil {
		return err
	}
	return b.tg.SendMessage(ctx, cc.chat, fmt.Sprintf(msgAskQuote, id, req.Qty),
		&telegram.ForceReply{ForceReply: true, Placeholder: "1450 3"})
}

// quoteFor finds the request a reply in the managers' chat answers.
var quoteFor = regexp.MustCompile(`Запит №(\d+)`)

// quoteText is a price, in hryvnias with optional kopiyky, and a lead time
// in days: "1450 3", "1450,50 грн 3 дні".
var quoteText = regexp.MustCompile(`^\s*(\d{1,7})(?:[.,](\d{1,2}))?\s*(?:грн\.?)?\s+(\d{1,3})\s*(?:\p{L}+\.?)?\s*$`)

// parseQuote reads a manager's "1450 3".
func parseQuote(s string) (domain.Money, int, bool) {
	m := quoteText.FindStringSubmatch(s)
	if m == nil {
		return 0, 0, false
	}
	uah, _ := strconv.ParseInt(m[1], 10, 64)
	price := domain.UAH(uah)
	if m[2] != "" {
		kop, _ := strconv.ParseInt(m[2], 10, 64)
		if len(m[2]) == 1 {
			kop *= 10
		}
		price += domain.Money(kop)
	}
	days, _ := strconv.Atoi(m[3])
	return price, days, price > 0
}

// handleQuoteReply takes a manager's answer to askQuote. It reports whether
// the message was one.
func (b *Bot) handleQuoteReply(ctx context.Context, m *telegram.Message) bool {
	if m.ReplyToMessage == nil {
		return false
	}
	match := quoteFor.FindStringSubmatch(m.ReplyToMessage.Text)
	if match == nil {
		return false
	}
	id, _ := strconv.ParseInt(match[1], 10, 64)
	chat := m.Chat.ID
	price, days, ok := parseQuote(m.Text)
	if !ok {
		b.send(ctx, chat, fmt.Sprintf(msgBadQuote, id), &telegram.ForceReply{ForceReply: true, Placeholder: "1450 3"})
		return true
	}
	req, err := b.requests.Quote(ctx, id, price, days, domain.QuoteTTL)
	switch {
	case errors.Is(err, domain.ErrBadTransition), errors.Is(err, domain.ErrNotFound):
		b.send(ctx, chat, fmt.Sprintf(msgQuoteTooLate, id), nil)
		return true
	case err != nil:
		b.fail(ctx, chat, "quote a request", err)
		return true
	}
	text, kb := quoteView(req)
	if err := b.tg.SendMessage(ctx, req.UserID, text, kb); err != nil {
		b.log.ErrorContext(ctx, "could not send a quote", slog.Int64("request_id", id), slog.Any("error", err))
		b.send(ctx, chat, fmt.Sprintf(msgQuoteUndelivered, id), nil)
		return true
	}
	b.markAdminRequest(ctx, req, fmt.Sprintf("✅ %s, %s — надіслано", req.Price, leadDaysLabel(req.LeadDays)))
	b.send(ctx, chat, fmt.Sprintf(msgQuoteSent, id), nil)
	return true
}

// requestNotFound closes a request no supplier has.
func (b *Bot) requestNotFound(ctx context.Context, cc callbackCtx, id int64) (string, error) {
	req, err := b.requests.NotFound(ctx, id)
	if err != nil {
		return "", err
	}
	b.send(ctx, req.UserID, notFoundView(req), nil)
	return "", ignoreNotModified(b.tg.EditMessageReplyMarkup(ctx, cc.chat, cc.msg, labelKeyboard("❌ Не знайшли")))
}

// buyQuote puts the quoted part in the buyer's cart; the usual checkout
// takes it from there.
func (b *Bot) buyQuote(ctx context.Context, cc callbackCtx, id int64) (string, error) {
	req, err := b.requests.Get(ctx, id)
	if err != nil {
		return "", err
	}
	if req.UserID != cc.user {
		return "", errBadCallback
	}
	if req.Status != domain.RequestQuoted || time.Now().After(req.ExpiresAt) {
		return "", b.editText(ctx, cc, msgQuoteExpired, nil)
	}
	for range req.Qty {
		if _, err := b.cart.Add(ctx, cc.user, req.ProductID); err != nil {
			if errors.Is(err, domain.ErrQtyLimit) {
				break // already in the cart from an earlier tap
			}
			return "", err
		}
	}
	if err := ignoreNotModified(b.tg.EditMessageReplyMarkup(ctx, cc.chat, cc.msg, labelKeyboard("🛒 У кошику"))); err != nil {
		b.log.WarnContext(ctx, "could not mark a quote taken", slog.Any("error", err))
	}
	b.sendCart(ctx, cc.chat, cc.user)
	return msgAddedToCartToast, nil
}

// declineQuote closes a quote the buyer does not want.
func (b *Bot) declineQuote(ctx context.Context, cc callbackCtx, id int64) (string, error) {
	req, err := b.requests.Get(ctx, id)
	if err != nil {
		return "", err
	}
	if req.UserID != cc.user {
		return "", errBadCallback
	}
	if req, err = b.requests.Decline(ctx, id); err != nil {
		return "", err
	}
	b.markAdminRequest(ctx, req, "🚫 Покупець відмовився")
	return "", ignoreNotModified(b.tg.EditMessageReplyMarkup(ctx, cc.chat, cc.msg, labelKeyboard("🚫 Ви відмовились")))
}

// markAdminRequest replaces the buttons of the managers' message with where
// the request ended.
func (b *Bot) markAdminRequest(ctx context.Context, req *domain.PartRequest, label string) {
	if req.AdminMessageID == 0 {
		return
	}
	if err := ignoreNotModified(b.tg.EditMessageReplyMarkup(ctx, b.adminChat, req.AdminMessageID, labelKeyboard(label))); err != nil {
		b.log.WarnContext(ctx, "could not mark a request", slog.Int64("request_id", req.ID), slog.Any("error", err))
	}
}

// labelKeyboard is a single button that only says what happened.
func labelKeyboard(label string) *telegram.InlineKeyboard {
	return &telegram.InlineKeyboard{Rows: [][]telegram.InlineButton{{button(label, actNoop)}}}
}

// catalogPartLabel is the button under the consultant's answer.
func catalogPartLabel(p domain.CatalogPart) string {
	return strings.TrimSpace("🚚 "+p.Brand+" "+p.Number) + " — під замовлення"
}
