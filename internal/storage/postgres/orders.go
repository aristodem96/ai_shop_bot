package postgres

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/service"
)

var _ service.OrderRepository = (*OrderRepository)(nil)

// OrderRepository stores orders and moves stock with them.
//
// Every method that changes an order locks its row first (FOR UPDATE), and
// every method that moves stock locks the product rows in id order. The first
// makes a webhook, a status poll and the expiry job safe to run at the same
// moment on the same order; the second keeps two checkouts that share
// products from deadlocking each other.
type OrderRepository struct {
	pool *pgxpool.Pool
}

// NewOrderRepository returns a repository backed by pool.
func NewOrderRepository(pool *pgxpool.Pool) *OrderRepository {
	return &OrderRepository{pool: pool}
}

const orderColumns = `o.id, o.user_id, o.status, o.total, o.customer_name, o.phone, o.delivery,
	coalesce(o.invoice_id, ''), coalesce(o.payment_url, ''), o.reserved_until, o.created_at,
	coalesce(o.paid_at, 'epoch'::timestamptz)`

func scanOrder(row pgx.Row) (*domain.Order, error) {
	var (
		o      domain.Order
		status string
		total  int64
	)
	err := row.Scan(&o.ID, &o.UserID, &status, &total, &o.Contacts.Name, &o.Contacts.Phone,
		&o.Contacts.Delivery, &o.InvoiceID, &o.PaymentURL, &o.ReservedUntil, &o.CreatedAt, &o.PaidAt)
	if err != nil {
		return nil, err
	}
	o.Status, o.Total = domain.OrderStatus(status), domain.Money(total)
	if o.PaidAt.Equal(time.Unix(0, 0)) {
		o.PaidAt = time.Time{}
	}
	return &o, nil
}

func (r *OrderRepository) loadItems(ctx context.Context, q pgx.Tx, o *domain.Order) error {
	var rows pgx.Rows
	var err error
	sql := `SELECT product_id, name, brand, price, qty FROM order_items WHERE order_id = $1 ORDER BY product_id`
	if q != nil {
		rows, err = q.Query(ctx, sql, o.ID)
	} else {
		rows, err = r.pool.Query(ctx, sql, o.ID)
	}
	if err != nil {
		return fmt.Errorf("postgres: list items of order %d: %w", o.ID, err)
	}
	o.Items, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.OrderItem, error) {
		var (
			i     domain.OrderItem
			price int64
		)
		err := row.Scan(&i.ProductID, &i.Name, &i.Brand, &price, &i.Qty)
		i.Price = domain.Money(price)
		return i, err
	})
	if err != nil {
		return fmt.Errorf("postgres: scan order items: %w", err)
	}
	return nil
}

// PlaceOrder turns the buyer's cart into an order and takes its units out of
// stock, all in one transaction. It is the guarantee that a unit is sold
// once: the product rows are locked, the stock is checked under the lock and
// decremented before the lock is released, so of two buyers racing for the
// last unit exactly one gets it -- the other sees ErrOutOfStock.
//
// A buyer who already has an unpaid order gets that order back, created
// false, instead of a second reservation.
func (r *OrderRepository) PlaceOrder(ctx context.Context, userID int64, c domain.Contacts, reservedUntil time.Time) (*domain.Order, bool, error) {
	var (
		order   *domain.Order
		created bool
	)
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		existing, err := scanOrder(tx.QueryRow(ctx, `
			SELECT `+orderColumns+` FROM orders o
			WHERE o.user_id = $1 AND o.status = 'awaiting_payment'
			FOR UPDATE`, userID))
		if err == nil {
			order = existing
			return r.loadItems(ctx, tx, order)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("postgres: find open order: %w", err)
		}

		// Lock the products in the cart, in id order.
		rows, err := tx.Query(ctx, `
			SELECT p.id, p.name, p.brand, p.price, ci.qty, p.stock, p.is_active
			FROM cart_items ci
			JOIN products p ON p.id = ci.product_id
			WHERE ci.user_id = $1
			ORDER BY p.id
			FOR UPDATE OF p`, userID)
		if err != nil {
			return fmt.Errorf("postgres: lock cart products: %w", err)
		}
		type line struct {
			item   domain.OrderItem
			stock  int
			active bool
		}
		lines, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (line, error) {
			var (
				l     line
				price int64
			)
			err := row.Scan(&l.item.ProductID, &l.item.Name, &l.item.Brand, &price, &l.item.Qty, &l.stock, &l.active)
			l.item.Price = domain.Money(price)
			return l, err
		})
		if err != nil {
			return fmt.Errorf("postgres: scan cart products: %w", err)
		}
		if len(lines) == 0 {
			return domain.ErrCartEmpty
		}

		o := &domain.Order{UserID: userID, Status: domain.OrderAwaitingPayment, Contacts: c, ReservedUntil: reservedUntil}
		ids := make([]int64, len(lines))
		qtys := make([]int32, len(lines))
		for i, l := range lines {
			if !l.active || l.item.Qty > l.stock {
				return fmt.Errorf("%w: %s (%s)", domain.ErrOutOfStock, l.item.Name, l.item.Brand)
			}
			o.Items = append(o.Items, l.item)
			o.Total += l.item.Subtotal()
			ids[i], qtys[i] = l.item.ProductID, int32(l.item.Qty) //nolint:gosec // qty is capped at MaxQtyPerLine
		}

		order, err = scanOrder(tx.QueryRow(ctx, `
			INSERT INTO orders (user_id, status, total, customer_name, phone, delivery, reserved_until)
			VALUES ($1, 'awaiting_payment', $2, $3, $4, $5, $6)
			RETURNING id, user_id, status, total, customer_name, phone, delivery,
				'', '', reserved_until, created_at, 'epoch'::timestamptz`,
			userID, int64(o.Total), c.Name, c.Phone, c.Delivery, reservedUntil))
		if err != nil {
			return fmt.Errorf("postgres: insert order: %w", err)
		}
		order.Items = o.Items

		if _, err := tx.Exec(ctx, `
			INSERT INTO order_items (order_id, product_id, name, brand, price, qty)
			SELECT $1, c.product_id, p.name, p.brand, p.price, c.qty
			FROM cart_items c JOIN products p ON p.id = c.product_id
			WHERE c.user_id = $2`, order.ID, userID); err != nil {
			return fmt.Errorf("postgres: insert order items: %w", err)
		}
		if err := moveStock(ctx, tx, ids, qtys, -1); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM carts WHERE user_id = $1`, userID); err != nil {
			return fmt.Errorf("postgres: clear cart: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO customers (user_id, name, phone, delivery) VALUES ($1, $2, $3, $4)
			ON CONFLICT (user_id) DO UPDATE SET name = EXCLUDED.name, phone = EXCLUDED.phone,
				delivery = EXCLUDED.delivery, updated_at = now()`,
			userID, c.Name, c.Phone, c.Delivery); err != nil {
			return fmt.Errorf("postgres: save contacts: %w", err)
		}
		created = true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return order, created, nil
}

// moveStock adds sign*qty to each product's stock. The CHECK (stock >= 0) on
// products backs the check made under the lock: a bug here fails loudly
// instead of selling a unit twice.
func moveStock(ctx context.Context, tx pgx.Tx, ids []int64, qtys []int32, sign int32) error {
	_, err := tx.Exec(ctx, `
		UPDATE products p SET stock = p.stock + $3 * x.qty, updated_at = now()
		FROM unnest($1::bigint[], $2::int[]) AS x(id, qty)
		WHERE p.id = x.id`, ids, qtys, sign)
	if err != nil {
		return fmt.Errorf("postgres: move stock: %w", err)
	}
	return nil
}

// releaseStock gives an order's units back and, for an order that never got
// paid, puts them back in the buyer's cart: a buyer whose payment failed
// should be able to try again without filling the cart from scratch.
func releaseStock(ctx context.Context, tx pgx.Tx, o *domain.Order, refillCart bool) error {
	ids := make([]int64, len(o.Items))
	qtys := make([]int32, len(o.Items))
	for i, it := range o.Items {
		ids[i], qtys[i] = it.ProductID, int32(it.Qty) //nolint:gosec // qty is capped at MaxQtyPerLine
	}
	// Lock in id order, like PlaceOrder, before touching stock.
	if _, err := tx.Exec(ctx, `SELECT 1 FROM products WHERE id = ANY($1) ORDER BY id FOR UPDATE`, ids); err != nil {
		return fmt.Errorf("postgres: lock products: %w", err)
	}
	if err := moveStock(ctx, tx, ids, qtys, 1); err != nil {
		return err
	}
	if !refillCart {
		return nil
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO carts (user_id) VALUES ($1)
		ON CONFLICT (user_id) DO UPDATE SET updated_at = now(), reminded_at = NULL`, o.UserID); err != nil {
		return fmt.Errorf("postgres: restore cart: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO cart_items (user_id, product_id, qty)
		SELECT $1, x.id, x.qty FROM unnest($2::bigint[], $3::int[]) AS x(id, qty)
		ON CONFLICT (user_id, product_id) DO UPDATE SET qty = cart_items.qty + EXCLUDED.qty`,
		o.UserID, ids, qtys); err != nil {
		return fmt.Errorf("postgres: restore cart lines: %w", err)
	}
	return nil
}

// takeStock re-reserves a lapsed order's units, if they are all still there.
func takeStock(ctx context.Context, tx pgx.Tx, o *domain.Order) (bool, error) {
	ids := make([]int64, len(o.Items))
	qtys := make([]int32, len(o.Items))
	for i, it := range o.Items {
		ids[i], qtys[i] = it.ProductID, int32(it.Qty) //nolint:gosec // qty is capped at MaxQtyPerLine
	}
	var short int
	err := tx.QueryRow(ctx, `
		WITH locked AS (SELECT id, stock FROM products WHERE id = ANY($1) ORDER BY id FOR UPDATE)
		SELECT count(*) FROM unnest($1::bigint[], $2::int[]) AS x(id, qty)
		JOIN locked l ON l.id = x.id
		WHERE l.stock < x.qty`, ids, qtys).Scan(&short)
	if err != nil {
		return false, fmt.Errorf("postgres: check stock: %w", err)
	}
	if short > 0 {
		return false, nil
	}
	return true, moveStock(ctx, tx, ids, qtys, -1)
}

// AttachInvoice records the payment provider's invoice on an order.
func (r *OrderRepository) AttachInvoice(ctx context.Context, orderID int64, invoiceID, url string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE orders SET invoice_id = $2, payment_url = $3, updated_at = now() WHERE id = $1`,
		orderID, invoiceID, url)
	if err != nil {
		return fmt.Errorf("postgres: attach invoice to order %d: %w", orderID, err)
	}
	return nil
}

// lockOrder reads an order with its items under a row lock.
func (r *OrderRepository) lockOrder(ctx context.Context, tx pgx.Tx, where string, arg any) (*domain.Order, error) {
	o, err := scanOrder(tx.QueryRow(ctx, `SELECT `+orderColumns+` FROM orders o WHERE `+where+` FOR UPDATE`, arg))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: lock order: %w", err)
	}
	return o, r.loadItems(ctx, tx, o)
}

func setStatus(ctx context.Context, tx pgx.Tx, o *domain.Order, to domain.OrderStatus) error {
	_, err := tx.Exec(ctx, `
		UPDATE orders SET status = $2, updated_at = now(),
			paid_at = CASE WHEN $2 = 'paid' THEN now() ELSE paid_at END
		WHERE id = $1`, o.ID, string(to))
	if err != nil {
		return fmt.Errorf("postgres: set status of order %d: %w", o.ID, err)
	}
	if to == domain.OrderPaid {
		o.PaidAt = time.Now()
	}
	o.Status = to
	return nil
}

// ApplyPayment applies a payment event to its order, idempotently.
//
// An event no newer than the last one applied changes nothing, so a webhook
// delivered twice, a webhook racing the status poll, or a "processing" that
// arrives after "success" are all harmless. Changed is true only when the
// order's status moved, which is what callers notify on: a duplicate event
// never produces a duplicate message.
func (r *OrderRepository) ApplyPayment(ctx context.Context, ev domain.PaymentEvent) (service.PaymentResult, error) {
	var res service.PaymentResult
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		o, err := r.lockOrder(ctx, tx, "o.invoice_id = $1", ev.InvoiceID)
		if errors.Is(err, domain.ErrNotFound) && ev.Reference != "" {
			// The invoice was created but its id never got saved (a crash
			// between the two): the reference still names the order.
			if id, perr := strconv.ParseInt(ev.Reference, 10, 64); perr == nil {
				o, err = r.lockOrder(ctx, tx, "o.id = $1", id)
			}
		}
		if err != nil {
			return err
		}
		res.Order, res.From = o, o.Status

		var last *time.Time
		if err := tx.QueryRow(ctx, `SELECT payment_modified_at FROM orders WHERE id = $1`, o.ID).Scan(&last); err != nil {
			return fmt.Errorf("postgres: read last payment event: %w", err)
		}
		if last != nil && !ev.ModifiedAt.After(*last) {
			return nil // stale or duplicate
		}
		if _, err := tx.Exec(ctx, `
			UPDATE orders SET payment_modified_at = $2,
				invoice_id = coalesce(invoice_id, $3), updated_at = now()
			WHERE id = $1`, o.ID, ev.ModifiedAt, ev.InvoiceID); err != nil {
			return fmt.Errorf("postgres: record payment event: %w", err)
		}

		to, err := paymentTransition(ctx, tx, o, ev)
		if err != nil || to == "" {
			return err
		}
		res.Changed = true
		return setStatus(ctx, tx, o, to)
	})
	return res, err
}

// paymentTransition decides what a payment event does to an order, moving
// stock where that is part of it. An empty status means no change.
func paymentTransition(ctx context.Context, tx pgx.Tx, o *domain.Order, ev domain.PaymentEvent) (domain.OrderStatus, error) {
	switch ev.Status {
	case domain.PaymentSuccess:
		unpaid := o.Status == domain.OrderAwaitingPayment || o.Status == domain.OrderExpired || o.Status == domain.OrderCancelled
		if !unpaid {
			return "", nil
		}
		if ev.Amount != o.Total {
			// Money that does not match the order is a person's job, not a
			// guess: the order stops holding stock and waits for a refund.
			if o.Status == domain.OrderAwaitingPayment {
				if err := releaseStock(ctx, tx, o, false); err != nil {
					return "", err
				}
			}
			return domain.OrderNeedsRefund, nil
		}
		switch o.Status {
		case domain.OrderAwaitingPayment:
			return domain.OrderPaid, nil
		case domain.OrderExpired, domain.OrderCancelled:
			// Paid after the reservation lapsed: keep the sale if the stock
			// is still there, otherwise the money goes back.
			ok, err := takeStock(ctx, tx, o)
			if err != nil {
				return "", err
			}
			if ok {
				return domain.OrderPaid, nil
			}
			return domain.OrderNeedsRefund, nil
		}
	case domain.PaymentFailure, domain.PaymentExpired:
		if o.Status == domain.OrderAwaitingPayment {
			return domain.OrderCancelled, releaseStock(ctx, tx, o, true)
		}
	case domain.PaymentReversed:
		if o.Status == domain.OrderPaid || o.Status == domain.OrderConfirmed {
			return domain.OrderCancelled, releaseStock(ctx, tx, o, false)
		}
	}
	return "", nil
}

// Expire ends an unpaid order whose reservation has lapsed, giving its stock
// back and its items back to the cart. It does nothing to an order that got
// paid in the meantime.
func (r *OrderRepository) Expire(ctx context.Context, orderID int64) (service.PaymentResult, error) {
	var res service.PaymentResult
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		o, err := r.lockOrder(ctx, tx, "o.id = $1", orderID)
		if err != nil {
			return err
		}
		res.Order, res.From = o, o.Status
		if o.Status != domain.OrderAwaitingPayment {
			return nil
		}
		if err := releaseStock(ctx, tx, o, true); err != nil {
			return err
		}
		res.Changed = true
		return setStatus(ctx, tx, o, domain.OrderExpired)
	})
	return res, err
}

// SetStatus makes a manager's move. Cancelling an order gives its stock back.
func (r *OrderRepository) SetStatus(ctx context.Context, orderID int64, to domain.OrderStatus) (service.PaymentResult, error) {
	var res service.PaymentResult
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		o, err := r.lockOrder(ctx, tx, "o.id = $1", orderID)
		if err != nil {
			return err
		}
		res.Order, res.From = o, o.Status
		if o.Status == to {
			return nil // a double tap
		}
		if !o.Status.CanMoveTo(to) {
			return fmt.Errorf("%w: %s -> %s", domain.ErrBadTransition, o.Status, to)
		}
		if to == domain.OrderCancelled && o.Status.HoldsStock() {
			if err := releaseStock(ctx, tx, o, false); err != nil {
				return err
			}
		}
		res.Changed = true
		return setStatus(ctx, tx, o, to)
	})
	return res, err
}

// Order returns an order with its items.
func (r *OrderRepository) Order(ctx context.Context, id int64) (*domain.Order, error) {
	o, err := scanOrder(r.pool.QueryRow(ctx, `SELECT `+orderColumns+` FROM orders o WHERE o.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: get order %d: %w", id, err)
	}
	return o, r.loadItems(ctx, nil, o)
}

// OrdersOf returns a buyer's latest orders with their items.
func (r *OrderRepository) OrdersOf(ctx context.Context, userID int64, limit int) ([]domain.Order, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+orderColumns+` FROM orders o
		WHERE o.user_id = $1 ORDER BY o.created_at DESC, o.id DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: list orders of %d: %w", userID, err)
	}
	orders, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.Order, error) {
		o, err := scanOrder(row)
		if err != nil {
			return domain.Order{}, err
		}
		return *o, nil
	})
	if err != nil {
		return nil, fmt.Errorf("postgres: scan orders: %w", err)
	}
	for i := range orders {
		if err := r.loadItems(ctx, nil, &orders[i]); err != nil {
			return nil, err
		}
	}
	return orders, nil
}

// OpenOrders returns unpaid orders with an invoice, oldest first: the ones
// whose payment status is worth asking about.
func (r *OrderRepository) OpenOrders(ctx context.Context, limit int) ([]domain.Order, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+orderColumns+` FROM orders o
		WHERE o.status = 'awaiting_payment'
		ORDER BY o.reserved_until LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: list open orders: %w", err)
	}
	orders, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.Order, error) {
		o, err := scanOrder(row)
		if err != nil {
			return domain.Order{}, err
		}
		return *o, nil
	})
	if err != nil {
		return nil, fmt.Errorf("postgres: scan open orders: %w", err)
	}
	return orders, nil
}

// Customer returns the contacts a buyer used last, or nil.
func (r *OrderRepository) Customer(ctx context.Context, userID int64) (*domain.Contacts, error) {
	var c domain.Contacts
	err := r.pool.QueryRow(ctx, `SELECT name, phone, delivery FROM customers WHERE user_id = $1`, userID).
		Scan(&c.Name, &c.Phone, &c.Delivery)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: get contacts of %d: %w", userID, err)
	}
	return &c, nil
}
