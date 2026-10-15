package domain

// Cart limits. They are business rules, so they live in code: nothing a buyer
// types -- or, from stage 2, anything the model says -- can change them.
const (
	// MaxQtyPerLine caps one product. Parts come in sets of two or four at
	// most (pads, plugs, shocks); ten of one part is a trade order, which
	// goes through a manager, not a chat bot.
	MaxQtyPerLine = 10
	// MaxCartLines caps distinct lines, which also bounds the cart message
	// below Telegram's 4096 character and 100 button limits.
	MaxCartLines = 15
)

// Cart is a user's cart with prices and stock as they are now.
type Cart struct {
	UserID int64
	Lines  []CartLine
}

// CartLine is one product in a cart.
type CartLine struct {
	ProductID   int64
	ProductName string
	Brand       string
	Price       Money
	Qty         int
	// Stock is the current stock. It may have dropped below Qty since the
	// line was added; the cart shows that instead of hiding it.
	Stock int
	// OnOrder lines come from a supplier in about LeadDays days; their stock
	// is not tracked, so they are never short.
	OnOrder  bool
	LeadDays int
}

// Subtotal is the line's price times its quantity.
func (l CartLine) Subtotal() Money { return l.Price * Money(l.Qty) }

// Short reports whether stock no longer covers the quantity.
func (l CartLine) Short() bool { return !l.OnOrder && l.Qty > l.Stock }

// Unavailable reports whether the line cannot be bought at all: the product
// is sold out or off sale.
func (l CartLine) Unavailable() bool { return !l.OnOrder && l.Stock == 0 }

// Total is the sum of all lines.
func (c *Cart) Total() Money {
	var t Money
	for _, l := range c.Lines {
		t += l.Subtotal()
	}
	return t
}

// Items is the number of units across all lines.
func (c *Cart) Items() int {
	n := 0
	for _, l := range c.Lines {
		n += l.Qty
	}
	return n
}

// Line finds the line for a product.
func (c *Cart) Line(productID int64) (CartLine, bool) {
	for _, l := range c.Lines {
		if l.ProductID == productID {
			return l, true
		}
	}
	return CartLine{}, false
}
