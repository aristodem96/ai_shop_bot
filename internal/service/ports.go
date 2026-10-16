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
	Category(ctx context.Context, id int64) (*domain.Category, error)
	// ProductsByCategory returns one page of active products, in stock first,
	// plus the total count. A non-zero vehicleID limits it to parts that fit
	// that vehicle, universal ones included.
	ProductsByCategory(ctx context.Context, categoryID, vehicleID int64, limit, offset int) ([]domain.Product, int, error)
	// CategoryCounts counts the active products filed directly in each
	// category, only those that fit the vehicle unless vehicleID is zero.
	CategoryCounts(ctx context.Context, vehicleID int64) (map[int64]int, error)
	// Product returns an active product with the vehicles it fits.
	Product(ctx context.Context, id int64) (*domain.Product, error)
	// Analogues returns in-stock parts interchangeable with p: the same OEM
	// number first, then the same kind of part for the same vehicles. A
	// non-zero vehicleID keeps only parts that fit it.
	Analogues(ctx context.Context, p *domain.Product, vehicleID int64, limit int) ([]domain.Product, error)
	// Search returns in-stock products matching every set field of f, best
	// match first.
	Search(ctx context.Context, f SearchFilter) ([]domain.Product, error)
	// KitParts returns in-stock parts of one type that fit the vehicle,
	// cheapest first. A non-empty viscosity keeps only oils of that grade.
	KitParts(ctx context.Context, vehicleID int64, partType, viscosity string, limit int) ([]domain.Product, error)
}

// VehicleRepository reads the vehicle list. It never returns the whole list:
// a real parts catalogue covers tens of thousands of vehicle types.
type VehicleRepository interface {
	// FindVehicles returns at most limit vehicles matching q.
	FindVehicles(ctx context.Context, q VehicleQuery, limit int) ([]domain.Vehicle, error)
	// Makes returns the distinct makes covered.
	Makes(ctx context.Context) ([]string, error)
	Vehicle(ctx context.Context, id int64) (*domain.Vehicle, error)
}

// GarageRepository stores each buyer's car.
type GarageRepository interface {
	// Garage returns the buyer's car, or nil if none was chosen.
	Garage(ctx context.Context, userID int64) (*domain.Vehicle, error)
	SetGarage(ctx context.Context, userID, vehicleID int64) error
	ClearGarage(ctx context.Context, userID int64) error
}

// CartRepository stores carts.
type CartRepository interface {
	// Cart returns the user's cart, empty if there is none.
	Cart(ctx context.Context, userID int64) (*domain.Cart, error)
	// SetQty sets the quantity of a line, creating the cart and the line as
	// needed. Zero removes the line.
	SetQty(ctx context.Context, userID, productID int64, qty int) error
	Clear(ctx context.Context, userID int64) error
}
