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

// maxShown caps the cards one answer can carry. More than five photos in a row
// bury the conversation.
const maxShown = 5

// runState is what the tools accumulate during one buyer message.
type runState struct {
	userID      int64
	shown       []int64
	cartChanged bool
}

type handler func(ctx context.Context, run *runState, input json.RawMessage) (any, error)

// toolbox holds the tool definitions sent to the model and their handlers.
type toolbox struct {
	defs     []llm.Tool
	handlers map[string]handler
	catalog  Catalog
	cart     Cart
	// catSlug maps category ids to the slugs the model knows them by.
	catSlug map[int64]string
}

var (
	seasons = []string{"summer", "demi", "winter", "all"}
	genders = []string{"men", "women", "unisex"}
	styles  = []string{"casual", "classic", "outdoor", "sport", "street"}
)

func newToolbox(catalog Catalog, cart Cart, cats []domain.Category) *toolbox {
	tb := &toolbox{catalog: catalog, cart: cart, catSlug: make(map[int64]string, len(cats))}
	slugs := make([]string, 0, len(cats))
	catalogue := make([]string, 0, len(cats))
	for _, c := range cats {
		tb.catSlug[c.ID] = c.Slug
		slugs = append(slugs, c.Slug)
		catalogue = append(catalogue, c.Slug+" ("+c.Title+")")
	}
	colors := domain.Colors()

	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	enum := func(desc string, values []string) map[string]any {
		return map[string]any{"type": "string", "enum": values, "description": desc}
	}
	enumList := func(desc string, values []string) map[string]any {
		return map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": values}, "description": desc}
	}
	integer := func(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }

	tb.defs = []llm.Tool{
		{
			Name: "search_products",
			Description: "Search the shop's catalogue. Returns only products with at least one size in stock, " +
				"cheapest first, or best keyword match first when query is set. Categories: " +
				strings.Join(catalogue, ", ") + ".",
			Schema: map[string]any{"type": "object", "properties": map[string]any{
				"query":          str("Optional word stems to match in name, brand and description, e.g. \"пухов\", \"Nord\". Leave empty when the filters describe the wish."),
				"category":       enum("Kind of item.", slugs),
				"min_price_uah":  integer("Lowest price in hryvnias."),
				"max_price_uah":  integer("Highest price in hryvnias."),
				"size":           str("A size that must be in stock: XS-XXL for clothing, 28-36 waist for jeans, 37-45 for shoes, ONE for accessories."),
				"colors":         enumList("Only these colours.", colors),
				"exclude_colors": enumList("Not these colours.", colors),
				"season":         enum("Season; all-season products always match too.", seasons),
				"gender":         enum("Who it is for; unisex products always match too.", genders),
				"styles":         enumList("Any of these styles.", styles),
				"limit":          integer("How many results, 1-10. Default 5."),
			}},
		},
		{
			Name:        "get_product",
			Description: "Get the full details of one product: description, season, styles and which sizes are in stock.",
			Schema: map[string]any{"type": "object", "properties": map[string]any{
				"product_id": integer("Product id from a search result."),
			}, "required": []string{"product_id"}},
		},
		{
			Name: "show_products",
			Description: "Show products to the buyer as cards with photo, price and sizes, under your reply. " +
				"Use it whenever you recommend products. At most 5 per reply.",
			Schema: map[string]any{"type": "object", "properties": map[string]any{
				"product_ids": map[string]any{
					"type": "array", "items": map[string]any{"type": "integer"},
					"description": "Ids from search results, in the order to show them.",
				},
			}, "required": []string{"product_ids"}},
		},
		{
			Name:        "add_to_cart",
			Description: "Add one unit of a product in a size to the buyer's cart. Only when the buyer asked for it.",
			Schema: map[string]any{"type": "object", "properties": map[string]any{
				"product_id": integer("Product id."),
				"size":       str("Size label exactly as listed for the product."),
			}, "required": []string{"product_id", "size"}},
		},
	}

	tb.handlers = map[string]handler{
		"search_products": tb.searchProducts,
		"get_product":     tb.getProduct,
		"show_products":   tb.showProducts,
		"add_to_cart":     tb.addToCart,
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
			err = fmt.Errorf("unknown tool %q", c.Name)
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
		return "product not found"
	case errors.Is(err, domain.ErrOutOfStock):
		return "this size is out of stock"
	case errors.Is(err, domain.ErrNoSuchSize):
		return "the product does not come in this size"
	case errors.Is(err, domain.ErrQtyLimit):
		return fmt.Sprintf("at most %d units of one size per order", domain.MaxQtyPerLine)
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

// productView is a product as the model sees it.
type productView struct {
	ID       int64    `json:"id"`
	Name     string   `json:"name"`
	Brand    string   `json:"brand"`
	Category string   `json:"category"`
	Color    string   `json:"color"`
	Season   string   `json:"season"`
	Gender   string   `json:"gender"`
	Styles   []string `json:"styles"`
	PriceUAH int64    `json:"price_uah"`
	Sizes    []string `json:"sizes_in_stock"`
	// LowStock lists sizes with only a unit or two left.
	LowStock    []string `json:"low_stock,omitempty"`
	Description string   `json:"description,omitempty"`
}

func (tb *toolbox) view(p *domain.Product, withDescription bool) productView {
	v := productView{
		ID: p.ID, Name: p.Name, Brand: p.Brand, Category: tb.catSlug[p.CategoryID],
		Color: p.Color, Season: p.Season, Gender: p.Gender, Styles: p.Styles,
		PriceUAH: int64(p.Price) / 100, Sizes: []string{},
	}
	for _, vr := range p.Variants {
		if vr.Stock > 0 {
			v.Sizes = append(v.Sizes, vr.Size)
		}
		if vr.Stock > 0 && vr.Stock <= domain.LowStock {
			v.LowStock = append(v.LowStock, vr.Size)
		}
	}
	if withDescription {
		v.Description = p.Description
	}
	return v
}

func (tb *toolbox) searchProducts(ctx context.Context, _ *runState, input json.RawMessage) (any, error) {
	var in struct {
		Query         string   `json:"query"`
		Category      string   `json:"category"`
		MinPriceUAH   int64    `json:"min_price_uah"`
		MaxPriceUAH   int64    `json:"max_price_uah"`
		Size          string   `json:"size"`
		Colors        []string `json:"colors"`
		ExcludeColors []string `json:"exclude_colors"`
		Season        string   `json:"season"`
		Gender        string   `json:"gender"`
		Styles        []string `json:"styles"`
		Limit         int      `json:"limit"`
	}
	if err := decode(input, &in); err != nil {
		return nil, err
	}
	if in.MinPriceUAH < 0 || in.MaxPriceUAH < 0 || (in.MaxPriceUAH > 0 && in.MinPriceUAH > in.MaxPriceUAH) {
		return nil, fmt.Errorf("%w: price range %d-%d", errInput, in.MinPriceUAH, in.MaxPriceUAH)
	}

	res, err := tb.catalog.Search(ctx, service.SearchFilter{
		Query: in.Query, Category: in.Category,
		MinPrice: domain.UAH(in.MinPriceUAH), MaxPrice: domain.UAH(in.MaxPriceUAH),
		Size: in.Size, Colors: in.Colors, ExcludeColors: in.ExcludeColors,
		Season: in.Season, Gender: in.Gender, Styles: in.Styles, Limit: in.Limit,
	})
	if err != nil {
		return nil, err
	}

	views := make([]productView, len(res.Products))
	for i := range res.Products {
		views[i] = tb.view(&res.Products[i], false)
	}
	return map[string]any{"relaxed": res.Relaxed, "products": views}, nil
}

func (tb *toolbox) getProduct(ctx context.Context, _ *runState, input json.RawMessage) (any, error) {
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
	return tb.view(p, true), nil
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
		ProductID int64  `json:"product_id"`
		Size      string `json:"size"`
	}
	if err := decode(input, &in); err != nil {
		return nil, err
	}
	cart, err := tb.cart.AddSize(ctx, run.userID, in.ProductID, in.Size)
	if err != nil {
		return nil, err
	}
	run.cartChanged = true
	return map[string]any{
		"added":          true,
		"cart_items":     cart.Items(),
		"cart_total_uah": int64(cart.Total()) / 100,
	}, nil
}
