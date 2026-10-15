package importer

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
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
}

// Store is what an import needs from the database.
type Store interface {
	// BrandAliases maps every known spelling, as a brand key, to the
	// canonical brand name.
	BrandAliases(ctx context.Context) (map[string]string, error)
	VehicleModels(ctx context.Context) ([]Model, error)
	// CategorySlugs lists the categories that exist.
	CategorySlugs(ctx context.Context) ([]string, error)
	// Apply writes the batch in one transaction, or only measures what it
	// would write when dryRun is set.
	Apply(ctx context.Context, b *Batch, dryRun bool) (Result, error)
}

// Batch is a file resolved against the catalogue, ready to be written.
type Batch struct {
	Source            string
	Products          []Product
	NewBrands         []NewBrand
	CreateCategories  map[string]string
	DeactivateMissing bool
	Report            *Report
	StartedAt         time.Time
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
	PartType    string
	Universal   bool
	OEM         []string
	Price       domain.Money
	Stock       *int // nil: on order
	LeadDays    *int
	Fits        []ModelFit
}

// ModelFit is a fitment to a model of the catalogue.
type ModelFit struct {
	ModelID          int64
	YearFrom, YearTo int
	Position, Note   string
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
	// WithoutFitment counts products that fit no known vehicle and are not
	// universal: a buyer with a car chosen will not see them.
	WithoutFitment int     `json:"without_fitment"`
	Categories     []Count `json:"categories"`
}

// Result is what Apply wrote.
type Result struct {
	Inserted, Updated, Deactivated, Fitments int
	RunID                                    int64 // 0 for a dry run
}

// Run imports a file. With dryRun nothing is kept, but the counts are those a
// real run would produce.
func Run(ctx context.Context, store Store, p *Profile, path string, dryRun bool) (*Report, Result, error) {
	started := time.Now()
	sheets, err := ReadFile(path, p.delimiter())
	if err != nil {
		return nil, Result{}, err
	}
	parsed, err := Parse(sheets, p)
	if err != nil {
		return nil, Result{}, err
	}
	b, err := Resolve(ctx, store, p, parsed)
	if err != nil {
		return nil, Result{}, err
	}
	b.StartedAt = started
	b.Report.File = filepath.Base(path)
	res, err := store.Apply(ctx, b, dryRun)
	if err != nil {
		return b.Report, Result{}, err
	}
	return b.Report, res, nil
}

// Resolve turns parsed offers into catalogue products.
func Resolve(ctx context.Context, store Store, p *Profile, parsed *Parsed) (*Batch, error) {
	if err := checkCategories(ctx, store, p); err != nil {
		return nil, err
	}
	brands, err := resolveBrands(ctx, store, parsed.Offers)
	if err != nil {
		return nil, err
	}
	vehicles, err := newVehicleResolver(ctx, store, p)
	if err != nil {
		return nil, err
	}

	rep := &Report{Rows: parsed.Rows, Offers: len(parsed.Offers), Skipped: len(parsed.Problems)}
	rep.Problems = parsed.Problems[:min(len(parsed.Problems), maxProblems)]
	b := &Batch{
		Source:            p.Source,
		CreateCategories:  p.CreateCategories,
		DeactivateMissing: p.DeactivateMissing,
		Report:            rep,
		NewBrands:         brands.created,
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
			Category:    p.DefaultCategory,
		}
		if prod.Stock == nil {
			prod.LeadDays = p.LeadTimeDays
		}
		if pn := domain.NormalizeOEM(o.PartNumber); pn != "" {
			prod.OEM = []string{pn}
		}
		if r := matchRule(p.Rules, o); r != nil {
			prod.Category, prod.PartType, prod.Universal = r.Category, r.PartType, r.Universal
		}
		categories[prod.Category]++

		seen := make(map[int64]bool)
		for _, f := range o.Fits {
			for _, id := range vehicles.resolve(f.Vehicle) {
				// Two names of the file may lead to one model.
				if !seen[id] {
					seen[id] = true
					prod.Fits = append(prod.Fits, ModelFit{ModelID: id, YearFrom: f.YearFrom, YearTo: f.YearTo, Position: f.Position, Note: f.Note})
				}
			}
		}
		if len(prod.Fits) == 0 && !prod.Universal {
			rep.WithoutFitment++
		}
		b.Products = append(b.Products, prod)
	}
	rep.UnmatchedVehicles = sortedCounts(vehicles.unmatched)
	rep.Categories = sortedCounts(categories)
	return b, nil
}

// checkCategories fails before any work when a profile names a category that
// does not exist: every row of it would land nowhere.
func checkCategories(ctx context.Context, store Store, p *Profile) error {
	slugs, err := store.CategorySlugs(ctx)
	if err != nil {
		return err
	}
	exists := make(map[string]bool, len(slugs))
	for _, s := range slugs {
		exists[s] = true
	}
	for s := range p.CreateCategories {
		exists[s] = true
	}
	var errs []error
	if !exists[p.DefaultCategory] {
		errs = append(errs, fmt.Errorf("default_category %q does not exist", p.DefaultCategory))
	}
	for i, r := range p.Rules {
		if !exists[r.Category] {
			errs = append(errs, fmt.Errorf("rules[%d]: category %q does not exist", i, r.Category))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("importer: %w (add it to create_categories, or use one of %s)", err, strings.Join(slugs, ", "))
	}
	return nil
}

// matchRule finds the first rule whose words occur in the row's name or
// supplier category.
func matchRule(rules []Rule, o *Offer) *Rule {
	text := strings.ToLower(o.Name + " " + o.Category)
	for i := range rules {
		for _, w := range rules[i].Contains {
			if strings.Contains(text, strings.ToLower(w)) {
				return &rules[i]
			}
		}
	}
	return nil
}

// sku builds the shop's code for a new product from the supplier's key. It
// is stable -- the same offer gets the same code on every machine -- and 50
// bits of hash keep 100 000 offers clear of collisions.
func sku(p *Profile, key string) string {
	sum := sha256.Sum256([]byte(p.Source + "\x00" + key))
	return p.SKUPrefix + "-" + base32.StdEncoding.EncodeToString(sum[:])[:10]
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
	mapped    map[string][]int64 // the profile's own map
	unmatched map[string]int
}

func newVehicleResolver(ctx context.Context, store Store, p *Profile) (*vehicleResolver, error) {
	models, err := store.VehicleModels(ctx)
	if err != nil {
		return nil, err
	}
	r := &vehicleResolver{
		byName:    make(map[string][]int64),
		byModel:   make(map[string][]int64),
		mapped:    make(map[string][]int64),
		unmatched: make(map[string]int),
	}
	for _, m := range models {
		k := vehicleKey(m.Make + m.Model)
		r.byName[k] = append(r.byName[k], m.ID)
		mk := vehicleKey(m.Model)
		r.byModel[mk] = append(r.byModel[mk], m.ID)
	}
	var errs []error
	for name, targets := range p.Vehicles {
		for _, t := range targets {
			ids, ok := r.byName[vehicleKey(t)]
			if !ok {
				errs = append(errs, fmt.Errorf("vehicles[%q]: no model %q in the catalogue", name, t))
				continue
			}
			k := vehicleKey(name)
			r.mapped[k] = append(r.mapped[k], ids...)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("importer: %w", err)
	}
	return r, nil
}

// resolve finds the models a vehicle name means: the profile's map first,
// then an exact "Make Model", then a model name alone if only one make has
// it. Anything else is reported, not guessed: a wrong fitment sells a part
// that does not fit.
func (r *vehicleResolver) resolve(name string) []int64 {
	k := vehicleKey(name)
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
