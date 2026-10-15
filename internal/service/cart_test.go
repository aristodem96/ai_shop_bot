package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
)

const user = int64(42)

// Variant ids used across the tests.
const (
	vJacketM    = 11 // plenty in stock
	vJacketL    = 12 // the last unit
	vJacketXL   = 13 // sold out
	vHoodieBase = 100
)

func newCartFixture() (*CartService, *fakeCatalog, *fakeCarts) {
	catalog := &fakeCatalog{
		products: []domain.Product{{
			ID: 1, CategoryID: 1, Name: "Пуховик", Price: domain.UAH(3499),
			Variants: []domain.Variant{
				{ID: vJacketM, ProductID: 1, Size: "M", Stock: 10},
				{ID: vJacketL, ProductID: 1, Size: "L", Stock: 1},
				{ID: vJacketXL, ProductID: 1, Size: "XL", Stock: 0},
			},
		}},
	}
	carts := newFakeCarts(catalog)
	return NewCartService(carts, catalog), catalog, carts
}

func TestCartAdd(t *testing.T) {
	svc, _, _ := newCartFixture()
	ctx := context.Background()

	cart, err := svc.Add(ctx, user, vJacketM)
	require.NoError(t, err)
	require.Len(t, cart.Lines, 1)
	assert.Equal(t, 1, cart.Lines[0].Qty)

	cart, err = svc.Add(ctx, user, vJacketM)
	require.NoError(t, err)
	assert.Equal(t, 2, cart.Lines[0].Qty, "a second add increments the same line")
	assert.Equal(t, domain.UAH(6998), cart.Total())
}

func TestCartAddRespectsStock(t *testing.T) {
	svc, _, _ := newCartFixture()
	ctx := context.Background()

	_, err := svc.Add(ctx, user, vJacketXL)
	require.ErrorIs(t, err, domain.ErrOutOfStock, "a sold-out size cannot be added")

	_, err = svc.Add(ctx, user, vJacketL)
	require.NoError(t, err, "the last unit can be added once")
	_, err = svc.Add(ctx, user, vJacketL)
	require.ErrorIs(t, err, domain.ErrOutOfStock, "but not twice")
}

func TestCartAddRespectsQtyLimit(t *testing.T) {
	svc, _, _ := newCartFixture()
	ctx := context.Background()

	for range domain.MaxQtyPerLine {
		_, err := svc.Add(ctx, user, vJacketM)
		require.NoError(t, err)
	}
	_, err := svc.Add(ctx, user, vJacketM)
	require.ErrorIs(t, err, domain.ErrQtyLimit)
}

func TestCartAddRespectsLineLimit(t *testing.T) {
	svc, catalog, _ := newCartFixture()
	ctx := context.Background()

	hoodie := domain.Product{ID: 2, CategoryID: 1, Name: "Худі", Price: domain.UAH(1199)}
	for i := range domain.MaxCartLines + 1 {
		hoodie.Variants = append(hoodie.Variants, domain.Variant{ID: int64(vHoodieBase + i), ProductID: 2, Stock: 3})
	}
	catalog.products = append(catalog.products, hoodie)

	for i := range domain.MaxCartLines {
		_, err := svc.Add(ctx, user, int64(vHoodieBase+i))
		require.NoError(t, err)
	}
	_, err := svc.Add(ctx, user, int64(vHoodieBase+domain.MaxCartLines))
	require.ErrorIs(t, err, domain.ErrCartFull, "a new line beyond the limit is refused")

	_, err = svc.Add(ctx, user, vHoodieBase)
	require.NoError(t, err, "an existing line can still grow")
}

func TestCartAddUnknownVariant(t *testing.T) {
	svc, _, _ := newCartFixture()
	_, err := svc.Add(context.Background(), user, 999)
	require.ErrorIs(t, err, domain.ErrNotFound)
}

func TestCartDecrement(t *testing.T) {
	svc, _, _ := newCartFixture()
	ctx := context.Background()

	_, _ = svc.Add(ctx, user, vJacketM)
	_, _ = svc.Add(ctx, user, vJacketM)

	cart, err := svc.Decrement(ctx, user, vJacketM)
	require.NoError(t, err)
	assert.Equal(t, 1, cart.Lines[0].Qty)

	cart, err = svc.Decrement(ctx, user, vJacketM)
	require.NoError(t, err)
	assert.Empty(t, cart.Lines, "decrementing the last unit removes the line")

	cart, err = svc.Decrement(ctx, user, vJacketM)
	require.NoError(t, err, "a double tap on a removed line is harmless")
	assert.Empty(t, cart.Lines)
}

func TestCartDecrementCutsShortLineToStock(t *testing.T) {
	svc, catalog, _ := newCartFixture()
	ctx := context.Background()

	for range 4 {
		_, _ = svc.Add(ctx, user, vJacketM)
	}
	// Someone else bought most of the stock after the line was added.
	catalog.products[0].Variants[0].Stock = 2

	cart, err := svc.Decrement(ctx, user, vJacketM)
	require.NoError(t, err)
	assert.Equal(t, 2, cart.Lines[0].Qty, "one tap brings the line down to what exists")
	assert.False(t, cart.Lines[0].Short())
}

func TestCartRemoveAndClear(t *testing.T) {
	svc, _, _ := newCartFixture()
	ctx := context.Background()

	_, _ = svc.Add(ctx, user, vJacketM)
	_, _ = svc.Add(ctx, user, vJacketL)

	cart, err := svc.Remove(ctx, user, vJacketM)
	require.NoError(t, err)
	require.Len(t, cart.Lines, 1)
	assert.Equal(t, int64(vJacketL), cart.Lines[0].VariantID)

	require.NoError(t, svc.Clear(ctx, user))
	cart, err = svc.Get(ctx, user)
	require.NoError(t, err)
	assert.Empty(t, cart.Lines)
}

func TestCartAddSize(t *testing.T) {
	svc, _, _ := newCartFixture()
	ctx := context.Background()

	cart, err := svc.AddSize(ctx, user, 1, "m")
	require.NoError(t, err, "size labels match case-insensitively")
	assert.Equal(t, int64(vJacketM), cart.Lines[0].VariantID)

	_, err = svc.AddSize(ctx, user, 1, "XXXL")
	require.ErrorIs(t, err, domain.ErrNoSuchSize)

	_, err = svc.AddSize(ctx, user, 1, "XL")
	require.ErrorIs(t, err, domain.ErrOutOfStock)
}
