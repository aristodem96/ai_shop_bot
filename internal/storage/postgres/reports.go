package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/reports"
)

var _ reports.Store = (*ReportRepository)(nil)

// ReportRepository reads the shop's sales.
type ReportRepository struct {
	pool *pgxpool.Pool
}

// NewReportRepository returns a repository backed by pool.
func NewReportRepository(pool *pgxpool.Pool) *ReportRepository { return &ReportRepository{pool: pool} }

// sold is the condition "o is a sale of the period $1-$2": paid within it
// and not cancelled since.
const sold = `o.paid_at >= $1 AND o.paid_at < $2 AND o.status IN ('paid', 'confirmed', 'shipped')`

// topLines bounds the products a report lists.
const topLines = 100

// Report reads the sales of a period, in one snapshot.
func (r *ReportRepository) Report(ctx context.Context, from, to time.Time) (*reports.Report, error) {
	rep := &reports.Report{}
	err := pgx.BeginTxFunc(ctx, r.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT o.id, o.paid_at, o.customer_name, o.phone, o.pickup, o.delivery, o.status, o.total,
				(SELECT coalesce(sum(i.qty), 0) FROM order_items i WHERE i.order_id = o.id)
			FROM orders o WHERE `+sold+` ORDER BY o.paid_at, o.id`, from, to)
		if err != nil {
			return fmt.Errorf("sales: %w", err)
		}
		var s reports.Sale
		if _, err := pgx.ForEachRow(rows, []any{&s.ID, &s.PaidAt, &s.Customer, &s.Phone, &s.Pickup, &s.Delivery,
			&s.Status, &s.Total, &s.Items}, func() error {
			rep.Sales = append(rep.Sales, s)
			rep.Orders++
			rep.Revenue += s.Total
			if s.Pickup {
				rep.Pickup++
			} else {
				rep.Delivery++
			}
			return nil
		}); err != nil {
			return fmt.Errorf("scan sales: %w", err)
		}

		if err := tx.QueryRow(ctx, `
			SELECT count(*), coalesce(sum(total), 0) FROM orders
			WHERE paid_at >= $1 AND paid_at < $2 AND status IN ('cancelled', 'needs_refund')`, from, to).
			Scan(&rep.Refunds, &rep.RefundSum); err != nil {
			return fmt.Errorf("refunds: %w", err)
		}
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM orders WHERE created_at >= $1 AND created_at < $2 AND paid_at IS NULL`, from, to).
			Scan(&rep.Unpaid); err != nil {
			return fmt.Errorf("unpaid: %w", err)
		}
		if err := tx.QueryRow(ctx, `
			SELECT coalesce(sum(i.price * i.qty), 0) FROM order_items i JOIN orders o ON o.id = i.order_id
			WHERE `+sold+` AND NOT i.from_stock`, from, to).Scan(&rep.OnOrder); err != nil {
			return fmt.Errorf("on order: %w", err)
		}

		lines := func(sql string, withBrand bool) ([]reports.Line, error) {
			rows, err := tx.Query(ctx, sql, from, to, topLines)
			if err != nil {
				return nil, err
			}
			var (
				out []reports.Line
				l   reports.Line
				sum int64
			)
			dest := []any{&l.Name, &l.Qty, &sum}
			if withBrand {
				dest = []any{&l.Name, &l.Brand, &l.Qty, &sum}
			}
			_, err = pgx.ForEachRow(rows, dest, func() error {
				l.Sum = domain.Money(sum)
				out = append(out, l)
				return nil
			})
			return out, err
		}
		if rep.Products, err = lines(`
			SELECT i.name, i.brand, sum(i.qty)::int, sum(i.price * i.qty) FROM order_items i JOIN orders o ON o.id = i.order_id
			WHERE `+sold+` GROUP BY i.name, i.brand ORDER BY 4 DESC, 1 LIMIT $3`, true); err != nil {
			return fmt.Errorf("products: %w", err)
		}
		if rep.Categories, err = lines(`
			SELECT coalesce(s.title || ' › ', '') || c.title, sum(i.qty)::int, sum(i.price * i.qty)
			FROM order_items i JOIN orders o ON o.id = i.order_id
			JOIN products p ON p.id = i.product_id JOIN categories c ON c.id = p.category_id
			LEFT JOIN categories s ON s.id = c.parent_id
			WHERE `+sold+` GROUP BY 1 ORDER BY 3 DESC, 1 LIMIT $3`, false); err != nil {
			return fmt.Errorf("categories: %w", err)
		}

		q := &rep.Requests
		if err := tx.QueryRow(ctx, `
			SELECT count(*),
				count(*) FILTER (WHERE status IN ('quoted', 'declined', 'expired') OR product_id IS NOT NULL),
				count(*) FILTER (WHERE status = 'not_found'),
				count(*) FILTER (WHERE status = 'declined'),
				count(*) FILTER (WHERE status = 'expired'),
				count(*) FILTER (WHERE status = 'new'),
				count(*) FILTER (WHERE EXISTS (
					SELECT 1 FROM order_items i JOIN orders o ON o.id = i.order_id
					WHERE i.product_id = r.product_id AND o.status IN ('paid', 'confirmed', 'shipped')))
			FROM part_requests r WHERE r.created_at >= $1 AND r.created_at < $2`, from, to).
			Scan(&q.Made, &q.Quoted, &q.NotFound, &q.Declined, &q.Expired, &q.Waiting, &q.Paid); err != nil {
			return fmt.Errorf("requests: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("postgres: report %s–%s: %w", from.Format(time.DateOnly), to.Format(time.DateOnly), err)
	}
	return rep, nil
}
