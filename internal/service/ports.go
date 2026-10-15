// Package service holds the shop's use cases. It talks to storage through the
// small ports below, which internal/storage/postgres implements and the tests
// replace with in-memory fakes.
package service

import (
	"context"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
)

// CatalogRepository reads the catalogue.
type CatalogRepository interface {
	Categories(ctx context.Context) ([]domain.Category, error)
	CategoryBySlug(ctx context.Context, slug string) (*domain.Category, error)
	// ProductsByCategory returns one page of active products, in-stock first,
	// with TotalStock filled and Variants left empty, plus the total count.
	ProductsByCategory(ctx context.Context, categoryID int64, limit, offset int) ([]domain.Product, int, error)
	// Product returns an active product with its variants in size order.
	Product(ctx context.Context, id int64) (*domain.Product, error)
	// Similar returns in-stock products of the same category, other than p,
	// closest in style and price first.
	Similar(ctx context.Context, p *domain.Product, limit int) ([]domain.Product, error)
	// Variant returns one size of an active product.
	Variant(ctx context.Context, id int64) (*domain.Variant, error)
}

// CartRepository stores carts.
type CartRepository interface {
	// Cart returns the user's cart, empty if there is none.
	Cart(ctx context.Context, userID int64) (*domain.Cart, error)
	// SetQty sets the quantity of a line, creating the cart and the line as
	// needed. Zero removes the line.
	SetQty(ctx context.Context, userID, variantID int64, qty int) error
	Clear(ctx context.Context, userID int64) error
}
