package bot

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
)

func TestCatalogueReady(t *testing.T) {
	b := newTestBot()
	car := domain.Vehicle{Make: "BMW", Model: "3 (E46)", Engine: "320 i <2.2>", YearFrom: 2000, YearTo: 2005}
	b.CatalogueReady(context.Background(), chat, &car)

	m := b.tg.last("sendMessage")
	assert.Equal(t, chat, m.chat, "to the buyer, in their private chat")
	assert.Contains(t, m.text, "BMW 3 (E46) 320 i &lt;2.2&gt; (2000–2005)", "the car named, escaped")
	assert.Contains(t, m.text, "завантажено")
}

func TestCatalogueEmpty(t *testing.T) {
	b := newTestBot()
	b.CatalogueEmpty(context.Background(), chat, &octavia)
	m := b.tg.last("sendMessage")
	assert.Equal(t, chat, m.chat)
	assert.Contains(t, m.text, "немає деталей")
	assert.Contains(t, m.text, "менеджеру", "the buyer is shown a way on")
	assert.NotContains(t, m.text, "завантажено")
}

func TestEmptyCatalogueIsSaidPlainly(t *testing.T) {
	empty := &domain.CatalogProgress{State: "empty"}
	text, kb := categoriesView(nil, &octavia, empty)
	assert.Contains(t, text, msgCatalogueEmpty)
	assert.NotContains(t, text, "я напишу вам", "nothing is coming")
	assert.NotContains(t, callbacks(kb), encode(actMenuRefresh), "nothing to refresh")

	text, kb = carView(&octavia, empty)
	assert.Contains(t, text, msgCatalogueEmpty)
	assert.NotContains(t, text, "завантажується")
	assert.NotContains(t, callbacks(kb), encode(actCarScreen))
}

type fakeProgress struct{ p *domain.CatalogProgress }

func (f fakeProgress) Progress(context.Context, int64) (*domain.CatalogProgress, error) {
	return f.p, nil
}

func TestRefreshSaysWhereTheCatalogueIs(t *testing.T) {
	b, tg := withConsultant(&fakeConsultant{}, fakeLimiter{allow: true})
	b.garage.car = &domain.Vehicle{ID: 990, Make: "BMW", Model: "3 (E46)", Engine: "320 d", YearFrom: 1998, YearTo: 2001}
	ctx := context.Background()

	b.UseCatalogueProgress(fakeProgress{&domain.CatalogProgress{State: "running"}})
	b.Handle(ctx, text(btnCatalog))
	menu := tg.last("sendMessage")
	assert.Contains(t, menu.text, "Каталог деталей завантажується…")
	require.Contains(t, callbacks(menu.markup), encode(actMenuRefresh))

	b.Handle(ctx, press(encode(actMenuRefresh)))
	assert.Contains(t, tg.last("answerCallbackQuery").text, "Ще завантажується", "a tap that changes nothing on screen still answers")

	// The tree still being walked, the wanted parts already in.
	b.UseCatalogueProgress(fakeProgress{&domain.CatalogProgress{State: "running", Parts: 1500}})
	b.Handle(ctx, press(encode(actMenuRefresh)))
	assert.Contains(t, tg.last("answerCallbackQuery").text, "уже 1 500 деталей")
	assert.Contains(t, tg.last("editMessageText").text, "уже 1 500 деталей, найпотрібніші — першими")

	b.UseCatalogueProgress(fakeProgress{&domain.CatalogProgress{State: "running", SectionsDone: 90, SectionsTotal: 363, Parts: 2000}})
	b.Handle(ctx, press(encode(actMenuRefresh)))
	assert.Contains(t, tg.last("answerCallbackQuery").text, "24%")
	assert.Contains(t, tg.last("editMessageText").text, "24% (2 000 деталей)")

	b.UseCatalogueProgress(fakeProgress{&domain.CatalogProgress{State: "done", Parts: 100, Filed: 100}})
	b.Handle(ctx, press(encode(actCarScreen)))
	assert.Equal(t, "✅ Каталог готовий", tg.last("answerCallbackQuery").text)
}

func TestEmptyMenuWhileLoading(t *testing.T) {
	text, _ := categoriesView(nil, &octavia, &domain.CatalogProgress{State: "running"})
	assert.Contains(t, text, "я напишу вам", "an empty menu while loading is not a dead end")
	text, _ = categoriesView(nil, &octavia, &domain.CatalogProgress{State: "done"})
	assert.Contains(t, text, "поки нічого немає")
}

func TestNoCallToWriteWhileTheCatalogueLoads(t *testing.T) {
	b, tg := withConsultant(&fakeConsultant{}, fakeLimiter{allow: true})
	b.garage.car = &octavia
	ctx := context.Background()

	b.UseCatalogueProgress(fakeProgress{&domain.CatalogProgress{State: "running"}})
	b.Handle(ctx, text(btnCatalog))
	assert.NotContains(t, tg.last("sendMessage").text, msgTypeHint, "the consultant would find little yet")
	b.Handle(ctx, text(btnCar))
	assert.NotContains(t, tg.last("sendMessage").text, msgTypeHint)

	b.UseCatalogueProgress(fakeProgress{&domain.CatalogProgress{State: "done", Parts: 10, Filed: 10}})
	b.Handle(ctx, text(btnCatalog))
	assert.Equal(t, 1, strings.Count(tg.last("sendMessage").text, "напишіть"), "once loaded, one call to write")
}

func TestLoadingMenuOnlyAsksToWait(t *testing.T) {
	text, _ := categoriesView(nil, &octavia, &domain.CatalogProgress{State: "running"})
	assert.NotContains(t, text, "напишіть", "nothing to find yet")
	assert.Contains(t, text, "я напишу вам")
}
