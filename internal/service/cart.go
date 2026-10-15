package service

import (
	"context"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
)

// CartService changes carts within the shop's rules.
//
// The stock check here is advisory: it stops a buyer from putting more in the
// cart than exists, but two buyers can still hold the last unit in their carts
// at once. That is deliberate -- a cart is not a reservation. The guarantee
// that a unit is sold once comes at checkout (stage 3), where stock is
// reserved under SELECT ... FOR UPDATE.
//
// Two taps by the same user cannot race each other either: the bot handles a
// chat's updates one at a time, so a read-check-write here is safe without a
// transaction.
type CartService struct {
	carts   CartRepository
	catalog CatalogRepository
}

// NewCartService wires a CartService.
func NewCartService(carts CartRepository, catalog CatalogRepository) *CartService {
	return &CartService{carts: carts, catalog: catalog}
}

// Get returns the user's cart.
func (s *CartService) Get(ctx context.Context, userID int64) (*domain.Cart, error) {
	return s.carts.Cart(ctx, userID)
}

// Add puts one more unit of a product into the cart and returns the cart after.
func (s *CartService) Add(ctx context.Context, userID, productID int64) (*domain.Cart, error) {
	p, err := s.catalog.Product(ctx, productID)
	if err != nil {
		return nil, err
	}
	cart, err := s.carts.Cart(ctx, userID)
	if err != nil {
		return nil, err
	}

	line, exists := cart.Line(productID)
	qty := line.Qty + 1
	switch {
	case !exists && len(cart.Lines) >= domain.MaxCartLines:
		return nil, domain.ErrCartFull
	case qty > domain.MaxQtyPerLine:
		return nil, domain.ErrQtyLimit
	case qty > p.Stock:
		return nil, domain.ErrOutOfStock
	}

	if err := s.carts.SetQty(ctx, userID, productID, qty); err != nil {
		return nil, err
	}
	return s.carts.Cart(ctx, userID)
}

// Decrement takes one unit away, removing the line at zero.
func (s *CartService) Decrement(ctx context.Context, userID, productID int64) (*domain.Cart, error) {
	cart, err := s.carts.Cart(ctx, userID)
	if err != nil {
		return nil, err
	}
	line, ok := cart.Line(productID)
	if !ok {
		// Already gone, e.g. a double tap. The cart as it is is the answer.
		return cart, nil
	}

	// A line left above stock (the stock dropped after it was added) is cut
	// straight down to what exists, so one tap makes the cart buyable again.
	qty := line.Qty - 1
	if line.Short() {
		qty = line.Stock
	}

	if err := s.carts.SetQty(ctx, userID, productID, qty); err != nil {
		return nil, err
	}
	return s.carts.Cart(ctx, userID)
}

// Remove deletes a line.
func (s *CartService) Remove(ctx context.Context, userID, productID int64) (*domain.Cart, error) {
	if err := s.carts.SetQty(ctx, userID, productID, 0); err != nil {
		return nil, err
	}
	return s.carts.Cart(ctx, userID)
}

// Clear empties the cart.
func (s *CartService) Clear(ctx context.Context, userID int64) error {
	return s.carts.Clear(ctx, userID)
}
