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

// CategoryEntry is a category in a menu, with how many parts it holds.
type CategoryEntry struct {
	Category domain.Category
	Count    int
}

// ProductPage is one page of a category listing, or the menu of a section:
// a section with subcategories shows them (Children) rather than products,
// unless All asks for everything in it.
type ProductPage struct {
	Category domain.Category
	// Parent is the section of a subcategory, nil for a section.
	Parent *domain.Category
	// Children are the section's subcategories that have parts, for the
	// vehicle when one is set. Total counts the parts in all of them.
	Children []CategoryEntry
	Total    int
	// All lists the products of a whole section.
	All      bool
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

// Categories lists every category, sections and subcategories, in menu
// order.
func (s *CatalogService) Categories(ctx context.Context) ([]domain.Category, error) {
	return s.repo.Categories(ctx)
}

// Menu lists the sections that have parts -- for the vehicle when it is set
// with how many each holds. A section with nothing in it is left out: an
// empty menu entry is a dead end, and with a car chosen most of a big
// catalogue is.
func (s *CatalogService) Menu(ctx context.Context, vehicle *domain.Vehicle) ([]CategoryEntry, error) {
	cats, counts, err := s.tree(ctx, vehicle)
	if err != nil {
		return nil, err
	}
	return entries(cats, counts, 0), nil
}

// tree loads every category and the parts filed directly in each.
func (s *CatalogService) tree(ctx context.Context, vehicle *domain.Vehicle) ([]domain.Category, map[int64]int, error) {
	cats, err := s.repo.Categories(ctx)
	if err != nil {
		return nil, nil, err
	}
	counts, err := s.repo.CategoryCounts(ctx, vehicleIDOf(vehicle))
	if err != nil {
		return nil, nil, err
	}
	return cats, counts, nil
}

// entries lists the children of parentID (the sections, for zero) that hold
// parts, each counting its own subcategories too.
func entries(cats []domain.Category, counts map[int64]int, parentID int64) []CategoryEntry {
	var out []CategoryEntry
	for _, c := range cats {
		if c.ParentID != parentID {
			continue
		}
		n := counts[c.ID]
		for _, sub := range cats {
			if sub.ParentID == c.ID {
				n += counts[sub.ID]
			}
		}
		if n > 0 {
			out = append(out, CategoryEntry{Category: c, Count: n})
		}
	}
	return out
}

func vehicleIDOf(v *domain.Vehicle) int64 {
	if v == nil {
		return 0
	}
	return v.ID
}

// CategoryPage opens a category, limited to parts that fit vehicle when it
// is not nil. A section with subcategories opens as their menu, unless all
// asks for every part in it. A page past the end is clamped to the last one:
// a stale button pressed after the catalogue shrank should still show
// something useful rather than an empty list.
func (s *CatalogService) CategoryPage(ctx context.Context, categoryID int64, vehicle *domain.Vehicle, page int, all bool) (*ProductPage, error) {
	cats, err := s.repo.Categories(ctx)
	if err != nil {
		return nil, err
	}
	var cat, parent *domain.Category
	hasChildren := false
	for i := range cats {
		if cats[i].ID == categoryID {
			cat = &cats[i]
		}
		if cats[i].ParentID == categoryID {
			hasChildren = true
		}
	}
	if cat == nil {
		return nil, domain.ErrNotFound
	}
	for i := range cats {
		if cats[i].ID == cat.ParentID {
			parent = &cats[i]
		}
	}
	vehicleID := vehicleIDOf(vehicle)

	if hasChildren && !all {
		counts, err := s.repo.CategoryCounts(ctx, vehicleID)
		if err != nil {
			return nil, err
		}
		children := entries(cats, counts, cat.ID)
		if len(children) > 0 {
			total := counts[cat.ID]
			for _, e := range children {
				total += e.Count
			}
			return &ProductPage{Category: *cat, Parent: parent, Children: children, Total: total, Vehicle: vehicle, Pages: 1}, nil
		}
		// Every subcategory is empty, for this car or at all: a menu of
		// nothing is a dead end, so the section's own products show
		// instead, if it has any.
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

	return &ProductPage{
		Category: *cat, Parent: parent, All: hasChildren, Total: total,
		Products: products, Vehicle: vehicle, Page: page, Pages: pages,
	}, nil
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
	analogues, err := s.repo.Analogues(ctx, p, vehicleIDOf(vehicle), analogueLimit)
	if err != nil {
		return nil, nil, fmt.Errorf("analogues of product %d: %w", id, err)
	}
	return p, analogues, nil
}
