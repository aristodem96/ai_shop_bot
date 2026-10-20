package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
)

// OrderRepository stores orders.
type OrderRepository interface {
	// PlaceOrder turns the cart into an order and takes its stock, or returns
	// the buyer's open order with created false.
	PlaceOrder(ctx context.Context, userID int64, c domain.Contacts, reservedUntil time.Time) (*domain.Order, bool, error)
	AttachInvoice(ctx context.Context, orderID int64, invoiceID, url string) error
	ApplyPayment(ctx context.Context, ev domain.PaymentEvent) (PaymentResult, error)
	Expire(ctx context.Context, orderID int64) (PaymentResult, error)
	// Cancel cancels an order still awaiting payment: its stock goes back
	// on sale and its items back into the cart.
	Cancel(ctx context.Context, orderID int64) (PaymentResult, error)
	SetStatus(ctx context.Context, orderID int64, to domain.OrderStatus) (PaymentResult, error)
	Order(ctx context.Context, id int64) (*domain.Order, error)
	OrdersOf(ctx context.Context, userID int64, limit int) ([]domain.Order, error)
	OpenOrders(ctx context.Context, limit int) ([]domain.Order, error)
	Customer(ctx context.Context, userID int64) (*domain.Contacts, error)
}

// PaymentResult is an order after a change, and whether its status moved.
type PaymentResult struct {
	Order   *domain.Order
	From    domain.OrderStatus
	Changed bool
}

// InvoiceRequest asks the payment provider for a payment page.
type InvoiceRequest struct {
	OrderID     int64
	Amount      domain.Money
	Destination string
	Validity    time.Duration
}

// Invoice is a payment page.
type Invoice struct {
	ID  string
	URL string
}

// PaymentProvider creates invoices and reports on them.
type PaymentProvider interface {
	CreateInvoice(ctx context.Context, req InvoiceRequest) (*Invoice, error)
	InvoiceStatus(ctx context.Context, invoiceID string) (*domain.PaymentEvent, error)
	// RemoveInvoice makes an unpaid invoice's page stop taking payments.
	RemoveInvoice(ctx context.Context, invoiceID string) error
}

// OrderNotifier is told about every order whose status moved: the buyer and
// the shop's managers hear about it there.
type OrderNotifier interface {
	OrderChanged(ctx context.Context, o *domain.Order, from domain.OrderStatus)
}

// OrderService places orders and follows them through payment.
type OrderService struct {
	repo     OrderRepository
	payments PaymentProvider
	notify   OrderNotifier
	log      *slog.Logger
	now      func() time.Time
}

// NewOrderService wires an OrderService.
func NewOrderService(repo OrderRepository, payments PaymentProvider, notify OrderNotifier, log *slog.Logger) *OrderService {
	return &OrderService{repo: repo, payments: payments, notify: notify, log: log, now: time.Now}
}

// Contact field limits. A name or an address longer than these is a paste
// gone wrong, not a delivery detail.
const (
	maxNameRunes     = 100
	maxDeliveryRunes = 200
)

// ValidateContacts normalises and checks contacts typed by a buyer.
func ValidateContacts(c domain.Contacts) (domain.Contacts, error) {
	c.Name, c.Delivery = strings.TrimSpace(c.Name), strings.TrimSpace(c.Delivery)
	phone, ok := domain.NormalizePhone(c.Phone)
	switch {
	case utf8.RuneCountInString(c.Name) < 2 || utf8.RuneCountInString(c.Name) > maxNameRunes:
		return c, fmt.Errorf("%w: name", domain.ErrInvalidContacts)
	case !ok:
		return c, fmt.Errorf("%w: phone", domain.ErrInvalidContacts)
	case utf8.RuneCountInString(c.Delivery) < 5 || utf8.RuneCountInString(c.Delivery) > maxDeliveryRunes:
		return c, fmt.Errorf("%w: delivery", domain.ErrInvalidContacts)
	}
	c.Phone = phone
	return c, nil
}

// Checkout places the cart as an order and returns it with its payment link.
//
// Order of work: the stock is reserved first, in Postgres, then the invoice is
// created. Reserving after the invoice would let two buyers both pay for the
// last unit. If the invoice cannot be created, the order is expired straight
// away so its stock goes back on sale.
func (s *OrderService) Checkout(ctx context.Context, userID int64, c domain.Contacts) (*domain.Order, error) {
	c, err := ValidateContacts(c)
	if err != nil {
		return nil, err
	}
	order, created, err := s.repo.PlaceOrder(ctx, userID, c, s.now().Add(domain.ReservationTTL))
	if err != nil {
		return nil, err
	}
	if !created && order.PaymentURL != "" {
		return order, nil // the buyer's open order, already payable
	}

	inv, err := s.payments.CreateInvoice(ctx, InvoiceRequest{
		OrderID:     order.ID,
		Amount:      order.Total,
		Destination: fmt.Sprintf("Замовлення №%d", order.ID),
		Validity:    time.Until(order.ReservedUntil),
	})
	if err != nil {
		if _, xerr := s.repo.Expire(context.WithoutCancel(ctx), order.ID); xerr != nil {
			s.log.ErrorContext(ctx, "could not release an order without an invoice",
				slog.Int64("order_id", order.ID), slog.Any("error", xerr))
		}
		return nil, fmt.Errorf("create invoice for order %d: %w", order.ID, err)
	}
	if err := s.repo.AttachInvoice(ctx, order.ID, inv.ID, inv.URL); err != nil {
		// The invoice exists; the webhook finds the order by reference.
		return nil, err
	}
	order.InvoiceID, order.PaymentURL = inv.ID, inv.URL
	return order, nil
}

// HandlePayment applies a payment event, from a webhook or a poll, and
// notifies when the order's status moved.
func (s *OrderService) HandlePayment(ctx context.Context, ev domain.PaymentEvent) error {
	res, err := s.repo.ApplyPayment(ctx, ev)
	if err != nil {
		return err
	}
	s.changed(ctx, res)
	return nil
}

// SetStatus is a manager's move from the admin chat.
func (s *OrderService) SetStatus(ctx context.Context, orderID int64, to domain.OrderStatus) (*domain.Order, error) {
	res, err := s.repo.SetStatus(ctx, orderID, to)
	if err != nil {
		return nil, err
	}
	s.changed(ctx, res)
	return res.Order, nil
}

func (s *OrderService) changed(ctx context.Context, res PaymentResult) {
	if res.Changed {
		s.notify.OrderChanged(ctx, res.Order, res.From)
	}
}

// reconcileBatch bounds one reconciliation pass.
const reconcileBatch = 50

// Reconcile asks the payment provider about every unpaid order and expires
// those whose reservation has lapsed.
//
// It is the backstop for webhooks: a lost one, or none at all when the bot
// runs without a public URL, as it does on a laptop. An order is only expired
// after its invoice was asked about, so a payment whose webhook went missing
// is found and kept instead of being cancelled.
func (s *OrderService) Reconcile(ctx context.Context) error {
	orders, err := s.repo.OpenOrders(ctx, reconcileBatch)
	if err != nil {
		return err
	}
	var errs []error
	for _, o := range orders {
		if o.InvoiceID != "" {
			ev, err := s.payments.InvoiceStatus(ctx, o.InvoiceID)
			if err != nil {
				errs = append(errs, fmt.Errorf("status of order %d: %w", o.ID, err))
				continue // do not expire what could not be checked
			}
			if ev.Reference == "" {
				ev.Reference = strconv.FormatInt(o.ID, 10)
			}
			if err := s.HandlePayment(ctx, *ev); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		if s.now().After(o.ReservedUntil) {
			res, err := s.repo.Expire(ctx, o.ID)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			s.changed(ctx, res)
		}
	}
	return errors.Join(errs...)
}

// CancelUnpaid is a buyer cancelling their order before paying. The
// invoice is asked about first: a payment that went through, its webhook
// not yet here, keeps the order, and the order comes back with its new
// status and ErrBadTransition. Then the invoice is invalidated, so its page
// takes no payment for a cancelled order -- and should that fail and the
// buyer pay anyway, the payment is handled like any late one.
func (s *OrderService) CancelUnpaid(ctx context.Context, userID, orderID int64) (*domain.Order, error) {
	o, err := s.repo.Order(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if o.UserID != userID {
		return nil, domain.ErrNotFound
	}
	if o.Status == domain.OrderAwaitingPayment && o.InvoiceID != "" {
		ev, err := s.payments.InvoiceStatus(ctx, o.InvoiceID)
		if err != nil {
			return nil, fmt.Errorf("status of order %d: %w", o.ID, err)
		}
		if ev.Reference == "" {
			ev.Reference = strconv.FormatInt(o.ID, 10)
		}
		if err := s.HandlePayment(ctx, *ev); err != nil {
			return nil, err
		}
		if o, err = s.repo.Order(ctx, orderID); err != nil {
			return nil, err
		}
	}
	if o.Status != domain.OrderAwaitingPayment {
		return o, fmt.Errorf("%w: %s -> %s", domain.ErrBadTransition, o.Status, domain.OrderCancelled)
	}
	if o.InvoiceID != "" {
		if err := s.payments.RemoveInvoice(ctx, o.InvoiceID); err != nil {
			s.log.WarnContext(ctx, "could not invalidate the invoice of a cancelled order", slog.Int64("order_id", o.ID), slog.Any("error", err))
		}
	}
	res, err := s.repo.Cancel(ctx, o.ID)
	if err != nil {
		return nil, err
	}
	return res.Order, nil
}

// Order returns one order.
func (s *OrderService) Order(ctx context.Context, id int64) (*domain.Order, error) {
	return s.repo.Order(ctx, id)
}

// OrdersOf returns a buyer's latest orders.
func (s *OrderService) OrdersOf(ctx context.Context, userID int64, limit int) ([]domain.Order, error) {
	return s.repo.OrdersOf(ctx, userID, limit)
}

// Customer returns the contacts a buyer used last, or nil.
func (s *OrderService) Customer(ctx context.Context, userID int64) (*domain.Contacts, error) {
	return s.repo.Customer(ctx, userID)
}
