package service

import (
	"context"
	"fmt"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
)

// PageSize is how many products one catalogue page shows. Eight buttons fit a
// phone screen without scrolling past the navigation row.
const PageSize = 8

// similarLimit bounds the "similar" list for the same reason.
const similarLimit = 5

// ProductPage is one page of a category listing.
type ProductPage struct {
	Category domain.Category
	Products []domain.Product
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

// CategoryPage returns one page of a category. A page past the end is clamped
// to the last one: a stale button pressed after the catalogue shrank should
// still show something useful rather than an empty list.
func (s *CatalogService) CategoryPage(ctx context.Context, categoryID int64, page int) (*ProductPage, error) {
	cat, err := s.repo.Category(ctx, categoryID)
	if err != nil {
		return nil, err
	}

	page = max(page, 0)
	products, total, err := s.repo.ProductsByCategory(ctx, cat.ID, PageSize, page*PageSize)
	if err != nil {
		return nil, err
	}

	pages := max((total+PageSize-1)/PageSize, 1)
	if page >= pages {
		page = pages - 1
		products, _, err = s.repo.ProductsByCategory(ctx, cat.ID, PageSize, page*PageSize)
		if err != nil {
			return nil, err
		}
	}

	return &ProductPage{Category: *cat, Products: products, Page: page, Pages: pages}, nil
}

// Product returns a product card's data.
func (s *CatalogService) Product(ctx context.Context, id int64) (*domain.Product, error) {
	return s.repo.Product(ctx, id)
}

// Similar returns alternatives to a product.
func (s *CatalogService) Similar(ctx context.Context, id int64) (*domain.Product, []domain.Product, error) {
	p, err := s.repo.Product(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	similar, err := s.repo.Similar(ctx, p, similarLimit)
	if err != nil {
		return nil, nil, fmt.Errorf("similar to product %d: %w", id, err)
	}
	return p, similar, nil
}
