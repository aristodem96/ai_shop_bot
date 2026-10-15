// Package agent is the shop's AI consultant: it turns a buyer's free text
// into catalogue searches and cart actions through tools, and answers with
// text plus the ids of products to show as cards.
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
	"time"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
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
	AddSize(ctx context.Context, userID, productID int64, size string) (*domain.Cart, error)
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
	// CartChanged is set when a tool added something to the cart.
	CartChanged bool
}

// Agent runs the conversation loop.
type Agent struct {
	llm     llm.Client
	catalog Catalog
	cart    Cart
	history History
	tools   *toolbox
	cfg     Config
	log     *slog.Logger
}

// New builds an Agent. The categories are read once here, because they go
// into a tool schema that must stay byte-identical between requests for the
// provider's prompt cache to hit.
func New(ctx context.Context, client llm.Client, catalog Catalog, cart Cart, history History, cfg Config, log *slog.Logger) (*Agent, error) {
	cats, err := catalog.Categories(ctx)
	if err != nil {
		return nil, fmt.Errorf("agent: load categories: %w", err)
	}
	return &Agent{
		llm:     client,
		catalog: catalog,
		cart:    cart,
		history: history,
		tools:   newToolbox(catalog, cart, cats),
		cfg:     cfg,
		log:     log,
	}, nil
}

// Fixed answers for the turns that end without a usable model answer. They
// are shop wording, not model output.
const (
	replyStuck   = "Не вдалося підібрати відповідь. Спробуйте сформулювати інакше або скористайтеся каталогом."
	replyRefusal = "Я можу допомогти лише з вибором одягу в нашому магазині. Що шукаєте?"
)

// Reply answers one buyer message.
//
// Thinking blocks and history. Claude signs each thinking block together with
// everything before it in the conversation, and rejects a block whose prefix
// changed. Within this loop the turns go back to the model exactly as they
// came (llm.Message.Native), so the check passes. Between buyer messages the
// history is stored without thinking blocks at all: nothing replayed later is
// bound to anything, so trimming old turns from the front can never
// invalidate what is left. The model loses its reasoning from earlier turns,
// which for a shopping chat is a fair price for a history that can be cut.
func (a *Agent) Reply(ctx context.Context, userID, chatID int64, text string) (*Reply, error) {
	ctx, cancel := context.WithTimeout(ctx, a.cfg.Timeout)
	defer cancel()

	history, err := a.history.Load(ctx, chatID)
	if err != nil {
		return nil, fmt.Errorf("agent: load history: %w", err)
	}

	turn := []llm.Message{{Role: llm.RoleUser, Text: text}}
	run := &runState{userID: userID}
	var usage llm.Usage
	start := time.Now()

	for step := range a.cfg.MaxSteps {
		resp, err := a.llm.Chat(ctx, llm.Request{
			System:   systemPrompt,
			Tools:    a.tools.defs,
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
			results := a.tools.run(ctx, run, resp.Message.ToolCalls)
			turn = append(turn, llm.Message{Role: llm.RoleUser, ToolResults: results})
			continue
		}

		// An answer, possibly cut off by max_tokens; a cut answer still
		// beats none, and the prompt keeps answers far below the limit.
		reply := &Reply{Text: resp.Message.Text, ProductIDs: run.shown, CartChanged: run.cartChanged}
		if reply.Text == "" && len(reply.ProductIDs) == 0 {
			reply.Text = replyStuck
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
	return &Reply{Text: replyStuck, ProductIDs: run.shown, CartChanged: run.cartChanged}, nil
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

func startsTurn(m llm.Message) bool {
	return m.Role == llm.RoleUser && len(m.ToolResults) == 0
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
