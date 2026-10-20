package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aristodem96/ai_shop_bot/internal/telegram"
)

// callAgain is how soon the same buyer may call the managers again: a
// buyer who repeats "дайте менеджера" should not ring the bell each time.
const callAgain = 10 * time.Minute

// handoffs remembers who wrote to the bot and when each buyer last called
// the managers. Telegram tells a bot a buyer's name only in their messages,
// so it is kept as they come.
type handoffs struct {
	mu     sync.Mutex
	people map[int64]telegram.User
	called map[int64]time.Time
}

func (h *handoffs) saw(u *telegram.User) {
	if u == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.people == nil {
		h.people = map[int64]telegram.User{}
	}
	h.people[u.ID] = *u
}

// ring reports whether the buyer may call now, and if so marks the call.
func (h *handoffs) ring(user int64, now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.called == nil {
		h.called = map[int64]time.Time{}
	}
	if last, ok := h.called[user]; ok && now.Sub(last) < callAgain {
		return false
	}
	h.called[user] = now
	return true
}

func (h *handoffs) person(user int64) (telegram.User, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	u, ok := h.people[user]
	return u, ok
}

// errNoManagersChat means there is nowhere to call a manager to.
var errNoManagersChat = errors.New("bot: no managers' chat")

// CallManager puts a buyer who wants a person before the managers: what
// they need, who they are, how to reach them. It implements agent.Handoff.
func (b *Bot) CallManager(ctx context.Context, userID int64, reason string) (bool, error) {
	if b.adminChat == 0 {
		return false, errNoManagersChat
	}
	if !b.handoffs.ring(userID, time.Now()) {
		return false, nil
	}
	if err := b.tg.SendMessage(ctx, b.adminChat, b.handoffView(ctx, userID, reason), nil); err != nil {
		return false, fmt.Errorf("bot: call a manager: %w", err)
	}
	return true, nil
}

// handoffView is the managers' message about a buyer who wants a person.
func (b *Bot) handoffView(ctx context.Context, userID int64, reason string) string {
	var s strings.Builder
	fmt.Fprintf(&s, "🙋 <b>Покупець просить менеджера</b>\n%s\n", esc(reason))
	if u, ok := b.handoffs.person(userID); ok {
		name := esc(u.FirstName)
		if u.Username != "" {
			name += " (@" + esc(u.Username) + ")"
		}
		fmt.Fprintf(&s, "\n👤 %s", name)
	}
	if b.orders != nil {
		if c, err := b.orders.Customer(ctx, userID); err == nil && c != nil {
			fmt.Fprintf(&s, "\n📱 %s, %s", esc(c.Name), esc(c.Phone))
		}
	}
	if car := b.car(ctx, userID); car != nil {
		fmt.Fprintf(&s, "\n🚗 %s", esc(car.Label()))
	}
	fmt.Fprintf(&s, "\n\n<a href=\"tg://user?id=%d\">Написати покупцю</a>", userID)
	return s.String()
}
