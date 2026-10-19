package postgres

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/service"
)

func cleanRequests(t *testing.T) {
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = testPool.Exec(ctx, `DELETE FROM orders WHERE user_id BETWEEN 7000 AND 7099`)
		_, _ = testPool.Exec(ctx, `DELETE FROM carts WHERE user_id BETWEEN 7000 AND 7099`)
		_, _ = testPool.Exec(ctx, `DELETE FROM customers WHERE user_id BETWEEN 7000 AND 7099`)
		_, _ = testPool.Exec(ctx, `UPDATE part_requests SET product_id = NULL`)
		_, _ = testPool.Exec(ctx, `DELETE FROM products WHERE request_id IS NOT NULL`)
		_, _ = testPool.Exec(ctx, `DELETE FROM part_requests`)
	})
}

var bremboRequest = domain.PartRequest{UserID: 7001, Vehicle: "Chevrolet Lacetti 1.6", Brand: "BREMBO", Number: "P 85 075",
	PartKey: "P85075", Name: "Гальмівні колодки", Category: "brake-pads", Qty: 2}

func TestPartRequestQuoteSellsOnlyThroughTheRequest(t *testing.T) {
	skipShort(t)
	cleanRequests(t)
	ctx := context.Background()
	repo := NewPartRequestRepository(testPool)

	id, err := repo.Create(ctx, bremboRequest)
	require.NoError(t, err)
	req, err := repo.Quote(ctx, id, domain.UAH(1450), 3, domain.QuoteTTL)
	require.NoError(t, err)
	assert.Equal(t, domain.RequestQuoted, req.Status)
	assert.Equal(t, domain.UAH(1450), req.Price)
	assert.Equal(t, 3, req.LeadDays)
	assert.Equal(t, domain.UAH(2900), req.Total())
	assert.WithinDuration(t, time.Now().Add(domain.QuoteTTL), req.ExpiresAt, time.Minute)

	_, err = repo.Quote(ctx, id, domain.UAH(1), 1, domain.QuoteTTL)
	require.ErrorIs(t, err, domain.ErrBadTransition, "a second manager's quote does not replace the first")

	p, err := NewCatalogRepository(testPool).Product(ctx, req.ProductID)
	require.NoError(t, err)
	assert.Equal(t, "Гальмівні колодки P 85 075 (запит №"+strconv.FormatInt(id, 10)+")", p.Name)
	assert.True(t, p.OnOrder)
	assert.Equal(t, categoryBySlug(t, "brake-pads"), p.CategoryID)

	found, err := NewCatalogRepository(testPool).Search(ctx, service.SearchFilter{PartNumber: "P85075", Limit: 10})
	require.NoError(t, err)
	assert.Empty(t, found, "a quote is not in the catalogue")

	buyerCart(t, 7001, req.ProductID, 2)
	o := place(t, 7001)
	assert.Equal(t, domain.UAH(2900), o.Total, "it sells through the usual cart and order")

	_, err = testPool.Exec(ctx, `UPDATE part_requests SET expires_at = now() - interval '1 minute'`)
	require.NoError(t, err)
	n, err := repo.Expire(ctx)
	require.NoError(t, err)
	assert.Zero(t, n, "an order placed in time keeps the quote")
}

func TestPartRequestExpiresAndDeclines(t *testing.T) {
	skipShort(t)
	cleanRequests(t)
	ctx := context.Background()
	repo := NewPartRequestRepository(testPool)

	expiring, err := repo.Create(ctx, bremboRequest)
	require.NoError(t, err)
	declined, err := repo.Create(ctx, bremboRequest)
	require.NoError(t, err)
	missing, err := repo.Create(ctx, bremboRequest)
	require.NoError(t, err)

	q, err := repo.Quote(ctx, expiring, domain.UAH(1450), 3, -time.Minute)
	require.NoError(t, err)
	n, err := repo.Expire(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	_, err = NewCatalogRepository(testPool).Product(ctx, q.ProductID)
	require.ErrorIs(t, err, domain.ErrNotFound, "an expired quote is off sale")
	_, err = repo.Decline(ctx, expiring)
	require.ErrorIs(t, err, domain.ErrBadTransition)

	q, err = repo.Quote(ctx, declined, domain.UAH(900), 1, domain.QuoteTTL)
	require.NoError(t, err)
	r, err := repo.Decline(ctx, declined)
	require.NoError(t, err)
	assert.Equal(t, domain.RequestDeclined, r.Status)
	_, err = NewCatalogRepository(testPool).Product(ctx, q.ProductID)
	require.ErrorIs(t, err, domain.ErrNotFound)

	r, err = repo.NotFound(ctx, missing)
	require.NoError(t, err)
	assert.Equal(t, domain.RequestNotFound, r.Status)
	_, err = repo.Quote(ctx, missing, domain.UAH(1), 1, domain.QuoteTTL)
	require.ErrorIs(t, err, domain.ErrBadTransition)
	_, err = repo.NotFound(ctx, 999999)
	require.ErrorIs(t, err, domain.ErrNotFound)

	require.NoError(t, repo.SetAdminMessage(ctx, missing, 555))
	r, _ = repo.Get(ctx, missing)
	assert.Equal(t, int64(555), r.AdminMessageID)
}
