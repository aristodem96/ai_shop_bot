package domain

import "slices"

// Category groups products in the catalogue menu.
type Category struct {
	ID    int64
	Slug  string
	Title string
}

// Product is one model in one colour.
type Product struct {
	ID          int64
	CategoryID  int64
	SKU         string
	Name        string
	Brand       string
	Description string
	// Color is a code such as "black"; ColorLabel turns it into a word. Codes
	// keep filtering exact, whatever grammatical form a buyer types.
	Color    string
	Season   string
	Gender   string
	Styles   []string
	Price    Money
	ImageURL string
	// Variants are the sizes in display order. Listing queries may leave them
	// empty and fill TotalStock instead.
	Variants   []Variant
	TotalStock int
}

// InStock reports whether any size can be bought.
func (p *Product) InStock() bool { return p.TotalStock > 0 }

// Variant finds a size of the product by its id.
func (p *Product) Variant(id int64) (Variant, bool) {
	for _, v := range p.Variants {
		if v.ID == id {
			return v, true
		}
	}
	return Variant{}, false
}

// Variant is one size of a product with its own stock.
type Variant struct {
	ID        int64
	ProductID int64
	Size      string
	Stock     int
}

// LowStock is the level at which a size is flagged as almost gone. Showing it
// is honest with the buyer and nudges a decision.
const LowStock = 2

var colorLabels = map[string]string{
	"black": "чорний",
	"white": "білий",
	"grey":  "сірий",
	"beige": "бежевий",
	"brown": "коричневий",
	"blue":  "синій",
	"navy":  "темно-синій",
	"green": "зелений",
	"khaki": "хакі",
	"olive": "оливковий",
	"red":   "червоний",
}

// Colors lists the colour codes in a stable order. Tool schemas are built from
// it, and a schema that reordered itself between runs would defeat the
// model provider's prompt cache.
func Colors() []string {
	codes := make([]string, 0, len(colorLabels))
	for c := range colorLabels {
		codes = append(codes, c)
	}
	slices.Sort(codes)
	return codes
}

// ColorLabel returns the Ukrainian name of a colour code.
func ColorLabel(code string) string {
	if l, ok := colorLabels[code]; ok {
		return l
	}
	return code
}

var seasonLabels = map[string]string{
	"summer": "літо",
	"demi":   "демісезон",
	"winter": "зима",
	"all":    "всесезон",
}

// SeasonLabel returns the Ukrainian name of a season code.
func SeasonLabel(code string) string {
	if l, ok := seasonLabels[code]; ok {
		return l
	}
	return code
}
