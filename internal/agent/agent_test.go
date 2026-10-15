package agent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/llm"
	"github.com/aristodem96/ai_shop_bot/internal/service"
)

// --- fakes -------------------------------------------------------------------

var olivePuffer = domain.Product{
	ID: 2, CategoryID: 1, Name: "Пуховик «Nord»", Brand: "Nord Line", Color: "olive",
	Season: "winter", Gender: "unisex", Styles: []string{"casual"}, Price: domain.UAH(3499),
	Description: "Теплий пуховик.", TotalStock: 4,
	Variants: []domain.Variant{{ID: 21, Size: "M", Stock: 1}, {ID: 22, Size: "L", Stock: 3}, {ID: 23, Size: "XL"}},
}

type fakeCatalog struct {
	filters []service.SearchFilter
}

func (f *fakeCatalog) Categories(context.Context) ([]domain.Category, error) {
	return []domain.Category{{ID: 1, Slug: "jackets", Title: "Куртки"}}, nil
}

func (f *fakeCatalog) Search(_ context.Context, sf service.SearchFilter) (*service.SearchResult, error) {
	f.filters = append(f.filters, sf)
	return &service.SearchResult{Products: []domain.Product{olivePuffer}}, nil
}

func (f *fakeCatalog) Product(_ context.Context, id int64) (*domain.Product, error) {
	if id == olivePuffer.ID {
		p := olivePuffer
		return &p, nil
	}
	return nil, domain.ErrNotFound
}

type fakeCart struct{ added []string }

func (f *fakeCart) AddSize(_ context.Context, _, productID int64, size string) (*domain.Cart, error) {
	if size == "XL" {
		return nil, domain.ErrOutOfStock
	}
	f.added = append(f.added, size)
	return &domain.Cart{Lines: []domain.CartLine{{Price: domain.UAH(3499), Qty: len(f.added), ProductID: productID}}}, nil
}

type fakeHistory struct {
	mu   sync.Mutex
	msgs map[int64][]llm.Message
}

func newFakeHistory() *fakeHistory { return &fakeHistory{msgs: map[int64][]llm.Message{}} }

func (f *fakeHistory) Load(_ context.Context, chat int64) ([]llm.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.msgs[chat], nil
}

// Save stores a JSON round trip, the way Redis would, so a test sees exactly
// what survives persistence -- and Native does not.
func (f *fakeHistory) Save(_ context.Context, chat int64, msgs []llm.Message) error {
	raw, err := json.Marshal(msgs)
	if err != nil {
		return err
	}
	var back []llm.Message
	if err := json.Unmarshal(raw, &back); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs[chat] = back
	return nil
}

func (f *fakeHistory) Clear(_ context.Context, chat int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.msgs, chat)
	return nil
}

const chat = int64(7)

func newAgent(t *testing.T, model *llm.Fake) (*Agent, *fakeCatalog, *fakeCart, *fakeHistory) {
	t.Helper()
	catalog, cart, history := &fakeCatalog{}, &fakeCart{}, newFakeHistory()
	a, err := New(context.Background(), model, catalog, cart, history,
		Config{MaxSteps: 4, MaxHistory: 20, Timeout: 5 * time.Second},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	return a, catalog, cart, history
}

// --- dialogues ---------------------------------------------------------------

func TestSearchShowAnswer(t *testing.T) {
	model := llm.NewFake(
		llm.Call("c1", "search_products", map[string]any{
			"category": "jackets", "season": "winter", "max_price_uah": 3500,
			"size": "M", "exclude_colors": []string{"black"},
		}),
		llm.Call("c2", "show_products", map[string]any{"product_ids": []int64{2}}),
		llm.Say("Ось теплий оливковий пуховик у розмірі M."),
	)
	a, catalog, _, history := newAgent(t, model)

	reply, err := a.Reply(context.Background(), chat, chat, "тепла куртка до 3500, розмір M, не чорна")
	require.NoError(t, err)

	assert.Equal(t, "Ось теплий оливковий пуховик у розмірі M.", reply.Text)
	assert.Equal(t, []int64{2}, reply.ProductIDs)
	assert.Zero(t, model.Remaining())

	// The model's arguments became a typed filter, money in kopiyky.
	require.Len(t, catalog.filters, 1)
	f := catalog.filters[0]
	assert.Equal(t, domain.UAH(3500), f.MaxPrice)
	assert.Equal(t, []string{"black"}, f.ExcludeColors)

	// The model was shown the search result: ids, hryvnias and sizes in stock.
	result := model.Requests[1].Messages[2].ToolResults[0]
	assert.False(t, result.IsError)
	assert.Contains(t, result.Content, `"id":2`)
	assert.Contains(t, result.Content, `"price_uah":3499`)
	assert.Contains(t, result.Content, `"sizes_in_stock":["M","L"]`, "a sold-out size is not offered")
	assert.Contains(t, result.Content, `"low_stock":["M"]`)

	// The whole exchange is remembered for the next message.
	saved := history.msgs[chat]
	require.Len(t, saved, 6)
	assert.Equal(t, "тепла куртка до 3500, розмір M, не чорна", saved[0].Text)
	assert.Equal(t, "Ось теплий оливковий пуховик у розмірі M.", saved[5].Text)
}

func TestPrefixStaysStableAcrossSteps(t *testing.T) {
	model := llm.NewFake(
		llm.Call("c1", "search_products", map[string]any{"category": "jackets"}),
		llm.Say("Є пуховик."),
		llm.Say("Так, він теплий."),
	)
	a, _, _, _ := newAgent(t, model)
	ctx := context.Background()

	_, err := a.Reply(ctx, chat, chat, "куртка")
	require.NoError(t, err)
	_, err = a.Reply(ctx, chat, chat, "а він теплий?")
	require.NoError(t, err)

	// System and tools are what the provider caches and what Claude binds
	// thinking to; they must not move by a byte.
	first, _ := json.Marshal(model.Requests[0].Tools)
	for _, r := range model.Requests[1:] {
		again, _ := json.Marshal(r.Tools)
		assert.Equal(t, string(first), string(again))
		assert.Equal(t, model.Requests[0].System, r.System)
	}
	// And the second buyer message is appended after the first exchange.
	last := model.Requests[2].Messages
	assert.Equal(t, "куртка", last[0].Text)
	assert.Equal(t, "а він теплий?", last[len(last)-1].Text)
}

func TestInventedProductIsNotShown(t *testing.T) {
	model := llm.NewFake(
		llm.Call("c1", "show_products", map[string]any{"product_ids": []int64{2, 999}}),
		llm.Say("Ось пуховик."),
	)
	a, _, _, _ := newAgent(t, model)

	reply, err := a.Reply(context.Background(), chat, chat, "покажи щось")
	require.NoError(t, err)

	assert.Equal(t, []int64{2}, reply.ProductIDs, "only the real product becomes a card")
	result := model.Requests[1].Messages[2].ToolResults[0]
	assert.True(t, result.IsError)
	assert.Contains(t, result.Content, "999", "the model is told which id was wrong")
}

func TestAddToCart(t *testing.T) {
	model := llm.NewFake(
		llm.Call("c1", "add_to_cart", map[string]any{"product_id": 2, "size": "XL"}),
		llm.Call("c2", "add_to_cart", map[string]any{"product_id": 2, "size": "L"}),
		llm.Say("XL немає, додав L."),
	)
	a, _, cart, _ := newAgent(t, model)

	reply, err := a.Reply(context.Background(), chat, chat, "додай пуховик у XL")
	require.NoError(t, err)

	assert.True(t, reply.CartChanged)
	assert.Equal(t, []string{"L"}, cart.added)
	refused := model.Requests[1].Messages[2].ToolResults[0]
	assert.True(t, refused.IsError)
	assert.Equal(t, "this size is out of stock", refused.Content)
}

func TestBadToolInputIsReportedToTheModel(t *testing.T) {
	model := llm.NewFake(
		llm.Call("c1", "search_products", map[string]any{"categry": "jackets"}),
		llm.Call("c2", "drop_database", map[string]any{}),
		llm.Say("Вибачте."),
	)
	a, catalog, _, _ := newAgent(t, model)

	_, err := a.Reply(context.Background(), chat, chat, "куртка")
	require.NoError(t, err)

	assert.Empty(t, catalog.filters, "a misspelled filter must not run as an unfiltered search")
	typo := model.Requests[1].Messages[2].ToolResults[0]
	assert.True(t, typo.IsError)
	assert.Contains(t, typo.Content, "categry")
	unknown := model.Requests[2].Messages[4].ToolResults[0]
	assert.True(t, unknown.IsError)
}

func TestRunningOutOfSteps(t *testing.T) {
	steps := make([]llm.FakeStep, 0, 4)
	for i := range 4 {
		steps = append(steps, llm.Call("c"+strings.Repeat("x", i), "search_products", map[string]any{}))
	}
	a, _, _, history := newAgent(t, llm.NewFake(steps...))

	reply, err := a.Reply(context.Background(), chat, chat, "щось")
	require.NoError(t, err)
	assert.Equal(t, replyStuck, reply.Text)

	// Only the question and the fixed answer are stored: a history ending on
	// tool results with no answer would be malformed for the next request.
	saved := history.msgs[chat]
	require.Len(t, saved, 2)
	assert.Equal(t, llm.RoleAssistant, saved[1].Role)
}

func TestRefusalIsNotRemembered(t *testing.T) {
	model := llm.NewFake(llm.FakeStep{Response: &llm.Response{
		Message: llm.Message{Role: llm.RoleAssistant}, StopReason: llm.StopRefusal,
	}})
	a, _, _, history := newAgent(t, model)

	reply, err := a.Reply(context.Background(), chat, chat, "щось недозволене")
	require.NoError(t, err)
	assert.Equal(t, replyRefusal, reply.Text)
	assert.Empty(t, history.msgs[chat])
}

func TestProviderOutageIsReported(t *testing.T) {
	a, _, _, _ := newAgent(t, llm.NewFake(llm.FakeStep{Err: llm.ErrUnavailable}))
	_, err := a.Reply(context.Background(), chat, chat, "куртка")
	require.Error(t, err)
	assert.True(t, IsUnavailable(err))
}

func TestReset(t *testing.T) {
	a, _, _, history := newAgent(t, llm.NewFake(llm.Say("Привіт!")))
	_, err := a.Reply(context.Background(), chat, chat, "привіт")
	require.NoError(t, err)
	require.NotEmpty(t, history.msgs[chat])

	require.NoError(t, a.Reset(context.Background(), chat))
	assert.Empty(t, history.msgs[chat])
}

// --- history trimming --------------------------------------------------------

func TestTrimDropsWholeTurns(t *testing.T) {
	user := func(s string) llm.Message { return llm.Message{Role: llm.RoleUser, Text: s} }
	call := llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "c"}}}
	result := llm.Message{Role: llm.RoleUser, ToolResults: []llm.ToolResult{{CallID: "c"}}}
	answer := llm.Message{Role: llm.RoleAssistant, Text: "ok"}

	msgs := []llm.Message{user("1"), call, result, answer, user("2"), answer, user("3"), call, result, answer}

	got := trim(msgs, 6)
	assert.Equal(t, "2", got[0].Text, "the first turn goes as a whole")
	assert.Len(t, got, 6)

	got = trim(msgs, 3)
	assert.Equal(t, "3", got[0].Text, "a tool result is never left without its call")
	assert.Len(t, got, 4, "one turn longer than the cap is kept whole")
}
