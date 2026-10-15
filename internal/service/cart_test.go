package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
)

const user = int64(42)

// Product ids used across the tests.
const (
	pPads     = 1 // plenty in stock
	pLastDisc = 2 // the last unit
	pSoldOut  = 3
	pBase     = 100
)

func newCartFixture() (*CartService, *fakeCatalog) {
	catalog := &fakeCatalog{products: []domain.Product{
		{ID: pPads, Name: "Колодки", Price: domain.UAH(1450), Stock: 20},
		{ID: pLastDisc, Name: "Диск", Price: domain.UAH(1890), Stock: 1},
		{ID: pSoldOut, Name: "Колодки Lanos", Price: domain.UAH(450), Stock: 0},
	}}
	return NewCartService(newFakeCarts(catalog), catalog), catalog
}

func TestCartAdd(t *testing.T) {
	svc, _ := newCartFixture()
	ctx := context.Background()

	cart, err := svc.Add(ctx, user, pPads)
	require.NoError(t, err)
	require.Len(t, cart.Lines, 1)
	assert.Equal(t, 1, cart.Lines[0].Qty)

	cart, err = svc.Add(ctx, user, pPads)
	require.NoError(t, err)
	assert.Equal(t, 2, cart.Lines[0].Qty, "a second add increments the same line")
	assert.Equal(t, domain.UAH(2900), cart.Total())
}

func TestCartAddRespectsStock(t *testing.T) {
	svc, _ := newCartFixture()
	ctx := context.Background()

	_, err := svc.Add(ctx, user, pSoldOut)
	require.ErrorIs(t, err, domain.ErrOutOfStock, "a sold-out part cannot be added")

	_, err = svc.Add(ctx, user, pLastDisc)
	require.NoError(t, err, "the last unit can be added once")
	_, err = svc.Add(ctx, user, pLastDisc)
	require.ErrorIs(t, err, domain.ErrOutOfStock, "but not twice")
}

func TestCartAddRespectsQtyLimit(t *testing.T) {
	svc, _ := newCartFixture()
	ctx := context.Background()

	for range domain.MaxQtyPerLine {
		_, err := svc.Add(ctx, user, pPads)
		require.NoError(t, err)
	}
	_, err := svc.Add(ctx, user, pPads)
	require.ErrorIs(t, err, domain.ErrQtyLimit)
}

func TestCartAddRespectsLineLimit(t *testing.T) {
	svc, catalog := newCartFixture()
	ctx := context.Background()

	for i := range domain.MaxCartLines + 1 {
		catalog.products = append(catalog.products, domain.Product{ID: int64(pBase + i), Stock: 3})
	}
	for i := range domain.MaxCartLines {
		_, err := svc.Add(ctx, user, int64(pBase+i))
		require.NoError(t, err)
	}
	_, err := svc.Add(ctx, user, int64(pBase+domain.MaxCartLines))
	require.ErrorIs(t, err, domain.ErrCartFull, "a new line beyond the limit is refused")

	_, err = svc.Add(ctx, user, pBase)
	require.NoError(t, err, "an existing line can still grow")
}

func TestCartAddUnknownProduct(t *testing.T) {
	svc, _ := newCartFixture()
	_, err := svc.Add(context.Background(), user, 999)
	require.ErrorIs(t, err, domain.ErrNotFound)
}

func TestCartDecrement(t *testing.T) {
	svc, _ := newCartFixture()
	ctx := context.Background()

	_, _ = svc.Add(ctx, user, pPads)
	_, _ = svc.Add(ctx, user, pPads)

	cart, err := svc.Decrement(ctx, user, pPads)
	require.NoError(t, err)
	assert.Equal(t, 1, cart.Lines[0].Qty)

	cart, err = svc.Decrement(ctx, user, pPads)
	require.NoError(t, err)
	assert.Empty(t, cart.Lines, "decrementing the last unit removes the line")

	cart, err = svc.Decrement(ctx, user, pPads)
	require.NoError(t, err, "a double tap on a removed line is harmless")
	assert.Empty(t, cart.Lines)
}

func TestCartDecrementCutsShortLineToStock(t *testing.T) {
	svc, catalog := newCartFixture()
	ctx := context.Background()

	for range 4 {
		_, _ = svc.Add(ctx, user, pPads)
	}
	// Someone else bought most of the stock after the line was added.
	catalog.products[0].Stock = 2

	cart, err := svc.Decrement(ctx, user, pPads)
	require.NoError(t, err)
	assert.Equal(t, 2, cart.Lines[0].Qty, "one tap brings the line down to what exists")
	assert.False(t, cart.Lines[0].Short())
}

func TestCartRemoveAndClear(t *testing.T) {
	svc, _ := newCartFixture()
	ctx := context.Background()

	_, _ = svc.Add(ctx, user, pPads)
	_, _ = svc.Add(ctx, user, pLastDisc)

	cart, err := svc.Remove(ctx, user, pPads)
	require.NoError(t, err)
	require.Len(t, cart.Lines, 1)
	assert.Equal(t, int64(pLastDisc), cart.Lines[0].ProductID)

	require.NoError(t, svc.Clear(ctx, user))
	cart, err = svc.Get(ctx, user)
	require.NoError(t, err)
	assert.Empty(t, cart.Lines)
}
