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

func TestCategoriesViewShowsCounts(t *testing.T) {
	menu := []service.CategoryEntry{
		{Category: domain.Category{ID: 1, Title: "Гальмівна система"}, Count: 3301},
		{Category: domain.Category{ID: 9, Title: "Інше"}, Count: 4},
	}
	text, kb := categoriesView(menu, &octavia, nil)
	assert.Contains(t, text, "Оберіть розділ")
	assert.Equal(t, "Гальмівна система · 3 301", kb.Rows[0][0].Text, "the count says what is behind a button")
	assert.Equal(t, encode(actCategory, 1, 0), kb.Rows[0][0].CallbackData)

	text, _ = categoriesView(nil, &octavia, nil)
	assert.Contains(t, text, "поки нічого немає", "a car the catalogue has nothing for is told so")
}

func TestSectionView(t *testing.T) {
	brakes := domain.Category{ID: 1, Title: "Гальмівна система"}
	pg := &service.ProductPage{Category: brakes, Total: 14, Pages: 1, Children: []service.CategoryEntry{
		{Category: domain.Category{ID: 11, Title: "Гальмівні колодки", ParentID: 1}, Count: 9},
		{Category: domain.Category{ID: 12, Title: "Гальмівні диски", ParentID: 1}, Count: 5},
	}}
	text, kb := productPageView(pg)
	assert.Contains(t, text, "Оберіть підкатегорію")
	assert.Equal(t, []string{
		encode(actCategory, 11, 0), encode(actCategory, 12, 0), encode(actCategoryAll, 1, 0), encode(actCategories), encode(actCart),
	}, callbacks(kb))
	assert.Equal(t, "📋 Усі товари розділу · 14", kb.Rows[2][0].Text)
}

func TestProductPageBackLeadsUp(t *testing.T) {
	brakes := domain.Category{ID: 1, Title: "Гальмівна система"}
	pads := domain.Category{ID: 11, Title: "Гальмівні колодки", ParentID: 1}
	many := make([]domain.Product, service.PageSize)

	_, kb := productPageView(&service.ProductPage{Category: pads, Parent: &brakes, Products: many, Pages: 2})
	back := kb.Rows[len(kb.Rows)-1][0]
	assert.Equal(t, "⬅️ Гальмівна система", back.Text, "a subcategory goes back to its section")
	assert.Equal(t, encode(actCategory, 1, 0), back.CallbackData)
	assert.Contains(t, callbacks(kb), encode(actCategory, 11, 1))

	_, kb = productPageView(&service.ProductPage{Category: brakes, All: true, Products: many, Pages: 2})
	assert.Contains(t, callbacks(kb), encode(actCategoryAll, 1, 1), "paging a whole section stays in it")
	assert.Equal(t, encode(actCategory, 1, 0), kb.Rows[len(kb.Rows)-1][0].CallbackData, "and goes back to its menu")
}

func TestProductPageListsPartsOnOrder(t *testing.T) {
	pg := &service.ProductPage{
		Category: domain.Category{ID: 3, Title: "Рульове керування"},
		Vehicle:  &octavia,
		Products: []domain.Product{{ID: 5, Name: "Тяга рульова", Brand: "Febi", Price: domain.UAH(900), Stock: 1}},
		OnOrder:  []domain.CatalogPart{{ID: 31, Name: "Наконечник рульової тяги", Brand: "LEMFORDER", Number: "37143 01"}},
		Pages:    1,
	}
	text, kb := productPageView(pg)
	assert.Contains(t, text, "під замовлення")
	assert.Equal(t, encode(actProduct, 5), kb.Rows[0][0].CallbackData, "the stock first")
	assert.Equal(t, "🔎 Наконечник рульової тяги · LEMFORDER 37143 01", kb.Rows[1][0].Text)
	assert.Equal(t, encode(actRequest, 31), kb.Rows[1][0].CallbackData, "a part on order opens its request")

	pg.Products = nil
	text, _ = productPageView(pg)
	assert.NotContains(t, text, "нічого немає", "parts on order alone are not an empty page")
}

func TestCarScreenShowsCatalogueProgress(t *testing.T) {
	text, kb := carView(&octavia, &domain.CatalogProgress{State: "running", SectionsDone: 120, SectionsTotal: 363, Parts: 4210})
	assert.Contains(t, text, "завантажується: 33% (4 210 деталей)")
	assert.Contains(t, callbacks(kb), encode(actCarScreen), "while it loads, the screen can be refreshed")

	text, kb = carView(&octavia, &domain.CatalogProgress{State: "done", Parts: 10112, Filed: 7900})
	assert.Contains(t, text, "Каталог деталей: 10 112, з них 7 900 уже в розділах")
	assert.Contains(t, callbacks(kb), encode(actCarScreen), "the menu is still filling up")

	text, kb = carView(&octavia, &domain.CatalogProgress{State: "done", Parts: 10112, Filed: 10112})
	assert.Contains(t, text, "Каталог деталей: 10 112")
	assert.NotContains(t, text, "розділах")
	assert.NotContains(t, callbacks(kb), encode(actCarScreen), "nothing left to wait for")

	text, _ = carView(&octavia, nil)
	assert.NotContains(t, text, "Каталог деталей", "a car from the shop's own list has no catalogue")

	text, _ = categoriesView(nil, &octavia, &domain.CatalogProgress{State: "queued"})
	assert.Contains(t, text, "у черзі")
}
