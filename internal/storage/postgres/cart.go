package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/service"
)

var _ service.CartRepository = (*CartRepository)(nil)

// CartRepository stores carts.
type CartRepository struct {
	pool *pgxpool.Pool
}

// NewCartRepository returns a repository backed by pool.
func NewCartRepository(pool *pgxpool.Pool) *CartRepository {
	return &CartRepository{pool: pool}
}

// Cart returns the user's cart joined with current prices and stock. A product
// taken off sale stays in the cart with zero stock, so the buyer sees why it
// cannot be bought instead of watching it silently vanish.
func (r *CartRepository) Cart(ctx context.Context, userID int64) (*domain.Cart, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT p.id, p.name, p.brand, p.price, ci.qty,
		       CASE WHEN p.is_active THEN p.stock ELSE 0 END
		FROM cart_items ci
		JOIN products p ON p.id = ci.product_id
		WHERE ci.user_id = $1
		ORDER BY ci.added_at, ci.product_id`, userID)
	if err != nil {
		return nil, fmt.Errorf("postgres: get cart of %d: %w", userID, err)
	}
	lines, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.CartLine, error) {
		var (
			l     domain.CartLine
			price int64
		)
		err := row.Scan(&l.ProductID, &l.ProductName, &l.Brand, &price, &l.Qty, &l.Stock)
		l.Price = domain.Money(price)
		return l, err
	})
	if err != nil {
		return nil, fmt.Errorf("postgres: scan cart: %w", err)
	}
	return &domain.Cart{UserID: userID, Lines: lines}, nil
}

// SetQty writes a line in one statement: the cart row is upserted in a CTE and
// the line upserted from it, so a cart never exists half-written and the call
// costs one round trip.
//
// Any change resets reminded_at: the abandoned cart reminder fires once per
// abandonment, and a buyer who came back and changed the cart has not
// abandoned the old one.
func (r *CartRepository) SetQty(ctx context.Context, userID, productID int64, qty int) error {
	if qty <= 0 {
		_, err := r.pool.Exec(ctx, `
			WITH removed AS (
				DELETE FROM cart_items WHERE user_id = $1 AND product_id = $2
			)
			UPDATE carts SET updated_at = now(), reminded_at = NULL WHERE user_id = $1`,
			userID, productID)
		if err != nil {
			return fmt.Errorf("postgres: remove cart line: %w", err)
		}
		return nil
	}

	_, err := r.pool.Exec(ctx, `
		WITH cart AS (
			INSERT INTO carts (user_id) VALUES ($1)
			ON CONFLICT (user_id) DO UPDATE SET updated_at = now(), reminded_at = NULL
			RETURNING user_id
		)
		INSERT INTO cart_items (user_id, product_id, qty)
		SELECT user_id, $2, $3 FROM cart
		ON CONFLICT (user_id, product_id) DO UPDATE SET qty = EXCLUDED.qty`,
		userID, productID, qty)
	if isForeignKeyViolation(err) {
		return domain.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("postgres: set cart line: %w", err)
	}
	return nil
}

// Clear deletes the cart; its lines go with it through ON DELETE CASCADE.
func (r *CartRepository) Clear(ctx context.Context, userID int64) error {
	if _, err := r.pool.Exec(ctx, `DELETE FROM carts WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("postgres: clear cart of %d: %w", userID, err)
	}
	return nil
}
