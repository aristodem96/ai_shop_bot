// Package directory finds buyers' vehicles in the Auto-DB Pro catalogue: its
// makes, models and modifications, read through a cache of the shop's own.
//
// It is a service.VehicleRepository, so the consultant's find_vehicle, the
// garage and everything that labels a car keep working unchanged; only
// where the vehicles come from changes. A modification a buyer may choose is
// written to the vehicles table with its Auto-DB id, and from there it is a
// vehicle like any other.
package directory

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/aristodem96/ai_shop_bot/internal/autodb"
	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/service"
)

var _ service.VehicleRepository = (*Directory)(nil)

// API is the part of the Auto-DB client the directory reads.
type API interface {
	Makes(ctx context.Context, g autodb.Group) ([]autodb.Make, error)
	Models(ctx context.Context, g autodb.Group, makeID int64) ([]autodb.Model, error)
	Modifications(ctx context.Context, g autodb.Group, modelID int64) ([]autodb.Modification, error)
}

// Store is the cache, and the vehicles table chosen modifications go to.
type Store interface {
	Makes(ctx context.Context) ([]autodb.Make, error)
	SaveMakes(ctx context.Context, makes []autodb.Make) error
	// Models returns a make's models and whether they were ever fetched.
	Models(ctx context.Context, makeID int64) ([]autodb.Model, bool, error)
	SaveModels(ctx context.Context, makeID int64, models []autodb.Model) error
	Modifications(ctx context.Context, modelID int64) ([]autodb.Modification, bool, error)
	SaveModifications(ctx context.Context, modelID int64, mods []autodb.Modification) error
	// UpsertVehicle writes a modification as a vehicle and returns its id.
	// shopModelID links it to the shop's model it is, 0 for none.
	UpsertVehicle(ctx context.Context, v domain.Vehicle, modID, shopModelID int64) (int64, error)
	// ShopModels lists the shop's own models of a make that price lists fit
	// parts to.
	ShopModels(ctx context.Context) ([]ShopModel, error)
	Vehicle(ctx context.Context, id int64) (*domain.Vehicle, error)
}

// ShopModel is a model of the shop's own vehicle list.
type ShopModel struct {
	ID    int64
	Make  string
	Model string
}

// Directory finds vehicles in the Auto-DB catalogue.
type Directory struct {
	api   API
	store Store
	log   *slog.Logger
	// fallback finds what the catalogue lacks: the shop's own list.
	fallback service.VehicleRepository
}

// New returns a Directory.
func New(api API, store Store, log *slog.Logger) *Directory {
	return &Directory{api: api, store: store, log: log}
}

// WithFallback makes the shop's own vehicle list answer for the cars the
// catalogue does not have. Auto-DB has no Chery, BYD or JAC, which a shop
// selling Chinese parts lives on.
func (d *Directory) WithFallback(r service.VehicleRepository) *Directory {
	d.fallback = r
	return d
}

// maxModels bounds the models one lookup reads modifications of: "Golf"
// matches eight generations, and each is a call to a slow API the first
// time.
const maxModels = 4

// FindVehicles finds modifications matching what the buyer said. The make
// is required -- "1.6" alone is every car there is -- and the model is
// matched as a fragment, generation codes and all: "lacetti" finds
// "LACETTI (J200)" and its estate.
func (d *Directory) FindVehicles(ctx context.Context, q service.VehicleQuery, limit int) ([]domain.Vehicle, error) {
	found, err := d.fromCatalog(ctx, q, limit)
	if err != nil || len(found) > 0 || d.fallback == nil {
		return found, err
	}
	return d.fallback.FindVehicles(ctx, q, limit)
}

func (d *Directory) fromCatalog(ctx context.Context, q service.VehicleQuery, limit int) ([]domain.Vehicle, error) {
	if strings.TrimSpace(q.Make) == "" {
		return nil, nil
	}
	mk, ok, err := d.findMake(ctx, q.Make)
	if err != nil || !ok {
		return nil, err
	}
	models, err := d.models(ctx, mk.ID)
	if err != nil {
		return nil, err
	}
	want := key(q.Model)
	var picked []autodb.Model
	for _, m := range models {
		if (want == "" || strings.Contains(key(m.Name), want)) && yearsOverlap(m.Years, q.Year) {
			picked = append(picked, m)
		}
	}
	if len(picked) > maxModels {
		picked = picked[:maxModels]
	}

	shop, err := d.store.ShopModels(ctx)
	if err != nil {
		return nil, err
	}
	var out []domain.Vehicle
	for _, m := range picked {
		mods, err := d.modifications(ctx, m.ID)
		if err != nil {
			return nil, err
		}
		shopModel := LinkModel(mk.Name, m.Name, shop)
		for _, mod := range mods {
			v := ToVehicle(mk.Name, m, mod)
			if !engineMatches(q.Engine, mod, v) || !yearIn(q.Year, v) {
				continue
			}
			id, err := d.store.UpsertVehicle(ctx, v, mod.ID, shopModel)
			if err != nil {
				return nil, err
			}
			v.ID = id
			out = append(out, v)
			if len(out) == limit {
				return out, nil
			}
		}
	}
	return out, nil
}

// Makes lists the makes the directory covers, the shop's own included.
func (d *Directory) Makes(ctx context.Context) ([]string, error) {
	makes, err := d.makes(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(makes))
	seen := make(map[string]bool, len(makes))
	for _, m := range makes {
		names = append(names, titleCase(m.Name))
		seen[canonMake(m.Name)] = true
	}
	if d.fallback != nil {
		own, err := d.fallback.Makes(ctx)
		if err != nil {
			return nil, err
		}
		for _, n := range own {
			if !seen[canonMake(n)] {
				names = append(names, n)
				seen[canonMake(n)] = true
			}
		}
	}
	slices.Sort(names)
	return names, nil
}

// Vehicle returns a vehicle chosen earlier.
func (d *Directory) Vehicle(ctx context.Context, id int64) (*domain.Vehicle, error) {
	return d.store.Vehicle(ctx, id)
}

// Warm fills the cache with every make and every make's models, so the
// first buyer to name a car does not wait for them. It runs in the
// background at start; a make that fails is logged and left for a lookup to
// fetch.
func (d *Directory) Warm(ctx context.Context) {
	ctx = autodb.Background(ctx)
	makes, err := d.makes(ctx)
	if err != nil {
		d.log.WarnContext(ctx, "could not warm the vehicle directory", slog.Any("error", err))
		return
	}
	for _, m := range makes {
		if ctx.Err() != nil {
			return
		}
		if _, err := d.models(ctx, m.ID); err != nil {
			d.log.WarnContext(ctx, "could not warm the models of a make", slog.String("make", m.Name), slog.Any("error", err))
		}
	}
	d.log.InfoContext(ctx, "vehicle directory warmed", slog.Int("makes", len(makes)))
}

func (d *Directory) makes(ctx context.Context) ([]autodb.Make, error) {
	makes, err := d.store.Makes(ctx)
	if err != nil || len(makes) > 0 {
		return makes, err
	}
	if makes, err = d.api.Makes(ctx, autodb.Passenger); err != nil {
		return nil, err
	}
	return makes, d.store.SaveMakes(ctx, makes)
}

func (d *Directory) models(ctx context.Context, makeID int64) ([]autodb.Model, error) {
	models, fetched, err := d.store.Models(ctx, makeID)
	if err != nil || fetched {
		return models, err
	}
	if models, err = d.api.Models(ctx, autodb.Passenger, makeID); err != nil {
		return nil, err
	}
	return models, d.store.SaveModels(ctx, makeID, models)
}

func (d *Directory) modifications(ctx context.Context, modelID int64) ([]autodb.Modification, error) {
	mods, fetched, err := d.store.Modifications(ctx, modelID)
	if err != nil || fetched {
		return mods, err
	}
	if mods, err = d.api.Modifications(ctx, autodb.Passenger, modelID); err != nil {
		return nil, err
	}
	return mods, d.store.SaveModifications(ctx, modelID, mods)
}

// makeSynonyms are what buyers call makes the catalogue names otherwise.
var makeSynonyms = map[string]string{
	"volkswagen": "vw", "фольксваген": "vw", "фольцваген": "vw",
	"mercedes": "mercedesbenz", "мерседес": "mercedesbenz",
	"шкода": "skoda", "тойота": "toyota", "рено": "renault", "хюндай": "hyundai", "хендай": "hyundai",
	"кіа": "kia", "киа": "kia", "форд": "ford", "шевроле": "chevrolet", "деу": "daewoo", "дэу": "daewoo",
	"ніссан": "nissan", "ниссан": "nissan", "мазда": "mazda", "опель": "opel", "пежо": "peugeot",
	"сітроен": "citroen", "ситроен": "citroen", "ауді": "audi", "ауди": "audi", "бмв": "bmw",
	"міцубісі": "mitsubishi", "мицубиси": "mitsubishi", "хонда": "honda", "субару": "subaru",
	"чері": "chery", "чери": "chery", "джилі": "geely", "джили": "geely", "заз": "zaz", "ваз": "lada", "лада": "lada",
}

// findMake matches a make by name, then by synonym, then by a unique
// prefix: "merc" is Mercedes-Benz, "m" is nothing.
func (d *Directory) findMake(ctx context.Context, name string) (autodb.Make, bool, error) {
	makes, err := d.makes(ctx)
	if err != nil {
		return autodb.Make{}, false, err
	}
	want := canonMake(name)
	var prefixed []autodb.Make
	for _, m := range makes {
		k := key(m.Name)
		if k == want {
			return m, true, nil
		}
		if len(want) >= 3 && strings.HasPrefix(k, want) {
			prefixed = append(prefixed, m)
		}
	}
	if len(prefixed) == 1 {
		return prefixed[0], true, nil
	}
	return autodb.Make{}, false, nil
}

// ToVehicle turns a modification into the shop's vehicle: "Chevrolet",
// "Lacetti (J200)", 2005–, "1.6 F16D3, 80 kW", petrol. The engine carries the
// code and power because one model has several "1.8"s.
func ToVehicle(makeName string, model autodb.Model, mod autodb.Modification) domain.Vehicle {
	v := domain.Vehicle{Make: titleCase(makeName), Model: titleCase(model.Name), Cylinders: 4}
	v.YearFrom, v.YearTo = parseInterval(first(mod.Attrs["ConstructionInterval"]))
	if v.YearFrom == 0 {
		v.YearFrom, v.YearTo = parseInterval(model.Years)
	}
	v.Engine = engineLabel(model.Name, mod)
	v.Fuel = fuelOf(mod.Attrs)
	if n, err := strconv.Atoi(first(mod.Attrs["NumberOfCylinders"])); err == nil && n > 0 {
		v.Cylinders = n
	}
	return v
}

// engineLabel is what tells modifications of a model apart: the name's tail
// after the model ("1.6", "2.0 D"), the engine code and the power in kW.
func engineLabel(modelName string, mod autodb.Modification) string {
	tail := mod.Name
	if i := strings.Index(strings.ToUpper(tail), strings.ToUpper(modelName)); i >= 0 {
		tail = tail[i+len(modelName):]
	}
	parts := []string{strings.TrimSpace(tail)}
	if code := first(mod.Attrs["EngineCode"]); code != "" {
		parts = append(parts, code)
	}
	label := strings.Join(slices.DeleteFunc(parts, func(s string) bool { return s == "" }), " ")
	for _, p := range mod.Attrs["Power"] {
		if strings.HasSuffix(p, "kW") {
			label += ", " + p
			break
		}
	}
	if label == "" {
		return mod.Name
	}
	return label
}

func fuelOf(attrs map[string][]string) string {
	f := strings.ToLower(first(attrs["FuelType"]) + " " + first(attrs["EngineType"]))
	switch {
	case strings.Contains(f, "электр") || strings.Contains(f, "електр") || strings.Contains(f, "electric"):
		return "electric"
	case strings.Contains(f, "гибрид") || strings.Contains(f, "гібрид") || strings.Contains(f, "hybrid"):
		return "hybrid"
	case strings.Contains(f, "дизел") || strings.Contains(f, "diesel"):
		return "diesel"
	}
	return "petrol"
}

// engineMatches reports whether the buyer's engine words fit a
// modification: a displacement ("1.6") compares with its capacity, anything
// else is a fragment of the engine label ("tdi", "f16d3").
func engineMatches(want string, mod autodb.Modification, v domain.Vehicle) bool {
	want = strings.TrimSpace(strings.ReplaceAll(want, ",", "."))
	if want == "" {
		return true
	}
	// "дизель", "diesel", "бензин": the fuel, not a fragment of the label.
	if fuel, ok := fuelWords[key(want)]; ok {
		return v.Fuel == fuel
	}
	if d, err := strconv.ParseFloat(strings.Fields(want)[0], 64); err == nil && d > 0 && d < 10 {
		capacity := strings.TrimSuffix(first(mod.Attrs["Capacity"]), " l")
		if c, err := strconv.ParseFloat(strings.ReplaceAll(capacity, ",", "."), 64); err == nil {
			if fmt.Sprintf("%.1f", c) != fmt.Sprintf("%.1f", d) {
				return false
			}
			rest := strings.Join(strings.Fields(want)[1:], " ")
			return rest == "" || strings.Contains(key(v.Engine+mod.Name), key(rest))
		}
	}
	return strings.Contains(key(v.Engine+" "+mod.Name), key(want))
}

// fuelWords are how buyers name a fuel.
var fuelWords = map[string]string{
	"diesel": "diesel", "дизель": "diesel", "дизельний": "diesel", "дизельный": "diesel", "tdi": "diesel",
	"petrol": "petrol", "бензин": "petrol", "бензиновий": "petrol", "gasoline": "petrol",
	"hybrid": "hybrid", "гібрид": "hybrid", "гибрид": "hybrid",
	"electric": "electric", "електро": "electric", "электро": "electric", "ev": "electric",
}

func yearIn(year int, v domain.Vehicle) bool {
	return year == 0 || (year >= v.YearFrom && (v.YearTo == 0 || year <= v.YearTo))
}

func yearsOverlap(span string, year int) bool {
	if year == 0 {
		return true
	}
	from, to := parseInterval(span)
	return from == 0 || (year >= from && (to == 0 || year <= to))
}

var yearRe = regexp.MustCompile(`(?:\d{2}\.)?(\d{4})`)

// parseInterval reads "03.2005 - 12.2009" or "03.2005 - " into years.
func parseInterval(s string) (from, to int) {
	a, b, _ := strings.Cut(s, "-")
	if m := yearRe.FindStringSubmatch(a); m != nil {
		from, _ = strconv.Atoi(m[1])
	}
	if m := yearRe.FindStringSubmatch(b); m != nil {
		to, _ = strconv.Atoi(m[1])
	}
	return from, to
}

// key compares names by letters and digits only, in lower case.
// canonMake is a make's key with buyers' and price lists' names mapped to
// the catalogue's: "Volkswagen" and "VW" are one make.
func canonMake(s string) string {
	k := key(s)
	if c, ok := makeSynonyms[k]; ok {
		return c
	}
	return k
}

// LinkModel finds the shop's model an Auto-DB model is: the one whose every
// word is a word of the catalogue's name, the most specific when several
// are. "LACETTI (J200)" is the shop's "Lacetti"; "AVEO / KALOS Hatchback
// (T200)" is "Aveo T200" and not "Aveo T250". Zero when none is.
func LinkModel(makeName, modelName string, shop []ShopModel) int64 {
	words := map[string]bool{}
	for _, w := range splitWords(modelName) {
		words[w] = true
	}
	mk := canonMake(makeName)
	var (
		best      int64
		bestScore int
	)
	for _, m := range shop {
		if canonMake(m.Make) != mk {
			continue
		}
		score := 0
		for _, w := range splitWords(m.Model) {
			if !words[w] {
				score = 0
				break
			}
			score += len(w)
		}
		if score > bestScore {
			best, bestScore = m.ID, score
		}
	}
	return best
}

// splitWords cuts a name into lower-case words of letters and digits.
func splitWords(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

func key(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// titleCase turns the catalogue's capitals into a name: "CHEVROLET" is
// "Chevrolet", "LACETTI (J200)" is "Lacetti (J200)". Codes with digits, and
// short all-letter names like "VW" or "BMW", stay as they are.
func titleCase(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		core := strings.Trim(w, "()")
		if strings.ContainsAny(core, "0123456789") || len([]rune(core)) <= 3 {
			continue
		}
		// Each part of a hyphenated word: "Mercedes-Benz", "Rolls-Royce".
		r := []rune(strings.ToLower(w))
		start := true
		for j, c := range r {
			if unicode.IsLetter(c) && start {
				r[j] = unicode.ToUpper(c)
			}
			start = c == '-' || (start && !unicode.IsLetter(c))
		}
		words[i] = string(r)
	}
	return strings.Join(words, " ")
}

func first(vs []string) string {
	if len(vs) == 0 {
		return ""
	}
	return vs[0]
}
