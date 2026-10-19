// Package enrich adds to the shop's products what the Auto-DB catalogue
// knows of them: photos, the carmakers' numbers a part replaces, and the
// same part from other brands.
//
// A product is matched to its article by number and brand. The API searches
// by article number in any spelling, and names a brand only by supplier id,
// so the hit is accepted when its supplier is the product's brand -- known
// from crawls -- or, for a price list that names no brand, when it is the
// only hit. A guess would put another part's photo on the card.
//
// What it finds goes where the bot already looks: the photo into an empty
// image_url, the carmakers' numbers into oem_numbers, which search and
// analogues match on. Analogues also follow the catalogue's cross list.
package enrich

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/aristodem96/ai_shop_bot/internal/autodb"
	"github.com/aristodem96/ai_shop_bot/internal/directory"
	"github.com/aristodem96/ai_shop_bot/internal/domain"
)

// API is the part of the Auto-DB client enrichment reads.
type API interface {
	Search(ctx context.Context, number string) ([]autodb.Article, error)
	Info(ctx context.Context, supplierID int64, number string) (*autodb.ArticleInfo, error)
}

// Article is what Auto-DB knows of one article.
type Article struct {
	SupplierID int64
	Key        string // the number, normalised
	Number     string
	// Brand is the article's brand when its card names it -- the cross
	// list often lists the article itself -- else "".
	Brand  string
	Name   string
	Specs  []domain.Spec
	Images []string
	// OEMKeys are the carmakers' numbers the part replaces, normalised.
	OEMKeys []string
	Cross   []Cross
	// Vehicles are the cars it fits, the first autodb.MaxVehicles of them.
	// VehiclesKnown is false for an article cached before they were kept.
	Vehicles      []autodb.ArticleVehicle
	VehiclesKnown bool
}

// Cross is the same part from another brand.
type Cross struct {
	BrandKey string
	Key      string
}

// Product is a product waiting to be matched.
type Product struct {
	ID      int64
	PartKey string
	// BrandKeys are the keys the product's brand goes by: its own and its
	// aliases. Empty for a price list that names no brand.
	BrandKeys []string
}

// Store holds articles and marks products.
type Store interface {
	// Article returns a cached article.
	Article(ctx context.Context, supplierID int64, key string) (*Article, bool, error)
	SaveArticle(ctx context.Context, a *Article) error
	// Pending returns products not looked for yet, those buyers opened
	// first.
	Pending(ctx context.Context, limit int) ([]Product, error)
	// PendingProduct returns the product if it was not looked for yet.
	PendingProduct(ctx context.Context, id int64) (*Product, bool, error)
	// ProductArticle names the article a product was matched to.
	ProductArticle(ctx context.Context, productID int64) (supplierID int64, number string, ok bool, err error)
	// SupplierKeys returns the brand keys of the supplier ids it knows.
	SupplierKeys(ctx context.Context, ids []int64) (map[int64]string, error)
	// Matched records the product's article and fills what the product
	// lacks from it; Unmatched records that none was found.
	Matched(ctx context.Context, productID int64, a *Article) error
	Unmatched(ctx context.Context, productID int64) error
}

// Config tunes an Enricher.
type Config struct {
	// Parallel is how many products are looked for at once.
	Parallel int
	// Idle is the wait when nothing is pending.
	Idle time.Duration
}

// Enricher matches products and caches articles.
type Enricher struct {
	api   API
	store Store
	cfg   Config
	log   *slog.Logger
}

// New returns an Enricher.
func New(api API, store Store, cfg Config, log *slog.Logger) *Enricher {
	if cfg.Parallel <= 0 {
		cfg.Parallel = 2
	}
	if cfg.Idle <= 0 {
		cfg.Idle = 10 * time.Minute
	}
	return &Enricher{api: api, store: store, cfg: cfg, log: log}
}

// Article returns what Auto-DB knows of an article, from the cache or, the
// first time, from the API.
func (e *Enricher) Article(ctx context.Context, supplierID int64, number string) (*Article, error) {
	key := domain.NormalizeOEM(number)
	a, ok, err := e.store.Article(ctx, supplierID, key)
	if err != nil || (ok && a.VehiclesKnown) {
		return a, err
	}
	info, err := e.api.Info(ctx, supplierID, number)
	if err != nil {
		return nil, err
	}
	a = fromInfo(supplierID, number, info)
	if err := e.store.SaveArticle(ctx, a); err != nil {
		return nil, err
	}
	return a, nil
}

// fromInfo keeps what the shop uses of an article card.
func fromInfo(supplierID int64, number string, info *autodb.ArticleInfo) *Article {
	a := &Article{SupplierID: supplierID, Key: domain.NormalizeOEM(number), Number: number, Name: info.Name,
		Vehicles: info.Vehicles, VehiclesKnown: true}
	for _, s := range info.Specs {
		a.Specs = append(a.Specs, domain.Spec{Name: s.Name, Value: s.Value})
	}
	for _, im := range info.Images {
		a.Images = append(a.Images, im.URL)
	}
	seen := map[string]bool{}
	for _, o := range info.OEM {
		if k := domain.NormalizeOEM(o.Number); k != "" && !seen[k] {
			seen[k] = true
			a.OEMKeys = append(a.OEMKeys, k)
		}
	}
	crossSeen := map[Cross]bool{}
	for _, c := range info.Cross {
		x := Cross{BrandKey: domain.NormalizeOEM(c.Brand), Key: domain.NormalizeOEM(c.Number)}
		if x.Key == a.Key {
			a.Brand = c.Brand
			continue
		}
		if x.Key == "" || crossSeen[x] {
			continue
		}
		crossSeen[x] = true
		a.Cross = append(a.Cross, x)
	}
	return a
}

// Fits are the cars a product fits by the catalogue, a line per model.
type Fits struct {
	Lines []string
	// Complete is false when the catalogue cut the list: then a car that
	// is not on it may still fit.
	Complete bool
}

// ProductFits returns the cars a product fits by its catalogue article,
// nil for a product with no article.
func (e *Enricher) ProductFits(ctx context.Context, productID int64) (*Fits, error) {
	supplierID, number, ok, err := e.store.ProductArticle(ctx, productID)
	if err != nil || !ok {
		return nil, err
	}
	return e.ArticleFits(ctx, supplierID, number)
}

// ArticleFits returns the cars an article fits.
func (e *Enricher) ArticleFits(ctx context.Context, supplierID int64, number string) (*Fits, error) {
	a, err := e.Article(ctx, supplierID, number)
	if err != nil {
		return nil, err
	}
	return &Fits{Lines: directory.FitLines(a.Vehicles), Complete: len(a.Vehicles) < autodb.MaxVehicles}, nil
}

// Product matches one product now, if it was not looked for yet: a card a
// buyer opens gets its photo on the first view.
func (e *Enricher) Product(ctx context.Context, productID int64) error {
	p, ok, err := e.store.PendingProduct(ctx, productID)
	if err != nil || !ok {
		return err
	}
	return e.match(ctx, *p)
}

// Run matches pending products in the background until ctx is done.
func (e *Enricher) Run(ctx context.Context) error {
	// The stock pass is background work, unlike a card a buyer waits for.
	ctx = autodb.Background(ctx)
	for ctx.Err() == nil {
		n, err := e.Pass(ctx, 50)
		if err != nil && ctx.Err() == nil {
			e.log.WarnContext(ctx, "could not enrich products", slog.Any("error", err))
		}
		if n > 0 && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
		case <-time.After(e.cfg.Idle):
		}
	}
	return nil
}

// Pass matches up to limit pending products and returns how many it took.
// A product whose search failed stays pending for the next pass; one that
// keeps the API from answering at all stops the pass.
func (e *Enricher) Pass(ctx context.Context, limit int) (int, error) {
	pending, err := e.store.Pending(ctx, limit)
	if err != nil || len(pending) == 0 {
		return 0, err
	}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(e.cfg.Parallel)
	for _, p := range pending {
		g.Go(func() error {
			err := e.match(gctx, p)
			if errors.Is(err, autodb.ErrIPNotAllowed) || errors.Is(err, autodb.ErrUnauthorized) {
				return err
			}
			if err != nil {
				e.log.WarnContext(gctx, "could not enrich a product", slog.Int64("product_id", p.ID), slog.Any("error", err))
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return 0, err
	}
	return len(pending), nil
}

// match finds the product's article and records what it found.
func (e *Enricher) match(ctx context.Context, p Product) error {
	hits, err := e.api.Search(ctx, p.PartKey)
	if err != nil {
		return fmt.Errorf("enrich: search %s: %w", p.PartKey, err)
	}
	var same []autodb.Article
	ids := make([]int64, 0, len(hits))
	for _, h := range hits {
		if domain.NormalizeOEM(h.Number) == p.PartKey {
			same = append(same, h)
			ids = append(ids, h.SupplierID)
		}
	}
	names, err := e.store.SupplierKeys(ctx, ids)
	if err != nil {
		return err
	}
	if hit, ok := pick(p, same, names); ok {
		a, err := e.Article(ctx, hit.SupplierID, hit.Number)
		if err != nil {
			return fmt.Errorf("enrich: article %s of %d: %w", hit.Number, hit.SupplierID, err)
		}
		return e.store.Matched(ctx, p.ID, a)
	}
	// Hits from brands no crawl has named yet: their cards may name them,
	// the cross list listing the article itself. A call each, made only
	// here, for a few.
	if len(p.BrandKeys) > 0 {
		asked := 0
		for _, h := range same {
			if names[h.SupplierID] != "" || asked == maxUnknownHits {
				continue
			}
			asked++
			a, err := e.Article(ctx, h.SupplierID, h.Number)
			if err != nil {
				return fmt.Errorf("enrich: article %s of %d: %w", h.Number, h.SupplierID, err)
			}
			if slices.Contains(p.BrandKeys, domain.NormalizeOEM(a.Brand)) {
				return e.store.Matched(ctx, p.ID, a)
			}
		}
	}
	return e.store.Unmatched(ctx, p.ID)
}

// maxUnknownHits bounds the cards read to learn whose hits are.
const maxUnknownHits = 3

// pick chooses the product's own article among hits with its number: the
// one from its brand, or for a product with no brand the only one.
func pick(p Product, hits []autodb.Article, names map[int64]string) (autodb.Article, bool) {
	if len(p.BrandKeys) == 0 {
		if len(hits) == 1 {
			return hits[0], true
		}
		return autodb.Article{}, false
	}
	for _, h := range hits {
		for _, k := range p.BrandKeys {
			if names[h.SupplierID] == k {
				return h, true
			}
		}
	}
	return autodb.Article{}, false
}
