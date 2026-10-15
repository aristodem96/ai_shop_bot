package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/llm"
	"github.com/aristodem96/ai_shop_bot/internal/service"
)

// maxShown caps the cards one answer can carry. More than five in a row bury
// the conversation.
const maxShown = 5

// maxAddQty caps one add_to_cart call: a set of four plugs or two discs, not
// a trade order.
const maxAddQty = 4

// runState is what the tools accumulate during one buyer message.
type runState struct {
	userID      int64
	shown       []int64
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
	// catSlug maps category ids to the slugs the model knows them by.
	catSlug map[int64]string
}

func newToolbox(catalog Catalog, cart Cart, garage Garage, cats []domain.Category) *toolbox {
	tb := &toolbox{catalog: catalog, cart: cart, garage: garage, catSlug: make(map[int64]string, len(cats))}
	slugs := make([]string, 0, len(cats))
	catalogue := make([]string, 0, len(cats))
	for _, c := range cats {
		tb.catSlug[c.ID] = c.Slug
		slugs = append(slugs, c.Slug)
		catalogue = append(catalogue, c.Slug+" ("+c.Title+")")
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
			Description: "Find vehicles in the shop's list by make, model, year and engine. " +
				"Returns the matching vehicles with their ids; with no match, the makes the shop covers.",
			Schema: object(map[string]any{
				"make":   str("Make in Latin letters, e.g. \"Skoda\"."),
				"model":  str("Model in Latin letters, e.g. \"Octavia\"."),
				"year":   integer("Year of manufacture."),
				"engine": str("Engine as the buyer gives it, e.g. \"1.4\", \"TDI\"."),
			}),
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
			Description: "Search the catalogue. Returns only parts in stock, cheapest first, or best keyword match " +
				"first when query is set. Categories: " + strings.Join(catalogue, ", ") + ".",
			Schema: object(map[string]any{
				"vehicle_id":    integer("Only parts that fit this vehicle; universal products always match."),
				"category":      map[string]any{"type": "string", "enum": slugs, "description": "Kind of part."},
				"query":         str("Word stems of the kind of part, e.g. \"колодк\", \"амортиз\". Leave empty when category says it."),
				"part_number":   str("An OEM or shop part number in any spelling."),
				"brand":         str("Part brand."),
				"min_price_uah": integer("Lowest price in hryvnias."),
				"max_price_uah": integer("Highest price in hryvnias."),
				"limit":         integer("How many results, 1-10. Default 5."),
			}),
		},
		{
			Name:        "get_part",
			Description: "Get the full details of one part: description, specs, OEM numbers, the cars it fits and whether it fits the buyer's saved car.",
			Schema:      object(map[string]any{"product_id": integer("Product id from search results.")}, "product_id"),
		},
		{
			Name: "show_products",
			Description: "Show parts to the buyer as cards with photo, price, stock and fit, under your reply. " +
				"Use it whenever you recommend parts. At most 5 per reply.",
			Schema: object(map[string]any{
				"product_ids": map[string]any{
					"type": "array", "items": map[string]any{"type": "integer"},
					"description": "Ids from search results, in the order to show them.",
				},
			}, "product_ids"),
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
		"get_my_vehicle": tb.getMyVehicle,
		"set_my_vehicle": tb.setMyVehicle,
		"search_parts":   tb.searchParts,
		"get_part":       tb.getPart,
		"show_products":  tb.showProducts,
		"add_to_cart":    tb.addToCart,
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
	ID         int64    `json:"id"`
	Name       string   `json:"name"`
	Brand      string   `json:"brand"`
	Category   string   `json:"category"`
	PriceUAH   int64    `json:"price_uah"`
	Stock      string   `json:"stock"`
	Universal  bool     `json:"universal,omitempty"`
	OEMNumbers []string `json:"oem_numbers,omitempty"`
	Specs      []string `json:"specs,omitempty"`
	// The fields below are filled for get_part only.
	Description   string   `json:"description,omitempty"`
	Fits          []string `json:"fits,omitempty"`
	FitsMyVehicle *bool    `json:"fits_my_vehicle,omitempty"`
}

func (tb *toolbox) view(p *domain.Product) productView {
	v := productView{
		ID: p.ID, Name: p.Name, Brand: p.Brand, Category: tb.catSlug[p.CategoryID],
		PriceUAH: int64(p.Price) / 100, Universal: p.Universal, OEMNumbers: p.OEMNumbers,
	}
	switch {
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

	// Nothing matched: tell the model what the shop does cover, so it can
	// spot a misspelt make or say plainly which cars are served.
	if len(found) == 0 {
		makes, err := tb.garage.Makes(ctx)
		if err != nil {
			return nil, err
		}
		out["makes_covered"] = makes
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

func (tb *toolbox) searchParts(ctx context.Context, _ *runState, input json.RawMessage) (any, error) {
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
	return map[string]any{"relaxed": res.Relaxed, "products": views}, nil
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
		v.Fits = append(v.Fits, f.Label())
	}
	// Fit is decided here, from the fitment table, not left for the model to
	// infer from a list of names.
	if car, err := tb.garage.Get(ctx, run.userID); err == nil && car != nil {
		fits := p.FitsVehicle(car.ID)
		v.FitsMyVehicle = &fits
	}
	return v, nil
}

// showProducts is how the agent puts cards under its answer. Each id is looked
// up: an id the model made up -- or one of a product taken off sale since --
// fails here and never reaches the buyer.
func (tb *toolbox) showProducts(ctx context.Context, run *runState, input json.RawMessage) (any, error) {
	var in struct {
		ProductIDs []int64 `json:"product_ids"`
	}
	if err := decode(input, &in); err != nil {
		return nil, err
	}

	var unknown []int64
	for _, id := range in.ProductIDs {
		if slices.Contains(run.shown, id) {
			continue
		}
		if len(run.shown) == maxShown {
			return nil, fmt.Errorf("%w: at most %d products per reply", errInput, maxShown)
		}
		if _, err := tb.catalog.Product(ctx, id); err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				unknown = append(unknown, id)
				continue
			}
			return nil, err
		}
		run.shown = append(run.shown, id)
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("%w: no products with ids %v; use ids from search results", errInput, unknown)
	}
	return map[string]any{"shown": len(run.shown)}, nil
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
