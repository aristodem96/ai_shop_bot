package service

import (
	"context"
	"slices"
	"sync"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
)

// The doubles below are written by hand rather than generated: the ports are
// small, and the tests need a repository that really stores things, not a pile
// of expectation setup.

// fakeCatalog is an in-memory CatalogRepository.
type fakeCatalog struct {
	categories []domain.Category
	products   []domain.Product
}

func (f *fakeCatalog) Categories(context.Context) ([]domain.Category, error) {
	return f.categories, nil
}

func (f *fakeCatalog) Category(_ context.Context, id int64) (*domain.Category, error) {
	for _, c := range f.categories {
		if c.ID == id {
			return &c, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (f *fakeCatalog) ProductsByCategory(_ context.Context, categoryID int64, limit, offset int) ([]domain.Product, int, error) {
	var all []domain.Product
	for _, p := range f.products {
		if p.CategoryID == categoryID {
			all = append(all, p)
		}
	}
	if offset >= len(all) {
		return nil, len(all), nil
	}
	return all[offset:min(offset+limit, len(all))], len(all), nil
}

func (f *fakeCatalog) Product(_ context.Context, id int64) (*domain.Product, error) {
	for _, p := range f.products {
		if p.ID == id {
			return &p, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (f *fakeCatalog) Similar(_ context.Context, p *domain.Product, limit int) ([]domain.Product, error) {
	var out []domain.Product
	for _, q := range f.products {
		if q.CategoryID == p.CategoryID && q.ID != p.ID && len(out) < limit {
			out = append(out, q)
		}
	}
	return out, nil
}

func (f *fakeCatalog) Variant(_ context.Context, id int64) (*domain.Variant, error) {
	for _, p := range f.products {
		if v, ok := p.Variant(id); ok {
			return &v, nil
		}
	}
	return nil, domain.ErrNotFound
}

// fakeCarts is an in-memory CartRepository that joins against a fakeCatalog,
// the way the real one joins against product tables.
type fakeCarts struct {
	mu      sync.Mutex
	catalog *fakeCatalog
	qty     map[int64]map[int64]int // user -> variant -> qty
	order   map[int64][]int64       // user -> variants in insertion order
}

func newFakeCarts(c *fakeCatalog) *fakeCarts {
	return &fakeCarts{catalog: c, qty: map[int64]map[int64]int{}, order: map[int64][]int64{}}
}

func (f *fakeCarts) Cart(_ context.Context, userID int64) (*domain.Cart, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	cart := &domain.Cart{UserID: userID}
	for _, vid := range f.order[userID] {
		for _, p := range f.catalog.products {
			if v, ok := p.Variant(vid); ok {
				cart.Lines = append(cart.Lines, domain.CartLine{
					VariantID: vid, ProductID: p.ID, ProductName: p.Name,
					Size: v.Size, Price: p.Price, Qty: f.qty[userID][vid], Stock: v.Stock,
				})
			}
		}
	}
	return cart, nil
}

func (f *fakeCarts) SetQty(_ context.Context, userID, variantID int64, qty int) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.qty[userID] == nil {
		f.qty[userID] = map[int64]int{}
	}
	_, exists := f.qty[userID][variantID]
	switch {
	case qty == 0:
		delete(f.qty[userID], variantID)
		f.order[userID] = slices.DeleteFunc(f.order[userID], func(v int64) bool { return v == variantID })
	case !exists:
		f.qty[userID][variantID] = qty
		f.order[userID] = append(f.order[userID], variantID)
	default:
		f.qty[userID][variantID] = qty
	}
	return nil
}

func (f *fakeCarts) Clear(_ context.Context, userID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.qty, userID)
	delete(f.order, userID)
	return nil
}
