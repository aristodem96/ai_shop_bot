package postgres

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/service"
)

var buyer = domain.Contacts{Name: "Тарас Шевченко", Phone: "+380671234567", Delivery: "Київ, відділення 25"}

// withStock pins a product's stock for one test and restores it after.
func withStock(t *testing.T, sku string, stock int) *domain.Product {
	t.Helper()
	ctx := context.Background()
	p := productBySKU(t, sku)
	_, err := testPool.Exec(ctx, `UPDATE products SET stock = $2 WHERE id = $1`, p.ID, stock)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `UPDATE products SET stock = $2 WHERE id = $1`, p.ID, p.Stock)
	})
	p.Stock = stock
	return p
}

func stockOf(t *testing.T, productID int64) int {
	t.Helper()
	var n int
	require.NoError(t, testPool.QueryRow(context.Background(), `SELECT stock FROM products WHERE id = $1`, productID).Scan(&n))
	return n
}

// buyerCart fills a user's cart and removes the user's orders and cart after
// the test.
func buyerCart(t *testing.T, user int64, productID int64, qty int) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, NewCartRepository(testPool).SetQty(ctx, user, productID, qty))
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM orders WHERE user_id = $1`, user)
		_, _ = testPool.Exec(ctx, `DELETE FROM carts WHERE user_id = $1`, user)
		_, _ = testPool.Exec(ctx, `DELETE FROM customers WHERE user_id = $1`, user)
	})
}

func place(t *testing.T, user int64) *domain.Order {
	t.Helper()
	o, created, err := NewOrderRepository(testPool).PlaceOrder(context.Background(), user, buyer, time.Now().Add(domain.ReservationTTL))
	require.NoError(t, err)
	require.True(t, created)
	return o
}

func TestPlaceOrderReservesStock(t *testing.T) {
	skipShort(t)
	ctx := context.Background()
	repo := NewOrderRepository(testPool)
	const user = int64(5001)
	pads := withStock(t, "BRK-002", 5)
	buyerCart(t, user, pads.ID, 2)

	o := place(t, user)
	assert.Equal(t, domain.OrderAwaitingPayment, o.Status)
	assert.Equal(t, pads.Price*2, o.Total)
	require.Len(t, o.Items, 1)
	assert.Equal(t, pads.Price, o.Items[0].Price, "the price is frozen on the order")
	assert.Equal(t, 3, stockOf(t, pads.ID), "two units are held for the order")

	cart, err := NewCartRepository(testPool).Cart(ctx, user)
	require.NoError(t, err)
	assert.Empty(t, cart.Lines, "the cart became the order")

	c, err := repo.Customer(ctx, user)
	require.NoError(t, err)
	assert.Equal(t, buyer, *c, "contacts are remembered for next time")

	// A second checkout returns the open order instead of reserving again.
	again, created, err := repo.PlaceOrder(ctx, user, buyer, time.Now().Add(domain.ReservationTTL))
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, o.ID, again.ID)
	assert.Equal(t, 3, stockOf(t, pads.ID))
}

func TestPlaceOrderEmptyCart(t *testing.T) {
	skipShort(t)
	_, _, err := NewOrderRepository(testPool).PlaceOrder(context.Background(), 5002, buyer, time.Now())
	require.ErrorIs(t, err, domain.ErrCartEmpty)
}

// The race the reservation exists for: many buyers check out the last unit at
// once. Exactly one gets it; nobody else is charged for it; stock never goes
// below zero.
func TestLastUnitRace(t *testing.T) {
	skipShort(t)
	ctx := context.Background()
	repo := NewOrderRepository(testPool)
	pads := withStock(t, "BRK-001", 1)

	const buyers = 10
	for i := range int64(buyers) {
		buyerCart(t, 5100+i, pads.ID, 1)
	}

	var (
		wg              sync.WaitGroup
		mu              sync.Mutex
		won, outOfStock int
	)
	start := make(chan struct{})
	for i := range int64(buyers) {
		wg.Go(func() {
			<-start
			_, _, err := repo.PlaceOrder(ctx, 5100+i, buyer, time.Now().Add(domain.ReservationTTL))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				won++
			case assert.ErrorIs(t, err, domain.ErrOutOfStock):
				outOfStock++
			}
		})
	}
	close(start)
	wg.Wait()

	assert.Equal(t, 1, won, "exactly one buyer gets the last unit")
	assert.Equal(t, buyers-1, outOfStock, "everyone else is told it is gone")
	assert.Zero(t, stockOf(t, pads.ID), "and stock never goes negative")
}

func success(o *domain.Order, at time.Time) domain.PaymentEvent {
	return domain.PaymentEvent{InvoiceID: o.InvoiceID, Status: domain.PaymentSuccess, Amount: o.Total, ModifiedAt: at}
}

func withInvoice(t *testing.T, o *domain.Order, id string) {
	t.Helper()
	require.NoError(t, NewOrderRepository(testPool).AttachInvoice(context.Background(), o.ID, id, "https://pay.example/"+id))
	o.InvoiceID = id
}

func TestApplyPaymentIsIdempotent(t *testing.T) {
	skipShort(t)
	ctx := context.Background()
	repo := NewOrderRepository(testPool)
	const user = int64(5201)
	oil := withStock(t, "OIL-001", 10)
	buyerCart(t, user, oil.ID, 1)
	o := place(t, user)
	withInvoice(t, o, "inv-5201")
	t0 := time.Now()

	res, err := repo.ApplyPayment(ctx, domain.PaymentEvent{InvoiceID: o.InvoiceID, Status: domain.PaymentProcessing, ModifiedAt: t0})
	require.NoError(t, err)
	assert.False(t, res.Changed, "processing is progress, not a change")

	res, err = repo.ApplyPayment(ctx, success(o, t0.Add(time.Second)))
	require.NoError(t, err)
	assert.True(t, res.Changed)
	assert.Equal(t, domain.OrderPaid, res.Order.Status)
	assert.Equal(t, domain.OrderAwaitingPayment, res.From)

	res, err = repo.ApplyPayment(ctx, success(o, t0.Add(time.Second)))
	require.NoError(t, err)
	assert.False(t, res.Changed, "the same webhook again changes nothing")

	res, err = repo.ApplyPayment(ctx, domain.PaymentEvent{InvoiceID: o.InvoiceID, Status: domain.PaymentFailure, ModifiedAt: t0.Add(-time.Minute)})
	require.NoError(t, err)
	assert.False(t, res.Changed, "an older event arriving late is ignored")

	got, err := repo.Order(ctx, o.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.OrderPaid, got.Status)
	assert.Equal(t, 9, stockOf(t, oil.ID), "paid: the unit stays sold")
}

func TestApplyPaymentFindsOrderByReference(t *testing.T) {
	skipShort(t)
	const user = int64(5202)
	oil := withStock(t, "OIL-002", 10)
	buyerCart(t, user, oil.ID, 1)
	o := place(t, user) // no invoice id saved

	res, err := NewOrderRepository(testPool).ApplyPayment(context.Background(), domain.PaymentEvent{
		InvoiceID: "inv-lost", Reference: strconv.FormatInt(o.ID, 10), Status: domain.PaymentSuccess, Amount: o.Total, ModifiedAt: time.Now(),
	})
	require.NoError(t, err)
	assert.True(t, res.Changed)
	assert.Equal(t, domain.OrderPaid, res.Order.Status)
}

func TestFailedPaymentReleasesStockAndRefillsCart(t *testing.T) {
	skipShort(t)
	ctx := context.Background()
	const user = int64(5203)
	plugs := withStock(t, "ENG-002", 8)
	buyerCart(t, user, plugs.ID, 4)
	o := place(t, user)
	withInvoice(t, o, "inv-5203")
	require.Equal(t, 4, stockOf(t, plugs.ID))

	res, err := NewOrderRepository(testPool).ApplyPayment(ctx, domain.PaymentEvent{
		InvoiceID: o.InvoiceID, Status: domain.PaymentFailure, ModifiedAt: time.Now(),
	})
	require.NoError(t, err)
	assert.Equal(t, domain.OrderCancelled, res.Order.Status)
	assert.Equal(t, 8, stockOf(t, plugs.ID), "the units are back on sale")

	cart, err := NewCartRepository(testPool).Cart(ctx, user)
	require.NoError(t, err)
	require.Len(t, cart.Lines, 1, "and back in the cart, ready for another try")
	assert.Equal(t, 4, cart.Lines[0].Qty)
}

func TestWrongAmountNeedsRefund(t *testing.T) {
	skipShort(t)
	const user = int64(5204)
	oil := withStock(t, "OIL-003", 5)
	buyerCart(t, user, oil.ID, 1)
	o := place(t, user)
	withInvoice(t, o, "inv-5204")

	ev := success(o, time.Now())
	ev.Amount = o.Total - 100
	res, err := NewOrderRepository(testPool).ApplyPayment(context.Background(), ev)
	require.NoError(t, err)
	assert.Equal(t, domain.OrderNeedsRefund, res.Order.Status)
	assert.Equal(t, 5, stockOf(t, oil.ID), "an order waiting for a refund holds no stock")
}

func TestExpiryAndLatePayment(t *testing.T) {
	skipShort(t)
	ctx := context.Background()
	repo := NewOrderRepository(testPool)
	disc := withStock(t, "BRK-006", 1)

	const user = int64(5205)
	buyerCart(t, user, disc.ID, 1)
	o := place(t, user)
	withInvoice(t, o, "inv-5205")

	res, err := repo.Expire(ctx, o.ID)
	require.NoError(t, err)
	assert.True(t, res.Changed)
	assert.Equal(t, domain.OrderExpired, res.Order.Status)
	assert.Equal(t, 1, stockOf(t, disc.ID), "the unit is back on sale")

	res, err = repo.Expire(ctx, o.ID)
	require.NoError(t, err)
	assert.False(t, res.Changed, "expiring twice is harmless")

	// Paid at the last second, after the expiry ran: the unit is still
	// there, so the sale is kept.
	res, err = repo.ApplyPayment(ctx, success(o, time.Now()))
	require.NoError(t, err)
	assert.Equal(t, domain.OrderPaid, res.Order.Status)
	assert.Zero(t, stockOf(t, disc.ID))

	// The same, but someone else bought the unit in between.
	const other = int64(5206)
	_, err = testPool.Exec(ctx, `UPDATE products SET stock = 1 WHERE id = $1`, disc.ID)
	require.NoError(t, err)
	buyerCart(t, other, disc.ID, 1)
	o2 := place(t, other)
	withInvoice(t, o2, "inv-5206")
	_, err = repo.Expire(ctx, o2.ID)
	require.NoError(t, err)
	_, err = testPool.Exec(ctx, `UPDATE products SET stock = 0 WHERE id = $1`, disc.ID)
	require.NoError(t, err)

	res, err = repo.ApplyPayment(ctx, success(o2, time.Now()))
	require.NoError(t, err)
	assert.Equal(t, domain.OrderNeedsRefund, res.Order.Status, "paid for a unit that is gone: refund")
}

func TestManagerTransitions(t *testing.T) {
	skipShort(t)
	ctx := context.Background()
	repo := NewOrderRepository(testPool)
	const user = int64(5207)
	filter := withStock(t, "FLT-007", 3)
	buyerCart(t, user, filter.ID, 1)
	o := place(t, user)
	withInvoice(t, o, "inv-5207")

	_, err := repo.SetStatus(ctx, o.ID, domain.OrderShipped)
	require.ErrorIs(t, err, domain.ErrBadTransition, "nothing ships unpaid")

	_, err = repo.ApplyPayment(ctx, success(o, time.Now()))
	require.NoError(t, err)
	res, err := repo.SetStatus(ctx, o.ID, domain.OrderConfirmed)
	require.NoError(t, err)
	assert.True(t, res.Changed)
	res, err = repo.SetStatus(ctx, o.ID, domain.OrderConfirmed)
	require.NoError(t, err)
	assert.False(t, res.Changed, "a double tap")

	res, err = repo.SetStatus(ctx, o.ID, domain.OrderCancelled)
	require.NoError(t, err)
	assert.Equal(t, domain.OrderCancelled, res.Order.Status)
	assert.Equal(t, 3, stockOf(t, filter.ID), "a cancelled order gives its stock back")

	orders, err := repo.OrdersOf(ctx, user, 5)
	require.NoError(t, err)
	require.Len(t, orders, 1)
	assert.Len(t, orders[0].Items, 1)
}

// onOrder switches a product to "supplier delivers in days" for one test.
func onOrder(t *testing.T, sku string, days int) *domain.Product {
	t.Helper()
	ctx := context.Background()
	p := withStock(t, sku, 0)
	_, err := testPool.Exec(ctx, `UPDATE products SET lead_time_days = $2 WHERE id = $1`, p.ID, days)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `UPDATE products SET lead_time_days = NULL WHERE id = $1`, p.ID)
	})
	p.OnOrder, p.LeadDays = true, days
	return p
}

func TestOnOrderSellsWithoutStock(t *testing.T) {
	skipShort(t)
	ctx := context.Background()
	const user = int64(5401)
	shock := onOrder(t, "SUS-001", 3)
	pads := withStock(t, "BRK-002", 5)

	card := productBySKU(t, "SUS-001")
	assert.True(t, card.OnOrder)
	assert.Equal(t, 3, card.LeadDays)
	found, err := NewCatalogRepository(testPool).Search(ctx, service.SearchFilter{PartNumber: card.OEMNumbers[0], Limit: 5})
	require.NoError(t, err)
	assert.Contains(t, ids(found), shock.ID, "a part on order is for sale, though none is on the shelf")

	buyerCart(t, user, shock.ID, 2)
	require.NoError(t, NewCartRepository(testPool).SetQty(ctx, user, pads.ID, 1))
	cart, err := NewCartRepository(testPool).Cart(ctx, user)
	require.NoError(t, err)
	line, _ := cart.Line(shock.ID)
	assert.True(t, line.OnOrder)
	assert.False(t, line.Short(), "a line on order is never short")

	o := place(t, user)
	byID := map[int64]domain.OrderItem{}
	for _, it := range o.Items {
		byID[it.ProductID] = it
	}
	assert.False(t, byID[shock.ID].FromStock)
	assert.True(t, byID[pads.ID].FromStock)
	assert.Equal(t, 0, stockOf(t, shock.ID), "nothing is taken off a shelf the part is not on")
	assert.Equal(t, 4, stockOf(t, pads.ID))

	// The product goes back to the shelf while the order is open, as a
	// re-import may do. Cancelling must not give back units never taken.
	_, err = testPool.Exec(ctx, `UPDATE products SET lead_time_days = NULL WHERE id = $1`, shock.ID)
	require.NoError(t, err)
	_, err = NewOrderRepository(testPool).Expire(ctx, o.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, stockOf(t, shock.ID), "no stock is created out of nothing")
	assert.Equal(t, 5, stockOf(t, pads.ID))

	cart, err = NewCartRepository(testPool).Cart(ctx, user)
	require.NoError(t, err)
	assert.Len(t, cart.Lines, 2, "both lines are back in the cart")
}

func ids(ps []domain.Product) []int64 {
	out := make([]int64, len(ps))
	for i, p := range ps {
		out[i] = p.ID
	}
	return out
}
