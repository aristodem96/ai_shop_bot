package bot

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/telegram"
)

// CatalogueProgress says how far a car's parts catalogue has loaded.
// postgres.CatalogPartRepository is the implementation.
type CatalogueProgress interface {
	// Progress is nil for a car outside the catalogue.
	Progress(ctx context.Context, vehicleID int64) (*domain.CatalogProgress, error)
}

// UseCatalogueProgress shows the buyer how far their car's catalogue has
// loaded, on the car's screen and above the catalogue's menu.
func (b *Bot) UseCatalogueProgress(p CatalogueProgress) { b.catalogue = p }

// progress is the car's catalogue progress, nil when there is none to show.
func (b *Bot) progress(ctx context.Context, car *domain.Vehicle) *domain.CatalogProgress {
	if b.catalogue == nil || car == nil {
		return nil
	}
	p, err := b.catalogue.Progress(ctx, car.ID)
	if err != nil {
		b.log.WarnContext(ctx, "could not read catalogue progress", slog.Int64("vehicle_id", car.ID), slog.Any("error", err))
		return nil
	}
	return p
}

// progressToast answers "refresh": the screen may not change between two
// taps -- a catalogue's first minutes are spent finding its sections -- and
// a tap that seems to do nothing reads as broken.
func (b *Bot) progressToast(ctx context.Context, user int64) string {
	p := b.progress(ctx, b.car(ctx, user))
	switch {
	case p == nil:
		return ""
	case p.State == "done" && p.Filed < p.Parts:
		return "Каталог завантажено, розділи ще заповнюються: " + num(p.Filed) + " з " + num(p.Parts) + " деталей"
	case p.State == "done":
		return "✅ Каталог готовий"
	case p.State == "running" && p.SectionsTotal > 0:
		return fmt.Sprintf("⏳ Ще завантажується: %d%%. Я напишу, коли буде готово.", p.SectionsDone*100/p.SectionsTotal)
	case p.State == "running" && p.Parts > 0:
		return fmt.Sprintf("⏳ Ще завантажується: уже %s деталей. Я напишу, коли буде готово.", num(p.Parts))
	case p.State == "running":
		return "⏳ Ще завантажується. Я напишу, коли буде готово."
	case p.State == "failed":
		return "⚠️ Не вдалося завантажити, пробую ще раз"
	case p.Empty():
		return "У каталозі виробника для цієї модифікації немає деталей"
	}
	return "⏳ Каталог у черзі на завантаження. Я напишу, коли буде готово."
}

// CatalogueEmpty tells a buyer who waited that the catalogue has no parts
// for their car, rather than leaving them to wait or find an empty menu.
func (b *Bot) CatalogueEmpty(ctx context.Context, userID int64, v *domain.Vehicle) {
	b.send(ctx, userID, "⚠️ Для <b>"+esc(v.Label())+"</b> у каталозі виробника поки немає деталей: дані для цієї модифікації неповні.\n\n"+
		"Напишіть, яка деталь потрібна, — я передам менеджеру, і він підбере її вручну.", nil)
}

// CatalogueReady tells a buyer that the parts catalogue of a car they
// chose or asked about is loaded, so they need not ask again at random.
func (b *Bot) CatalogueReady(ctx context.Context, userID int64, v *domain.Vehicle) {
	text := "✅ Каталог деталей для <b>" + esc(v.Label()) + "</b> завантажено."
	if p := b.progress(ctx, v); p != nil && p.Parts > 0 {
		text += "\nУ ньому " + num(p.Parts) + " деталей."
		if p.Filed < p.Parts {
			text += " У розділах каталогу поки " + num(p.Filed) + " — решта з'явиться за кілька хвилин, а консультант знайде будь-яку вже зараз."
		}
	}
	text += "\n\nТепер я можу підібрати для неї будь-яку деталь — і зі складу, і під замовлення. Просто напишіть, що шукаєте, наприклад «шарова опора передня», — або відкрийте каталог."
	b.send(ctx, userID, text, &telegram.InlineKeyboard{Rows: [][]telegram.InlineButton{
		{button("🛍 Каталог для цього авто", actCategories)},
	}})
}
