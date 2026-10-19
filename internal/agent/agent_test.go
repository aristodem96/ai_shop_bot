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

var octavia = domain.Vehicle{ID: 5, Make: "Skoda", Model: "Octavia A7", YearFrom: 2013, YearTo: 2020, Engine: "1.4 TSI", Fuel: "petrol"}

var frontPads = domain.Product{
	ID: 2, CategoryID: 1, SKU: "BRK-001", Name: "Колодки гальмівні передні", Brand: "AutoLine",
	Price: domain.UAH(1450), Stock: 1, OEMNumbers: []string{"5Q0698151"},
	Specs:       []domain.Spec{{Name: "Вісь", Value: "передня"}},
	Description: "Органічні колодки.", Fits: []domain.Vehicle{octavia},
}

type fakeCatalog struct {
	filters []service.SearchFilter
}

func (f *fakeCatalog) Categories(context.Context) ([]domain.Category, error) {
	return []domain.Category{{ID: 1, Slug: "brakes", Title: "Гальмівна система"}}, nil
}

func (f *fakeCatalog) Search(_ context.Context, sf service.SearchFilter) (*service.SearchResult, error) {
	f.filters = append(f.filters, sf)
	return &service.SearchResult{Products: []domain.Product{frontPads}}, nil
}

func (f *fakeCatalog) Product(_ context.Context, id int64) (*domain.Product, error) {
	if id == frontPads.ID {
		p := frontPads
		return &p, nil
	}
	return nil, domain.ErrNotFound
}

// fakeCart holds up to the product's stock.
type fakeCart struct{ qty int }

func (f *fakeCart) Add(_ context.Context, _, productID int64) (*domain.Cart, error) {
	if productID != frontPads.ID {
		return nil, domain.ErrNotFound
	}
	if f.qty+1 > frontPads.Stock {
		return nil, domain.ErrOutOfStock
	}
	f.qty++
	return &domain.Cart{Lines: []domain.CartLine{{ProductID: productID, Price: frontPads.Price, Qty: f.qty, Stock: frontPads.Stock}}}, nil
}

type fakeGarage struct {
	saved   map[int64]int64
	queries []service.VehicleQuery
}

func (f *fakeGarage) Find(_ context.Context, q service.VehicleQuery) ([]domain.Vehicle, error) {
	f.queries = append(f.queries, q)
	if strings.EqualFold(q.Make, octavia.Make) {
		return []domain.Vehicle{octavia}, nil
	}
	return nil, nil
}

func (f *fakeGarage) Makes(context.Context) ([]string, error) { return []string{octavia.Make}, nil }

func (f *fakeGarage) Get(_ context.Context, user int64) (*domain.Vehicle, error) {
	if _, ok := f.saved[user]; ok {
		v := octavia
		return &v, nil
	}
	return nil, nil
}

func (f *fakeGarage) Set(_ context.Context, user, vehicleID int64) (*domain.Vehicle, error) {
	if vehicleID != octavia.ID {
		return nil, domain.ErrNotFound
	}
	f.saved[user] = vehicleID
	v := octavia
	return &v, nil
}

type fakeKits struct{}

func (fakeKits) Kit(_ context.Context, vehicleID int64) (*service.Kit, error) {
	if vehicleID != octavia.ID {
		return nil, domain.ErrNotFound
	}
	return &service.Kit{
		Vehicle: domain.Vehicle{ID: octavia.ID, Make: "Skoda", Model: "Octavia A7", Engine: "1.4 TSI",
			YearFrom: 2013, YearTo: 2020, OilViscosity: "5W-30", OilVolume: 3.6},
		Items:   []service.KitItem{{PartType: domain.PartEngineOil, Product: frontPads, Qty: 1}},
		Missing: []string{domain.PartCabinFilter},
	}, nil
}

type fakeOrders struct{ asked []int64 }

func (f *fakeOrders) OrdersOf(_ context.Context, user int64, _ int) ([]domain.Order, error) {
	f.asked = append(f.asked, user)
	return []domain.Order{{ID: 42, Status: domain.OrderShipped, Total: domain.UAH(1450),
		Items:    []domain.OrderItem{{Name: "Колодки", Brand: "AutoLine", Qty: 1}},
		Contacts: domain.Contacts{Delivery: "Київ, відділення 25"}}}, nil
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

type fixture struct {
	orders  *fakeOrders
	agent   *Agent
	catalog *fakeCatalog
	cart    *fakeCart
	garage  *fakeGarage
	history *fakeHistory
}

func newAgent(t *testing.T, model *llm.Fake) fixture {
	t.Helper()
	f := fixture{orders: &fakeOrders{}, catalog: &fakeCatalog{}, cart: &fakeCart{}, garage: &fakeGarage{saved: map[int64]int64{}}, history: newFakeHistory()}
	a, err := New(context.Background(), model, f.catalog, f.cart, f.garage, fakeKits{}, f.orders, f.history,
		Config{MaxSteps: 5, MaxHistory: 30, Timeout: 5 * time.Second},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	f.agent = a
	return f
}

// result returns the tool result the model received on request n.
func result(model *llm.Fake, n int) llm.ToolResult {
	msgs := model.Requests[n].Messages
	return msgs[len(msgs)-1].ToolResults[0]
}

// --- dialogues ---------------------------------------------------------------

func TestFindCarSearchShowAnswer(t *testing.T) {
	model := llm.NewFake(
		llm.Call("c1", "find_vehicle", map[string]any{"make": "Skoda", "model": "Octavia", "year": 2015, "engine": "1.4"}),
		llm.Call("c2", "search_parts", map[string]any{"vehicle_id": 5, "category": "brakes", "query": "колодк передн"}),
		llm.Call("c3", "show_products", map[string]any{"product_ids": []int64{2}}),
		llm.Say("Ось передні колодки на вашу Octavia 1.4 TSI."),
	)
	f := newAgent(t, model)

	reply, err := f.agent.Reply(context.Background(), chat, chat, "передні колодки на октавію 2015 1.4")
	require.NoError(t, err)

	assert.Equal(t, "Ось передні колодки на вашу Octavia 1.4 TSI.", reply.Text)
	assert.Equal(t, []int64{2}, reply.ProductIDs)
	assert.Zero(t, model.Remaining())

	assert.Equal(t, service.VehicleQuery{Make: "Skoda", Model: "Octavia", Year: 2015, Engine: "1.4"}, f.garage.queries[0])
	assert.Contains(t, result(model, 1).Content, `"label":"Skoda Octavia A7 1.4 TSI (2013–2020)"`)

	require.Len(t, f.catalog.filters, 1)
	assert.Equal(t, int64(5), f.catalog.filters[0].VehicleID, "the search is for the car that was found")
	assert.Equal(t, "brakes", f.catalog.filters[0].Category)

	found := result(model, 2).Content
	assert.Contains(t, found, `"id":2`)
	assert.Contains(t, found, `"price_uah":1450`)
	assert.Contains(t, found, `"stock":"last_units"`)
	assert.Contains(t, found, `"oem_numbers":["5Q0698151"]`)

	saved := f.history.msgs[chat]
	require.Len(t, saved, 8)
	assert.Equal(t, "передні колодки на октавію 2015 1.4", saved[0].Text)
}

func TestUnknownCarListsSimilarMakes(t *testing.T) {
	model := llm.NewFake(
		llm.Call("c1", "find_vehicle", map[string]any{"make": "Skodda", "model": "Octavia"}),
		llm.Call("c2", "find_vehicle", map[string]any{"make": "Tesla", "model": "Model 3"}),
		llm.Say("На жаль, на Tesla деталей немає."),
	)
	f := newAgent(t, model)

	_, err := f.agent.Reply(context.Background(), chat, chat, "колодки")
	require.NoError(t, err)
	assert.Contains(t, result(model, 1).Content, `"similar_makes":["Skoda"]`, "a misspelt make is suggested")
	got := result(model, 2).Content
	assert.Contains(t, got, `"vehicles":[]`)
	assert.Contains(t, got, `"similar_makes":[]`, "not the catalogue's two hundred makes")
}

func TestSimilarMakes(t *testing.T) {
	makes := []string{"Chery", "Chevrolet", "Mercedes-Benz", "Skoda", "VW"}
	assert.Equal(t, []string{"Chery", "Chevrolet"}, similarMakes("Cheri", makes))
	assert.Equal(t, []string{"Mercedes-Benz"}, similarMakes("mercedes", makes))
}

func TestSaveMyCarAndCheckFit(t *testing.T) {
	model := llm.NewFake(
		llm.Call("c1", "get_my_vehicle", map[string]any{}),
		llm.Call("c2", "set_my_vehicle", map[string]any{"vehicle_id": 5}),
		llm.Call("c3", "get_part", map[string]any{"product_id": 2}),
		llm.Say("Так, підходить."),
	)
	f := newAgent(t, model)

	reply, err := f.agent.Reply(context.Background(), chat, chat, "у мене октавія 1.4, ці колодки підійдуть?")
	require.NoError(t, err)
	assert.True(t, reply.CarSaved, "the bot offers the catalogue for the saved car")

	assert.Equal(t, `{"vehicle":null}`, result(model, 1).Content)
	assert.Equal(t, int64(5), f.garage.saved[chat], "the car is saved for the catalogue too")
	part := result(model, 3).Content
	assert.Contains(t, part, `"fits_my_vehicle":true`, "fit comes from the fitment table, not the model")
	assert.Contains(t, part, `"fits":["Skoda Octavia A7 1.4 TSI (2013–2020)"]`)
}

func TestPrefixStaysStableAcrossSteps(t *testing.T) {
	model := llm.NewFake(
		llm.Call("c1", "search_parts", map[string]any{"category": "brakes"}),
		llm.Say("Є колодки."),
		llm.Say("Так, вони для передньої осі."),
	)
	f := newAgent(t, model)
	ctx := context.Background()

	_, err := f.agent.Reply(ctx, chat, chat, "колодки")
	require.NoError(t, err)
	_, err = f.agent.Reply(ctx, chat, chat, "вони передні?")
	require.NoError(t, err)

	// System and tools are what the provider caches and what Claude binds
	// thinking to; they must not move by a byte.
	first, _ := json.Marshal(model.Requests[0].Tools)
	for _, r := range model.Requests[1:] {
		again, _ := json.Marshal(r.Tools)
		assert.Equal(t, string(first), string(again))
		assert.Equal(t, model.Requests[0].System, r.System)
	}
	last := model.Requests[2].Messages
	assert.Equal(t, "колодки", last[0].Text)
	assert.Equal(t, "вони передні?", last[len(last)-1].Text)
}

func TestInventedProductIsNotShown(t *testing.T) {
	model := llm.NewFake(
		llm.Call("c1", "show_products", map[string]any{"product_ids": []int64{2, 999}}),
		llm.Say("Ось колодки."),
	)
	f := newAgent(t, model)

	reply, err := f.agent.Reply(context.Background(), chat, chat, "покажи щось")
	require.NoError(t, err)

	assert.Equal(t, []int64{2}, reply.ProductIDs, "only the real product becomes a card")
	res := result(model, 1)
	assert.True(t, res.IsError)
	assert.Contains(t, res.Content, "999", "the model is told which id was wrong")
}

func TestAddToCartStopsAtStock(t *testing.T) {
	model := llm.NewFake(
		llm.Call("c1", "add_to_cart", map[string]any{"product_id": 2, "quantity": 2}),
		llm.Say("Додав одну — більше немає."),
	)
	f := newAgent(t, model)

	reply, err := f.agent.Reply(context.Background(), chat, chat, "додай дві")
	require.NoError(t, err)

	assert.True(t, reply.CartChanged)
	assert.Equal(t, 1, f.cart.qty, "the last unit went in, the second did not")
	assert.Contains(t, result(model, 1).Content, `"added":1,"cart_items":1`)
}

func TestAddToCartRejectsBulk(t *testing.T) {
	model := llm.NewFake(
		llm.Call("c1", "add_to_cart", map[string]any{"product_id": 2, "quantity": 50}),
		llm.Say("Стільки не можна."),
	)
	f := newAgent(t, model)

	reply, err := f.agent.Reply(context.Background(), chat, chat, "додай 50 штук")
	require.NoError(t, err)
	assert.False(t, reply.CartChanged)
	assert.Zero(t, f.cart.qty)
	assert.True(t, result(model, 1).IsError)
}

func TestBadToolInputIsReportedToTheModel(t *testing.T) {
	model := llm.NewFake(
		llm.Call("c1", "search_parts", map[string]any{"categry": "brakes"}),
		llm.Call("c2", "drop_database", map[string]any{}),
		llm.Say("Вибачте."),
	)
	f := newAgent(t, model)

	_, err := f.agent.Reply(context.Background(), chat, chat, "колодки")
	require.NoError(t, err)

	assert.Empty(t, f.catalog.filters, "a misspelled filter must not run as an unfiltered search")
	typo := result(model, 1)
	assert.True(t, typo.IsError)
	assert.Contains(t, typo.Content, "categry")
	assert.True(t, result(model, 2).IsError)
}

func TestRunningOutOfSteps(t *testing.T) {
	steps := make([]llm.FakeStep, 0, 5)
	for i := range 5 {
		steps = append(steps, llm.Call("c"+strings.Repeat("x", i), "search_parts", map[string]any{}))
	}
	f := newAgent(t, llm.NewFake(steps...))

	reply, err := f.agent.Reply(context.Background(), chat, chat, "щось")
	require.NoError(t, err)
	assert.Equal(t, replyStuck, reply.Text)

	// Only the question and the fixed answer are stored: a history ending on
	// tool results with no answer would be malformed for the next request.
	saved := f.history.msgs[chat]
	require.Len(t, saved, 2)
	assert.Equal(t, llm.RoleAssistant, saved[1].Role)
}

func TestRefusalIsNotRemembered(t *testing.T) {
	model := llm.NewFake(llm.FakeStep{Response: &llm.Response{
		Message: llm.Message{Role: llm.RoleAssistant}, StopReason: llm.StopRefusal,
	}})
	f := newAgent(t, model)

	reply, err := f.agent.Reply(context.Background(), chat, chat, "щось недозволене")
	require.NoError(t, err)
	assert.Equal(t, replyRefusal, reply.Text)
	assert.Empty(t, f.history.msgs[chat])
}

func TestProviderOutageIsReported(t *testing.T) {
	f := newAgent(t, llm.NewFake(llm.FakeStep{Err: llm.ErrUnavailable}))
	_, err := f.agent.Reply(context.Background(), chat, chat, "колодки")
	require.Error(t, err)
	assert.True(t, IsUnavailable(err))
}

func TestReset(t *testing.T) {
	f := newAgent(t, llm.NewFake(llm.Say("Привіт!")))
	_, err := f.agent.Reply(context.Background(), chat, chat, "привіт")
	require.NoError(t, err)
	require.NotEmpty(t, f.history.msgs[chat])

	require.NoError(t, f.agent.Reset(context.Background(), chat))
	assert.Empty(t, f.history.msgs[chat])
}

func TestTextBeforeShowIsTheAnswer(t *testing.T) {
	// Seen live: the model speaks, shows products, then ends with no text.
	talkAndShow := llm.Call("c1", "show_products", map[string]any{"product_ids": []int64{2}})
	talkAndShow.Response.Message.Text = "Ось передні колодки."
	f := newAgent(t, llm.NewFake(talkAndShow, llm.Say("")))

	reply, err := f.agent.Reply(context.Background(), chat, chat, "колодки")
	require.NoError(t, err)
	assert.Equal(t, "Ось передні колодки.", reply.Text)
	assert.Equal(t, []int64{2}, reply.ProductIDs)
}

func TestExplanationIsNotLostToAClosingLine(t *testing.T) {
	// Seen in the evals: the explanation came with show_products, and the
	// final step said only "Shall I add it?". The step with the explanation
	// and the cards is the answer now; the model is not asked again.
	talkAndShow := llm.Call("c1", "show_products", map[string]any{"product_ids": []int64{2}})
	talkAndShow.Response.Message.Text = "Для Camry потрібна олива 0W-20 — ось вона."
	fake := llm.NewFake(talkAndShow, llm.Say("Додати в кошик?"))
	f := newAgent(t, fake)

	reply, err := f.agent.Reply(context.Background(), chat, chat, "яке масло?")
	require.NoError(t, err)
	assert.Equal(t, "Для Camry потрібна олива 0W-20 — ось вона.", reply.Text)
	assert.Equal(t, []int64{2}, reply.ProductIDs)
	assert.Equal(t, 1, fake.Remaining(), "one request, not two")
}

// Seen live: the model wrote the answer with show_products, was asked
// again after the cards, and retold it; the buyer read it twice.
func TestAnswerWithCardsIsNotRetold(t *testing.T) {
	answer := "Я запам'ятав ваш Daewoo Lanos 1.5. Для ближнього світла зазвичай ставлять H4."
	talkAndShow := llm.Call("c1", "show_products", map[string]any{"product_ids": []int64{2}})
	talkAndShow.Response.Message.Text = answer
	fake := llm.NewFake(talkAndShow, llm.Say("Я запам'ятав ваш Daewoo Lanos 1.5. Для ближнього світла — H4."))
	f := newAgent(t, fake)

	reply, err := f.agent.Reply(context.Background(), chat, chat, "лампа ближнього світла")
	require.NoError(t, err)
	assert.Equal(t, answer, reply.Text, "said once")

	saved := f.history.msgs[chat]
	last := saved[len(saved)-1]
	assert.Equal(t, llm.RoleAssistant, last.Role, "the stored turn ends with the model, not on tool results")
	assert.Equal(t, answer, last.Text)
}

func TestFailedShowProductsGoesBackToTheModel(t *testing.T) {
	bad := llm.Call("c1", "show_products", map[string]any{"product_ids": []int64{404}})
	bad.Response.Message.Text = "Ось варіант."
	good := llm.Call("c2", "show_products", map[string]any{"product_ids": []int64{2}})
	good.Response.Message.Text = "Ось варіант."
	fake := llm.NewFake(bad, good)
	f := newAgent(t, fake)

	reply, err := f.agent.Reply(context.Background(), chat, chat, "колодки")
	require.NoError(t, err)
	assert.Equal(t, []int64{2}, reply.ProductIDs, "a made-up id is sent back to be fixed, not shown")
	assert.Equal(t, "Ось варіант.", reply.Text, "the text written again with the retry is said once")
	assert.Zero(t, fake.Remaining())
}

func TestEmptyFinalTurnIsStoredWithTheSentText(t *testing.T) {
	f := newAgent(t, llm.NewFake(llm.Call("c1", "show_products", map[string]any{"product_ids": []int64{2}}), llm.Say("")))

	reply, err := f.agent.Reply(context.Background(), chat, chat, "колодки")
	require.NoError(t, err)

	saved := f.history.msgs[chat]
	assert.Equal(t, reply.Text, saved[len(saved)-1].Text, "no assistant message is stored empty")
}

func TestEmptyMessagesInOldHistoryAreDropped(t *testing.T) {
	// The shape a chat was left in by the bug this guards against.
	model := llm.NewFake(llm.Say("Усе добре!"))
	f := newAgent(t, model)
	f.history.msgs[chat] = []llm.Message{
		{Role: llm.RoleUser, Text: "колодки"},
		{Role: llm.RoleAssistant},
	}

	_, err := f.agent.Reply(context.Background(), chat, chat, "як справи")
	require.NoError(t, err)

	sent := model.Requests[0].Messages
	require.Len(t, sent, 2)
	assert.Equal(t, "колодки", sent[0].Text)
	assert.Equal(t, "як справи", sent[1].Text)
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

func TestPhotoIsSentOnceAndNotStored(t *testing.T) {
	model := llm.NewFake(
		llm.Call("c1", "search_parts", map[string]any{"category": "brakes", "query": "колодк"}),
		llm.Say("Схоже на передні колодки."),
		llm.Say("Так, ті самі."),
	)
	f := newAgent(t, model)
	ctx := context.Background()
	photo := llm.Image{MediaType: "image/jpeg", Data: []byte{0xff, 0xd8, 0xff}}

	_, err := f.agent.Reply(ctx, chat, chat, "що це за деталь?", photo)
	require.NoError(t, err)

	first := model.Requests[0].Messages[0]
	assert.Equal(t, []llm.Image{photo}, first.Images)
	assert.Equal(t, photoNote+"\nщо це за деталь?", first.Text)
	assert.Equal(t, []llm.Image{photo}, model.Requests[1].Messages[0].Images,
		"the photo stays in the turn while the model works on it")

	// The next message replays the history without the image, but with the
	// note and what the model said about it.
	_, err = f.agent.Reply(ctx, chat, chat, "а на октавію є?")
	require.NoError(t, err)
	replayed := model.Requests[2].Messages[0]
	assert.Empty(t, replayed.Images)
	assert.Contains(t, replayed.Text, photoNote)
}

func TestPhotoWithoutCaption(t *testing.T) {
	model := llm.NewFake(llm.Say("Це масляний фільтр."))
	f := newAgent(t, model)

	_, err := f.agent.Reply(context.Background(), chat, chat, "", llm.Image{MediaType: "image/jpeg", Data: []byte{1}})
	require.NoError(t, err)
	assert.Equal(t, photoNote, model.Requests[0].Messages[0].Text)
}

func TestServiceKit(t *testing.T) {
	model := llm.NewFake(
		llm.Call("c1", "service_kit", map[string]any{"vehicle_id": 5}),
		llm.Say("Ось комплект для ТО."),
	)
	f := newAgent(t, model)

	_, err := f.agent.Reply(context.Background(), chat, chat, "що треба на ТО?")
	require.NoError(t, err)
	got := result(model, 1).Content
	assert.Contains(t, got, `"oil":{"litres":3.6,"viscosity":"5W-30"}`)
	assert.Contains(t, got, `"missing_in_stock":["cabin_filter"]`)
	assert.Contains(t, got, `"total_uah":1450`)
}

func TestDecodeVIN(t *testing.T) {
	model := llm.NewFake(
		llm.Call("c1", "decode_vin", map[string]any{"vin": "tmb jg7ne 0 d0123456"}),
		llm.Call("c2", "decode_vin", map[string]any{"vin": "TMB123"}),
		llm.Say("Skoda 2013 року. Який двигун?"),
	)
	f := newAgent(t, model)

	_, err := f.agent.Reply(context.Background(), chat, chat, "мій VIN TMBJG7NE0D0123456")
	require.NoError(t, err)

	got := result(model, 1)
	assert.False(t, got.IsError)
	assert.Contains(t, got.Content, `"make":"Skoda"`)
	assert.Contains(t, got.Content, `"model_year_candidates":[2013`)

	bad := result(model, 2)
	assert.True(t, bad.IsError)
	assert.Contains(t, bad.Content, "17", "the model can tell the buyer what is wrong")
}

func TestOrderStatusIsTheBuyersOwn(t *testing.T) {
	model := llm.NewFake(
		llm.Call("c1", "order_status", map[string]any{}),
		llm.Say("Ваше замовлення №42 відправлено."),
	)
	f := newAgent(t, model)

	_, err := f.agent.Reply(context.Background(), chat, chat, "де моє замовлення?")
	require.NoError(t, err)
	assert.Equal(t, []int64{chat}, f.orders.asked, "the user comes from the chat, not from the model")
	got := result(model, 1).Content
	assert.Contains(t, got, `"status":"shipped"`)
	assert.Contains(t, got, `"meaning":"shipped by Nova Poshta`)
}
