// Package agent is the shop's AI consultant: it turns a buyer's free text --
// "front pads for a 2015 Octavia 1.4" -- into vehicle lookups, catalogue
// searches and cart actions through tools, and answers with text plus the ids
// of products to show as cards.
//
// The model never touches the database or the chat directly. It sees only
// what the tools return, and everything it asks for passes through the same
// services, with the same rules, as a button press.
package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/enrich"
	"github.com/aristodem96/ai_shop_bot/internal/llm"
	"github.com/aristodem96/ai_shop_bot/internal/service"
)

// Catalog is what the tools need from the catalogue.
type Catalog interface {
	Categories(ctx context.Context) ([]domain.Category, error)
	Search(ctx context.Context, f service.SearchFilter) (*service.SearchResult, error)
	Product(ctx context.Context, id int64) (*domain.Product, error)
}

// Cart is what the tools need from the cart.
type Cart interface {
	Add(ctx context.Context, userID, productID int64) (*domain.Cart, error)
}

// Garage is what the tools need to find vehicles and know the buyer's car.
type Garage interface {
	Find(ctx context.Context, q service.VehicleQuery) ([]domain.Vehicle, error)
	Makes(ctx context.Context) ([]string, error)
	// Models lists a make's models, those made in year unless zero.
	Models(ctx context.Context, makeName string, year int) ([]service.ModelName, error)
	Get(ctx context.Context, userID int64) (*domain.Vehicle, error)
	Set(ctx context.Context, userID, vehicleID int64) (*domain.Vehicle, error)
}

// Maintenance assembles service kits.
type Maintenance interface {
	Kit(ctx context.Context, vehicleID int64) (*service.Kit, error)
}

// Orders looks up the buyer's orders.
type Orders interface {
	OrdersOf(ctx context.Context, userID int64, limit int) ([]domain.Order, error)
}

// OrderCatalog is the parts catalogue of the buyer's car beyond the shop's
// stock: what the shop can order for it, at a price a manager quotes.
type OrderCatalog interface {
	ForVehicle(ctx context.Context, f service.SearchFilter) ([]domain.CatalogPart, error)
	// Kinds sums up what a search finds: up to limit kinds of part with
	// their counts, and how many parts in all.
	Kinds(ctx context.Context, f service.SearchFilter, limit int) ([]domain.PartKind, int, error)
	CatalogPart(ctx context.Context, id int64) (*domain.CatalogPart, error)
	// CrawlState is "" for a car outside the catalogue, "none", "queued" or
	// "running" while its parts are still loading, "done" or "failed".
	CrawlState(ctx context.Context, vehicleID int64) (string, error)
}

// CatalogLoader loads a car's parts catalogue. crawler.Crawler is the
// implementation.
type CatalogLoader interface {
	// Watch queues the car's catalogue unless it is loaded, and has the
	// buyer told by the bot when it is.
	Watch(ctx context.Context, userID, vehicleID int64) error
	// FetchNow loads at once the sections of the car's catalogue a search
	// names, by its category or its words, so a buyer asking while the
	// rest loads gets an answer now. A failure leaves the search to what
	// is stored.
	FetchNow(ctx context.Context, vehicleID int64, category, query string)
}

// Handoff puts a buyer who wants a person before the shop's managers.
type Handoff interface {
	// CallManager tells the managers what the buyer needs; false when they
	// were told about this buyer a moment ago.
	CallManager(ctx context.Context, userID int64, reason string) (bool, error)
}

// CatalogFits says what cars a stock product fits by the parts catalogue.
// enrich.Enricher is the implementation.
type CatalogFits interface {
	ProductFits(ctx context.Context, productID int64) (*enrich.Fits, error)
}

// History stores conversations between turns.
type History interface {
	Load(ctx context.Context, chatID int64) ([]llm.Message, error)
	Save(ctx context.Context, chatID int64, msgs []llm.Message) error
	Clear(ctx context.Context, chatID int64) error
}

// Config bounds the agent.
type Config struct {
	// MaxSteps caps model calls per buyer message. A loop that has not
	// answered by then is stuck, and each step costs money and seconds.
	MaxSteps int
	// MaxHistory caps the stored messages of a conversation. Older turns are
	// dropped whole, oldest first.
	MaxHistory int
	// Timeout bounds one buyer message from start to answer.
	Timeout time.Duration
}

// Reply is the agent's answer to one buyer message.
type Reply struct {
	Text string
	// ProductIDs are the products to show as cards, in order. Every id was
	// checked against the catalogue when the model asked to show it.
	ProductIDs []int64
	// CatalogPartIDs are catalogue parts to offer on order, checked the same
	// way.
	CatalogPartIDs []int64
	// CartChanged is set when a tool added something to the cart.
	CartChanged bool
	// CarSaved is set when the buyer's car was saved during this turn.
	CarSaved bool
}

// Agent runs the conversation loop.
type Agent struct {
	llm     llm.Client
	history History
	cfg     Config
	log     *slog.Logger

	// tools carry the category tree in search_parts' schema. The owner
	// edits the tree in the managers' chat, so it is read every turn and
	// the tools rebuilt when it changed -- rarely: the schema must stay
	// byte-identical between requests for the provider's prompt cache to
	// hit.
	mu      sync.RWMutex
	tools   *toolbox
	treeSig string
}

// New builds an Agent.
func New(ctx context.Context, client llm.Client, catalog Catalog, cart Cart, garage Garage, kits Maintenance, orders Orders, history History, cfg Config, log *slog.Logger) (*Agent, error) {
	cats, err := catalog.Categories(ctx)
	if err != nil {
		return nil, fmt.Errorf("agent: load categories: %w", err)
	}
	return &Agent{
		llm:     client,
		history: history,
		tools:   newToolbox(catalog, cart, garage, kits, orders, cats),
		treeSig: treeSignature(cats),
		cfg:     cfg,
		log:     log,
	}, nil
}

// UseOrderCatalog lets searches offer parts the shop can order beyond its
// stock. Without it the agent sells the stock alone.
func (a *Agent) UseOrderCatalog(c OrderCatalog) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.tools.parts = c
}

// UseCatalogLoader has a search for a car whose catalogue is not loaded
// load it, and the buyer told when it is.
func (a *Agent) UseCatalogLoader(l CatalogLoader) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.tools.loader = l
}

// UseHandoff lets the consultant call a manager to the buyer.
func (a *Agent) UseHandoff(h Handoff) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.tools.handoff = h
}

// UseCatalogFits adds to get_part the cars a product fits by the parts
// catalogue.
func (a *Agent) UseCatalogFits(f CatalogFits) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.tools.fits = f
}

// toolsFor returns the tools for a turn, rebuilt if the category tree
// changed since the last one. A tree that cannot be read keeps the tools
// as they are: an outdated category list beats no answer.
func (a *Agent) toolsFor(ctx context.Context) *toolbox {
	a.mu.RLock()
	cur, sig := a.tools, a.treeSig
	a.mu.RUnlock()
	cats, err := cur.catalog.Categories(ctx)
	if err != nil {
		a.log.WarnContext(ctx, "could not read the category tree", slog.Any("error", err))
		return cur
	}
	if s := treeSignature(cats); s != sig {
		next := newToolbox(cur.catalog, cur.cart, cur.garage, cur.kits, cur.orders, cats)
		next.parts, next.fits, next.loader, next.handoff = cur.parts, cur.fits, cur.loader, cur.handoff
		a.mu.Lock()
		a.tools, a.treeSig, cur = next, s, next
		a.mu.Unlock()
	}
	return cur
}

// treeSignature is what of the tree the tools show: slugs, titles, parents.
func treeSignature(cats []domain.Category) string {
	var b strings.Builder
	for _, c := range cats {
		fmt.Fprintf(&b, "%d|%s|%s|%d;", c.ID, c.Slug, c.Title, c.ParentID)
	}
	return b.String()
}

// Fixed answers for the turns that end without a usable model answer. They
// are shop wording, not model output.
const (
	replyStuck   = "Не вдалося підібрати відповідь. Спробуйте сформулювати інакше або скористайтеся каталогом."
	replyRefusal = "Я можу допомогти лише з підбором автозапчастин у нашому магазині. Що шукаєте?"
)

// photoNote marks a buyer message that came with a photo. It is part of the
// stored text, so later turns -- which no longer carry the image -- still know
// a photo was sent and that the model's description of it refers to one.
const photoNote = "[Покупець надіслав фото]"

// Reply answers one buyer message, with the photos sent along with it.
//
// Thinking blocks and history. Claude signs each thinking block together with
// everything before it in the conversation, and rejects a block whose prefix
// changed. Within this loop the turns go back to the model exactly as they
// came (llm.Message.Native), so the check passes. Between buyer messages the
// history is stored without thinking blocks at all: nothing replayed later is
// bound to anything, so trimming old turns from the front can never
// invalidate what is left. The model loses its reasoning from earlier turns,
// which for a shopping chat is a fair price for a history that can be cut.
func (a *Agent) Reply(ctx context.Context, userID, chatID int64, text string, images ...llm.Image) (*Reply, error) {
	ctx, cancel := context.WithTimeout(ctx, a.cfg.Timeout)
	defer cancel()

	history, err := a.history.Load(ctx, chatID)
	if err != nil {
		return nil, fmt.Errorf("agent: load history: %w", err)
	}
	history = dropEmpty(history)

	if len(images) > 0 {
		text = strings.TrimSpace(photoNote + "\n" + text)
	}
	turn := []llm.Message{{Role: llm.RoleUser, Text: text, Images: images}}
	tools := a.toolsFor(ctx)
	run := &runState{userID: userID, question: text}
	var usage llm.Usage
	start := time.Now()

	for step := range a.cfg.MaxSteps {
		resp, err := a.llm.Chat(ctx, llm.Request{
			System:   systemPrompt,
			Tools:    tools.defs,
			Messages: concat(history, turn),
		})
		if err != nil {
			return nil, fmt.Errorf("agent: step %d: %w", step, err)
		}
		addUsage(&usage, resp.Usage)
		turn = append(turn, resp.Message)

		switch resp.StopReason {
		case llm.StopRefusal:
			// The refused exchange is not stored: replaying it would only
			// steer the next turn towards the same wall.
			a.logTurn(ctx, chatID, step+1, usage, start, "refusal")
			return &Reply{Text: replyRefusal}, nil

		case llm.StopToolUse:
			results := tools.run(ctx, run, resp.Message.ToolCalls)
			turn = append(turn, llm.Message{Role: llm.RoleUser, ToolResults: results})
			if !answered(resp.Message, results) {
				continue
			}
			// The model wrote its answer and showed the cards in one step.
			// Asking it again only gets the same answer retold, which the
			// buyer then reads twice; the step is the answer. The closing
			// assistant turn keeps the stored history well formed -- it may
			// not end on tool results -- and is left empty here so the text
			// is not counted twice; saving fills it with the reply.
			turn = append(turn, llm.Message{Role: llm.RoleAssistant})
		}

		// An answer, possibly cut off by max_tokens; a cut answer still
		// beats none, and the prompt keeps answers far below the limit.
		reply := &Reply{Text: turnText(turn), ProductIDs: run.shown, CatalogPartIDs: run.shownParts,
			CartChanged: run.cartChanged, CarSaved: run.carSaved}
		if reply.Text == "" && len(reply.ProductIDs) == 0 && len(reply.CatalogPartIDs) == 0 {
			reply.Text = replyStuck
		}
		// The stored answer is the text the buyer was actually given. A final
		// turn with no text of its own -- the model ended after show_products --
		// would otherwise be stored empty, and the API rejects an assistant
		// message with no content on every later request of this chat.
		if last := &turn[len(turn)-1]; strings.TrimSpace(last.Text) == "" {
			last.Text = reply.Text
		}
		a.save(ctx, chatID, history, turn)
		a.logTurn(ctx, chatID, step+1, usage, start, string(resp.StopReason))
		return reply, nil
	}

	// Out of steps. The half-finished tool loop is not stored, because it
	// ends on tool results with no answer; the buyer's message and the fixed
	// reply are, so the next turn still knows what was asked.
	a.save(ctx, chatID, history, []llm.Message{
		{Role: llm.RoleUser, Text: text},
		{Role: llm.RoleAssistant, Text: replyStuck},
	})
	a.logTurn(ctx, chatID, a.cfg.MaxSteps, usage, start, "max_steps")
	return &Reply{Text: replyStuck, ProductIDs: run.shown, CatalogPartIDs: run.shownParts, CartChanged: run.cartChanged, CarSaved: run.carSaved}, nil
}

// Reset forgets the conversation.
func (a *Agent) Reset(ctx context.Context, chatID int64) error {
	return a.history.Clear(ctx, chatID)
}

// save stores the conversation. A failure is logged, not returned: the buyer
// already has an answer, and losing the history costs only context.
func (a *Agent) save(ctx context.Context, chatID int64, history, turn []llm.Message) {
	msgs := trim(concat(history, turn), a.cfg.MaxHistory)
	// A fresh context: the turn's own may be nearly spent after a slow model.
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if err := a.history.Save(sctx, chatID, msgs); err != nil {
		a.log.WarnContext(ctx, "could not save conversation", slog.Int64("chat_id", chatID), slog.Any("error", err))
	}
}

func (a *Agent) logTurn(ctx context.Context, chatID int64, steps int, u llm.Usage, start time.Time, outcome string) {
	a.log.InfoContext(ctx, "agent turn",
		slog.Int64("chat_id", chatID),
		slog.String("outcome", outcome),
		slog.Int("steps", steps),
		slog.Int64("input_tokens", u.InputTokens),
		slog.Int64("cache_read_tokens", u.CacheReadTokens),
		slog.Int64("cache_write_tokens", u.CacheWriteTokens),
		slog.Int64("output_tokens", u.OutputTokens),
		slog.Duration("took", time.Since(start)))
}

// trim drops whole turns from the front until at most max messages remain. A
// turn starts with a buyer's text message, so a tool call is never separated
// from its result -- the API rejects a tool result without its call.
func trim(msgs []llm.Message, maxMsgs int) []llm.Message {
	for len(msgs) > maxMsgs {
		next := -1
		for i := 1; i < len(msgs); i++ {
			if startsTurn(msgs[i]) {
				next = i
				break
			}
		}
		if next == -1 {
			// One turn longer than the cap: keep it rather than store a
			// broken fragment.
			return msgs
		}
		msgs = msgs[next:]
	}
	return msgs
}

// dropEmpty removes messages with nothing in them, which an older version
// could store. One such message makes the API reject every request that
// replays it, locking the chat out of the consultant until the history
// expires. Without it, two user messages may end up adjacent, which the API
// accepts and reads as one turn.
func dropEmpty(msgs []llm.Message) []llm.Message {
	return slices.DeleteFunc(msgs, func(m llm.Message) bool {
		return strings.TrimSpace(m.Text) == "" && len(m.ToolCalls) == 0 && len(m.ToolResults) == 0
	})
}

func startsTurn(m llm.Message) bool {
	return m.Role == llm.RoleUser && len(m.ToolResults) == 0
}

// answered reports whether a step that called tools is already the answer:
// it has text for the buyer, every call was show_products, and none failed.
// A failed call has to go back to the model, which then fixes its ids.
func answered(m llm.Message, results []llm.ToolResult) bool {
	if strings.TrimSpace(m.Text) == "" {
		return false
	}
	for _, c := range m.ToolCalls {
		if c.Name != toolShowProducts {
			return false
		}
	}
	for _, r := range results {
		if r.IsError {
			return false
		}
	}
	return true
}

// turnText is everything the model said to the buyer in this turn. The model
// often explains its pick in the step that calls show_products and ends with
// a short closing line -- or none at all -- so the last step alone can be
// "Shall I add it to the cart?" with the explanation lost. Seen live; caught
// by the evals.
func turnText(turn []llm.Message) string {
	var parts []string
	for _, m := range turn {
		// The same text again -- written with a call that failed, then with
		// its retry -- is said once.
		if t := strings.TrimSpace(m.Text); m.Role == llm.RoleAssistant && t != "" && !slices.Contains(parts, t) {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n\n")
}

func concat(a, b []llm.Message) []llm.Message {
	out := make([]llm.Message, 0, len(a)+len(b))
	return append(append(out, a...), b...)
}

func addUsage(total *llm.Usage, u llm.Usage) {
	total.InputTokens += u.InputTokens
	total.OutputTokens += u.OutputTokens
	total.CacheReadTokens += u.CacheReadTokens
	total.CacheWriteTokens += u.CacheWriteTokens
}

// IsUnavailable reports whether err means the model provider is overloaded or
// unreachable, as opposed to a bug.
func IsUnavailable(err error) bool {
	return errors.Is(err, llm.ErrUnavailable) || errors.Is(err, context.DeadlineExceeded)
}
