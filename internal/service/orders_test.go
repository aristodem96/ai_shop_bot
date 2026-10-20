package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
)

// fakeOrders keeps orders in memory and implements the status rules the
// tests need; the real ones are tested against Postgres.
type fakeOrders struct {
	orders  map[int64]*domain.Order
	nextID  int64
	expired []int64
}

func newFakeOrders() *fakeOrders { return &fakeOrders{orders: map[int64]*domain.Order{}} }

func (f *fakeOrders) PlaceOrder(_ context.Context, userID int64, c domain.Contacts, until time.Time) (*domain.Order, bool, error) {
	for _, o := range f.orders {
		if o.UserID == userID && o.Status == domain.OrderAwaitingPayment {
			return o, false, nil
		}
	}
	f.nextID++
	o := &domain.Order{ID: f.nextID, UserID: userID, Status: domain.OrderAwaitingPayment, Contacts: c,
		Total: domain.UAH(1450), ReservedUntil: until}
	f.orders[o.ID] = o
	return o, true, nil
}

func (f *fakeOrders) AttachInvoice(_ context.Context, id int64, inv, url string) error {
	f.orders[id].InvoiceID, f.orders[id].PaymentURL = inv, url
	return nil
}

func (f *fakeOrders) ApplyPayment(_ context.Context, ev domain.PaymentEvent) (PaymentResult, error) {
	for _, o := range f.orders {
		if o.InvoiceID == ev.InvoiceID {
			from := o.Status
			if ev.Status == domain.PaymentSuccess && o.Status == domain.OrderAwaitingPayment {
				o.Status = domain.OrderPaid
			}
			return PaymentResult{Order: o, From: from, Changed: o.Status != from}, nil
		}
	}
	return PaymentResult{}, domain.ErrNotFound
}

func (f *fakeOrders) Expire(_ context.Context, id int64) (PaymentResult, error) {
	o := f.orders[id]
	from := o.Status
	if o.Status == domain.OrderAwaitingPayment {
		o.Status = domain.OrderExpired
		f.expired = append(f.expired, id)
	}
	return PaymentResult{Order: o, From: from, Changed: o.Status != from}, nil
}

func (f *fakeOrders) Cancel(_ context.Context, id int64) (PaymentResult, error) {
	o := f.orders[id]
	from := o.Status
	if o.Status == domain.OrderAwaitingPayment {
		o.Status = domain.OrderCancelled
	}
	return PaymentResult{Order: o, From: from, Changed: o.Status != from}, nil
}

func (f *fakeOrders) SetStatus(context.Context, int64, domain.OrderStatus) (PaymentResult, error) {
	return PaymentResult{}, nil
}

func (f *fakeOrders) Order(_ context.Context, id int64) (*domain.Order, error) {
	return f.orders[id], nil
}

func (f *fakeOrders) OrdersOf(context.Context, int64, int) ([]domain.Order, error) { return nil, nil }

func (f *fakeOrders) OpenOrders(context.Context, int) ([]domain.Order, error) {
	var out []domain.Order
	for _, o := range f.orders {
		if o.Status == domain.OrderAwaitingPayment {
			out = append(out, *o)
		}
	}
	return out, nil
}

func (f *fakeOrders) Customer(context.Context, int64) (*domain.Contacts, error) { return nil, nil }

type fakePayments struct {
	createErr error
	statusErr error
	status    domain.PaymentStatus
	requests  []InvoiceRequest
	removed   []string
}

func (f *fakePayments) RemoveInvoice(_ context.Context, id string) error {
	f.removed = append(f.removed, id)
	return nil
}

func (f *fakePayments) CreateInvoice(_ context.Context, req InvoiceRequest) (*Invoice, error) {
	f.requests = append(f.requests, req)
	if f.createErr != nil {
		return nil, f.createErr
	}
	return &Invoice{ID: "inv", URL: "https://pay.example/inv"}, nil
}

func (f *fakePayments) InvoiceStatus(_ context.Context, id string) (*domain.PaymentEvent, error) {
	if f.statusErr != nil {
		return nil, f.statusErr
	}
	return &domain.PaymentEvent{InvoiceID: id, Status: f.status, Amount: domain.UAH(1450), ModifiedAt: time.Now()}, nil
}

type fakeNotifier struct{ changes []domain.OrderStatus }

func (f *fakeNotifier) OrderChanged(_ context.Context, o *domain.Order, _ domain.OrderStatus) {
	f.changes = append(f.changes, o.Status)
}

var contacts = domain.Contacts{Name: "Тарас", Phone: "067 123 45 67", Delivery: "Київ, відділення 25"}

func newOrderFixture() (*OrderService, *fakeOrders, *fakePayments, *fakeNotifier) {
	repo, pay, note := newFakeOrders(), &fakePayments{status: domain.PaymentCreated}, &fakeNotifier{}
	return NewOrderService(repo, pay, note, slog.New(slog.NewTextHandler(io.Discard, nil))), repo, pay, note
}

func TestCheckout(t *testing.T) {
	svc, _, pay, _ := newOrderFixture()

	o, err := svc.Checkout(context.Background(), user, contacts)
	require.NoError(t, err)
	assert.Equal(t, "https://pay.example/inv", o.PaymentURL)
	assert.Equal(t, "+380671234567", o.Contacts.Phone, "the phone is normalised")

	require.Len(t, pay.requests, 1)
	req := pay.requests[0]
	assert.Equal(t, o.Total, req.Amount, "the invoice asks for exactly the order total")
	assert.InDelta(t, domain.ReservationTTL.Seconds(), req.Validity.Seconds(), 5,
		"the payment page lives as long as the reservation")

	again, err := svc.Checkout(context.Background(), user, contacts)
	require.NoError(t, err)
	assert.Equal(t, o.ID, again.ID)
	assert.Len(t, pay.requests, 1, "the open order is reused, not invoiced twice")
}

func TestCheckoutRejectsBadContacts(t *testing.T) {
	svc, _, pay, _ := newOrderFixture()
	bad := contacts
	bad.Phone = "12345"

	_, err := svc.Checkout(context.Background(), user, bad)
	require.ErrorIs(t, err, domain.ErrInvalidContacts)
	assert.Empty(t, pay.requests)
}

func TestCheckoutReleasesStockWhenInvoiceFails(t *testing.T) {
	svc, repo, pay, _ := newOrderFixture()
	pay.createErr = errors.New("monobank: HTTP 500")

	_, err := svc.Checkout(context.Background(), user, contacts)
	require.Error(t, err)
	assert.Len(t, repo.expired, 1, "an order that cannot be paid gives its stock back at once")
}

func TestHandlePaymentNotifiesOnce(t *testing.T) {
	svc, _, _, note := newOrderFixture()
	o, err := svc.Checkout(context.Background(), user, contacts)
	require.NoError(t, err)

	ev := domain.PaymentEvent{InvoiceID: o.InvoiceID, Status: domain.PaymentSuccess, ModifiedAt: time.Now()}
	require.NoError(t, svc.HandlePayment(context.Background(), ev))
	require.NoError(t, svc.HandlePayment(context.Background(), ev))
	assert.Equal(t, []domain.OrderStatus{domain.OrderPaid}, note.changes, "a repeated webhook sends no second message")
}

func TestReconcileFindsPaymentBeforeExpiring(t *testing.T) {
	svc, repo, pay, note := newOrderFixture()
	o, err := svc.Checkout(context.Background(), user, contacts)
	require.NoError(t, err)
	svc.now = func() time.Time { return o.ReservedUntil.Add(time.Minute) }
	pay.status = domain.PaymentSuccess // the webhook got lost

	require.NoError(t, svc.Reconcile(context.Background()))
	assert.Equal(t, domain.OrderPaid, repo.orders[o.ID].Status, "kept, not expired")
	assert.Empty(t, repo.expired)
	assert.Equal(t, []domain.OrderStatus{domain.OrderPaid}, note.changes)
}

func TestReconcileExpiresUnpaid(t *testing.T) {
	svc, repo, _, note := newOrderFixture()
	o, err := svc.Checkout(context.Background(), user, contacts)
	require.NoError(t, err)

	require.NoError(t, svc.Reconcile(context.Background()))
	assert.Empty(t, repo.expired, "still within the reservation")

	svc.now = func() time.Time { return o.ReservedUntil.Add(time.Minute) }
	require.NoError(t, svc.Reconcile(context.Background()))
	assert.Equal(t, []int64{o.ID}, repo.expired)
	assert.Equal(t, []domain.OrderStatus{domain.OrderExpired}, note.changes)
}

func TestReconcileDoesNotExpireWhatItCannotCheck(t *testing.T) {
	svc, repo, pay, _ := newOrderFixture()
	o, err := svc.Checkout(context.Background(), user, contacts)
	require.NoError(t, err)
	svc.now = func() time.Time { return o.ReservedUntil.Add(time.Minute) }
	pay.statusErr = errors.New("monobank: timeout")

	require.Error(t, svc.Reconcile(context.Background()))
	assert.Empty(t, repo.expired, "a payment may have gone through; try again next pass")
}

func TestValidateContacts(t *testing.T) {
	_, err := ValidateContacts(domain.Contacts{Name: "Я", Phone: contacts.Phone, Delivery: contacts.Delivery})
	require.ErrorIs(t, err, domain.ErrInvalidContacts)
	_, err = ValidateContacts(domain.Contacts{Name: "Тарас", Phone: contacts.Phone, Delivery: "Київ"})
	require.ErrorIs(t, err, domain.ErrInvalidContacts)
}

func TestBuyerCancelsAnUnpaidOrder(t *testing.T) {
	svc, repo, pay, _ := newOrderFixture()
	ctx := context.Background()
	o, err := svc.Checkout(ctx, user, contacts)
	require.NoError(t, err)

	_, err = svc.CancelUnpaid(ctx, user+1, o.ID)
	require.ErrorIs(t, err, domain.ErrNotFound, "only the buyer's own order")

	got, err := svc.CancelUnpaid(ctx, user, o.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.OrderCancelled, got.Status)
	assert.Equal(t, []string{"inv"}, pay.removed, "the payment page stops taking payments")
	assert.Equal(t, domain.OrderCancelled, repo.orders[o.ID].Status)
}

func TestCancelKeepsAnOrderJustPaid(t *testing.T) {
	svc, _, pay, _ := newOrderFixture()
	ctx := context.Background()
	o, err := svc.Checkout(ctx, user, contacts)
	require.NoError(t, err)
	pay.status = domain.PaymentSuccess // paid, the webhook not here yet

	got, err := svc.CancelUnpaid(ctx, user, o.ID)
	require.ErrorIs(t, err, domain.ErrBadTransition)
	assert.Equal(t, domain.OrderPaid, got.Status, "the payment wins")
	assert.Empty(t, pay.removed)
}
