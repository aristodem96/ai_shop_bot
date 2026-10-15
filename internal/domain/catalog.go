package domain

import (
	"fmt"
	"strings"
	"unicode"
)

// Category groups products in the catalogue menu.
type Category struct {
	ID    int64
	Slug  string
	Title string
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
	Stock     int
	ImageURL  string
	// Fits lists the vehicles the part fits. Listing queries leave it empty;
	// the product card query fills it.
	Fits []Vehicle
}

// InStock reports whether the part can be bought.
func (p *Product) InStock() bool { return p.Stock > 0 }

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
