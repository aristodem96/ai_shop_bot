package domain

import (
	"fmt"
	"strings"
	"unicode"
)

// Category groups products in the catalogue menu. Categories form a tree
// two levels deep: sections, and their subcategories.
type Category struct {
	ID    int64
	Slug  string
	Title string
	// ParentID is the section a subcategory belongs to, zero for a section.
	ParentID int64
}

// Vehicle is one make, model, generation and engine -- the level at which
// parts differ.
type Vehicle struct {
	ID       int64
	Make     string
	Model    string
	YearFrom int
	// YearTo is zero while the model is still produced.
	YearTo int
	Engine string
	Fuel   string
	// Service data; OilViscosity is empty and OilVolume zero when unknown.
	OilViscosity string
	OilVolume    float64 // litres
	Cylinders    int
}

// NeedsSparkPlugs reports whether the engine has spark plugs to service.
func (v Vehicle) NeedsSparkPlugs() bool { return v.Fuel == "petrol" || v.Fuel == "hybrid" }

// Label names the vehicle the way a buyer would recognise it:
// "Skoda Octavia A7 1.4 TSI (2013–2020)".
func (v Vehicle) Label() string {
	return fmt.Sprintf("%s %s %s (%s)", v.Make, v.Model, v.Engine, v.Years())
}

// Short is the label without the years, for buttons.
func (v Vehicle) Short() string {
	return fmt.Sprintf("%s %s %s", v.Make, v.Model, v.Engine)
}

// Years renders the production span, "2019–н.ч." for a current model.
func (v Vehicle) Years() string {
	if v.YearTo == 0 {
		return fmt.Sprintf("%d–н.ч.", v.YearFrom)
	}
	return fmt.Sprintf("%d–%d", v.YearFrom, v.YearTo)
}

// Spec is one display characteristic of a part.
type Spec struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Product is one part from one brand.
type Product struct {
	ID          int64
	CategoryID  int64
	SKU         string
	Name        string
	Brand       string
	Description string
	OEMNumbers  []string
	// PartType is the kind of part in a fixed vocabulary, see the PartType
	// constants; empty for parts no maintenance kit needs.
	PartType string
	Specs    []Spec
	// Universal parts fit every vehicle and have no Fits list.
	Universal bool
	Price     Money
	// Stock is the units on the shelf. It is not tracked for a part on
	// order.
	Stock int
	// OnOrder parts are delivered by a supplier in about LeadDays days and
	// can be bought in any quantity.
	OnOrder  bool
	LeadDays int
	ImageURL string
	// Fits lists the vehicles the part fits. Listing queries leave it empty;
	// the product card query fills it.
	Fits []Vehicle
	// FitNotes holds where and under what conditions the part fits a
	// vehicle ("передня вісь, з ABS"), by vehicle id, for the fitments that
	// have a note.
	FitNotes map[int64]string
}

// Available reports whether the part can be bought: it is on the shelf, or
// the supplier delivers it.
func (p *Product) Available() bool { return p.OnOrder || p.Stock > 0 }

// FitsVehicle reports whether the part fits the vehicle. It relies on Fits
// being loaded, which the card query does.
func (p *Product) FitsVehicle(vehicleID int64) bool {
	if p.Universal {
		return true
	}
	for _, v := range p.Fits {
		if v.ID == vehicleID {
			return true
		}
	}
	return false
}

// Part types the maintenance kit is built from.
const (
	PartEngineOil   = "engine_oil"
	PartOilFilter   = "oil_filter"
	PartAirFilter   = "air_filter"
	PartCabinFilter = "cabin_filter"
	PartSparkPlug   = "spark_plug"
)

// Spec returns the value of a named characteristic.
func (p *Product) Spec(name string) (string, bool) {
	for _, s := range p.Specs {
		if s.Name == name {
			return s.Value, true
		}
	}
	return "", false
}

// LowStock is the level at which stock is flagged as almost gone. Showing it
// is honest with the buyer and nudges a decision.
const LowStock = 2

// NormalizeOEM reduces a part number to letters and digits in upper case.
// Catalogues and buyers write the same number as "1K0 698 151 A",
// "1K0-698-151-A" or "1k0698151a"; all of them become "1K0698151A", which is
// how numbers are stored and compared.
func NormalizeOEM(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToUpper(r))
		}
	}
	return b.String()
}

// CatalogPart is a part the Auto-DB catalogue lists for a car and the shop
// does not stock. It has no price: the shop can order it, and a manager
// quotes the price once a supplier is found.
type CatalogPart struct {
	ID    int64
	Brand string
	// SupplierID is the brand's id in the catalogue, to ask it about the
	// part.
	SupplierID int64
	Number     string
	// Name is Ukrainian once translated, the catalogue's Russian until then.
	Name string
	// Category is the slug of the shop's category, "" when not yet known.
	Category string
}

// PartKind is a kind of part in a car's catalogue, by name, and how many of
// it there are: "Амортизатор", 45.
type PartKind struct {
	Name  string
	Count int
}

// CatalogProgress is how far a car's parts catalogue has loaded.
type CatalogProgress struct {
	// State is "none" before the load is queued, then "queued", "running",
	// and "done" or "failed".
	State                       string
	SectionsDone, SectionsTotal int
	// Parts counts the different parts loaded; Filed those of them already
	// in a category of the shop's menu, which takes their names translated.
	Parts, Filed int
}

// Loaded reports whether the whole catalogue is in.
func (p CatalogProgress) Loaded() bool { return p.State == "done" }

// Empty reports whether the catalogue has no parts for the car at all.
func (p CatalogProgress) Empty() bool { return p.State == "empty" }

// Loading reports whether the catalogue is still on its way.
func (p CatalogProgress) Loading() bool { return !p.Loaded() && !p.Empty() }
