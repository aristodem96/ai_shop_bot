package importer

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/base32"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
)

// maxProblems bounds the problems a report lists; the count is always exact.
const maxProblems = 30

// Model is a vehicle model of the catalogue.
type Model struct {
	ID          int64
	Make, Model string
	// YearFrom and YearTo span its vehicles' production, YearTo zero while
	// one is still made: "Chery Tiggo (2005–2016)" tells generations apart.
	YearFrom, YearTo int
}

// Label names a model for people.
func (m Model) Label() string {
	label := m.Make + " " + m.Model
	switch {
	case m.YearFrom == 0:
	case m.YearTo == 0:
		label += fmt.Sprintf(" (%d–н.ч.)", m.YearFrom)
	default:
		label += fmt.Sprintf(" (%d–%d)", m.YearFrom, m.YearTo)
	}
	return label
}

// Store is what an import needs from the database.
type Store interface {
	// BrandAliases maps every known spelling, as a brand key, to the
	// canonical brand name.
	BrandAliases(ctx context.Context) (map[string]string, error)
	VehicleModels(ctx context.Context) ([]Model, error)
	// VehicleAliases maps a supplier's vehicle names, as vehicleKey keys,
	// to the models the owner confirmed for them.
	VehicleAliases(ctx context.Context, source string) (map[string][]int64, error)
	// Categories lists the category tree with its keywords, in file order.
	Categories(ctx context.Context) ([]Category, error)
	// GroupCategories maps the supplier's decided groups to the slugs of
	// their categories.
	GroupCategories(ctx context.Context, supplierID int64) (map[GroupKey]string, error)
	// Apply writes the batch in one transaction, or only measures what it
	// would write when dryRun is set.
	Apply(ctx context.Context, b *Batch, dryRun bool) (Result, error)
}

// Batch is a file resolved against the catalogue, ready to be written.
type Batch struct {
	Source            string
	Products          []Product
	NewBrands         []NewBrand
	DeactivateMissing bool
	// MaxDeactivatePercent caps what share of the supplier's active
	// products the batch may take off sale, unless Force is set.
	MaxDeactivatePercent int
	Force                bool
	Report               *Report
	StartedAt            time.Time
}

// DeactivationError stops an import that would take off sale more of the
// supplier's products than its profile allows. The import changes nothing;
// the numbers say what it would have done.
type DeactivationError struct {
	Would, Active, LimitPercent int
}

func (e *DeactivationError) Error() string {
	return fmt.Sprintf("importer: the file would take %d of %d active products off sale, more than %d%%; "+
		"check that the file is complete, or force it", e.Would, e.Active, e.LimitPercent)
}

// Exceeded reports whether taking would of active products off sale passes
// the limit.
func Exceeded(would, active, limitPercent int) bool {
	return active > 0 && would*100 > limitPercent*active
}

// Options tune a run.
type Options struct {
	// DryRun applies nothing and reports what would change.
	DryRun bool
	// Force lets the run take off sale more than the profile allows.
	Force bool
}

// Product is an offer in catalogue terms.
type Product struct {
	ExternalID  string
	SKU         string // used only when the product is created
	Name        string
	Brand       string // canonical, or empty
	Description string
	Image       string
	Category    string // slug
	// Group is the supplier's own group of the row, kept to sort the
	// product again when the category tree changes.
	Group     string
	PartType  string
	Universal bool
	OEM       []string
	// PartKey is the supplier's part number, normalised: the key the
	// product meets the parts catalogue's lists by.
	PartKey  string
	Price    domain.Money
	Stock    *int // nil: on order
	LeadDays *int
	Fits     []ModelFit
}

// ModelFit is a fitment to a model of the catalogue.
type ModelFit struct {
	ModelID          int64
	YearFrom, YearTo int
	Position, Note   string
	// Via is the file's vehicle name it came from, as a vehicleKey.
	Via string
}

// NewBrand is a brand no import has met before.
type NewBrand struct {
	Name  string
	Alias string
}

// Count is a name and how many rows had it.
type Count struct {
	Name string `json:"name"`
	N    int    `json:"n"`
}

// Report is what an operator reads after an import, and what import_runs
// keeps of it.
type Report struct {
	File     string    `json:"file"`
	Rows     int       `json:"rows"`
	Offers   int       `json:"offers"`
	Skipped  int       `json:"skipped"`
	Problems []Problem `json:"problems,omitempty"`
	// NewBrands are created by this import. A misspelling shows up here
	// first; adding it as an alias of the right brand fixes every later run.
	NewBrands []string `json:"new_brands,omitempty"`
	// UnmatchedVehicles are vehicle names that match no model, with the
	// number of rows naming them: the profile's "vehicles" map is where they
	// go.
	UnmatchedVehicles []Count `json:"unmatched_vehicles,omitempty"`
	// VehicleExamples are a few product names per unmatched vehicle name:
	// "Амортизатор багажника TIGGO 4" says what "Chery Tiggo" covers.
	VehicleExamples map[string][]string `json:"vehicle_examples,omitempty"`
	// WithoutFitment counts products that fit no known vehicle and are not
	// universal: a buyer with a car chosen will not see them.
	WithoutFitment int     `json:"without_fitment"`
	Categories     []Count `json:"categories"`
	// Unsorted counts products neither a profile rule nor the tree's
	// keywords recognised, filed in the default category, and Unrecognised
	// shows the first few names: they are what a new keyword is made from.
	Unsorted     int      `json:"unsorted"`
	Unrecognised []string `json:"unrecognised,omitempty"`
	// Pending are the supplier's groups no category is decided for yet:
	// every group of a 1C export until it is mapped, and, for a file
	// without groups, the product names no word recognised.
	Pending []PendingGroup `json:"pending_groups,omitempty"`
}

// GroupKey names a supplier's group: its own group of products, or, in a
// file without groups, a product's name.
type GroupKey struct {
	Kind string // KindGroup or KindName
	Key  string
}

// Kinds of group keys.
const (
	KindGroup = "group"
	KindName  = "name"
)

// PendingGroup is a group waiting for its category, with what tells it
// apart: how many products, and a few of their names.
type PendingGroup struct {
	GroupKey
	Products int      `json:"products"`
	Examples []string `json:"examples,omitempty"`
}

// examplesPerGroup bounds the product names kept per pending group.
const examplesPerGroup = 5

// groupOf is the key a product is mapped by.
func groupOf(o *Offer) GroupKey {
	if g := clean(o.Category); g != "" {
		return GroupKey{Kind: KindGroup, Key: g}
	}
	return GroupKey{Kind: KindName, Key: o.Name}
}

// Result is what Apply wrote.
type Result struct {
	Inserted, Updated, Deactivated, Fitments int
	RunID                                    int64 // 0 for a dry run
}

// Run imports a file. With DryRun nothing is kept, but the counts are those a
// real run would produce. A run stopped by the deactivation limit returns
// those counts with a *DeactivationError.
func Run(ctx context.Context, store Store, p *Supplier, path string, opts Options) (*Report, Result, error) {
	started := time.Now()
	parsed, err := readAndParse(path, p)
	if err != nil {
		return nil, Result{}, err
	}
	b, err := Resolve(ctx, store, p, parsed)
	if err != nil {
		return nil, Result{}, err
	}
	b.StartedAt = started
	b.Report.File = filepath.Base(path)
	b.Force = opts.Force
	res, err := store.Apply(ctx, b, opts.DryRun)
	return b.Report, res, err
}

// Resolve turns parsed offers into catalogue products.
func Resolve(ctx context.Context, store Store, p *Supplier, parsed *Parsed) (*Batch, error) {
	cats, err := store.Categories(ctx)
	if err != nil {
		return nil, err
	}
	if !Leaves(cats)[defaultCategory] {
		return nil, fmt.Errorf("importer: no category %q for the rows nothing recognises", defaultCategory)
	}
	byID := make(map[string]*Category, len(cats))
	for i := range cats {
		byID[cats[i].Slug] = &cats[i]
	}
	classifier := NewClassifier(cats)
	brands, err := resolveBrands(ctx, store, parsed.Offers)
	if err != nil {
		return nil, err
	}
	vehicles, err := newVehicleResolver(ctx, store, p)
	if err != nil {
		return nil, err
	}
	groups := map[GroupKey]string{}
	if p.ID != 0 {
		if groups, err = store.GroupCategories(ctx, p.ID); err != nil {
			return nil, err
		}
	}
	leaves := Leaves(cats)
	pending := map[GroupKey]*PendingGroup{}
	var pendingOrder []GroupKey
	addPending := func(k GroupKey, name string) {
		pg, ok := pending[k]
		if !ok {
			pg = &PendingGroup{GroupKey: k}
			pending[k] = pg
			pendingOrder = append(pendingOrder, k)
		}
		pg.Products++
		if len(pg.Examples) < examplesPerGroup && !slices.Contains(pg.Examples, name) {
			pg.Examples = append(pg.Examples, name)
		}
	}

	rep := &Report{Rows: parsed.Rows, Offers: len(parsed.Offers), Skipped: len(parsed.Problems)}
	rep.Problems = parsed.Problems[:min(len(parsed.Problems), maxProblems)]
	b := &Batch{
		Source:               p.Source,
		DeactivateMissing:    true,
		MaxDeactivatePercent: maxDeactivatePercent,
		Report:               rep,
		NewBrands:            brands.created,
	}
	for _, nb := range brands.created {
		rep.NewBrands = append(rep.NewBrands, nb.Name)
	}

	categories := make(map[string]int)
	for _, o := range parsed.Offers {
		prod := Product{
			ExternalID:  o.Key,
			SKU:         sku(p, o.Key),
			Name:        o.Name,
			Brand:       brands.canonical(o.Brand),
			Description: o.Desc,
			Image:       o.Image,
			Price:       o.Price,
			Stock:       o.Stock,
			Category:    defaultCategory,
			Group:       o.Category,
		}
		if prod.Stock == nil {
			prod.LeadDays = p.LeadDays
		}
		if pn := domain.NormalizeOEM(o.PartNumber); pn != "" {
			prod.OEM, prod.PartKey = []string{pn}, pn
		}
		// A group mapped to a category decides; otherwise the tree's words
		// do, over the name and the supplier's group. The category gives
		// the part type and universality.
		gk := groupOf(o)
		slug, mapped := groups[gk]
		switch {
		case mapped && leaves[slug]:
			prod.Category = slug
		default:
			c := classifier.Classify(o.Name + " " + o.Category)
			if c != nil {
				prod.Category = c.Slug
			} else {
				rep.Unsorted++
				if len(rep.Unrecognised) < maxProblems && !slices.Contains(rep.Unrecognised, o.Name) {
					rep.Unrecognised = append(rep.Unrecognised, o.Name)
				}
			}
			// A group is mapped whatever its words say: it knows better. A
			// name only when no word knew it.
			if !mapped && (gk.Kind == KindGroup || c == nil) {
				addPending(gk, o.Name)
			}
		}
		if c := byID[prod.Category]; c != nil {
			prod.PartType, prod.Universal = c.PartType, c.Universal
		}
		categories[prod.Category]++

		seen := make(map[int64]bool)
		for _, f := range o.Fits {
			for _, id := range vehicles.resolve(f.Vehicle, o.Name) {
				// Two names of the file may lead to one model.
				if !seen[id] {
					seen[id] = true
					prod.Fits = append(prod.Fits, ModelFit{ModelID: id, YearFrom: f.YearFrom, YearTo: f.YearTo,
						Position: f.Position, Note: f.Note, Via: vehicleKey(f.Vehicle)})
				}
			}
		}
		if len(prod.Fits) == 0 && !prod.Universal {
			rep.WithoutFitment++
		}
		b.Products = append(b.Products, prod)
	}
	for _, k := range pendingOrder {
		rep.Pending = append(rep.Pending, *pending[k])
	}
	rep.UnmatchedVehicles = sortedCounts(vehicles.unmatched)
	rep.VehicleExamples = vehicles.examples
	rep.Categories = sortedCounts(categories)
	return b, nil
}

// sku builds the shop's code for a new product from the supplier's key. It
// is stable -- the same offer gets the same code on every machine -- and 50
// bits of hash keep 100 000 offers clear of collisions.
func sku(p *Supplier, key string) string {
	sum := sha256.Sum256([]byte(p.Source + "\x00" + key))
	return p.skuPrefix() + "-" + base32.StdEncoding.EncodeToString(sum[:])[:10]
}

// brandKey reduces a brand to letters and digits in upper case: "K.Y.B.",
// "kyb" and "KYB " are one brand. The migration builds the first aliases with
// the same rule.
func brandKey(s string) string { return domain.NormalizeOEM(s) }

type brandResolver struct {
	byKey   map[string]string
	created []NewBrand
}

// resolveBrands maps every spelling in the file to a brand, creating brands
// for keys never seen before. A new brand takes the spelling most rows use:
// "Mogen" over "mogen" when the supplier mostly writes it so.
func resolveBrands(ctx context.Context, store Store, offers []*Offer) (*brandResolver, error) {
	known, err := store.BrandAliases(ctx)
	if err != nil {
		return nil, err
	}
	spellings := make(map[string]map[string]int)
	for _, o := range offers {
		k := brandKey(o.Brand)
		if k == "" {
			continue
		}
		if _, ok := known[k]; ok {
			continue
		}
		if spellings[k] == nil {
			spellings[k] = make(map[string]int)
		}
		spellings[k][o.Brand]++
	}
	r := &brandResolver{byKey: known}
	for k, s := range spellings {
		name := mostCommon(s)
		r.byKey[k] = name
		r.created = append(r.created, NewBrand{Name: name, Alias: k})
	}
	slices.SortFunc(r.created, func(a, b NewBrand) int { return cmp.Compare(a.Name, b.Name) })
	return r, nil
}

func (r *brandResolver) canonical(brand string) string { return r.byKey[brandKey(brand)] }

func mostCommon(counts map[string]int) string {
	best, n := "", 0
	for s, c := range counts {
		if c > n || (c == n && s < best) {
			best, n = s, c
		}
	}
	return best
}

// makeAbbreviations are how price lists shorten makes the directory spells
// out.
var makeAbbreviations = map[string]string{
	"vw": "Volkswagen", "фольксваген": "Volkswagen", "mercedes": "Mercedes-Benz", "mb": "Mercedes-Benz",
	"мерседес": "Mercedes-Benz", "шкода": "Skoda", "шевроле": "Chevrolet", "деу": "Daewoo", "дэу": "Daewoo",
	"чері": "Chery", "чери": "Chery", "джилі": "Geely", "джили": "Geely",
}

// expandMake writes the make of a vehicle name in full: "VW Golf VII" is
// "Volkswagen Golf VII".
func expandMake(name string) string {
	first, rest, _ := strings.Cut(strings.TrimSpace(name), " ")
	if full, ok := makeAbbreviations[strings.ToLower(first)]; ok {
		return full + " " + rest
	}
	return name
}

// vehicleKey compares vehicle names by letters and digits only, in lower
// case: "Chery Tiggo-7" and "chery tiggo 7" are one name.
func vehicleKey(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

type vehicleResolver struct {
	byName    map[string][]int64 // "make model" key
	byModel   map[string][]int64 // "model" key, for files that omit the make
	mapped    map[string][]int64 // the profile's own map, then the owner's
	unmatched map[string]int
	examples  map[string][]string
}

func newVehicleResolver(ctx context.Context, store Store, p *Supplier) (*vehicleResolver, error) {
	models, err := store.VehicleModels(ctx)
	if err != nil {
		return nil, err
	}
	r := &vehicleResolver{
		byName:    make(map[string][]int64),
		byModel:   make(map[string][]int64),
		mapped:    make(map[string][]int64),
		unmatched: make(map[string]int),
		examples:  make(map[string][]string),
	}
	for _, m := range models {
		k := vehicleKey(m.Make + m.Model)
		r.byName[k] = append(r.byName[k], m.ID)
		mk := vehicleKey(m.Model)
		r.byModel[mk] = append(r.byModel[mk], m.ID)
	}
	// The names the owner mapped in the chat, for this supplier.
	aliases, err := store.VehicleAliases(ctx, p.Source)
	if err != nil {
		return nil, err
	}
	maps.Copy(r.mapped, aliases)
	return r, nil
}

// examplesPerVehicle bounds the product names kept per unmatched name.
const examplesPerVehicle = 3

// resolve finds the models a vehicle name means: the profile's map first,
// then an exact "Make Model", then a model name alone if only one make has
// it. Anything else is reported, not guessed: a wrong fitment sells a part
// that does not fit.
func (r *vehicleResolver) resolve(name, product string) []int64 {
	k := vehicleKey(expandMake(name))
	if ids, ok := r.mapped[k]; ok {
		return ids
	}
	if ids, ok := r.byName[k]; ok {
		return ids
	}
	if ids := r.byModel[k]; len(ids) == 1 {
		return ids
	}
	r.unmatched[name]++
	if ex := r.examples[name]; len(ex) < examplesPerVehicle && !slices.Contains(ex, product) {
		r.examples[name] = append(ex, product)
	}
	return nil
}

func sortedCounts(m map[string]int) []Count {
	out := make([]Count, 0, len(m))
	for name, n := range m {
		out = append(out, Count{Name: name, N: n})
	}
	slices.SortFunc(out, func(a, b Count) int {
		return cmp.Or(cmp.Compare(b.N, a.N), cmp.Compare(a.Name, b.Name))
	})
	return out
}
