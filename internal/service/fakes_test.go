package service

import (
	"context"
	"slices"
	"strings"
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
	// search, when set, answers Search; filters records every call.
	search  func(SearchFilter) []domain.Product
	filters []SearchFilter
	// pageVehicles records the vehicle of every ProductsByCategory call.
	pageVehicles []int64
}

func (f *fakeCatalog) Categories(context.Context) ([]domain.Category, error) {
	return f.categories, nil
}

// inCategory mirrors the SQL: a section holds its subcategories' products.
func (f *fakeCatalog) inCategory(productCat, categoryID int64) bool {
	if productCat == categoryID {
		return true
	}
	for _, c := range f.categories {
		if c.ID == productCat && c.ParentID == categoryID {
			return true
		}
	}
	return false
}

func (f *fakeCatalog) CategoryCounts(_ context.Context, vehicleID int64) (map[int64]int, error) {
	out := make(map[int64]int)
	for _, p := range f.products {
		if vehicleID == 0 || p.FitsVehicle(vehicleID) {
			out[p.CategoryID]++
		}
	}
	return out, nil
}

func (f *fakeCatalog) Category(_ context.Context, id int64) (*domain.Category, error) {
	for _, c := range f.categories {
		if c.ID == id {
			return &c, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (f *fakeCatalog) ProductsByCategory(_ context.Context, categoryID, vehicleID int64, limit, offset int) ([]domain.Product, int, error) {
	f.pageVehicles = append(f.pageVehicles, vehicleID)
	var all []domain.Product
	for _, p := range f.products {
		if f.inCategory(p.CategoryID, categoryID) && (vehicleID == 0 || p.FitsVehicle(vehicleID)) {
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

func (f *fakeCatalog) Analogues(_ context.Context, p *domain.Product, vehicleID int64, limit int) ([]domain.Product, error) {
	var out []domain.Product
	for _, q := range f.products {
		if q.ID != p.ID && q.CategoryID == p.CategoryID && (vehicleID == 0 || q.FitsVehicle(vehicleID)) && len(out) < limit {
			out = append(out, q)
		}
	}
	return out, nil
}

func (f *fakeCatalog) Search(_ context.Context, sf SearchFilter) ([]domain.Product, error) {
	f.filters = append(f.filters, sf)
	if f.search != nil {
		return f.search(sf), nil
	}
	return f.products[:min(sf.Limit, len(f.products))], nil
}

func (f *fakeCatalog) KitParts(_ context.Context, vehicleID int64, partType, viscosity string, limit int) ([]domain.Product, error) {
	var out []domain.Product
	for _, p := range f.products {
		grade, _ := p.Spec("В'язкість")
		if p.PartType != partType || p.Stock == 0 || !p.FitsVehicle(vehicleID) || (viscosity != "" && grade != viscosity) {
			continue
		}
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b domain.Product) int { return int(a.Price - b.Price) })
	return out[:min(limit, len(out))], nil
}

// fakeCarts is an in-memory CartRepository that joins against a fakeCatalog,
// the way the real one joins against the products table.
type fakeCarts struct {
	mu      sync.Mutex
	catalog *fakeCatalog
	qty     map[int64]map[int64]int // user -> product -> qty
	order   map[int64][]int64       // user -> products in insertion order
}

func newFakeCarts(c *fakeCatalog) *fakeCarts {
	return &fakeCarts{catalog: c, qty: map[int64]map[int64]int{}, order: map[int64][]int64{}}
}

func (f *fakeCarts) Cart(_ context.Context, userID int64) (*domain.Cart, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	cart := &domain.Cart{UserID: userID}
	for _, pid := range f.order[userID] {
		for _, p := range f.catalog.products {
			if p.ID == pid {
				cart.Lines = append(cart.Lines, domain.CartLine{
					ProductID: p.ID, ProductName: p.Name, Brand: p.Brand,
					Price: p.Price, Qty: f.qty[userID][pid], Stock: p.Stock,
				})
			}
		}
	}
	return cart, nil
}

func (f *fakeCarts) SetQty(_ context.Context, userID, productID int64, qty int) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.qty[userID] == nil {
		f.qty[userID] = map[int64]int{}
	}
	_, exists := f.qty[userID][productID]
	switch {
	case qty == 0:
		delete(f.qty[userID], productID)
		f.order[userID] = slices.DeleteFunc(f.order[userID], func(v int64) bool { return v == productID })
	case !exists:
		f.qty[userID][productID] = qty
		f.order[userID] = append(f.order[userID], productID)
	default:
		f.qty[userID][productID] = qty
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

// fakeVehicles is an in-memory VehicleRepository and GarageRepository.
type fakeVehicles struct {
	list    []domain.Vehicle
	garages map[int64]int64
}

func (f *fakeVehicles) FindVehicles(_ context.Context, q VehicleQuery, limit int) ([]domain.Vehicle, error) {
	has := func(field, want string) bool {
		return want == "" || strings.Contains(strings.ToLower(field), strings.ToLower(want))
	}
	var out []domain.Vehicle
	for _, v := range f.list {
		inYears := q.Year == 0 || (q.Year >= v.YearFrom && (v.YearTo == 0 || q.Year <= v.YearTo))
		if has(v.Make, q.Make) && has(v.Model, q.Model) && has(v.Engine, q.Engine) && inYears && len(out) < limit {
			out = append(out, v)
		}
	}
	return out, nil
}

func (f *fakeVehicles) Models(_ context.Context, makeName string, _ int) ([]ModelName, error) {
	var out []ModelName
	for _, v := range f.list {
		if strings.EqualFold(v.Make, makeName) {
			out = append(out, ModelName{Name: v.Model})
		}
	}
	return out, nil
}

func (f *fakeVehicles) Makes(context.Context) ([]string, error) {
	var makes []string
	for _, v := range f.list {
		if !slices.Contains(makes, v.Make) {
			makes = append(makes, v.Make)
		}
	}
	return makes, nil
}

func (f *fakeVehicles) Vehicle(_ context.Context, id int64) (*domain.Vehicle, error) {
	for _, v := range f.list {
		if v.ID == id {
			return &v, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (f *fakeVehicles) Garage(ctx context.Context, userID int64) (*domain.Vehicle, error) {
	id, ok := f.garages[userID]
	if !ok {
		return nil, nil
	}
	return f.Vehicle(ctx, id)
}

func (f *fakeVehicles) SetGarage(_ context.Context, userID, vehicleID int64) error {
	f.garages[userID] = vehicleID
	return nil
}

func (f *fakeVehicles) ClearGarage(_ context.Context, userID int64) error {
	delete(f.garages, userID)
	return nil
}
