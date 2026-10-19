package bot

import (
	"context"
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
	assert.Contains(t, menu.text, "збираю розділи")
	require.Contains(t, callbacks(menu.markup), encode(actMenuRefresh))

	b.Handle(ctx, press(encode(actMenuRefresh)))
	assert.Contains(t, tg.last("answerCallbackQuery").text, "збираю розділи", "a tap that changes nothing on screen still answers")

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
