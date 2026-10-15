package bot

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/service"
)

func TestProductCaption(t *testing.T) {
	p := &domain.Product{
		Name: "Лампа <H7>", Brand: "Iskra", SKU: "ELC-003", Price: domain.UAH(290), Stock: 2,
		Specs:     []domain.Spec{{Name: "Цоколь", Value: "H7"}},
		Universal: true,
	}
	got := productCaption(p, nil)

	assert.Contains(t, got, "<b>Лампа &lt;H7&gt;</b>", "catalogue text is escaped for HTML")
	assert.Contains(t, got, "Iskra · арт. ELC-003")
	assert.Contains(t, got, "останні 2 шт.")
	assert.Contains(t, got, "Цоколь: H7")
	assert.Contains(t, got, "Універсальний товар")
}

func TestProductCaptionFit(t *testing.T) {
	p := &domain.Product{Name: "Колодки", OEMNumbers: []string{"5Q0698151"}, Stock: 9,
		Fits: []domain.Vehicle{octavia, {ID: 1, Make: "VW", Model: "Golf", Engine: "1.6"},
			{ID: 2, Make: "VW", Model: "Passat", Engine: "2.0"}, {ID: 3, Make: "Skoda", Model: "Superb", Engine: "2.0"},
			{ID: 4, Make: "Seat", Model: "Leon", Engine: "1.4"}}}

	assert.Contains(t, productCaption(p, &octavia), "✅ Підходить до вашого Skoda Octavia A7 1.4 TSI")
	assert.Contains(t, productCaption(p, &lanos), "⚠️ Не підходить до вашого Daewoo Lanos 1.5")
	assert.Contains(t, productCaption(p, nil), "та ще 1", "without a car, a short list of fits")
	assert.Contains(t, productCaption(p, nil), "OEM: <code>5Q0698151</code>")
}

func TestProductCaptionSoldOut(t *testing.T) {
	p := &domain.Product{ID: 9, Name: "Колодки Lanos", Price: domain.UAH(450)}
	assert.Contains(t, productCaption(p, nil), "немає в наявності")
	assert.NotContains(t, callbacks(productKeyboard(p)), encode(actAdd, 9), "a sold-out card has no add button")
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

func TestProductPageForMyCar(t *testing.T) {
	text, _ := productPageView(&service.ProductPage{Category: domain.Category{Title: "Фільтри"}, Vehicle: &lanos, Pages: 1})
	assert.Contains(t, text, "Daewoo Lanos 1.5")
	assert.Contains(t, text, "Для вашого авто в цій категорії поки нічого немає.")
}

func TestCartViewFlagsShortLines(t *testing.T) {
	c := &domain.Cart{Lines: []domain.CartLine{
		{ProductID: 1, ProductName: "Свічка", Brand: "Iskra", Price: domain.UAH(260), Qty: 4, Stock: 1},
		{ProductID: 2, ProductName: "Фільтр", Brand: "FiltroMax", Price: domain.UAH(290), Qty: 1, Stock: 0},
	}}
	text, kb := cartView(c)

	assert.Contains(t, text, "Свічка (Iskra)")
	assert.Contains(t, text, "залишилось лише 1 шт.")
	assert.Contains(t, text, "більше немає в наявності")
	assert.Contains(t, text, "(5 шт.)")
	assert.Contains(t, callbacks(kb), encode(actDec, 1))
	assert.Contains(t, callbacks(kb), encode(actClear))
}

func TestProductCaptionOnOrder(t *testing.T) {
	p := &domain.Product{ID: 9, Name: "Амортизатор", Price: domain.UAH(2390), OnOrder: true, LeadDays: 3,
		Fits: []domain.Vehicle{octavia}, FitNotes: map[int64]string{octavia.ID: "передня вісь"}}

	got := productCaption(p, &octavia)
	assert.Contains(t, got, "🚚 під замовлення, 3 дні")
	assert.Contains(t, got, "Підходить до вашого Skoda Octavia A7 1.4 TSI (передня вісь)", "the note tells two fitting parts apart")
	assert.Contains(t, callbacks(productKeyboard(p)), encode(actAdd, 9), "a part on order can be bought")
}

func TestPlural(t *testing.T) {
	for n, want := range map[int]string{
		1: "1 день", 2: "2 дні", 4: "4 дні", 5: "5 днів", 11: "11 днів", 14: "14 днів", 21: "21 день", 22: "22 дні",
	} {
		assert.Equal(t, want, plural(n, "день", "дні", "днів"))
	}
}
