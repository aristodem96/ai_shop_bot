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
	sent     []string
	captions []string
	err      error
}

func (f *fakePhotos) Send(_ context.Context, _ int64, url, caption string, _ telegram.ReplyMarkup) error {
	f.sent = append(f.sent, url)
	f.captions = append(f.captions, caption)
	return f.err
}

type fakeEnricher struct{ products []int64 }

func (f *fakeEnricher) Product(_ context.Context, id int64) error {
	f.products = append(f.products, id)
	return nil
}

func (f *fakeEnricher) ProductFits(_ context.Context, id int64) (*enrich.Fits, error) {
	if id != padsID {
		return nil, nil
	}
	return &enrich.Fits{Lines: []string{"Skoda Octavia III (5E3) 1.4 TSI, 2.0 TDI (2012–)", "VW Golf VII (5G1) 1.6 TDI (2012–)",
		"Seat Leon (5F1) 1.4 (2012–)", "Audi A3 (8V1) 1.4 (2012–)", "Skoda Superb III (3V3) 2.0 (2015–)"}, Complete: true}, nil
}

func (f *fakeEnricher) ArticleFits(context.Context, int64, string) (*enrich.Fits, error) {
	return &enrich.Fits{Lines: []string{"Chevrolet Lacetti (J200) 1.4 16V, 1.6 (2005–)"}}, nil
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
	assert.Contains(t, ph.captions[0], "🚗 <b>За каталогом підходить до:</b>\n• Skoda Octavia III (5E3) 1.4 TSI, 2.0 TDI (2012–)")
	assert.Contains(t, ph.captions[0], "• … і ще 1 модель", "four models, then a count")

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
	assert.Contains(t, ph.captions[0], "• Chevrolet Lacetti (J200) 1.4 16V, 1.6 (2005–)\n• … та інші",
		"a list the catalogue cut says «and others», not a count")
}

type fakeCatalogPartsOf struct{ p domain.CatalogPart }

func (f fakeCatalogPartsOf) CatalogPart(_ context.Context, id int64) (*domain.CatalogPart, error) {
	if id != f.p.ID {
		return nil, domain.ErrNotFound
	}
	p := f.p
	return &p, nil
}
