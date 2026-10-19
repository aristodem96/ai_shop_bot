package bot

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/enrich"
	"github.com/aristodem96/ai_shop_bot/internal/telegram"
)

type fakePhotos struct {
	sent []string
	err  error
}

func (f *fakePhotos) Send(_ context.Context, _ int64, url, _ string, _ telegram.ReplyMarkup) error {
	f.sent = append(f.sent, url)
	return f.err
}

type fakeEnricher struct{ products []int64 }

func (f *fakeEnricher) Product(_ context.Context, id int64) error {
	f.products = append(f.products, id)
	return nil
}

func (f *fakeEnricher) Article(_ context.Context, supplierID int64, number string) (*enrich.Article, error) {
	if supplierID != 65 || number != "P 85 075" {
		return nil, errors.New("unknown")
	}
	return &enrich.Article{Images: []string{"https://image.auto-db.pro/images/p.webp"}}, nil
}

func TestCardIsEnrichedAndItsPhotoConverted(t *testing.T) {
	b := newTestBot()
	ph, en := &fakePhotos{}, &fakeEnricher{}
	b.UsePhotos(ph)
	b.UseEnrichment(en)

	b.Handle(context.Background(), press(encode(actProduct, padsID)))
	assert.Equal(t, []int64{padsID}, en.products, "matched before the card is drawn")
	assert.Equal(t, []string{"https://example.com/pads.png"}, ph.sent)

	ph.err = errors.New("HTTP 404")
	b.Handle(context.Background(), press(encode(actProduct, padsID)))
	assert.Equal(t, "sendMessage", b.tg.last("sendMessage").method, "a photo that fails leaves the card in text")
}

func TestRequestShowsTheCatalogPhoto(t *testing.T) {
	b, _ := withRequests()
	ph := &fakePhotos{}
	b.UsePhotos(ph)
	b.UseEnrichment(&fakeEnricher{})
	withSupplier := brembo
	withSupplier.SupplierID = 65
	b.UseRequests(b.requests, fakeCatalogPartsOf{withSupplier})

	b.Handle(context.Background(), press(encode(actRequest, 31)))
	require.Equal(t, []string{"https://image.auto-db.pro/images/p.webp"}, ph.sent)
}

type fakeCatalogPartsOf struct{ p domain.CatalogPart }

func (f fakeCatalogPartsOf) CatalogPart(_ context.Context, id int64) (*domain.CatalogPart, error) {
	if id != f.p.ID {
		return nil, domain.ErrNotFound
	}
	p := f.p
	return &p, nil
}
