package bot

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/service"
)

func TestProductCaption(t *testing.T) {
	p := &domain.Product{
		Name: "Tee <3", Brand: "Urban Fox", Color: "white", Season: "summer",
		Price: domain.UAH(399), TotalStock: 4,
		Variants: []domain.Variant{
			{Size: "S", Stock: 0},
			{Size: "M", Stock: 1},
			{Size: "L", Stock: 3},
		},
	}
	got := productCaption(p)

	assert.Contains(t, got, "<b>Tee &lt;3</b>", "catalogue text is escaped for HTML")
	assert.Contains(t, got, "білий · літо")
	assert.Contains(t, got, "Розміри: <s>S</s> · M (1 шт.) · L")
}

func TestProductCaptionSoldOut(t *testing.T) {
	p := &domain.Product{Name: "Belt", Price: domain.UAH(599), Variants: []domain.Variant{{Size: "ONE"}}}
	assert.Contains(t, productCaption(p), "Немає в наявності")
	assert.NotContains(t, callbacks(productKeyboard(p)), encode(actPickSize, 0),
		"a sold-out card has no add button")
}

func TestProductPageNavigation(t *testing.T) {
	pg := &service.ProductPage{Category: domain.Category{ID: 3}, Page: 1, Pages: 3}
	_, kb := productPageView(pg)
	data := callbacks(kb)

	assert.Contains(t, data, encode(actCategory, 3, 0), "back a page")
	assert.Contains(t, data, encode(actCategory, 3, 2), "forward a page")

	pg.Page = 0
	_, kb = productPageView(pg)
	assert.NotContains(t, callbacks(kb), encode(actCategory, 3, -1), "no back arrow on the first page")
}

func TestCartViewFlagsShortLines(t *testing.T) {
	c := &domain.Cart{Lines: []domain.CartLine{
		{VariantID: 1, ProductName: "Худі", Size: "M", Price: domain.UAH(1199), Qty: 3, Stock: 1},
		{VariantID: 2, ProductName: "Шапка", Size: "ONE", Price: domain.UAH(399), Qty: 1, Stock: 0},
	}}
	text, kb := cartView(c)

	assert.Contains(t, text, "залишилось лише 1 шт.")
	assert.Contains(t, text, "більше немає в наявності")
	assert.Contains(t, text, "(4 шт.)")
	assert.Contains(t, callbacks(kb), encode(actDec, 1))
	assert.Contains(t, callbacks(kb), encode(actClear))
}
