// Package bot is the Telegram front end of the auto parts shop: the buyer's
// car, the catalogue, product cards and the cart, driven entirely by buttons. No model is involved
// in anything a button does -- that path is instant, free and deterministic.
// Free text goes to the AI consultant from stage 2.
package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
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
	Menu(ctx context.Context, car *domain.Vehicle) ([]service.CategoryEntry, error)
	CategoryPage(ctx context.Context, categoryID int64, car *domain.Vehicle, page int, all bool) (*service.ProductPage, error)
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

// Maintenance assembles service kits.
type Maintenance interface {
	Kit(ctx context.Context, vehicleID int64) (*service.Kit, error)
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
	// PickupAddress is where buyers collect orders from the shop, with its
	// hours: "Київ, вул. Автозаводська 1, пн–сб 9:00–19:00". Empty offers
	// delivery only.
	PickupAddress string
}

// Bot handles chat updates.
type Bot struct {
	updates UpdateSource
	tg      Messenger
	catalog Catalog
	cart    Cart
	garage  Garage
	kits    Maintenance
	cfg     Config
	log     *slog.Logger

	// orders and sessions are nil until UseOrders is called.
	orders    Orders
	sessions  Sessions
	adminChat int64

	// consultant, limiter and files are nil until UseConsultant is called.
	consultant Consultant
	limiter    Limiter
	files      Files

	// matching is nil until UseVehicleMatching is called.
	matching  VehicleMatching
	matchJobs *jobs[matchJob, *matchJob]
	carDrafts *jobs[carDraft, *carDraft]

	// reports and docs are nil until UseReports is called.
	reports Reports
	docs    Documents

	// grouping and reviews are nil until UseGrouping is called.
	grouping Grouping
	reviews  *jobs[review, *review]

	// cats is nil until UseCategories is called.
	cats    Categories
	catJobs *jobs[catJob, *catJob]

	// photos and enricher are nil until UsePhotos and UseEnrichment are
	// called.
	photos    Photos
	enricher  Enricher
	catalogue CatalogueProgress // nil: no progress shown

	// requests and parts are nil until UseRequests is called.
	requests PartRequests
	parts    CatalogParts

	// dialogStore keeps the managers' dialogs across restarts; nil keeps
	// them in memory. Set by KeepDialogs, before the Use calls.
	dialogStore func(prefix string, ttl time.Duration) Sessions

	// imports is nil until UseImports is called.
	imports     Imports
	importFiles Files
	importDir   string
	importJobs  *jobs[importJob, *importJob]
}

// New wires a Bot.
func New(updates UpdateSource, tg Messenger, catalog Catalog, cart Cart, garage Garage, kits Maintenance, cfg Config, log *slog.Logger) *Bot {
	return &Bot{updates: updates, tg: tg, catalog: catalog, cart: cart, garage: garage, kits: kits, cfg: cfg, log: log}
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
	// /id works anywhere: a managers' group needs it to find its own id for
	// ADMIN_CHAT_ID. In a group, commands may carry the bot's name: /id@bot.
	if m.Text == "/id" || strings.HasPrefix(m.Text, "/id@") {
		b.send(ctx, m.Chat.ID, fmt.Sprintf("ID цього чату: <code>%d</code>", m.Chat.ID), nil)
		return
	}
	// A file in the managers' chat is a price list or the vehicle
	// directory. Checked before the private-only rule: that chat is often
	// a group.
	if m.Document != nil && b.imports != nil && b.isAdmin(m.Chat.ID) {
		b.receiveImport(ctx, m)
		return
	}
	// A main menu button is never an answer to a managers' dialog: in the
	// owner's private chat, "🚗 Моє авто" pressed while a dialog waits for
	// a supplier's name or a car's description would become one.
	if b.isAdmin(m.Chat.ID) && !menuButton(m.Text) {
		if _, ok := commandArg(m.Text, cmdAdmin); ok {
			b.send(ctx, m.Chat.ID, msgAdminHelp, nil)
			return
		}
		if _, ok := commandArg(m.Text, cmdCars); ok && b.matching != nil {
			text, kb := b.carsList(ctx)
			b.send(ctx, m.Chat.ID, text, kb)
			return
		}
		if b.requests != nil && b.handleQuoteReply(ctx, m) {
			return
		}
		if _, ok := commandArg(m.Text, cmdSuppliers); ok && b.imports != nil {
			text, kb, err := b.suppliersList(ctx)
			if err != nil {
				b.fail(ctx, m.Chat.ID, "list suppliers", err)
				return
			}
			b.send(ctx, m.Chat.ID, text, kb)
			return
		}
		if b.imports != nil && b.handleImportReply(ctx, m) {
			return
		}
		if b.matching != nil && b.handleCarText(ctx, m) {
			return
		}
		if b.reports != nil && b.handleReportMessage(ctx, m) {
			return
		}
		if b.grouping != nil && b.cats != nil && b.imports != nil && b.handleGroupsMessage(ctx, m) {
			return
		}
		if b.cats != nil && b.handleCategoriesMessage(ctx, m) {
			return
		}
	}
	// A cart is personal. In a group every member would share the buttons of
	// one message while each had a cart of their own, which cannot work.
	if m.Chat.Type != telegram.ChatPrivate {
		b.send(ctx, m.Chat.ID, msgPrivateOnly, nil)
		return
	}
	chat, user := m.Chat.ID, m.From.ID

	if st, ok := b.inCheckout(ctx, chat, m); ok {
		b.checkoutInput(ctx, chat, user, m, st)
		return
	}
	if len(m.Photo) > 0 {
		b.consultPhoto(ctx, chat, user, m)
		return
	}

	switch m.Text {
	case "/start":
		welcome := msgWelcome
		if b.consultant == nil {
			welcome = msgWelcomeButtons
		}
		b.send(ctx, chat, welcome, b.mainMenu())
		b.start(ctx, chat, user)
	case "/catalog", btnCatalog:
		b.sendCategories(ctx, chat, user)
	case cmdMyCar, btnCar:
		b.sendCar(ctx, chat, user)
	case "/cart", btnCart:
		b.sendCart(ctx, chat, user)
	case "/help", btnHelp:
		b.send(ctx, chat, msgHelp, b.mainMenu())
	case "/orders", btnOrders:
		b.sendOrders(ctx, chat, user)
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
	// photo: the pressed message is a photo with a caption, which has no
	// text to edit.
	photo bool
}

func (b *Bot) handleCallback(ctx context.Context, q *telegram.CallbackQuery) {
	cc := callbackCtx{user: q.From.ID, chat: q.From.ID}
	if q.Message != nil {
		cc.chat, cc.msg = q.Message.Chat.ID, q.Message.MessageID
		cc.photo = len(q.Message.Photo) > 0
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
	case actCheckout, actPay, actEditContacts, actCancelCheckout, actManage:
		if b.orders == nil {
			// A bot running without payments may still meet these buttons
			// on old messages.
			return "", errPaymentUnavailable
		}
	}
	switch cb.act {
	case actCategories:
		return "", b.editCategories(ctx, cc)
	case actCategory:
		return "", b.editProductPage(ctx, cc, a[0], int(a[1]), false)
	case actCategoryAll:
		return "", b.editProductPage(ctx, cc, a[0], int(a[1]), true)
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
	case actKit:
		return "", b.sendKit(ctx, cc)
	case actKitAdd:
		return b.addKit(ctx, cc)
	case actCheckout:
		return b.startCheckout(ctx, cc)
	case actPay:
		return b.pay(ctx, cc)
	case actEditContacts:
		return "", b.askContacts(ctx, cc.chat)
	case actCancelCheckout:
		return "", b.cancelCheckout(ctx, cc)
	case actManage:
		return b.manage(ctx, cc, a[0], a[1])
	case actReport, actReportExcel:
		if b.reports == nil {
			return "", errBadCallback
		}
		return b.routeReports(ctx, cc, cb)
	case actGroupsNext, actGroupsCreate, actGroupsDoubts, actGroupAccept, actGroupPick, actGroupMixed:
		if b.grouping == nil || b.cats == nil || b.imports == nil {
			return "", errBadCallback
		}
		return b.routeGroups(ctx, cc, cb)
	case actCatOpen, actCatNew, actCatRename, actCatAddWord, actCatRemoveWord, actCatDelete, actCatPick, actCatApply, actCatCancel:
		if b.cats == nil {
			return "", errBadCallback
		}
		return b.routeCategories(ctx, cc, cb)
	case actCarAdd, actCarToggle, actCarText, actCarBack:
		if b.matching == nil {
			return "", errBadCallback
		}
		return b.routeCar(ctx, cc, cb)
	case actMatchStart, actMatchPick, actMatchMakes, actMatchMake, actMatchBack, actMatchSkip, actMatchStop, actCarDelete, actMatchAdd:
		if b.matching == nil || b.imports == nil {
			return "", errBadCallback
		}
		return b.routeMatch(ctx, cc, cb)
	case actSetupField, actSetupColumn, actSetupNext, actSetupLead, actSetupMarkup, actSetupSupplier:
		if b.imports == nil {
			return "", errBadCallback
		}
		return b.routeSetup(ctx, cc, cb)
	case actSupplierOpen, actSupplierMarkup, actSupplierLead, actSupplierDelete:
		if b.imports == nil {
			return "", errBadCallback
		}
		return b.routeSuppliers(ctx, cc, cb)
	case actImportApply, actImportCancel:
		if b.imports == nil {
			return "", errBadCallback
		}
		switch cb.act {
		case actImportApply:
			return b.applyImport(ctx, cc, a[0], a[1])
		default:
			return b.cancelImport(ctx, cc, a[0])
		}
	case actRequest, actRequestQty, actRequestFound, actRequestNone, actRequestBuy, actRequestDecline:
		if b.requests == nil {
			return "", errBadCallback
		}
		return b.routeRequest(ctx, cc, cb)
	case actForgetCar:
		return "", b.forgetCar(ctx, cc)
	case actCarScreen:
		return b.progressToast(ctx, cc.user), b.editCar(ctx, cc)
	case actMenuRefresh:
		return b.progressToast(ctx, cc.user), b.editCategories(ctx, cc)
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
	case errors.Is(err, domain.ErrOutOfStock):
		// A checkout names the part that ran out.
		if name, ok := strings.CutPrefix(err.Error(), domain.ErrOutOfStock.Error()+": "); ok {
			return fmt.Sprintf(msgOutOfStockNamed, name)
		}
		return msgOutOfStock
	case errors.Is(err, domain.ErrCartEmpty):
		return msgCartEmptyCheckout
	case errors.Is(err, domain.ErrInvalidContacts):
		return msgBadContacts
	case errors.Is(err, domain.ErrBadTransition):
		return msgBadTransition
	case errors.Is(err, errPaymentUnavailable):
		return msgPaymentUnavailable
	case errors.Is(err, errBadCallback):
		return msgStaleButton
	case errors.Is(err, domain.ErrQtyLimit):
		return msgQtyLimit
	case errors.Is(err, domain.ErrCartFull):
		return msgCartFull
	case errors.Is(err, errNoCar):
		return msgNoCarForKit
	case errors.Is(err, domain.ErrNotFound):
		return msgGone
	}
	b.log.ErrorContext(ctx, "callback failed", slog.String("data", data), slog.Any("error", err))
	return msgInternalFail
}
