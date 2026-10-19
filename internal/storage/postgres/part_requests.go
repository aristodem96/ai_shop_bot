package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
)

// PartRequestRepository stores buyers' requests for parts on order.
type PartRequestRepository struct {
	pool *pgxpool.Pool
}

// NewPartRequestRepository returns a repository backed by pool.
func NewPartRequestRepository(pool *pgxpool.Pool) *PartRequestRepository {
	return &PartRequestRepository{pool: pool}
}

// Create stores a new request and returns its id.
func (r *PartRequestRepository) Create(ctx context.Context, req domain.PartRequest) (int64, error) {
	var id int64
	err := r.pool.QueryRow(ctx, `
		INSERT INTO part_requests (user_id, vehicle, brand, number, part_key, name, category, qty)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`,
		req.UserID, req.Vehicle, req.Brand, req.Number, req.PartKey, req.Name, req.Category, req.Qty).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("postgres: create part request: %w", err)
	}
	return id, nil
}

const requestColumns = `r.id, r.user_id, r.vehicle, r.brand, r.number, r.part_key, r.name, r.category, r.qty, r.status,
	coalesce(r.product_id, 0), coalesce(p.price, 0), coalesce(p.lead_time_days, 0), r.expires_at, coalesce(r.admin_message_id, 0)`

func scanRequest(row pgx.Row) (*domain.PartRequest, error) {
	var (
		req     domain.PartRequest
		expires *time.Time
	)
	err := row.Scan(&req.ID, &req.UserID, &req.Vehicle, &req.Brand, &req.Number, &req.PartKey, &req.Name, &req.Category,
		&req.Qty, &req.Status, &req.ProductID, &req.Price, &req.LeadDays, &expires, &req.AdminMessageID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if expires != nil {
		req.ExpiresAt = *expires
	}
	return &req, nil
}

// Get returns a request.
func (r *PartRequestRepository) Get(ctx context.Context, id int64) (*domain.PartRequest, error) {
	req, err := scanRequest(r.pool.QueryRow(ctx, `
		SELECT `+requestColumns+` FROM part_requests r LEFT JOIN products p ON p.id = r.product_id
		WHERE r.id = $1`, id))
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return nil, fmt.Errorf("postgres: part request %d: %w", id, err)
	}
	return req, err
}

// SetAdminMessage remembers the managers' message of a request, to update
// its buttons once the request is answered.
func (r *PartRequestRepository) SetAdminMessage(ctx context.Context, id, messageID int64) error {
	if _, err := r.pool.Exec(ctx, `UPDATE part_requests SET admin_message_id = $2 WHERE id = $1`, id, messageID); err != nil {
		return fmt.Errorf("postgres: admin message of request %d: %w", id, err)
	}
	return nil
}

// Quote prices a new request: a product is made for it, on order, sold only
// through the request, and the quote holds for ttl.
func (r *PartRequestRepository) Quote(ctx context.Context, id int64, price domain.Money, leadDays int, ttl time.Duration) (*domain.PartRequest, error) {
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM part_requests WHERE id = $1 FOR UPDATE`, id).Scan(&status); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return domain.ErrNotFound
			}
			return err
		}
		if status != string(domain.RequestNew) {
			return domain.ErrBadTransition
		}
		// The catalogue's category when the name was filed under one,
		// "Інше" otherwise; the product is not listed either way.
		var productID int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO products (sku, category_id, name, brand, price, stock, image_url, lead_time_days,
			                      source, external_id, part_key, request_id)
			SELECT 'REQ-' || r.id,
			       coalesce((SELECT c.id FROM categories c WHERE c.slug = r.category), (SELECT c.id FROM categories c WHERE c.slug = 'other')),
			       r.name || ' ' || r.number || ' (запит №' || r.id || ')', r.brand, $2, 0, '', $3,
			       'request', r.id::text, r.part_key, r.id
			FROM part_requests r WHERE r.id = $1
			RETURNING id`, id, int64(price), leadDays).Scan(&productID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			UPDATE part_requests SET status = 'quoted', product_id = $2, quoted_at = now(),
			       expires_at = now() + make_interval(secs => $3)
			WHERE id = $1`, id, productID, ttl.Seconds())
		return err
	})
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrBadTransition) {
			return nil, err
		}
		return nil, fmt.Errorf("postgres: quote request %d: %w", id, err)
	}
	return r.Get(ctx, id)
}

// NotFound closes a new request the managers could not source.
func (r *PartRequestRepository) NotFound(ctx context.Context, id int64) (*domain.PartRequest, error) {
	return r.move(ctx, id, domain.RequestNew, domain.RequestNotFound)
}

// Decline closes a quoted request the buyer turned down, and takes its
// product off sale.
func (r *PartRequestRepository) Decline(ctx context.Context, id int64) (*domain.PartRequest, error) {
	return r.move(ctx, id, domain.RequestQuoted, domain.RequestDeclined)
}

func (r *PartRequestRepository) move(ctx context.Context, id int64, from, to domain.RequestStatus) (*domain.PartRequest, error) {
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE part_requests SET status = $3 WHERE id = $1 AND status = $2`, id, from, to)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM part_requests WHERE id = $1)`, id).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return domain.ErrNotFound
			}
			return domain.ErrBadTransition
		}
		_, err = tx.Exec(ctx, `UPDATE products SET is_active = false, updated_at = now() WHERE request_id = $1`, id)
		return err
	})
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrBadTransition) {
			return nil, err
		}
		return nil, fmt.Errorf("postgres: request %d to %s: %w", id, to, err)
	}
	return r.Get(ctx, id)
}

// Expire closes quotes that ran out and takes their products off sale,
// except where an order already holds the product: a buyer who placed an
// order in time gets to pay for it. It returns how many expired.
func (r *PartRequestRepository) Expire(ctx context.Context) (int, error) {
	var n int
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			UPDATE part_requests r SET status = 'expired'
			WHERE r.status = 'quoted' AND r.expires_at < now()
			  AND NOT EXISTS (
			      SELECT 1 FROM order_items i JOIN orders o ON o.id = i.order_id
			      WHERE i.product_id = r.product_id AND o.status IN ('awaiting_payment', 'paid', 'confirmed', 'shipped'))
			RETURNING r.id`)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return err
		}
		n = len(ids)
		_, err = tx.Exec(ctx, `UPDATE products SET is_active = false, updated_at = now() WHERE request_id = ANY($1)`, ids)
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("postgres: expire quotes: %w", err)
	}
	return n, nil
}
