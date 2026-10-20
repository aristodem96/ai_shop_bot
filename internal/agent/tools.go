package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/llm"
	"github.com/aristodem96/ai_shop_bot/internal/service"
)

// maxShown caps the cards one answer can carry. More than five in a row bury
// the conversation.
const maxShown = 5

// maxCatalogFits caps the catalogue's models get_part returns: enough to
// answer "will it fit my Nubira?", not a page of tokens.
const maxCatalogFits = 30

// maxAddQty caps one add_to_cart call: a set of plugs or a few cans of oil,
// not a trade order.
const maxAddQty = 6

// runState is what the tools accumulate during one buyer message.
// toolShowProducts puts cards under the answer. A step that calls only it,
// with text, is the answer itself; see Agent.Reply.
const toolShowProducts = "show_products"

type runState struct {
	userID int64
	// question is the buyer's message this turn answers.
	question    string
	shown       []int64
	shownParts  []int64
	cartChanged bool
	carSaved    bool
}

type handler func(ctx context.Context, run *runState, input json.RawMessage) (any, error)

// toolbox holds the tool definitions sent to the model and their handlers.
type toolbox struct {
	defs     []llm.Tool
	handlers map[string]handler
	catalog  Catalog
	cart     Cart
	garage   Garage
	kits     Maintenance
	orders   Orders
	parts    OrderCatalog  // nil: stock only
	fits     CatalogFits   // nil: no catalogue fits
	loader   CatalogLoader // nil: catalogues load only when a car is chosen
	now      func() time.Time
	// catSlug maps category ids to the slugs the model knows them by.
	catSlug map[int64]string
}

func newToolbox(catalog Catalog, cart Cart, garage Garage, kits Maintenance, orders Orders, cats []domain.Category) *toolbox {
	tb := &toolbox{catalog: catalog, cart: cart, garage: garage, kits: kits, orders: orders, now: time.Now,
		catSlug: make(map[int64]string, len(cats))}
	// The model sees the tree as "section: sub, sub": a section's slug
	// searches everything under it, a subcategory's only itself.
	slugs := make([]string, 0, len(cats))
	catalogue := make([]string, 0, len(cats))
	for _, c := range cats {
		tb.catSlug[c.ID] = c.Slug
		slugs = append(slugs, c.Slug)
		if c.ParentID != 0 {
			continue
		}
		var subs []string
		for _, sub := range cats {
			if sub.ParentID == c.ID {
				subs = append(subs, sub.Slug)
			}
		}
		entry := c.Slug + " (" + c.Title + ")"
		if len(subs) > 0 {
			entry += ": " + strings.Join(subs, ", ")
		}
		catalogue = append(catalogue, entry)
	}

	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	integer := func(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }
	object := func(props map[string]any, required ...string) map[string]any {
		s := map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			s["required"] = required
		}
		return s
	}

	tb.defs = []llm.Tool{
		{
			Name: "find_vehicle",
			Description: "Find vehicles by make, model, year and engine, in the parts catalogue of every car and the shop's own list. " +
				"Returns the matching vehicles with their ids. With no match: the make's models as the catalogue names them, " +
				"to call again with the exact name -- a generation code a buyer uses (\"T4\", \"E46\", \"W210\", \"B5\") is often " +
				"not in it, the catalogue writing \"Transporter IV\" or \"3 (E46)\" -- or, for an unknown make, the makes known by a similar name.",
			Schema: object(map[string]any{
				"make":   str("Make in Latin letters, e.g. \"Skoda\"."),
				"model":  str("Model in Latin letters, e.g. \"Octavia\": a word of the catalogue's model name. Leave out a generation code the name may not hold; the year narrows the generation."),
				"year":   integer("Year of manufacture."),
				"engine": str("Engine as the buyer gives it: a displacement (\"1.6\"), an engine code (\"F16D3\") or a fuel (\"дизель\")."),
			}),
		},
		{
			Name: "decode_vin",
			Description: "Read a VIN (17 characters): returns the make and the possible model years. " +
				"Model and engine cannot be read from a VIN; use find_vehicle with the make and year, and ask the buyer for the engine if several match.",
			Schema: object(map[string]any{"vin": str("The VIN as the buyer wrote it.")}, "vin"),
		},
		{
			Name:        "get_my_vehicle",
			Description: "Get the car the buyer saved in the shop, or null if none.",
			Schema:      object(map[string]any{}),
		},
		{
			Name:        "set_my_vehicle",
			Description: "Save a vehicle as the buyer's car, so the catalogue filters by it. Use it whenever the buyer names their car.",
			Schema:      object(map[string]any{"vehicle_id": integer("Vehicle id from find_vehicle.")}, "vehicle_id"),
		},
		{
			Name: "search_parts",
			Description: "Search the catalogue. Returns only parts that can be bought (in stock or on order), cheapest first, or best keyword match " +
				"first when query is set. With vehicle_id it also returns catalog_parts: parts the manufacturers' catalogue lists for the car " +
				"that the shop does not stock, which the shop can order on request. Categories, as section: its subcategories (a section finds everything under it): " +
				strings.Join(catalogue, "; ") + ".",
			Schema: object(map[string]any{
				"vehicle_id":    integer("Only parts that fit this vehicle; universal products always match."),
				"category":      map[string]any{"type": "string", "enum": slugs, "description": "Kind of part."},
				"query":         str("The kind of part as short word stems, named every way a seller would know it, comma-separated: in Ukrainian and in Russian, and by common synonyms, e.g. \"гальмівн диск, тормозн диск\" or \"кульов опор, шаров опор, шарнир\". The shop's and the catalogue's names are in either language, so always give both. Leave empty when category says it all."),
				"part_number":   str("An OEM or shop part number in any spelling."),
				"brand":         str("Part brand."),
				"min_price_uah": integer("Lowest price in hryvnias."),
				"max_price_uah": integer("Highest price in hryvnias."),
				"limit":         integer("How many results, 1-10. Default 5."),
			}),
		},
		{
			Name: "get_part",
			Description: "Get the full details of one part: description, specs, OEM numbers, the cars it fits and whether it fits the buyer's saved car. " +
				"catalog_fits are the cars the manufacturers' catalogue lists the part for, one model per line.",
			Schema: object(map[string]any{"product_id": integer("Product id from search results.")}, "product_id"),
		},
		{
			Name: "service_kit",
			Description: "Get the routine maintenance kit for a vehicle: engine oil of the right grade and amount, " +
				"oil, air and cabin filters, and spark plugs for a petrol engine, with quantities and the total. " +
				"Use it for questions about a service, an oil change or \"ТО\".",
			Schema: object(map[string]any{"vehicle_id": integer("Vehicle id.")}, "vehicle_id"),
		},
		{
			Name: toolShowProducts,
			Description: "Show parts to the buyer under your reply: products as cards with photo, price, stock and fit, " +
				"catalogue parts as buttons to request them on order. Use it whenever you recommend parts. At most 5 of each per reply.",
			Schema: object(map[string]any{
				"product_ids": map[string]any{
					"type": "array", "items": map[string]any{"type": "integer"},
					"description": "Product ids from search results, in the order to show them.",
				},
				"catalog_part_ids": map[string]any{
					"type": "array", "items": map[string]any{"type": "integer"},
					"description": "Ids of catalog_parts from search results.",
				},
			}),
		},
		{
			Name:        "order_status",
			Description: "Get the buyer's latest orders with their status, items, total and delivery address.",
			Schema:      object(map[string]any{}),
		},
		{
			Name:        "add_to_cart",
			Description: "Add a part to the buyer's cart. Only when the buyer asked for it.",
			Schema: object(map[string]any{
				"product_id": integer("Product id."),
				"quantity":   integer("How many units, 1-4. Default 1."),
			}, "product_id"),
		},
	}

	tb.handlers = map[string]handler{
		"find_vehicle":   tb.findVehicle,
		"decode_vin":     tb.decodeVIN,
		"get_my_vehicle": tb.getMyVehicle,
		"set_my_vehicle": tb.setMyVehicle,
		"search_parts":   tb.searchParts,
		"get_part":       tb.getPart,
		"service_kit":    tb.serviceKit,
		toolShowProducts: tb.showProducts,
		"add_to_cart":    tb.addToCart,
		"order_status":   tb.orderStatus,
	}
	return tb
}

// run executes the calls of one model turn. Results are returned for every
// call, failures included: a call left unanswered is a 400 from the API, and an
// error the model can read is what lets it correct itself.
func (tb *toolbox) run(ctx context.Context, run *runState, calls []llm.ToolCall) []llm.ToolResult {
	results := make([]llm.ToolResult, 0, len(calls))
	for _, c := range calls {
		res := llm.ToolResult{CallID: c.ID}
		h, ok := tb.handlers[c.Name]
		var (
			out any
			err error
		)
		if ok {
			out, err = h(ctx, run, c.Input)
		} else {
			err = fmt.Errorf("%w: unknown tool %q", errInput, c.Name)
		}

		if err != nil {
			res.Content, res.IsError = toolError(err), true
		} else {
			raw, merr := json.Marshal(out)
			if merr != nil {
				res.Content, res.IsError = "internal error", true
			} else {
				res.Content = string(raw)
			}
		}
		results = append(results, res)
	}
	return results
}

// errInput marks a problem with the arguments the model sent.
var errInput = errors.New("invalid input")

// toolError words an error for the model. Domain refusals and input problems
// are explained so the model can recover; anything else is an internal fault
// whose details stay in our logs and out of the conversation.
func toolError(err error) string {
	switch {
	case errors.Is(err, errInput):
		return err.Error()
	case errors.Is(err, domain.ErrNotFound):
		return "not found"
	case errors.Is(err, domain.ErrOutOfStock):
		return "not enough in stock"
	case errors.Is(err, domain.ErrQtyLimit):
		return fmt.Sprintf("at most %d units of one part per order", domain.MaxQtyPerLine)
	case errors.Is(err, domain.ErrCartFull):
		return "the cart is full"
	}
	return "internal error, try again or continue without this tool"
}

// decode parses tool input strictly. Unknown fields are an error rather than
// silently ignored, so a misspelled filter cannot quietly widen a search.
func decode(input json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(input))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: %v", errInput, err) //nolint:errorlint // the decoder's message is for the model, not for matching
	}
	return nil
}

// --- views: what the model sees ----------------------------------------------

type vehicleView struct {
	ID    int64  `json:"id"`
	Label string `json:"label"`
	Fuel  string `json:"fuel"`
}

func viewVehicle(v domain.Vehicle) vehicleView {
	return vehicleView{ID: v.ID, Label: v.Label(), Fuel: v.Fuel}
}

type productView struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Brand    string `json:"brand"`
	Category string `json:"category"`
	PriceUAH int64  `json:"price_uah"`
	Stock    string `json:"stock"`
	// LeadDays is how long a part on order takes to arrive.
	LeadDays   int      `json:"lead_days,omitempty"`
	Universal  bool     `json:"universal,omitempty"`
	OEMNumbers []string `json:"oem_numbers,omitempty"`
	Specs      []string `json:"specs,omitempty"`
	// The fields below are filled for get_part only.
	Description   string   `json:"description,omitempty"`
	Fits          []string `json:"fits,omitempty"`
	FitsMyVehicle *bool    `json:"fits_my_vehicle,omitempty"`
	// FitNote is where and when the part fits the buyer's car ("front axle,
	// with ABS"): the detail that tells two fitting parts apart.
	FitNote string `json:"fit_note,omitempty"`
	// CatalogFits are the cars the parts catalogue lists the part for, a
	// model per line; CatalogFitsComplete is false when the catalogue cut
	// the list, so a car missing from it may still fit.
	CatalogFits         []string `json:"catalog_fits,omitempty"`
	CatalogFitsComplete *bool    `json:"catalog_fits_complete,omitempty"`
}

func (tb *toolbox) view(p *domain.Product) productView {
	v := productView{
		ID: p.ID, Name: p.Name, Brand: p.Brand, Category: tb.catSlug[p.CategoryID],
		PriceUAH: int64(p.Price) / 100, Universal: p.Universal, OEMNumbers: p.OEMNumbers,
	}
	switch {
	case p.OnOrder:
		v.Stock, v.LeadDays = "on_order", p.LeadDays
	case p.Stock == 0:
		v.Stock = "out_of_stock"
	case p.Stock <= domain.LowStock:
		v.Stock = "last_units"
	default:
		v.Stock = "in_stock"
	}
	for _, s := range p.Specs {
		v.Specs = append(v.Specs, s.Name+": "+s.Value)
	}
	return v
}

// --- vehicle tools -----------------------------------------------------------

func (tb *toolbox) findVehicle(ctx context.Context, _ *runState, input json.RawMessage) (any, error) {
	var in struct {
		Make   string `json:"make"`
		Model  string `json:"model"`
		Year   int    `json:"year"`
		Engine string `json:"engine"`
	}
	if err := decode(input, &in); err != nil {
		return nil, err
	}
	found, err := tb.garage.Find(ctx, service.VehicleQuery{Make: in.Make, Model: in.Model, Year: in.Year, Engine: in.Engine})
	if err != nil {
		return nil, err
	}
	views := make([]vehicleView, len(found))
	for i, v := range found {
		views[i] = viewVehicle(v)
	}
	out := map[string]any{"vehicles": views}

	if len(found) > 0 || in.Make == "" {
		return out, nil
	}
	// Nothing matched. A known make: its models as the catalogue names
	// them, so a buyer's "T4" can be found as "Transporter IV" in one more
	// call rather than guessed at until the steps run out.
	models, err := tb.garage.Models(ctx, in.Make, in.Year)
	if err != nil {
		return nil, err
	}
	if len(models) > 0 {
		views := make([]modelView, 0, min(len(models), maxModelsListed))
		for _, m := range models[:min(len(models), maxModelsListed)] {
			views = append(views, modelView{Name: m.Name, Years: m.Years})
		}
		out["models_of_make"] = views
		out["hint"] = "No vehicle matched. Pick the buyer's model from models_of_make and call find_vehicle again " +
			"with a distinctive word of its name as model (e.g. \"Transporter IV\" for a T4)."
		return out, nil
	}
	// An unknown make: the makes with a similar name, so the model can spot
	// a misspelt one. Not all of them: the catalogue knows two hundred.
	makes, err := tb.garage.Makes(ctx)
	if err != nil {
		return nil, err
	}
	out["similar_makes"] = similarMakes(in.Make, makes)
	return out, nil
}

// maxModelsListed bounds the models of a make listed for the model to pick
// from: a make like Volkswagen has well over a hundred.
const maxModelsListed = 150

// modelView is a model of a make as the catalogue names it.
type modelView struct {
	Name  string `json:"name"`
	Years string `json:"years,omitempty"`
}

// maxSimilarMakes bounds the makes a failed lookup suggests.
const maxSimilarMakes = 15

// similarMakes are the makes that contain the name asked for, or start with
// its first letter.
func similarMakes(name string, makes []string) []string {
	want := strings.ToLower(strings.TrimSpace(name))
	out := []string{}
	for _, m := range makes {
		lm := strings.ToLower(m)
		if strings.Contains(lm, want) || strings.Contains(want, lm) || (want != "" && lm != "" && lm[0] == want[0]) {
			out = append(out, m)
			if len(out) == maxSimilarMakes {
				break
			}
		}
	}
	return out
}

// decodeVIN reads what a VIN reliably says. An invalid one is an input error
// the model can relay: "a VIN has 17 characters" is useful to the buyer.
func (tb *toolbox) decodeVIN(_ context.Context, _ *runState, input json.RawMessage) (any, error) {
	var in struct {
		VIN string `json:"vin"`
	}
	if err := decode(input, &in); err != nil {
		return nil, err
	}
	v, err := domain.ParseVIN(in.VIN, tb.now().Year()+1)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errInput, err) //nolint:errorlint // the reason is for the model
	}
	out := map[string]any{"vin": v.Code, "wmi": v.WMI, "make": nil, "model_year_candidates": v.ModelYears}
	if v.Make != "" {
		out["make"] = v.Make
	}
	if len(v.ModelYears) == 0 {
		out["note"] = "this VIN does not encode the model year; ask the buyer for it"
	}
	return out, nil
}

func (tb *toolbox) getMyVehicle(ctx context.Context, run *runState, input json.RawMessage) (any, error) {
	if err := decode(input, &struct{}{}); err != nil {
		return nil, err
	}
	car, err := tb.garage.Get(ctx, run.userID)
	if err != nil {
		return nil, err
	}
	if car == nil {
		return map[string]any{"vehicle": nil}, nil
	}
	return map[string]any{"vehicle": viewVehicle(*car)}, nil
}

func (tb *toolbox) setMyVehicle(ctx context.Context, run *runState, input json.RawMessage) (any, error) {
	var in struct {
		VehicleID int64 `json:"vehicle_id"`
	}
	if err := decode(input, &in); err != nil {
		return nil, err
	}
	car, err := tb.garage.Set(ctx, run.userID, in.VehicleID)
	if err != nil {
		return nil, err
	}
	run.carSaved = true
	return map[string]any{"saved": viewVehicle(*car)}, nil
}

// --- catalogue tools ---------------------------------------------------------

func (tb *toolbox) searchParts(ctx context.Context, run *runState, input json.RawMessage) (any, error) {
	var in struct {
		VehicleID   int64  `json:"vehicle_id"`
		Category    string `json:"category"`
		Query       string `json:"query"`
		PartNumber  string `json:"part_number"`
		Brand       string `json:"brand"`
		MinPriceUAH int64  `json:"min_price_uah"`
		MaxPriceUAH int64  `json:"max_price_uah"`
		Limit       int    `json:"limit"`
	}
	if err := decode(input, &in); err != nil {
		return nil, err
	}
	if in.MinPriceUAH < 0 || in.MaxPriceUAH < 0 || (in.MaxPriceUAH > 0 && in.MinPriceUAH > in.MaxPriceUAH) {
		return nil, fmt.Errorf("%w: price range %d-%d", errInput, in.MinPriceUAH, in.MaxPriceUAH)
	}

	res, err := tb.catalog.Search(ctx, service.SearchFilter{
		VehicleID: in.VehicleID, Category: in.Category, Query: in.Query, PartNumber: in.PartNumber,
		Brand: in.Brand, MinPrice: domain.UAH(in.MinPriceUAH), MaxPrice: domain.UAH(in.MaxPriceUAH),
		Limit: in.Limit,
	})
	if err != nil {
		return nil, err
	}

	views := make([]productView, len(res.Products))
	for i := range res.Products {
		views[i] = tb.view(&res.Products[i])
	}
	out := map[string]any{"relaxed": res.Relaxed, "products": views}
	// Parts on order are a car's: the buyer's saved car when the search
	// names none, so a search "in general" does not leave them out.
	orderCar := in.VehicleID
	if orderCar == 0 && tb.parts != nil {
		if car, err := tb.garage.Get(ctx, run.userID); err == nil && car != nil {
			orderCar = car.ID
		}
	}
	if orderCar != 0 && tb.parts != nil {
		if err := tb.addCatalogParts(ctx, run.userID, out, service.SearchFilter{
			VehicleID: orderCar, Category: in.Category, Query: in.Query, PartNumber: in.PartNumber,
			Brand: in.Brand, Limit: maxShown,
		}); err != nil {
			return nil, err
		}
		if _, ok := out["catalog_parts"]; ok && in.VehicleID == 0 {
			out["catalog_parts_vehicle_id"] = orderCar
		}
	}
	return out, nil
}

type catalogPartView struct {
	ID       int64  `json:"catalog_part_id"`
	Name     string `json:"name"`
	Brand    string `json:"brand"`
	Number   string `json:"number"`
	Category string `json:"category,omitempty"`
}

// catalogNotes tell the model what a car's catalogue state means for the
// answer.
var catalogNotes = map[string]string{
	"pending": "the catalogue of this car is being loaded and takes a few minutes; catalog_parts may be incomplete, the buyer can ask again shortly",
	"loading": "the catalogue of this car is being loaded and takes a few minutes; catalog_parts may be incomplete. Show the catalog_parts that match. If none do, do not say the part is unavailable or that there is none: say it is not found yet because the catalogue is still loading. The bot will message the buyer by itself as soon as it is loaded, so tell them that, not to ask again later",
	"failed":  "the catalogue of this car could not be loaded right now; catalog_parts may be incomplete",
}

// maxCatalogShown and maxCatalogKinds bound the catalogue's answer to one
// search: a sample of parts, a few of each kind, and the kinds there are.
const (
	maxCatalogShown = 8
	maxCatalogKinds = 12
)

// catalogKindView is a kind of part the catalogue has for the car.
type catalogKindView struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// carOilGrade is the oil grade of the buyer's saved car, "" when unknown.
func (tb *toolbox) carOilGrade(ctx context.Context, userID int64) string {
	car, err := tb.garage.Get(ctx, userID)
	if err != nil || car == nil {
		return ""
	}
	return car.OilViscosity
}

// otherOilGrade reports whether p is engine oil of a grade other than the
// car's, which the buyer did not ask for by name.
func otherOilGrade(p *domain.Product, carGrade, question string) bool {
	if carGrade == "" || p.PartType != domain.PartEngineOil {
		return false
	}
	g, ok := p.Spec("В'язкість")
	if !ok || gradeKey(g) == gradeKey(carGrade) {
		return false
	}
	return !strings.Contains(gradeKey(question), gradeKey(g))
}

// gradeKey writes an oil grade, or text holding one, without its spelling:
// "5W-30", "5w30" and "5W 30" are "5w30".
func gradeKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// fetchNowWait bounds how long a search waits for the sections it loads
// itself: a few seconds of a buyer's time, inside the answer's deadline.
const fetchNowWait = 20 * time.Second

// addCatalogParts adds what the shop can order for the car beyond its stock.
// Price filters do not apply: these parts have no price until a manager
// quotes one.
func (tb *toolbox) addCatalogParts(ctx context.Context, userID int64, out map[string]any, f service.SearchFilter) error {
	state, err := tb.parts.CrawlState(ctx, f.VehicleID)
	if err != nil || state == "" {
		return err
	}
	switch {
	case state == "done":
	case tb.loader == nil:
		if state != "failed" {
			state = "pending"
		}
	default:
		// Not loaded: never queued, still running, or failed. Queue it
		// again if it must be, and have the buyer told when it is done.
		if err := tb.loader.Watch(ctx, userID, f.VehicleID); err != nil {
			return err
		}
		if f.Category != "" || f.Query != "" {
			fetchCtx, cancel := context.WithTimeout(ctx, fetchNowWait)
			tb.loader.FetchNow(fetchCtx, f.VehicleID, f.Category, f.Query)
			cancel()
		}
		state = "loading"
	}
	f.Limit = maxCatalogShown
	parts, err := tb.parts.ForVehicle(ctx, f)
	if err != nil {
		return err
	}
	views := make([]catalogPartView, len(parts))
	for i, p := range parts {
		views[i] = catalogPartView{ID: p.ID, Name: p.Name, Brand: p.Brand, Number: p.Number, Category: p.Category}
	}
	out["catalog_parts"] = views
	// The parts are a sample, a few of each kind; the kinds and the total
	// say what else there is, so the model never takes the sample for all.
	if len(parts) > 0 {
		kinds, total, err := tb.parts.Kinds(ctx, f, maxCatalogKinds)
		if err != nil {
			return err
		}
		kv := make([]catalogKindView, len(kinds))
		for i, k := range kinds {
			kv[i] = catalogKindView{Name: k.Name, Count: k.Count}
		}
		out["catalog_kinds"], out["catalog_parts_total"] = kv, total
	}
	if note := catalogNotes[state]; note != "" {
		out["catalog_note"] = note
	}
	return nil
}

func (tb *toolbox) getPart(ctx context.Context, run *runState, input json.RawMessage) (any, error) {
	var in struct {
		ProductID int64 `json:"product_id"`
	}
	if err := decode(input, &in); err != nil {
		return nil, err
	}
	p, err := tb.catalog.Product(ctx, in.ProductID)
	if err != nil {
		return nil, err
	}
	v := tb.view(p)
	v.Description = p.Description
	for _, f := range p.Fits {
		label := f.Label()
		if note := p.FitNotes[f.ID]; note != "" {
			label += " — " + note
		}
		v.Fits = append(v.Fits, label)
	}
	// Fit is decided here, from the fitment table, not left for the model to
	// infer from a list of names.
	if car, err := tb.garage.Get(ctx, run.userID); err == nil && car != nil {
		fits := p.FitsVehicle(car.ID)
		v.FitsMyVehicle = &fits
		v.FitNote = p.FitNotes[car.ID]
	}
	if tb.fits != nil {
		f, err := tb.fits.ProductFits(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		if f != nil && len(f.Lines) > 0 {
			v.CatalogFits, v.CatalogFitsComplete = f.Lines[:min(len(f.Lines), maxCatalogFits)], &f.Complete
		}
	}
	return v, nil
}

// showProducts is how the agent puts cards under its answer. Each id is looked
// up: an id the model made up -- or one of a product taken off sale since --
// fails here and never reaches the buyer.
func (tb *toolbox) showProducts(ctx context.Context, run *runState, input json.RawMessage) (any, error) {
	var in struct {
		ProductIDs     []int64 `json:"product_ids"`
		CatalogPartIDs []int64 `json:"catalog_part_ids"`
	}
	if err := decode(input, &in); err != nil {
		return nil, err
	}
	if err := tb.showCatalogParts(ctx, run, in.CatalogPartIDs); err != nil {
		return nil, err
	}

	var unknown, wrongOil []int64
	grade := tb.carOilGrade(ctx, run.userID)
	for _, id := range in.ProductIDs {
		if slices.Contains(run.shown, id) {
			continue
		}
		if len(run.shown) == maxShown {
			return nil, fmt.Errorf("%w: at most %d products per reply", errInput, maxShown)
		}
		p, err := tb.catalog.Product(ctx, id)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				unknown = append(unknown, id)
				continue
			}
			return nil, err
		}
		if otherOilGrade(p, grade, run.question) {
			wrongOil = append(wrongOil, id)
			continue
		}
		run.shown = append(run.shown, id)
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("%w: no products with ids %v; use ids from search results", errInput, unknown)
	}
	// Oil is universal, so a search finds every grade; the car takes one.
	// Shown beside it, another grade reads as a fit -- the buyer's text says
	// 0W-20 while a 5W-30 card sits under it -- so it is refused here, not
	// left to the model's care, unless the buyer named that grade.
	if len(wrongOil) > 0 {
		return nil, fmt.Errorf("%w: products %v are engine oils of another grade than the buyer's car takes (%s); "+
			"they were not shown -- show only %s oil unless the buyer asks for another grade, and do not offer them in your text",
			errInput, wrongOil, grade, grade)
	}
	return map[string]any{"shown": len(run.shown), "shown_catalog_parts": len(run.shownParts)}, nil
}

// showCatalogParts checks catalogue part ids the way showProducts checks
// products.
func (tb *toolbox) showCatalogParts(ctx context.Context, run *runState, ids []int64) error {
	if len(ids) > 0 && tb.parts == nil {
		return fmt.Errorf("%w: no catalogue parts in this shop", errInput)
	}
	var unknown []int64
	for _, id := range ids {
		if slices.Contains(run.shownParts, id) {
			continue
		}
		if len(run.shownParts) == maxShown {
			return fmt.Errorf("%w: at most %d catalogue parts per reply", errInput, maxShown)
		}
		if _, err := tb.parts.CatalogPart(ctx, id); err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				unknown = append(unknown, id)
				continue
			}
			return err
		}
		run.shownParts = append(run.shownParts, id)
	}
	if len(unknown) > 0 {
		return fmt.Errorf("%w: no catalogue parts with ids %v; use catalog_part_id from search results", errInput, unknown)
	}
	return nil
}

func (tb *toolbox) addToCart(ctx context.Context, run *runState, input json.RawMessage) (any, error) {
	var in struct {
		ProductID int64 `json:"product_id"`
		Quantity  int   `json:"quantity"`
	}
	if err := decode(input, &in); err != nil {
		return nil, err
	}
	switch {
	case in.Quantity == 0:
		in.Quantity = 1
	case in.Quantity < 0 || in.Quantity > maxAddQty:
		return nil, fmt.Errorf("%w: quantity must be 1-%d", errInput, maxAddQty)
	}

	// One unit at a time through the same rules as the button: stock and
	// limits are checked on every unit, and a refusal part-way reports how
	// many did go in.
	var (
		cart  *domain.Cart
		added int
	)
	for range in.Quantity {
		c, err := tb.cart.Add(ctx, run.userID, in.ProductID)
		if err != nil {
			if added == 0 {
				return nil, err
			}
			break
		}
		cart, added = c, added+1
	}
	run.cartChanged = true
	return map[string]any{
		"added":          added,
		"requested":      in.Quantity,
		"cart_items":     cart.Items(),
		"cart_total_uah": int64(cart.Total()) / 100,
	}, nil
}

// serviceKit returns the kit the shop's rules assemble. The model presents it
// and can add it to the cart; it does not choose what goes in.
func (tb *toolbox) serviceKit(ctx context.Context, _ *runState, input json.RawMessage) (any, error) {
	var in struct {
		VehicleID int64 `json:"vehicle_id"`
	}
	if err := decode(input, &in); err != nil {
		return nil, err
	}
	kit, err := tb.kits.Kit(ctx, in.VehicleID)
	if err != nil {
		return nil, err
	}

	type item struct {
		PartType     string        `json:"part_type"`
		Qty          int           `json:"qty"`
		Product      productView   `json:"product"`
		Alternatives []productView `json:"alternatives,omitempty"`
	}
	items := make([]item, len(kit.Items))
	for i, it := range kit.Items {
		items[i] = item{PartType: it.PartType, Qty: it.Qty, Product: tb.view(&it.Product)}
		for _, alt := range it.Alternatives {
			items[i].Alternatives = append(items[i].Alternatives, tb.view(&alt))
		}
	}
	type order struct {
		PartType string            `json:"part_type"`
		Qty      int               `json:"qty"`
		Options  []catalogPartView `json:"options"`
	}
	orders := make([]order, len(kit.OnOrder))
	for i, o := range kit.OnOrder {
		orders[i] = order{PartType: o.PartType, Qty: o.Qty}
		for _, p := range o.Options {
			orders[i].Options = append(orders[i].Options, catalogPartView{ID: p.ID, Name: p.Name, Brand: p.Brand, Number: p.Number, Category: p.Category})
		}
	}
	out := map[string]any{
		"vehicle":          viewVehicle(kit.Vehicle),
		"oil":              map[string]any{"viscosity": kit.Vehicle.OilViscosity, "litres": kit.Vehicle.OilVolume},
		"items":            items,
		"missing_in_stock": kit.Missing,
		"total_uah":        int64(kit.Total()) / 100,
	}
	if len(orders) > 0 {
		out["on_order"] = orders
	}
	return out, nil
}

// statusMeanings tell the model what each status means for the buyer, so it
// explains rather than recites a code.
var statusMeanings = map[domain.OrderStatus]string{
	domain.OrderAwaitingPayment: "placed, waiting for payment; the stock is held until reserved_until",
	domain.OrderPaid:            "paid, waiting for a manager to confirm it",
	domain.OrderConfirmed:       "confirmed by a manager, being prepared for shipping",
	domain.OrderShipped:         "shipped by Nova Poshta to the delivery address",
	domain.OrderCancelled:       "cancelled; if it was paid, the money is refunded to the card",
	domain.OrderExpired:         "not paid in time; the items went back to the cart",
	domain.OrderNeedsRefund:     "paid, but the stock ran out; a manager will refund the money",
}

// pickupMeanings replace statusMeanings for an order the buyer collects at
// the shop: confirmed is ready there, shipped is handed over.
var pickupMeanings = map[domain.OrderStatus]string{
	domain.OrderConfirmed: "confirmed by a manager and ready to collect at the shop, at the address and hours in delivery",
	domain.OrderShipped:   "collected at the shop",
}

// orderStatus lists the buyer's orders. Only the buyer's own: the user id
// comes from the chat, never from the model.
func (tb *toolbox) orderStatus(ctx context.Context, run *runState, input json.RawMessage) (any, error) {
	if err := decode(input, &struct{}{}); err != nil {
		return nil, err
	}
	orders, err := tb.orders.OrdersOf(ctx, run.userID, 5)
	if err != nil {
		return nil, err
	}
	type view struct {
		ID            int64    `json:"id"`
		Status        string   `json:"status"`
		Meaning       string   `json:"meaning"`
		TotalUAH      int64    `json:"total_uah"`
		CreatedAt     string   `json:"created_at"`
		ReservedUntil string   `json:"reserved_until,omitempty"`
		Items         []string `json:"items"`
		Delivery      string   `json:"delivery"`
		// Pickup orders are collected at the shop, delivery being its
		// address.
		Pickup bool `json:"pickup,omitempty"`
	}
	views := make([]view, len(orders))
	for i, o := range orders {
		meaning := statusMeanings[o.Status]
		if m, ok := pickupMeanings[o.Status]; ok && o.Contacts.Pickup {
			meaning = m
		}
		v := view{
			ID: o.ID, Status: string(o.Status), Meaning: meaning,
			TotalUAH: int64(o.Total) / 100, CreatedAt: o.CreatedAt.Format(time.RFC3339), Delivery: o.Contacts.Delivery,
			Pickup: o.Contacts.Pickup,
		}
		if o.Status == domain.OrderAwaitingPayment {
			v.ReservedUntil = o.ReservedUntil.Format(time.RFC3339)
		}
		for _, it := range o.Items {
			v.Items = append(v.Items, fmt.Sprintf("%s (%s) x%d", it.Name, it.Brand, it.Qty))
		}
		views[i] = v
	}
	return map[string]any{"orders": views}, nil
}
