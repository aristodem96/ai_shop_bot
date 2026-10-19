package bot

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

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
