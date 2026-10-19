package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
)

func TestReport(t *testing.T) {
	skipShort(t)
	ctx := context.Background()
	pads := withStock(t, "BRK-001", 10)

	// Orders paid in the period, paid and cancelled, never paid, and paid
	// the day before.
	order := func(user int64, status string, paidAt *time.Time, pickup bool) int64 {
		return reportOrder(t, user, pads.ID, status, paidAt, pickup)
	}
	from := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	in := from.Add(10 * time.Hour)
	before := from.Add(-time.Hour)
	sale := order(7101, "confirmed", &in, true)
	order(7102, "cancelled", &in, false)
	order(7103, "expired", nil, false)
	order(7104, "paid", &before, false) // the day before: not this period's

	rep, err := NewReportRepository(testPool).Report(ctx, from, from.AddDate(0, 0, 7))
	require.NoError(t, err)
	assert.Equal(t, 1, rep.Orders, "a sale is paid in the period and not cancelled")
	assert.Equal(t, pads.Price*2, rep.Revenue)
	assert.Equal(t, 1, rep.Pickup)
	assert.Equal(t, 1, rep.Refunds)
	assert.GreaterOrEqual(t, rep.Unpaid, 1)
	require.Len(t, rep.Sales, 1)
	assert.Equal(t, sale, rep.Sales[0].ID)
	assert.Equal(t, 2, rep.Sales[0].Items)
	assert.Equal(t, domain.OrderConfirmed, rep.Sales[0].Status)
	require.Len(t, rep.Products, 1)
	assert.Equal(t, 2, rep.Products[0].Qty)
	require.Len(t, rep.Categories, 1)
	assert.Contains(t, rep.Categories[0].Name, "›", "a category by its path")
}

// reportOrder places an order of two units and sets how it ended.
func reportOrder(t *testing.T, user, productID int64, status string, paidAt *time.Time, pickup bool) int64 {
	t.Helper()
	buyerCart(t, user, productID, 2)
	o := place(t, user)
	_, err := testPool.Exec(context.Background(), `UPDATE orders SET status = $2, paid_at = $3, pickup = $4 WHERE id = $1`,
		o.ID, status, paidAt, pickup)
	require.NoError(t, err)
	return o.ID
}
