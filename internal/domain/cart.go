package domain

// Cart limits. They are business rules, so they live in code: nothing a buyer
// types -- or, from stage 2, anything the model says -- can change them.
const (
	// MaxQtyPerLine caps one size of one product. A clothing shop selling ten
	// identical jackets to one person is a reseller or a mistake.
	MaxQtyPerLine = 5
	// MaxCartLines caps distinct lines, which also bounds the cart message
	// below Telegram's 4096 character and 100 button limits.
	MaxCartLines = 15
)

// Cart is a user's cart with prices and stock as they are now.
type Cart struct {
	UserID int64
	Lines  []CartLine
}

// CartLine is one size of one product in a cart.
type CartLine struct {
	VariantID   int64
	ProductID   int64
	ProductName string
	Size        string
	Price       Money
	Qty         int
	// Stock is the current stock of the size. It may have dropped below Qty
	// since the line was added; the cart shows that instead of hiding it.
	Stock int
}

// Subtotal is the line's price times its quantity.
func (l CartLine) Subtotal() Money { return l.Price * Money(l.Qty) }

// Short reports whether stock no longer covers the quantity.
func (l CartLine) Short() bool { return l.Qty > l.Stock }

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

// Line finds the line for a variant.
func (c *Cart) Line(variantID int64) (CartLine, bool) {
	for _, l := range c.Lines {
		if l.VariantID == variantID {
			return l, true
		}
	}
	return CartLine{}, false
}
