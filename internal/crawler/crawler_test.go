package crawler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aristodem96/ai_shop_bot/internal/autodb"
)

// tree is a parts tree: brakes (with pads and discs) and filters, a leaf.
type fakeAPI struct {
	mu        sync.Mutex
	failsLeft map[int64]int // section -> failures before it answers
	calls     int
	asked     []int64 // sections whose parts were asked for
}

var tree = map[int64][]autodb.Section{
	0:   {{ID: 1, Name: "тормозная система", HasChildren: true}, {ID: 2, Name: "фильтр"}},
	1:   {{ID: 11, Name: "колодки"}, {ID: 12, Name: "диски", HasChildren: true}},
	12:  {{ID: 121, Name: "диски передние"}},
	999: nil,
}

var partsOf = map[int64][]autodb.Part{
	11:  {{SupplierID: 65, Number: "S 59 513", Brand: "BREMBO", Name: "Колодки"}},
	121: {{SupplierID: 30, Number: "0 986 479 C93", Brand: "BOSCH", Name: "Диск"}, {SupplierID: 30, Number: "", Brand: "BOSCH"}},
	2:   {{SupplierID: 4, Number: "W 712/95", Brand: "MANN-FILTER", Name: "Фильтр масляный"}},
}

func (f *fakeAPI) Sections(_ context.Context, _ autodb.Group, _ int64, parent int64) ([]autodb.Section, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return tree[parent], nil
}

func (f *fakeAPI) SectionParts(_ context.Context, _ autodb.Group, _ int64, sec int64) ([]autodb.Part, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.asked = append(f.asked, sec)
	if f.failsLeft[sec] > 0 {
		f.failsLeft[sec]--
		return nil, errors.New("autodb: unavailable: timeout")
	}
	return partsOf[sec], nil
}

type memStore struct {
	mu       sync.Mutex
	parts    map[int64][]Part // by section
	progress [3]int
	finished error
	ended    bool
	queue    []int64
	waiters  map[int64][]Waiter // by modification
	watched  []Waiter
	sections []Section
	fetched  map[int64]bool
	matching []int64
}

func (s *memStore) SaveSections(_ context.Context, secs []Section) error {
	s.sections = append(s.sections, secs...)
	return nil
}
func (s *memStore) Fetched(context.Context, int64, time.Time) (map[int64]bool, error) {
	return s.fetched, nil
}
func (s *memStore) Matching(_ context.Context, vehicleID int64, _ string, _ []string, _ time.Time, _ int) (int64, []int64, error) {
	if vehicleID != 40 {
		return 0, nil, nil
	}
	return 18537, s.matching, nil
}

func (s *memStore) Enqueue(context.Context, int64, time.Duration) error { return nil }
func (s *memStore) Claim(context.Context) (int64, bool, error) {
	if len(s.queue) == 0 {
		return 0, false, nil
	}
	m := s.queue[0]
	s.queue = s.queue[1:]
	return m, true, nil
}
func (s *memStore) Watch(_ context.Context, user, vehicle int64) error {
	s.watched = append(s.watched, Waiter{UserID: user, VehicleID: vehicle})
	return nil
}
func (s *memStore) Waiters(_ context.Context, mod int64) ([]Waiter, error) {
	w := s.waiters[mod]
	delete(s.waiters, mod)
	return w, nil
}
func (s *memStore) RetryFailed(context.Context, time.Duration, time.Duration) error { return nil }
func (s *memStore) Requeue(context.Context) error                                   { return nil }
func (s *memStore) SaveParts(_ context.Context, _, sec int64, p []Part) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.parts[sec] = p
	return nil
}
func (s *memStore) Progress(_ context.Context, _ int64, total, done, parts int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.progress = [3]int{total, done, parts}
	return nil
}
func (s *memStore) Finish(_ context.Context, _ int64, err error) error {
	s.finished, s.ended = err, true
	return nil
}
func (s *memStore) Status(context.Context, int64) (Status, error) { return Status{}, nil }

func init() { retryDelay = time.Millisecond }

func newCrawler(api *fakeAPI) (*Crawler, *memStore) {
	store := &memStore{parts: map[int64][]Part{}}
	return New(api, store, Config{Parallel: 2}, slog.New(slog.NewTextHandler(io.Discard, nil))), store
}

func TestCrawlWalksTheTree(t *testing.T) {
	api := &fakeAPI{failsLeft: map[int64]int{11: 1}}
	c, store := newCrawler(api)

	require.NoError(t, c.Crawl(context.Background(), 18537))
	sections := make([]int64, 0, len(store.parts))
	for s := range store.parts {
		sections = append(sections, s)
	}
	sort.Slice(sections, func(i, j int) bool { return sections[i] < sections[j] })
	assert.Equal(t, []int64{2, 11, 121}, sections, "every leaf, at any depth, and none of the nodes")

	assert.Equal(t, []Part{{SupplierID: 65, ArticleKey: "S59513", Number: "S 59 513", Brand: "BREMBO", BrandKey: "BREMBO", Name: "Колодки"}},
		store.parts[11], "a section that failed once is asked again")
	assert.Equal(t, "MANNFILTER", store.parts[2][0].BrandKey, "keys are written the way products' are")
	assert.Len(t, store.parts[121], 1, "a part with no number is no key to match")
	assert.Equal(t, [3]int{3, 3, 4}, store.progress)
}

func TestCrawlFailsWhenTooMuchIsLost(t *testing.T) {
	api := &fakeAPI{failsLeft: map[int64]int{11: 10, 121: 10}}
	c, _ := newCrawler(api)

	err := c.Crawl(context.Background(), 18537)
	require.ErrorContains(t, err, "2 of 3 sections failed", "to be queued again rather than kept half done")
}

func TestRetryGivesUpOnARefusedAddress(t *testing.T) {
	calls := 0
	_, err := retry(context.Background(), func() (int, error) {
		calls++
		return 0, autodb.ErrIPNotAllowed
	})
	require.ErrorIs(t, err, autodb.ErrIPNotAllowed)
	assert.Equal(t, 1, calls, "asking again will not change the dashboard")
}

func TestNextTellsThoseWaiting(t *testing.T) {
	c, store := newCrawler(&fakeAPI{})
	store.queue = []int64{18537}
	store.waiters = map[int64][]Waiter{18537: {{UserID: 5, VehicleID: 40}, {UserID: 6, VehicleID: 41}}}
	var told []Waiter
	done := 0
	c.OnReady(func(_ context.Context, w Waiter) { told = append(told, w) })
	c.OnDone(func(int64) { done++ })

	worked, err := c.Next(context.Background())
	require.NoError(t, err)
	assert.True(t, worked)
	require.NoError(t, store.finished)
	assert.Equal(t, []Waiter{{UserID: 5, VehicleID: 40}, {UserID: 6, VehicleID: 41}}, told)
	assert.Equal(t, 1, done, "what follows a crawl, such as translating names, is woken")

	worked, err = c.Next(context.Background())
	require.NoError(t, err)
	assert.False(t, worked, "the queue is empty")
}

func TestNextKeepsWaitersOfAFailedCrawl(t *testing.T) {
	c, store := newCrawler(&fakeAPI{failsLeft: map[int64]int{11: 10, 121: 10}})
	store.queue = []int64{18537}
	store.waiters = map[int64][]Waiter{18537: {{UserID: 5, VehicleID: 40}}}
	c.OnReady(func(context.Context, Waiter) { t.Fatal("a failed crawl is no news") })

	_, err := c.Next(context.Background())
	require.NoError(t, err)
	require.Error(t, store.finished)
	assert.Len(t, store.waiters[18537], 1, "still waiting: the crawl is tried again")
}

func TestNextLeavesAStoppedCrawlRunning(t *testing.T) {
	c, store := newCrawler(&fakeAPI{})
	store.queue = []int64{18537}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Next(ctx)
	require.ErrorIs(t, err, context.Canceled)
	assert.False(t, store.ended, "not failed: the next start queues it again")
}

func TestWatchQueuesAndWaits(t *testing.T) {
	c, store := newCrawler(&fakeAPI{})
	require.NoError(t, c.Watch(context.Background(), 5, 40))
	assert.Equal(t, []Waiter{{UserID: 5, VehicleID: 40}}, store.watched)
}

func TestCrawlSavesPathsAndSkipsWhatIsStored(t *testing.T) {
	api := &fakeAPI{}
	c, store := newCrawler(api)
	store.fetched = map[int64]bool{11: true}

	require.NoError(t, c.Crawl(context.Background(), 18537))
	assert.ElementsMatch(t, []Section{
		{ID: 2, Path: "фильтр"}, {ID: 11, Path: "тормозная система › колодки"},
		{ID: 121, Path: "тормозная система › диски › диски передние"},
	}, store.sections, "paths, for a buyer's search to find sections by")
	assert.ElementsMatch(t, []int64{2, 121}, api.asked, "a section a search stored is not asked again")
	assert.Equal(t, [3]int{3, 3, 3}, store.progress, "and counts as done")
}

func TestPriorityPutsWearPartsFirst(t *testing.T) {
	assert.Equal(t, 0, priority("тормозная система › Тормозные колодки"))
	assert.Equal(t, 0, priority("фильтр › Масляный фильтр"))
	assert.Equal(t, 1, priority("Рулевое управления › Наконечник поперечной рулевой тяги"))
	assert.Equal(t, 2, priority("Система сцепления / навесные части › Комплект сцепления"))
	assert.Equal(t, len(priorities), priority("Внутренняя отделка › Ручка двери"))
}

func TestFetchNowLoadsTheSearchsSections(t *testing.T) {
	api := &fakeAPI{}
	c, store := newCrawler(api)
	store.matching = []int64{11, 2}

	names, err := c.FetchNow(context.Background(), 40, "brake-pads", nil)
	require.NoError(t, err)
	assert.ElementsMatch(t, []int64{11, 2}, api.asked)
	assert.ElementsMatch(t, []string{"Колодки", "Фильтр масляный"}, names, "to be translated before the search reads them")
	assert.Len(t, store.parts[11], 1)

	names, err = c.FetchNow(context.Background(), 41, "brake-pads", nil)
	require.NoError(t, err)
	assert.Empty(t, names, "a car outside the catalogue has nothing to load")
}
