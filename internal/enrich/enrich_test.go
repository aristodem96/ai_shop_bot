package enrich

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aristodem96/ai_shop_bot/internal/autodb"
)

// The live API's answers for a MANN oil filter, cut down.
type fakeAPI struct {
	hits  map[string][]autodb.Article
	infos map[int64]*autodb.ArticleInfo
	calls []string
}

func (f *fakeAPI) Search(_ context.Context, number string) ([]autodb.Article, error) {
	f.calls = append(f.calls, "search "+number)
	return f.hits[number], nil
}

func (f *fakeAPI) Info(_ context.Context, supplierID int64, number string) (*autodb.ArticleInfo, error) {
	f.calls = append(f.calls, "info "+number)
	return f.infos[supplierID], nil
}

var mannInfo = &autodb.ArticleInfo{
	Name:   "Фильтр",
	Specs:  []autodb.Spec{{Name: "Высота [мм]", Value: "79"}},
	Images: []autodb.Image{{URL: "https://image.auto-db.pro/images/A.webp"}},
	OEM:    []autodb.OEM{{Make: "VAG", Number: "04E 115 561 H"}, {Make: "VAG", Number: "04E115561H"}},
	Cross: []autodb.Cross{
		{Brand: "MANN-FILTER", Number: "W 712/95"},
		{Brand: "BOSCH", Number: "F 026 407 143"},
		{Brand: "KNECHT", Number: "OC 977/1"},
	},
}

type memStore struct {
	articles  map[int64]*Article
	suppliers map[int64]string
	pending   []Product
	matched   map[int64]*Article
	unmatched []int64
}

func newMemStore(p ...Product) *memStore {
	return &memStore{articles: map[int64]*Article{}, suppliers: map[int64]string{}, pending: p, matched: map[int64]*Article{}}
}

func (s *memStore) Article(_ context.Context, id int64, _ string) (*Article, bool, error) {
	a, ok := s.articles[id]
	return a, ok, nil
}

func (s *memStore) SaveArticle(_ context.Context, a *Article) error {
	s.articles[a.SupplierID] = a
	return nil
}

func (s *memStore) Pending(_ context.Context, limit int) ([]Product, error) {
	n := min(limit, len(s.pending))
	out := s.pending[:n]
	s.pending = s.pending[n:]
	return out, nil
}

func (s *memStore) PendingProduct(_ context.Context, id int64) (*Product, bool, error) {
	for _, p := range s.pending {
		if p.ID == id {
			return &p, true, nil
		}
	}
	return nil, false, nil
}

func (s *memStore) SupplierKeys(_ context.Context, ids []int64) (map[int64]string, error) {
	out := map[int64]string{}
	for _, id := range ids {
		if k, ok := s.suppliers[id]; ok {
			out[id] = k
		}
	}
	return out, nil
}

func (s *memStore) Matched(_ context.Context, id int64, a *Article) error {
	s.matched[id] = a
	return nil
}

func (s *memStore) Unmatched(_ context.Context, id int64) error {
	s.unmatched = append(s.unmatched, id)
	return nil
}

func newEnricher(api API, store Store) *Enricher {
	return New(api, store, Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func mannAPI() *fakeAPI {
	return &fakeAPI{
		hits: map[string][]autodb.Article{"W71295": {
			{SupplierID: 4, Number: "W 712/95"}, {SupplierID: 9, Number: "W712-95"}, {SupplierID: 4, Number: "W 712/95 X"},
		}},
		infos: map[int64]*autodb.ArticleInfo{4: mannInfo, 9: {Name: "Другой"}},
	}
}

func TestMatchByBrand(t *testing.T) {
	api, store := mannAPI(), newMemStore(Product{ID: 1, PartKey: "W71295", BrandKeys: []string{"MANN", "MANNFILTER"}})
	store.suppliers[4], store.suppliers[9] = "MANNFILTER", "OTHERBRAND"

	n, err := newEnricher(api, store).Pass(context.Background(), 10)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	a := store.matched[1]
	require.NotNil(t, a, "the hit from the product's brand, through its alias")
	assert.Equal(t, int64(4), a.SupplierID)
	assert.Equal(t, []string{"https://image.auto-db.pro/images/A.webp"}, a.Images)
	assert.Equal(t, []string{"04E115561H"}, a.OEMKeys, "carmakers' numbers normalised, once")
	assert.Equal(t, []Cross{{BrandKey: "BOSCH", Key: "F026407143"}, {BrandKey: "KNECHT", Key: "OC9771"}}, a.Cross,
		"the article itself is no analogue of itself")
	assert.Equal(t, "MANN-FILTER", a.Brand, "its own entry names its brand")
}

func TestUnknownBrandIsLearnedFromTheCard(t *testing.T) {
	api := mannAPI()
	api.hits["W71295"] = api.hits["W71295"][:1]
	store := newMemStore(Product{ID: 1, PartKey: "W71295", BrandKeys: []string{"MANNFILTER"}})

	_, err := newEnricher(api, store).Pass(context.Background(), 10)
	require.NoError(t, err)
	require.NotNil(t, store.matched[1], "no crawl named supplier 4, its card does")
	assert.Equal(t, "MANN-FILTER", store.articles[4].Brand, "kept, so the supplier is known next time")
}

func TestNoGuessing(t *testing.T) {
	for name, p := range map[string]Product{
		"another brand's article":   {ID: 1, PartKey: "W71295", BrandKeys: []string{"BOSCH"}},
		"no brand and several hits": {ID: 1, PartKey: "W71295"},
		"nothing found":             {ID: 1, PartKey: "T111117110", BrandKeys: []string{"BOSCH"}},
	} {
		api, store := mannAPI(), newMemStore(p)
		store.suppliers[4], store.suppliers[9] = "MANNFILTER", "OTHERBRAND"
		_, err := newEnricher(api, store).Pass(context.Background(), 10)
		require.NoError(t, err, name)
		assert.Empty(t, store.matched, name)
		assert.Equal(t, []int64{1}, store.unmatched, name)
	}
}

func TestArticleIsAskedOnce(t *testing.T) {
	api := mannAPI()
	e := newEnricher(api, newMemStore())
	for range 2 {
		a, err := e.Article(context.Background(), 4, "W 712/95")
		require.NoError(t, err)
		assert.Equal(t, "W71295", a.Key)
	}
	assert.Equal(t, []string{"info W 712/95"}, api.calls)
}

func TestProductOnDemand(t *testing.T) {
	api, store := mannAPI(), newMemStore(Product{ID: 7, PartKey: "W71295", BrandKeys: []string{"MANNFILTER"}})
	store.suppliers[4] = "MANNFILTER"
	e := newEnricher(api, store)
	require.NoError(t, e.Product(context.Background(), 7))
	assert.NotNil(t, store.matched[7])
	require.NoError(t, e.Product(context.Background(), 8), "a product matched before, or unknown, is no work")
}
