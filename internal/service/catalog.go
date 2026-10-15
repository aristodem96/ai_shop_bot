package service

import (
	"context"
	"fmt"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
)

// PageSize is how many products one catalogue page shows. Eight buttons fit a
// phone screen without scrolling past the navigation row.
const PageSize = 8

// analogueLimit bounds the "analogues" list for the same reason.
const analogueLimit = 5

// ProductPage is one page of a category listing.
type ProductPage struct {
	Category domain.Category
	Products []domain.Product
	// Vehicle is the car the page is filtered for, nil for the whole range.
	Vehicle *domain.Vehicle
	// Page is zero-based; Pages is at least 1, so "1/1" renders for an empty
	// category instead of "1/0".
	Page  int
	Pages int
}

// CatalogService browses the catalogue.
type CatalogService struct {
	repo CatalogRepository
}

// NewCatalogService wires a CatalogService.
func NewCatalogService(repo CatalogRepository) *CatalogService {
	return &CatalogService{repo: repo}
}

// Categories lists the catalogue sections in menu order.
func (s *CatalogService) Categories(ctx context.Context) ([]domain.Category, error) {
	return s.repo.Categories(ctx)
}

// CategoryPage returns one page of a category, limited to parts that fit
// vehicle when it is not nil. A page past the end is clamped to the last one:
// a stale button pressed after the catalogue shrank should still show
// something useful rather than an empty list.
func (s *CatalogService) CategoryPage(ctx context.Context, categoryID int64, vehicle *domain.Vehicle, page int) (*ProductPage, error) {
	cat, err := s.repo.Category(ctx, categoryID)
	if err != nil {
		return nil, err
	}
	var vehicleID int64
	if vehicle != nil {
		vehicleID = vehicle.ID
	}

	page = max(page, 0)
	products, total, err := s.repo.ProductsByCategory(ctx, cat.ID, vehicleID, PageSize, page*PageSize)
	if err != nil {
		return nil, err
	}

	pages := max((total+PageSize-1)/PageSize, 1)
	if page >= pages {
		page = pages - 1
		products, _, err = s.repo.ProductsByCategory(ctx, cat.ID, vehicleID, PageSize, page*PageSize)
		if err != nil {
			return nil, err
		}
	}

	return &ProductPage{Category: *cat, Products: products, Vehicle: vehicle, Page: page, Pages: pages}, nil
}

// Product returns a product card's data.
func (s *CatalogService) Product(ctx context.Context, id int64) (*domain.Product, error) {
	return s.repo.Product(ctx, id)
}

// Analogues returns parts interchangeable with a product, limited to those
// that fit vehicle when it is not nil.
func (s *CatalogService) Analogues(ctx context.Context, id int64, vehicle *domain.Vehicle) (*domain.Product, []domain.Product, error) {
	p, err := s.repo.Product(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	var vehicleID int64
	if vehicle != nil {
		vehicleID = vehicle.ID
	}
	analogues, err := s.repo.Analogues(ctx, p, vehicleID, analogueLimit)
	if err != nil {
		return nil, nil, fmt.Errorf("analogues of product %d: %w", id, err)
	}
	return p, analogues, nil
}
