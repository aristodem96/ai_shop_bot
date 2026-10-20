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
	"container/heap"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
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

// Waiter is a buyer waiting for a vehicle's catalogue, to be told when it is
// loaded.
type Waiter struct {
	UserID    int64
	VehicleID int64
}

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
	// Watch records that a buyer waits for the vehicle's crawl, unless it
	// is done or the vehicle is not from Auto-DB.
	Watch(ctx context.Context, userID, vehicleID int64) error
	// Waiters takes the buyers waiting for a modification's crawl.
	Waiters(ctx context.Context, modID int64) ([]Waiter, error)
	// RetryFailed queues again the failed crawls someone waits for that
	// failed more than after ago, and forgets waits older than keep.
	RetryFailed(ctx context.Context, after, keep time.Duration) error
	// SaveSections records the sections' paths, which are the same for
	// every vehicle: a buyer's search finds its sections by them, and their
	// parts are filed in the shop's categories by them.
	SaveSections(ctx context.Context, sections []Section) error
	// FileSections files every known section in the shop's categories
	// again, for words changed since.
	FileSections(ctx context.Context) error
	// Fetched returns the sections of a modification stored since then.
	Fetched(ctx context.Context, modID int64, since time.Time) (map[int64]bool, error)
	// Matching returns a vehicle's modification and up to limit sections
	// not stored for it since then whose paths hold a word of the category
	// (and its subcategories) or all the words of one of phrases; zero for
	// a vehicle not from the catalogue.
	Matching(ctx context.Context, vehicleID int64, category string, phrases []string, since time.Time, limit int) (int64, []int64, error)
}

// Section is a section with no subsections, where parts are listed, and
// its path in the tree: "тормозная система › Тормозные колодки".
type Section struct {
	ID   int64
	Path string
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
	// onReady tells a waiting buyer their car's catalogue is loaded.
	onReady func(ctx context.Context, w Waiter)
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

// OnReady sets how a buyer waiting for their car's catalogue is told it is
// loaded.
func (c *Crawler) OnReady(f func(ctx context.Context, w Waiter)) { c.onReady = f }

// Enqueue queues the crawl of a vehicle.
func (c *Crawler) Enqueue(ctx context.Context, vehicleID int64) error {
	return c.store.Enqueue(ctx, vehicleID, c.cfg.MaxAge)
}

// Watch queues the crawl of a vehicle a buyer chose or asked about, and
// unless it is done, has the buyer told when it is. A crawl that failed is
// queued again.
func (c *Crawler) Watch(ctx context.Context, userID, vehicleID int64) error {
	if err := c.store.Enqueue(ctx, vehicleID, c.cfg.MaxAge); err != nil {
		return err
	}
	return c.store.Watch(ctx, userID, vehicleID)
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
	if err := c.store.FileSections(ctx); err != nil {
		c.log.WarnContext(ctx, "could not file catalogue sections", slog.Any("error", err))
	}
	var retried time.Time
	for ctx.Err() == nil {
		// A buyer was promised a message when the catalogue is loaded, so a
		// crawl that failed -- the API down, the server's address not yet
		// allowed -- is tried again while someone waits for it.
		if time.Since(retried) >= retryFailedEvery {
			retried = time.Now()
			if err := c.store.RetryFailed(ctx, retryFailedAfter, waitKeep); err != nil {
				c.log.WarnContext(ctx, "could not requeue failed crawls", slog.Any("error", err))
			}
		}
		worked, err := c.Next(ctx)
		if err != nil && ctx.Err() == nil {
			c.log.WarnContext(ctx, "crawl queue failed", slog.Any("error", err))
		}
		if !worked {
			select {
			case <-ctx.Done():
			case <-time.After(c.cfg.Idle):
			}
		}
	}
	return nil
}

// Failed crawls someone waits for are looked at every retryFailedEvery and
// queued again retryFailedAfter after they failed. A buyer who has waited
// waitKeep is not told any more.
const (
	retryFailedEvery = time.Minute
	retryFailedAfter = 10 * time.Minute
	waitKeep         = 7 * 24 * time.Hour
)

// Next runs the oldest queued crawl, and tells those waiting for it when it
// is done. It reports false when the queue is empty.
func (c *Crawler) Next(ctx context.Context) (bool, error) {
	modID, ok, err := c.store.Claim(ctx)
	if err != nil || !ok {
		return false, err
	}
	start := time.Now()
	err = c.Crawl(ctx, modID)
	if ctx.Err() != nil {
		// Stopped, not failed: left running, the next start queues it again.
		return true, ctx.Err()
	}
	if ferr := c.store.Finish(context.WithoutCancel(ctx), modID, err); ferr != nil {
		return true, fmt.Errorf("crawler: finish crawl of %d: %w", modID, ferr)
	}
	c.log.InfoContext(ctx, "crawl finished", slog.Int64("mod_id", modID),
		slog.Duration("took", time.Since(start).Round(time.Second)), slog.Any("error", err))
	if err == nil {
		c.ready(ctx, modID)
	}
	return true, nil
}

// ready runs what follows a crawl that stored its parts.
func (c *Crawler) ready(ctx context.Context, modID int64) {
	if c.onDone != nil {
		c.onDone(modID)
	}
	if c.onReady == nil {
		return
	}
	waiters, err := c.store.Waiters(ctx, modID)
	if err != nil {
		c.log.WarnContext(ctx, "could not read who waits for a crawl", slog.Int64("mod_id", modID), slog.Any("error", err))
		return
	}
	for _, w := range waiters {
		c.onReady(ctx, w)
	}
}

// sectionRetries is how often one section is asked again before it counts
// as failed, retryDelay the first wait, growing with each attempt.
const sectionRetries = 2

var retryDelay = 2 * time.Second

// Crawl reads a modification's parts tree and stores every part.
//
// Walking the tree and loading its sections are one queue, not two steps:
// a section with parts is loaded as soon as it is found, and the most
// wanted branches -- brakes, filters, the chassis -- are walked and loaded
// first. A tree walked level by level before anything is loaded keeps the
// buyer at "collecting sections" while each level waits on its slowest
// call; this way a car's brakes are in within seconds. A section that keeps
// failing is skipped and counted; a crawl that loses more than a quarter of
// its sections fails as a whole, to be queued again.
func (c *Crawler) Crawl(ctx context.Context, modID int64) error {
	// A buyer's search may have stored some already.
	fetched, err := c.store.Fetched(ctx, modID, time.Now().Add(-c.cfg.MaxAge))
	if err != nil {
		return err
	}
	w := &walk{c: c, modID: modID, fetched: fetched}
	w.cond = sync.NewCond(&w.mu)
	heap.Push(&w.queue, &crawlTask{rank: 0})

	// Waiting workers wake when the crawl is stopped, not only when work
	// comes.
	stop := context.AfterFunc(ctx, func() {
		w.mu.Lock()
		w.cond.Broadcast()
		w.mu.Unlock()
	})
	defer stop()

	var wg sync.WaitGroup
	for range max(c.cfg.Parallel, 1) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.work(ctx)
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if w.rootErr != nil {
		return w.rootErr
	}
	if err := c.store.Progress(ctx, modID, len(w.leaves), w.done, w.parts); err != nil {
		c.log.WarnContext(ctx, "could not save crawl progress", slog.Any("error", err))
	}
	if w.failed*4 > max(len(w.leaves), 1) {
		return fmt.Errorf("crawler: %d of %d sections failed", w.failed, len(w.leaves))
	}
	return nil
}

// crawlTask is a section to ask about: its subsections, or, for a leaf,
// its parts. The root is the zero task.
type crawlTask struct {
	section Section
	leaf    bool
	rank    int // priority of the path: lower first
	seq     int // order found, among equal ranks
}

// taskQueue orders tasks by rank, then by the order they were found.
type taskQueue []*crawlTask

// Len, Less, Swap, Push and Pop make taskQueue a container/heap.
func (q taskQueue) Len() int { return len(q) }

// Less puts branches before leaves -- walking the tree takes seconds and
// tells the buyer how much there is to load -- then the more wanted first,
// then the earlier found.
func (q taskQueue) Less(i, j int) bool {
	if q[i].leaf != q[j].leaf {
		return !q[i].leaf
	}
	if q[i].rank != q[j].rank {
		return q[i].rank < q[j].rank
	}
	return q[i].seq < q[j].seq
}

// Swap swaps two tasks.
func (q taskQueue) Swap(i, j int) { q[i], q[j] = q[j], q[i] }

// Push adds a task; only *crawlTask is ever pushed.
func (q *taskQueue) Push(x any) {
	if t, ok := x.(*crawlTask); ok {
		*q = append(*q, t)
	}
}

// Pop takes the last task.
func (q *taskQueue) Pop() any {
	old := *q
	t := old[len(old)-1]
	*q = old[:len(old)-1]
	return t
}

// next takes the most wanted task.
func (q *taskQueue) next() *crawlTask {
	t, _ := heap.Pop(q).(*crawlTask)
	return t
}

// walk is one crawl in progress, shared by its workers.
type walk struct {
	c       *Crawler
	modID   int64
	fetched map[int64]bool

	mu       sync.Mutex
	cond     *sync.Cond
	queue    taskQueue
	inflight int
	seq      int
	// pending counts branches being walked; with none running and none
	// queued, every leaf is known.
	pending      int
	leaves       []Section
	done, failed int
	parts        int
	lastProgress int
	walked       bool // every branch walked, the number of sections known
	rootErr      error
}

// work takes the next task until the tree is done or the crawl stopped.
func (w *walk) work(ctx context.Context) {
	for {
		w.mu.Lock()
		for w.queue.Len() == 0 && w.inflight > 0 && ctx.Err() == nil {
			w.cond.Wait()
		}
		if ctx.Err() != nil || w.queue.Len() == 0 {
			w.cond.Broadcast()
			w.mu.Unlock()
			return
		}
		t := w.queue.next()
		w.inflight++
		if !t.leaf {
			w.pending++ // a branch being walked: more leaves may come
		}
		w.mu.Unlock()

		if t.leaf {
			w.load(ctx, t)
		} else {
			w.expand(ctx, t)
		}

		w.mu.Lock()
		w.inflight--
		if !t.leaf {
			w.pending--
			if w.pending == 0 && w.queueHasNoBranches() && !w.walked {
				// The whole tree is known: from here the buyer sees a
				// percentage.
				w.walked = true
				if err := w.c.store.Progress(ctx, w.modID, len(w.leaves), w.done, w.parts); err != nil {
					w.c.log.WarnContext(ctx, "could not save crawl progress", slog.Any("error", err))
				}
			}
		}
		w.cond.Broadcast()
		w.mu.Unlock()
	}
}

// expand asks for a section's subsections and queues them: a branch to walk
// further, or a leaf to load. Leaves' paths are saved as they are found, for
// a buyer's search to find them before the crawl ends.
func (w *walk) expand(ctx context.Context, t *crawlTask) {
	secs, err := retry(ctx, func() ([]autodb.Section, error) {
		return w.c.api.Sections(ctx, autodb.Passenger, w.modID, t.section.ID)
	})
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		w.mu.Lock()
		defer w.mu.Unlock()
		if t.section.ID == 0 {
			w.rootErr = fmt.Errorf("crawler: sections of %d: %w", w.modID, err)
			return
		}
		w.failed++
		w.c.log.WarnContext(ctx, "could not walk a section", slog.Int64("mod_id", w.modID), slog.Int64("section", t.section.ID), slog.Any("error", err))
		return
	}
	var found []Section
	w.mu.Lock()
	for _, s := range secs {
		path := s.Name
		if t.section.Path != "" {
			path = t.section.Path + " › " + s.Name
		}
		sec := Section{ID: s.ID, Path: path}
		w.seq++
		next := &crawlTask{section: sec, leaf: !s.HasChildren, rank: priority(path), seq: w.seq}
		if next.leaf {
			w.leaves = append(w.leaves, sec)
			found = append(found, sec)
			if w.fetched[sec.ID] {
				w.done++ // a buyer's search stored it already
				continue
			}
		}
		heap.Push(&w.queue, next)
	}
	w.mu.Unlock()
	if len(found) > 0 {
		if err := w.c.store.SaveSections(ctx, found); err != nil {
			w.c.log.WarnContext(ctx, "could not save section paths", slog.Any("error", err))
		}
	}
}

// load stores a leaf's parts and records the progress.
func (w *walk) load(ctx context.Context, t *crawlTask) {
	sec := t.section.ID
	got, err := retry(ctx, func() ([]autodb.Part, error) {
		return w.c.api.SectionParts(ctx, autodb.Passenger, w.modID, sec)
	})
	if err == nil {
		err = w.c.store.SaveParts(ctx, w.modID, sec, keyed(got))
	}
	if ctx.Err() != nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.done++
	if err != nil {
		w.failed++
		w.c.log.WarnContext(ctx, "could not crawl a section", slog.Int64("mod_id", w.modID), slog.Int64("section", sec), slog.Any("error", err))
	} else {
		w.parts += len(got)
	}
	if w.done-w.lastProgress >= 10 {
		w.lastProgress = w.done
		// The total is known only once no branch is left to walk; until
		// then the buyer sees the parts so far.
		total := 0
		if w.walked {
			total = len(w.leaves)
		}
		if perr := w.c.store.Progress(ctx, w.modID, total, w.done, w.parts); perr != nil {
			w.c.log.WarnContext(ctx, "could not save crawl progress", slog.Any("error", perr))
		}
	}
}

// queueHasNoBranches reports whether every queued task is a leaf. Called
// with mu held.
func (w *walk) queueHasNoBranches() bool {
	for _, t := range w.queue {
		if !t.leaf {
			return false
		}
	}
	return true
}

// priorities are the parts buyers ask for most, by words of the catalogue's
// section paths, most wanted first: wear parts, then the chassis, then the
// engine's service parts. A section matching none comes last.
var priorities = [][]string{
	{"тормоз", "фильтр", "сервис", "колодк"},
	{"амортиз", "подвеск", "рулев", "рычаг", "шарнир", "стабилизат", "ступиц", "пружин", "сайлентблок"},
	{"сцеплен", "ремен", "ремн", "свеч", "зажиган", "охлажд", "привод колеса", "шрус", "опора двиг", "подшипник"},
}

// priority ranks a section by its path: lower comes first.
func priority(path string) int {
	path = strings.ToLower(path)
	for rank, words := range priorities {
		for _, w := range words {
			if strings.Contains(path, w) {
				return rank
			}
		}
	}
	return len(priorities)
}

// fetchNowLimit bounds the sections one search loads while a buyer waits,
// and fetchNowParallel how many at once: a few seconds at a buyer's rate.
const (
	fetchNowLimit    = 6
	fetchNowParallel = 3
)

// FetchNow loads, while a buyer waits, the sections of their car's
// catalogue that a search names -- by its category's words or its own --
// and that are not stored yet, at a buyer's priority rather than the
// background's. It returns the names of the parts it brought, to be
// translated before the search reads them.
func (c *Crawler) FetchNow(ctx context.Context, vehicleID int64, category string, phrases []string) ([]string, error) {
	modID, secs, err := c.store.Matching(ctx, vehicleID, category, phrases, time.Now().Add(-c.cfg.MaxAge), fetchNowLimit)
	if err != nil || len(secs) == 0 {
		return nil, err
	}
	var (
		mu    sync.Mutex
		names []string
		seen  = map[string]bool{}
	)
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(fetchNowParallel)
	for _, sec := range secs {
		g.Go(func() error {
			got, err := c.api.SectionParts(gctx, autodb.Passenger, modID, sec)
			if err == nil {
				err = c.store.SaveParts(gctx, modID, sec, keyed(got))
			}
			if err != nil {
				c.log.WarnContext(gctx, "could not load a section for a buyer", slog.Int64("mod_id", modID), slog.Int64("section", sec), slog.Any("error", err))
				return nil
			}
			mu.Lock()
			defer mu.Unlock()
			for _, p := range got {
				if p.Name != "" && !seen[p.Name] {
					seen[p.Name] = true
					names = append(names, p.Name)
				}
			}
			return nil
		})
	}
	_ = g.Wait()
	return names, ctx.Err()
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
