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
	// OnOrder are the parts catalogue's parts for the vehicle that the shop
	// does not stock, listed after the stock: a manager names their price
	// on request. Total counts them too.
	OnOrder []domain.CatalogPart
	// Vehicle is the car the page is filtered for, nil for the whole range.
	Vehicle *domain.Vehicle
	// Page is zero-based; Pages is at least 1, so "1/1" renders for an empty
	// category instead of "1/0".
	Page  int
	Pages int
}

// OrderParts are the parts catalogue's parts for a vehicle that the shop
// does not stock. postgres.CatalogPartRepository is the implementation.
type OrderParts interface {
	// CategoryCounts counts them by the category they are filed in.
	CategoryCounts(ctx context.Context, vehicleID int64) (map[int64]int, error)
	// ByCategory returns one page of them in a category and its
	// subcategories, and how many there are.
	ByCategory(ctx context.Context, categoryID, vehicleID int64, limit, offset int) ([]domain.CatalogPart, int, error)
}

// CatalogService browses the catalogue.
type CatalogService struct {
	repo  CatalogRepository
	order OrderParts // nil: the stock alone
}

// UseOrderParts has the catalogue of a chosen car list, after the stock,
// the parts the shop can order for it: a shop with little on its shelves
// still shows every part the car takes.
func (s *CatalogService) UseOrderParts(o OrderParts) { s.order = o }

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
	counts, err := s.counts(ctx, vehicleIDOf(vehicle))
	if err != nil {
		return nil, nil, err
	}
	return cats, counts, nil
}

// counts are the parts filed directly in each category: the stock, and for
// a car the parts on order too.
func (s *CatalogService) counts(ctx context.Context, vehicleID int64) (map[int64]int, error) {
	counts, err := s.repo.CategoryCounts(ctx, vehicleID)
	if err != nil {
		return nil, err
	}
	if s.order == nil || vehicleID == 0 {
		return counts, nil
	}
	order, err := s.order.CategoryCounts(ctx, vehicleID)
	if err != nil {
		return nil, fmt.Errorf("parts on order per category: %w", err)
	}
	for id, n := range order {
		counts[id] += n
	}
	return counts, nil
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
		counts, err := s.counts(ctx, vehicleID)
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
	pg := &ProductPage{Category: *cat, Parent: parent, All: hasChildren, Vehicle: vehicle}
	total, err := s.fill(ctx, pg, vehicleID, page)
	if err != nil {
		return nil, err
	}
	pg.Total, pg.Page, pg.Pages = total, page, max((total+PageSize-1)/PageSize, 1)
	if page >= pg.Pages {
		pg.Page = pg.Pages - 1
		if _, err := s.fill(ctx, pg, vehicleID, pg.Page); err != nil {
			return nil, err
		}
	}
	return pg, nil
}

// fill puts one page of a category on pg -- the stock first, then the parts
// on order, as one list -- and returns how long the list is.
func (s *CatalogService) fill(ctx context.Context, pg *ProductPage, vehicleID int64, page int) (int, error) {
	from := page * PageSize
	products, stock, err := s.repo.ProductsByCategory(ctx, pg.Category.ID, vehicleID, PageSize, from)
	if err != nil {
		return 0, err
	}
	pg.Products, pg.OnOrder = products, nil
	if s.order == nil || vehicleID == 0 {
		return stock, nil
	}
	// The page starts in the stock or past it; what the stock leaves of it
	// is the parts on order, from where the stock ended.
	offset, limit := max(from-stock, 0), PageSize-len(products)
	parts, onOrder, err := s.order.ByCategory(ctx, pg.Category.ID, vehicleID, max(limit, 1), offset)
	if err != nil {
		return 0, fmt.Errorf("parts on order of category %d: %w", pg.Category.ID, err)
	}
	if limit > 0 {
		pg.OnOrder = parts
	}
	return stock + onOrder, nil
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
