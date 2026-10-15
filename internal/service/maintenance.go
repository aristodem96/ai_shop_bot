package service

import (
	"context"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
)

// KitItem is one line of a maintenance kit: the part picked by default and
// the alternatives a buyer could swap it for.
type KitItem struct {
	PartType     string
	Product      domain.Product
	Qty          int
	Alternatives []domain.Product
}

// Subtotal is the default part's price times its quantity.
func (i KitItem) Subtotal() domain.Money { return i.Product.Price * domain.Money(i.Qty) }

// Kit is what a routine service of one vehicle needs: oil, the oil, air and
// cabin filters, and spark plugs for a petrol engine.
type Kit struct {
	Vehicle domain.Vehicle
	Items   []KitItem
	// Missing lists the part types the kit needs but the shop has none of in
	// stock for this vehicle. The buyer is told, not left to assume.
	Missing []string
}

// Total is the price of the kit with the default parts.
func (k *Kit) Total() domain.Money {
	var t domain.Money
	for _, i := range k.Items {
		t += i.Subtotal()
	}
	return t
}

// kitAlternatives bounds the alternatives listed per kit line.
const kitAlternatives = 2

// MaintenanceService assembles maintenance kits.
//
// The kit is rules in code over catalogue data -- which part types, how much
// oil, how many plugs -- not something the model composes. The same kit comes
// from the "service kit" button and from the consultant, it is free to
// compute, and it never contains a part that does not fit.
type MaintenanceService struct {
	catalog  CatalogRepository
	vehicles VehicleRepository
}

// NewMaintenanceService wires a MaintenanceService.
func NewMaintenanceService(catalog CatalogRepository, vehicles VehicleRepository) *MaintenanceService {
	return &MaintenanceService{catalog: catalog, vehicles: vehicles}
}

// Kit assembles the maintenance kit for a vehicle.
func (s *MaintenanceService) Kit(ctx context.Context, vehicleID int64) (*Kit, error) {
	v, err := s.vehicles.Vehicle(ctx, vehicleID)
	if err != nil {
		return nil, err
	}
	kit := &Kit{Vehicle: *v}

	if err := s.addOil(ctx, kit); err != nil {
		return nil, err
	}

	parts := []struct {
		partType string
		qty      int
	}{
		{domain.PartOilFilter, 1},
		{domain.PartAirFilter, 1},
		{domain.PartCabinFilter, 1},
	}
	if v.NeedsSparkPlugs() {
		parts = append(parts, struct {
			partType string
			qty      int
		}{domain.PartSparkPlug, v.Cylinders})
	}

	for _, p := range parts {
		found, err := s.catalog.KitParts(ctx, v.ID, p.partType, "", kitAlternatives+1)
		if err != nil {
			return nil, err
		}
		if len(found) == 0 {
			kit.Missing = append(kit.Missing, p.partType)
			continue
		}
		kit.Items = append(kit.Items, KitItem{
			PartType: p.partType, Product: found[0], Qty: p.qty, Alternatives: found[1:],
		})
	}
	return kit, nil
}

// addOil covers the engine's oil volume with the cans in stock.
func (s *MaintenanceService) addOil(ctx context.Context, kit *Kit) error {
	v := kit.Vehicle
	if v.OilViscosity == "" || v.OilVolume <= 0 {
		kit.Missing = append(kit.Missing, domain.PartEngineOil)
		return nil
	}
	oils, err := s.catalog.KitParts(ctx, v.ID, domain.PartEngineOil, v.OilViscosity, 10)
	if err != nil {
		return err
	}

	cans := make([]can, 0, len(oils))
	for _, o := range oils {
		if litres := canVolume(&o); litres > 0 {
			cans = append(cans, can{product: o, litres: litres})
		}
	}
	plan := planOil(v.OilVolume, cans)
	if len(plan) == 0 {
		kit.Missing = append(kit.Missing, domain.PartEngineOil)
		return nil
	}
	for _, line := range plan {
		kit.Items = append(kit.Items, KitItem{PartType: domain.PartEngineOil, Product: line.product, Qty: line.qty})
	}
	return nil
}

type can struct {
	product domain.Product
	litres  float64
}

type canLine struct {
	product domain.Product
	qty     int
}

// planOil picks cans to cover volume: as many of the largest can as fit, then
// the remainder from the smallest can -- unless the remainder is over half a
// large can, when one more large can is cheaper than a row of small ones.
// 3.6 l is one 4 l can; 4.6 l is a 4 l and a 1 l.
func planOil(volume float64, cans []can) []canLine {
	if len(cans) == 0 {
		return nil
	}
	slices.SortFunc(cans, func(a, b can) int {
		switch {
		case a.litres != b.litres:
			if a.litres > b.litres {
				return -1
			}
			return 1
		default:
			return int(a.product.Price - b.product.Price)
		}
	})
	large, small := cans[0], cans[len(cans)-1]

	n := int(volume / large.litres)
	rest := volume - float64(n)*large.litres
	var plan []canLine
	switch {
	case rest <= 0.05: // the large cans cover it, within rounding
	case small.litres == large.litres || rest > large.litres/2:
		n++
	default:
		plan = append(plan, canLine{small.product, int(math.Ceil(rest/small.litres - 1e-9))})
	}
	if n > 0 {
		plan = append([]canLine{{large.product, n}}, plan...)
	}
	return plan
}

// canVolume reads the litres of an oil can from its "Об'єм" spec, "4 л".
func canVolume(p *domain.Product) float64 {
	v, ok := p.Spec("Об'єм")
	if !ok {
		return 0
	}
	f, err := strconv.ParseFloat(strings.ReplaceAll(strings.Fields(v)[0], ",", "."), 64)
	if err != nil {
		return 0
	}
	return f
}
