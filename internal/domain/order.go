package domain

import (
	"errors"
	"slices"
	"strings"
	"time"
	"unicode"
)

// OrderStatus is where an order is in its life.
type OrderStatus string

// Order statuses. The happy path is awaiting_payment -> paid -> confirmed ->
// shipped; an order left unpaid expires, and one can be cancelled until it
// ships.
const (
	// OrderAwaitingPayment holds the stock while the buyer pays.
	OrderAwaitingPayment OrderStatus = "awaiting_payment"
	OrderPaid            OrderStatus = "paid"
	OrderConfirmed       OrderStatus = "confirmed"
	OrderShipped         OrderStatus = "shipped"
	OrderCancelled       OrderStatus = "cancelled"
	// OrderExpired was never paid; its stock went back on sale.
	OrderExpired OrderStatus = "expired"
	// OrderNeedsRefund was paid after its reservation lapsed and the stock
	// had meanwhile been sold: the money has to go back.
	OrderNeedsRefund OrderStatus = "needs_refund"
)

// ReservationTTL is how long an unpaid order holds its stock. The payment
// page lives exactly as long, so a payment cannot start after the hold ends.
const ReservationTTL = 15 * time.Minute

// adminTransitions are the moves a manager may make from the admin chat.
var adminTransitions = map[OrderStatus][]OrderStatus{
	OrderPaid:      {OrderConfirmed, OrderCancelled},
	OrderConfirmed: {OrderShipped, OrderCancelled},
}

// CanMoveTo reports whether a manager may move an order from s to next.
func (s OrderStatus) CanMoveTo(next OrderStatus) bool {
	return slices.Contains(adminTransitions[s], next)
}

// HoldsStock reports whether the order's units are counted out of stock.
// Expired, cancelled and to-be-refunded orders have given theirs back.
func (s OrderStatus) HoldsStock() bool {
	switch s {
	case OrderAwaitingPayment, OrderPaid, OrderConfirmed, OrderShipped:
		return true
	}
	return false
}

// Contacts are where and to whom an order goes.
type Contacts struct {
	Name  string
	Phone string // normalised, +380XXXXXXXXX
	// Delivery is the city and Nova Poshta branch as the buyer wrote it.
	Delivery string
}

// Order is a placed order.
type Order struct {
	ID       int64
	UserID   int64
	Status   OrderStatus
	Items    []OrderItem
	Total    Money
	Contacts Contacts

	InvoiceID     string
	PaymentURL    string
	ReservedUntil time.Time
	CreatedAt     time.Time
	PaidAt        time.Time
}

// OrderItem is a line of an order, with the price frozen at the moment it was
// placed: what the buyer agreed to pay, whatever the catalogue says later.
type OrderItem struct {
	ProductID int64
	Name      string
	Brand     string
	Price     Money
	Qty       int
}

// Subtotal is the line's price times its quantity.
func (i OrderItem) Subtotal() Money { return i.Price * Money(i.Qty) }

// Errors of ordering.
var (
	ErrCartEmpty = errors.New("cart is empty")
	// ErrBadTransition means the order is not in a state that allows the move.
	ErrBadTransition = errors.New("order status does not allow this")
	// ErrInvalidContacts means a contact field failed validation.
	ErrInvalidContacts = errors.New("invalid contacts")
)

// NormalizePhone reads a Ukrainian mobile number in the usual spellings --
// "+380 67 123 45 67", "067-123-45-67", "380671234567" -- and returns it as
// +380XXXXXXXXX, or false if it is not one.
func NormalizePhone(s string) (string, bool) {
	var digits strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsDigit(r):
			digits.WriteRune(r)
		case r == '+' || r == ' ' || r == '-' || r == '(' || r == ')':
		default:
			return "", false
		}
	}
	d := digits.String()
	switch {
	case len(d) == 12 && strings.HasPrefix(d, "380"):
	case len(d) == 10 && strings.HasPrefix(d, "0"):
		d = "38" + d
	default:
		return "", false
	}
	return "+" + d, true
}

// PaymentEvent is a payment provider's word on an invoice, from a webhook or
// a status poll; both are handled the same way.
type PaymentEvent struct {
	InvoiceID string
	// Reference is the order id the invoice was created for, a fallback
	// for finding the order.
	Reference string
	Status    PaymentStatus
	Amount    Money
	// ModifiedAt orders events: only one newer than the last applied counts.
	ModifiedAt time.Time
}

// PaymentStatus is a payment provider's invoice status.
type PaymentStatus string

// Invoice statuses as Monobank names them. Only the final ones change an
// order; the rest are progress.
const (
	PaymentCreated    PaymentStatus = "created"
	PaymentProcessing PaymentStatus = "processing"
	PaymentHold       PaymentStatus = "hold"
	PaymentSuccess    PaymentStatus = "success"
	PaymentFailure    PaymentStatus = "failure"
	PaymentReversed   PaymentStatus = "reversed"
	PaymentExpired    PaymentStatus = "expired"
)
