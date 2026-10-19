package bot

import (
	"context"
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
	text += "\n\nТепер я можу підібрати для неї будь-яку деталь — і зі складу, і під замовлення. Напишіть, що шукаєте, або відкрийте каталог."
	b.send(ctx, userID, text, &telegram.InlineKeyboard{Rows: [][]telegram.InlineButton{
		{button("🛍 Каталог для цього авто", actCategories)},
	}})
}
