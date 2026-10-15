package bot

import (
	"context"
	"log/slog"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/service"
	"github.com/aristodem96/ai_shop_bot/internal/telegram"
)

// Notifier tells buyers and managers about order changes. It is called by the
// order service from wherever the change happened -- a webhook, the status
// poll, a manager's button -- so it sends directly rather than through the
// update dispatcher.
type Notifier struct {
	tg        Messenger
	adminChat int64
	log       *slog.Logger
}

var _ service.OrderNotifier = (*Notifier)(nil)

// NewNotifier returns a Notifier. adminChat zero sends nothing to managers.
func NewNotifier(tg Messenger, adminChat int64, log *slog.Logger) *Notifier {
	return &Notifier{tg: tg, adminChat: adminChat, log: log}
}

// OrderChanged implements service.OrderNotifier.
func (n *Notifier) OrderChanged(ctx context.Context, o *domain.Order, from domain.OrderStatus) {
	if text, kb := buyerNotice(o, from); text != "" {
		n.send(ctx, o.UserID, text, kb)
	}
	// Managers hear about what needs their hands: a new paid order to
	// confirm and ship, or money to return.
	if n.adminChat != 0 && (o.Status == domain.OrderPaid || o.Status == domain.OrderNeedsRefund) {
		text, kb := adminOrderView(o)
		n.send(ctx, n.adminChat, text, kb)
	}
}

func (n *Notifier) send(ctx context.Context, chat int64, text string, markup telegram.ReplyMarkup) {
	// A notice is sent after the change is committed; a failure here loses
	// the message, not the order, so it is logged rather than retried.
	if err := n.tg.SendMessage(ctx, chat, text, markup); err != nil {
		n.log.WarnContext(ctx, "could not send order notice", slog.Int64("chat_id", chat), slog.Any("error", err))
	}
}
