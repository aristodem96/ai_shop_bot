// Package crawler fills the cache of what fits a vehicle: for an Auto-DB
// modification, every part of every section of its parts tree.
//
// Auto-DB answers a section in seconds, and a modification has hundreds of
// sections, so a crawl takes minutes. It runs in the background: a buyer
// choosing a car queues it, a worker takes it from the queue, and the bot
// reads only what the crawl has stored. A car another buyer chose before is
// already there.
package crawler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/aristodem96/ai_shop_bot/internal/autodb"
	"github.com/aristodem96/ai_shop_bot/internal/domain"
)

// API is the part of the Auto-DB client a crawl reads.
type API interface {
	Sections(ctx context.Context, g autodb.Group, modID, parent int64) ([]autodb.Section, error)
	SectionParts(ctx context.Context, g autodb.Group, modID, sectionID int64) ([]autodb.Part, error)
}

// Part is a crawled part, keyed the way the shop's products are.
type Part struct {
	SupplierID int64
	ArticleKey string
	Number     string
	Brand      string
	BrandKey   string
	Name       string
}

// Status is where a vehicle's crawl is.
type Status struct {
	State          string // "", "queued", "running", "done" or "failed"
	SectionsTotal  int
	SectionsDone   int
	Parts          int
	FinishedAtUnix int64
}

// Done reports whether the vehicle's fitments are complete.
func (s Status) Done() bool { return s.State == "done" }

// Store is the queue and the cache.
type Store interface {
	// Enqueue queues the vehicle's modification, unless it is queued,
	// running, or done within maxAge. A vehicle not from Auto-DB is a no-op.
	Enqueue(ctx context.Context, vehicleID int64, maxAge time.Duration) error
	// Claim takes the oldest queued crawl and marks it running.
	Claim(ctx context.Context) (modID int64, ok bool, err error)
	// Requeue puts crawls left running by a stopped process back in the
	// queue.
	Requeue(ctx context.Context) error
	// SaveParts replaces the parts of one section of a modification.
	SaveParts(ctx context.Context, modID, sectionID int64, parts []Part) error
	Progress(ctx context.Context, modID int64, total, done, parts int) error
	Finish(ctx context.Context, modID int64, failure error) error
	Status(ctx context.Context, vehicleID int64) (Status, error)
}

// Config tunes a Crawler.
type Config struct {
	// Parallel is how many API calls one crawl makes at once.
	Parallel int
	// MaxAge is how long a crawl stays fresh before a buyer choosing the
	// car queues it again.
	MaxAge time.Duration
	// Idle is how long the worker waits when the queue is empty.
	Idle time.Duration
}

// Crawler crawls modifications from the queue.
type Crawler struct {
	api   API
	store Store
	cfg   Config
	log   *slog.Logger
	// onDone runs after a crawl stored its parts.
	onDone func(modID int64)
}

// New returns a Crawler.
func New(api API, store Store, cfg Config, log *slog.Logger) *Crawler {
	if cfg.Parallel <= 0 {
		cfg.Parallel = 4
	}
	if cfg.MaxAge <= 0 {
		cfg.MaxAge = 30 * 24 * time.Hour
	}
	if cfg.Idle <= 0 {
		cfg.Idle = 5 * time.Second
	}
	return &Crawler{api: api, store: store, cfg: cfg, log: log}
}

// OnDone sets what runs after each successful crawl, such as translating
// the names it brought.
func (c *Crawler) OnDone(f func(modID int64)) { c.onDone = f }

// Enqueue queues the crawl of a vehicle the buyer chose.
func (c *Crawler) Enqueue(ctx context.Context, vehicleID int64) error {
	return c.store.Enqueue(ctx, vehicleID, c.cfg.MaxAge)
}

// Status says how far the vehicle's crawl is.
func (c *Crawler) Status(ctx context.Context, vehicleID int64) (Status, error) {
	return c.store.Status(ctx, vehicleID)
}

// Run takes crawls from the queue until ctx is done.
func (c *Crawler) Run(ctx context.Context) error {
	// Crawls are background work and keep to its share of the API's rate.
	ctx = autodb.Background(ctx)
	if err := c.store.Requeue(ctx); err != nil {
		c.log.WarnContext(ctx, "could not requeue crawls left running", slog.Any("error", err))
	}
	for ctx.Err() == nil {
		modID, ok, err := c.store.Claim(ctx)
		if err != nil {
			c.log.WarnContext(ctx, "could not take a crawl", slog.Any("error", err))
		}
		if !ok || err != nil {
			select {
			case <-ctx.Done():
			case <-time.After(c.cfg.Idle):
			}
			continue
		}
		start := time.Now()
		err = c.Crawl(ctx, modID)
		if ferr := c.store.Finish(context.WithoutCancel(ctx), modID, err); ferr != nil {
			c.log.WarnContext(ctx, "could not finish a crawl", slog.Int64("mod_id", modID), slog.Any("error", ferr))
		}
		c.log.InfoContext(ctx, "crawl finished", slog.Int64("mod_id", modID),
			slog.Duration("took", time.Since(start).Round(time.Second)), slog.Any("error", err))
	}
	return nil
}

// sectionRetries is how often one section is asked again before it counts
// as failed, retryDelay the first wait, growing with each attempt.
const sectionRetries = 2

var retryDelay = 2 * time.Second

// Crawl reads a modification's parts tree and stores every part. Sections
// are asked in parallel, a few at a time. A section that keeps failing is
// skipped and counted; a crawl that loses more than a quarter of its
// sections fails as a whole, to be queued again.
func (c *Crawler) Crawl(ctx context.Context, modID int64) error {
	leaves, err := c.leaves(ctx, modID)
	if err != nil {
		return err
	}
	var (
		mu            sync.Mutex
		done, failed  int
		parts         int
		lastProgressN int
	)
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(c.cfg.Parallel)
	for _, sec := range leaves {
		g.Go(func() error {
			got, err := retry(gctx, func() ([]autodb.Part, error) {
				return c.api.SectionParts(gctx, autodb.Passenger, modID, sec)
			})
			if err == nil {
				err = c.store.SaveParts(gctx, modID, sec, keyed(got))
			}
			mu.Lock()
			defer mu.Unlock()
			done++
			if err != nil {
				failed++
				c.log.WarnContext(gctx, "could not crawl a section", slog.Int64("mod_id", modID), slog.Int64("section", sec), slog.Any("error", err))
			} else {
				parts += len(got)
			}
			if done-lastProgressN >= 10 || done == len(leaves) {
				lastProgressN = done
				if perr := c.store.Progress(gctx, modID, len(leaves), done, parts); perr != nil {
					c.log.WarnContext(gctx, "could not save crawl progress", slog.Any("error", perr))
				}
			}
			return nil
		})
	}
	_ = g.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if failed*4 > len(leaves) {
		return fmt.Errorf("crawler: %d of %d sections failed", failed, len(leaves))
	}
	return nil
}

// leaves walks the sections tree breadth first, a level at a time, and
// returns the sections with no children: the ones parts are listed in.
func (c *Crawler) leaves(ctx context.Context, modID int64) ([]int64, error) {
	var leaves []int64
	level := []int64{0}
	for len(level) > 0 {
		var (
			mu   sync.Mutex
			next []int64
		)
		g, gctx := errgroup.WithContext(ctx)
		g.SetLimit(c.cfg.Parallel)
		for _, parent := range level {
			g.Go(func() error {
				secs, err := retry(gctx, func() ([]autodb.Section, error) {
					return c.api.Sections(gctx, autodb.Passenger, modID, parent)
				})
				if err != nil {
					return fmt.Errorf("crawler: sections of %d under %d: %w", modID, parent, err)
				}
				mu.Lock()
				defer mu.Unlock()
				for _, s := range secs {
					if s.HasChildren {
						next = append(next, s.ID)
					} else {
						leaves = append(leaves, s.ID)
					}
				}
				return nil
			})
		}
		if err := g.Wait(); err != nil {
			return nil, err
		}
		level = next
	}
	return leaves, nil
}

// retry asks again after a failure that may pass. A refused address or a
// wrong password will not, and returns at once.
func retry[T any](ctx context.Context, f func() (T, error)) (T, error) {
	var (
		out T
		err error
	)
	for attempt := 0; attempt <= sectionRetries; attempt++ {
		if out, err = f(); err == nil || errors.Is(err, autodb.ErrIPNotAllowed) || errors.Is(err, autodb.ErrUnauthorized) {
			return out, err
		}
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case <-time.After(time.Duration(attempt+1) * retryDelay):
		}
	}
	return out, err
}

// keyed adds the keys a part meets the shop's products by.
func keyed(parts []autodb.Part) []Part {
	out := make([]Part, 0, len(parts))
	for _, p := range parts {
		k := domain.NormalizeOEM(p.Number)
		if k == "" {
			continue
		}
		out = append(out, Part{SupplierID: p.SupplierID, ArticleKey: k, Number: p.Number,
			Brand: p.Brand, BrandKey: domain.NormalizeOEM(p.Brand), Name: p.Name})
	}
	return out
}
