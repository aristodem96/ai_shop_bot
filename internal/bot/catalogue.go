package bot

import (
	"context"
	"fmt"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
)

// msgCatalogueReady tells a buyer their car's parts catalogue is loaded.
const msgCatalogueReady = `✅ Каталог деталей для <b>%s</b> завантажено.

Тепер я можу підібрати для неї будь-яку деталь — і зі складу, і під замовлення. Напишіть, що шукаєте.`

// CatalogueReady tells a buyer that the parts catalogue of a car they
// chose or asked about is loaded, so they need not ask again at random.
func (b *Bot) CatalogueReady(ctx context.Context, userID int64, v *domain.Vehicle) {
	b.send(ctx, userID, fmt.Sprintf(msgCatalogueReady, esc(v.Label())), nil)
}
