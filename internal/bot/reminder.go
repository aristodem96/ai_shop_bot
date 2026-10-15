package bot

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/telegram"
)

// AbandonedCarts finds carts to remind about. postgres.CartRepository is the
// implementation.
type AbandonedCarts interface {
	ClaimAbandoned(ctx context.Context, idleFor time.Duration, limit int) ([]int64, error)
}

// ReminderConfig tunes the abandoned cart reminder.
type ReminderConfig struct {
	// After is how long a cart sits untouched before the reminder.
	After time.Duration
	// Every is how often abandoned carts are looked for.
	Every time.Duration
	// Location, FromHour and ToHour bound when reminders go out: a shop
	// writing at three in the morning loses the customer it meant to win.
	Location *time.Location
	FromHour int
	ToHour   int
}

// reminderBatch is how many carts one claim takes. Small batches keep each
// transaction short and spread the messages over Telegram's rate limits.
const reminderBatch = 25

// Reminder sends one reminder per abandoned cart.
type Reminder struct {
	carts AbandonedCarts
	cart  Cart
	tg    Messenger
	cfg   ReminderConfig
	log   *slog.Logger
	now   func() time.Time
}

// NewReminder wires a Reminder.
func NewReminder(carts AbandonedCarts, cart Cart, tg Messenger, cfg ReminderConfig, log *slog.Logger) *Reminder {
	return &Reminder{carts: carts, cart: cart, tg: tg, cfg: cfg, log: log, now: time.Now}
}

// Run looks for abandoned carts every cfg.Every until ctx is cancelled.
func (r *Reminder) Run(ctx context.Context) error {
	t := time.NewTicker(r.cfg.Every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if _, err := r.RunOnce(ctx); err != nil && ctx.Err() == nil {
				r.log.WarnContext(ctx, "cart reminders failed", slog.Any("error", err))
			}
		}
	}
}

// RunOnce sends the reminders that are due and reports how many went out.
// Outside the sending hours it does nothing: the carts are not claimed, so
// they are reminded about in the morning instead of being skipped.
func (r *Reminder) RunOnce(ctx context.Context) (int, error) {
	if h := r.now().In(r.cfg.Location).Hour(); h < r.cfg.FromHour || h >= r.cfg.ToHour {
		return 0, nil
	}
	sent := 0
	for {
		users, err := r.carts.ClaimAbandoned(ctx, r.cfg.After, reminderBatch)
		if err != nil {
			return sent, err
		}
		for _, user := range users {
			if r.remind(ctx, user) {
				sent++
			}
		}
		if len(users) < reminderBatch {
			return sent, nil
		}
	}
}

// remind sends one reminder. The cart is read again so the message shows
// today's prices and only what is still in it.
func (r *Reminder) remind(ctx context.Context, user int64) bool {
	cart, err := r.cart.Get(ctx, user)
	if err != nil {
		r.log.WarnContext(ctx, "could not read cart for reminder", slog.Int64("user_id", user), slog.Any("error", err))
		return false
	}
	if len(cart.Lines) == 0 {
		return false
	}
	text, kb := reminderView(cart)
	// In a private chat the chat id is the user id.
	if err := r.tg.SendMessage(ctx, user, text, kb); err != nil {
		// Most often the buyer has blocked the bot. Not retried: the cart is
		// already marked, and a reminder is at most once by design.
		r.log.InfoContext(ctx, "cart reminder not delivered", slog.Int64("user_id", user), slog.Any("error", err))
		return false
	}
	return true
}

func reminderView(c *domain.Cart) (string, *telegram.InlineKeyboard) {
	text := fmt.Sprintf("🛒 У вашому кошику залишилось %d шт. на суму <b>%s</b>.\n\nЗапчастини ще чекають на вас — але наявність може змінитися.",
		c.Items(), c.Total())
	return text, &telegram.InlineKeyboard{Rows: [][]telegram.InlineButton{
		{button("🛒 Відкрити кошик", actCart)},
	}}
}
