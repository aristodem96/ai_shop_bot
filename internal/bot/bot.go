// Package bot is the Telegram front end of the auto parts shop: the buyer's
// car, the catalogue, product cards and the cart, driven entirely by buttons. No model is involved
// in anything a button does -- that path is instant, free and deterministic.
// Free text goes to the AI consultant from stage 2.
package bot

import (
	"context"
	"errors"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/service"
	"github.com/aristodem96/ai_shop_bot/internal/telegram"
)

// UpdateSource yields incoming events.
type UpdateSource interface {
	GetUpdates(ctx context.Context, offset int64, timeout time.Duration) ([]telegram.Update, error)
}

// Messenger sends and edits messages.
type Messenger interface {
	SendMessage(ctx context.Context, chatID int64, html string, markup telegram.ReplyMarkup) error
	SendPhoto(ctx context.Context, chatID int64, photoURL, captionHTML string, markup telegram.ReplyMarkup) error
	EditMessageText(ctx context.Context, chatID, messageID int64, html string, markup *telegram.InlineKeyboard) error
	EditMessageReplyMarkup(ctx context.Context, chatID, messageID int64, markup *telegram.InlineKeyboard) error
	AnswerCallbackQuery(ctx context.Context, id, text string, alert bool) error
	SendChatAction(ctx context.Context, chatID int64, action string) error
}

// Catalog is what the bot needs from the catalogue service.
type Catalog interface {
	Categories(ctx context.Context) ([]domain.Category, error)
	CategoryPage(ctx context.Context, categoryID int64, car *domain.Vehicle, page int) (*service.ProductPage, error)
	Product(ctx context.Context, id int64) (*domain.Product, error)
	Analogues(ctx context.Context, id int64, car *domain.Vehicle) (*domain.Product, []domain.Product, error)
}

// Cart is what the bot needs from the cart service.
type Cart interface {
	Get(ctx context.Context, userID int64) (*domain.Cart, error)
	Add(ctx context.Context, userID, productID int64) (*domain.Cart, error)
	Decrement(ctx context.Context, userID, productID int64) (*domain.Cart, error)
	Remove(ctx context.Context, userID, productID int64) (*domain.Cart, error)
	Clear(ctx context.Context, userID int64) error
}

// Garage is what the bot needs to know the buyer's car. The car is chosen by
// writing it to the consultant -- a menu of every make, model and engine does
// not scale past a demo -- so the bot only reads it and can forget it.
type Garage interface {
	Get(ctx context.Context, userID int64) (*domain.Vehicle, error)
	Clear(ctx context.Context, userID int64) error
}

// Config tunes update handling.
type Config struct {
	// MaxPending bounds updates accepted but not yet handled.
	MaxPending int
	// HandlerTimeout bounds the handling of one update.
	HandlerTimeout time.Duration
}

// Bot handles chat updates.
type Bot struct {
	updates UpdateSource
	tg      Messenger
	catalog Catalog
	cart    Cart
	garage  Garage
	cfg     Config
	log     *slog.Logger

	// consultant, limiter and files are nil until UseConsultant is called.
	consultant Consultant
	limiter    Limiter
	files      Files
}

// New wires a Bot.
func New(updates UpdateSource, tg Messenger, catalog Catalog, cart Cart, garage Garage, cfg Config, log *slog.Logger) *Bot {
	return &Bot{updates: updates, tg: tg, catalog: catalog, cart: cart, garage: garage, cfg: cfg, log: log}
}

// Run long-polls for updates until ctx is cancelled, then finishes the updates
// already accepted and acknowledges them before returning.
func (b *Bot) Run(ctx context.Context) error {
	// Handlers get a context detached from ctx: on shutdown an update that is
	// half done -- a cart line written, its message not yet edited -- should
	// finish rather than be cut off. HandlerTimeout still bounds it.
	d := newDispatcher(b.cfg.MaxPending, func(u telegram.Update) {
		hctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), b.cfg.HandlerTimeout)
		defer cancel()
		b.handleSafely(hctx, u)
	})

	var (
		offset  int64
		backoff = time.Second
	)
poll:
	for ctx.Err() == nil {
		updates, err := b.updates.GetUpdates(ctx, offset, telegram.PollTimeout)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			// Telegram being unreachable is not fatal: wait and try again,
			// backing off so an outage is not met with a request storm.
			b.log.WarnContext(ctx, "could not fetch updates, retrying",
				slog.Duration("backoff", backoff), slog.Any("error", err))
			select {
			case <-ctx.Done():
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		backoff = time.Second

		for _, u := range updates {
			if !d.Dispatch(ctx, u.ChatID(), u) {
				break poll
			}
			offset = u.UpdateID + 1
		}
	}

	d.Wait()
	b.acknowledge(ctx, offset)
	// Cancellation is how the bot is told to stop, so reaching here is a clean
	// exit. Returning ctx.Err() would make every graceful shutdown a failure.
	return nil //nolint:nilerr // the only error on this path is the expected cancellation
}

// acknowledge confirms the handled updates to Telegram.
//
// Telegram forgets an update only when a later getUpdates asks for a higher
// offset. The poll loop does that on its next call, but on shutdown there is
// no next call, so without this the last batch would be delivered -- and
// handled -- a second time after a restart: a second "+1" on a cart line.
func (b *Bot) acknowledge(ctx context.Context, offset int64) {
	if offset == 0 {
		return
	}
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := b.updates.GetUpdates(actx, offset, 0); err != nil {
		b.log.Warn("could not acknowledge the last updates", slog.Any("error", err))
	}
}

// handleSafely contains a panic to the one update that caused it. Without the
// recover, a single malformed update would take the bot down for everyone.
func (b *Bot) handleSafely(ctx context.Context, u telegram.Update) {
	start := time.Now()
	defer func() {
		if rec := recover(); rec != nil {
			b.log.ErrorContext(ctx, "update handler panicked",
				slog.Int64("update_id", u.UpdateID),
				slog.Any("panic", rec),
				slog.String("stack", string(debug.Stack())))
		}
		b.log.DebugContext(ctx, "update handled",
			slog.Int64("update_id", u.UpdateID),
			slog.Int64("chat_id", u.ChatID()),
			slog.Duration("took", time.Since(start)))
	}()
	b.Handle(ctx, u)
}

// Handle processes one update.
func (b *Bot) Handle(ctx context.Context, u telegram.Update) {
	switch {
	case u.Message != nil:
		b.handleMessage(ctx, u.Message)
	case u.CallbackQuery != nil:
		b.handleCallback(ctx, u.CallbackQuery)
	}
}

func (b *Bot) handleMessage(ctx context.Context, m *telegram.Message) {
	if m.From == nil {
		return
	}
	// A cart is personal. In a group every member would share the buttons of
	// one message while each had a cart of their own, which cannot work.
	if m.Chat.Type != telegram.ChatPrivate {
		b.send(ctx, m.Chat.ID, msgPrivateOnly, nil)
		return
	}
	chat, user := m.Chat.ID, m.From.ID

	if len(m.Photo) > 0 {
		b.consultPhoto(ctx, chat, user, m)
		return
	}

	switch m.Text {
	case "/start":
		b.send(ctx, chat, msgWelcome, mainMenu())
		b.start(ctx, chat, user)
	case "/catalog", btnCatalog:
		b.sendCategories(ctx, chat, user)
	case "/car", btnCar:
		b.sendCar(ctx, chat, user)
	case "/cart", btnCart:
		b.sendCart(ctx, chat, user)
	case "/help", btnHelp:
		b.send(ctx, chat, msgHelp, mainMenu())
	case "/new":
		b.resetConversation(ctx, chat)
	case "":
		// A sticker, a voice note, a file: nothing the consultant reads.
		b.send(ctx, chat, msgTextOnly, nil)
	default:
		b.consult(ctx, chat, user, m.Text)
	}
}

// callbackCtx is where a pressed button lives.
type callbackCtx struct {
	user, chat, msg int64
}

func (b *Bot) handleCallback(ctx context.Context, q *telegram.CallbackQuery) {
	cc := callbackCtx{user: q.From.ID, chat: q.From.ID}
	if q.Message != nil {
		cc.chat, cc.msg = q.Message.Chat.ID, q.Message.MessageID
	}

	var toast string
	cb, err := parseCallback(q.Data)
	if err == nil {
		toast, err = b.route(ctx, cc, cb)
	}
	// A refusal is shown as a dialog the user has to dismiss: a toast
	// vanishes before "this size is sold out" is read. Success is a toast.
	alert := err != nil
	if err != nil {
		toast = b.explain(ctx, err, q.Data)
	}

	// Answered last and always, error or not: an unanswered callback leaves a
	// spinner on the button for the user until Telegram gives up on it.
	if err := b.tg.AnswerCallbackQuery(ctx, q.ID, toast, alert); err != nil {
		b.log.WarnContext(ctx, "could not answer callback", slog.Any("error", err))
	}
}

// route runs a button's action and returns the toast to show.
func (b *Bot) route(ctx context.Context, cc callbackCtx, cb callback) (string, error) {
	a := cb.args
	switch cb.act {
	case actCategories:
		return "", b.editCategories(ctx, cc)
	case actCategory:
		return "", b.editProductPage(ctx, cc, a[0], int(a[1]))
	case actCategoryN:
		return "", b.sendProductPage(ctx, cc, a[0])
	case actProduct:
		return "", b.sendProduct(ctx, cc, a[0])
	case actAdd:
		return b.addToCart(ctx, cc, a[0])
	case actAnalogues:
		return "", b.sendAnalogues(ctx, cc, a[0])
	case actCart:
		b.sendCart(ctx, cc.chat, cc.user)
		return "", nil
	case actInc:
		return "", b.changeCart(ctx, cc, func() (*domain.Cart, error) { return b.cart.Add(ctx, cc.user, a[0]) })
	case actDec:
		return "", b.changeCart(ctx, cc, func() (*domain.Cart, error) { return b.cart.Decrement(ctx, cc.user, a[0]) })
	case actDel:
		return "", b.changeCart(ctx, cc, func() (*domain.Cart, error) { return b.cart.Remove(ctx, cc.user, a[0]) })
	case actClear:
		err := b.changeCart(ctx, cc, func() (*domain.Cart, error) {
			if err := b.cart.Clear(ctx, cc.user); err != nil {
				return nil, err
			}
			return &domain.Cart{UserID: cc.user}, nil
		})
		return msgCartCleared, err
	case actForgetCar:
		return "", b.forgetCar(ctx, cc)
	case actNoop:
		return "", nil
	}
	return "", errBadCallback
}

// explain turns an error into what the user is told. Expected outcomes -- a
// size sold out, a limit reached -- get their own wording. Anything else is a
// bug or an outage: it is logged, and the user gets a generic apology rather
// than an error string.
func (b *Bot) explain(ctx context.Context, err error, data string) string {
	switch {
	case errors.Is(err, errBadCallback):
		return msgStaleButton
	case errors.Is(err, domain.ErrOutOfStock):
		return msgOutOfStock
	case errors.Is(err, domain.ErrQtyLimit):
		return msgQtyLimit
	case errors.Is(err, domain.ErrCartFull):
		return msgCartFull
	case errors.Is(err, domain.ErrNotFound):
		return msgGone
	}
	b.log.ErrorContext(ctx, "callback failed", slog.String("data", data), slog.Any("error", err))
	return msgInternalFail
}
