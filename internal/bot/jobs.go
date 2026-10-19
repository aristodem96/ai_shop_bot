package bot

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// KeepDialogs keeps the managers' dialogs in stores newStore makes, one per
// kind, so a restart does not lose them. Call it before UseImports,
// UseVehicleMatching and UseWords.
func (b *Bot) KeepDialogs(newStore func(prefix string, ttl time.Duration) Sessions) {
	b.dialogStore = func(prefix string, ttl time.Duration) Sessions { return newStore(prefix, ttl) }
}

// dialogs returns the store for one kind of dialog, nil without KeepDialogs.
func (b *Bot) dialogs(prefix string, ttl time.Duration) Sessions {
	if b.dialogStore == nil {
		return nil
	}
	return b.dialogStore(prefix, ttl)
}

// firstJobID is where ids start: the clock, so ids of an earlier run are
// never handed out again.
var firstJobID = func() int64 { return time.Now().UnixMilli() }

// job is a managers' dialog in progress: an uploaded price list, a matching
// session, a car being added, a word being filed.
type job interface {
	jobID() int64
	setJobID(id int64)
	createdAt() time.Time
	setCreatedAt(t time.Time)
}

// jobs holds the dialogs of the managers' chat between button presses.
//
// They live in memory, and in Redis when a store is given: a restart in the
// middle of mapping forty vehicle names would otherwise drop the session,
// and its buttons would answer "expired". Ids start at the clock in
// milliseconds, so a restarted bot never reuses an id a button still
// carries.
//
// A job is changed in place by the handler that loaded it and written back
// with save; one chat is handled one update at a time, so two handlers do
// not race on one job.
type jobs[T any, P interface {
	*T
	job
}] struct {
	mu       sync.Mutex
	ttl      time.Duration
	next     int64
	items    map[int64]P
	awaiting map[int64]int64 // chat -> job its next message answers
	store    Sessions        // by job id; nil keeps them in memory only
	awaitAt  Sessions        // by chat id
	log      *slog.Logger
	// drop runs when an expired job is dropped, to remove its files.
	drop func(P)
}

func newJobs[T any, P interface {
	*T
	job
}](ttl time.Duration, store, awaitAt Sessions, log *slog.Logger) *jobs[T, P] {
	return &jobs[T, P]{ttl: ttl, next: firstJobID(), items: make(map[int64]P),
		awaiting: make(map[int64]int64), store: store, awaitAt: awaitAt, log: log}
}

func (s *jobs[T, P]) expired(j P) bool { return time.Since(j.createdAt()) > s.ttl }

// add gives the job a new id and keeps it, dropping those that waited too
// long.
func (s *jobs[T, P]) add(ctx context.Context, j P) P {
	s.mu.Lock()
	for id, x := range s.items {
		if s.expired(x) {
			if s.drop != nil {
				s.drop(x)
			}
			delete(s.items, id)
		}
	}
	s.next++
	j.setJobID(s.next)
	if j.createdAt().IsZero() {
		j.setCreatedAt(time.Now())
	}
	s.items[j.jobID()] = j
	s.mu.Unlock()
	s.save(ctx, j)
	return j
}

// get returns a job that has not expired, from memory or, after a restart,
// from the store.
func (s *jobs[T, P]) get(ctx context.Context, id int64) (P, bool) {
	j, ok := s.find(ctx, id)
	if !ok || s.expired(j) {
		return nil, false
	}
	return j, true
}

// find returns a job, expired or not.
func (s *jobs[T, P]) find(ctx context.Context, id int64) (P, bool) {
	s.mu.Lock()
	j, ok := s.items[id]
	s.mu.Unlock()
	if ok || s.store == nil {
		return j, ok
	}
	loaded := P(new(T))
	found, err := s.store.Load(ctx, id, loaded)
	if err != nil {
		s.log.WarnContext(ctx, "could not load a managers' dialog", slog.Int64("id", id), slog.Any("error", err))
	}
	if !found {
		return nil, false
	}
	s.mu.Lock()
	s.items[id] = loaded
	s.mu.Unlock()
	return loaded, true
}

// save writes a changed job back to the store.
func (s *jobs[T, P]) save(ctx context.Context, j P) {
	if s.store == nil {
		return
	}
	if err := s.store.Save(ctx, j.jobID(), j); err != nil {
		s.log.WarnContext(ctx, "could not keep a managers' dialog", slog.Int64("id", j.jobID()), slog.Any("error", err))
	}
}

// remove forgets a job.
func (s *jobs[T, P]) remove(ctx context.Context, id int64) {
	s.mu.Lock()
	delete(s.items, id)
	s.mu.Unlock()
	if s.store == nil {
		return
	}
	if err := s.store.Delete(ctx, id); err != nil {
		s.log.WarnContext(ctx, "could not forget a managers' dialog", slog.Int64("id", id), slog.Any("error", err))
	}
}

// take returns a job and forgets it: the button that ends it works once.
// An expired one is dropped, files and all.
func (s *jobs[T, P]) take(ctx context.Context, id int64) (P, bool) {
	j, ok := s.find(ctx, id)
	if !ok {
		return nil, false
	}
	s.remove(ctx, id)
	if s.expired(j) {
		if s.drop != nil {
			s.drop(j)
		}
		return nil, false
	}
	return j, true
}

// put keeps a job taken earlier, under its own id.
func (s *jobs[T, P]) put(ctx context.Context, j P) {
	s.mu.Lock()
	s.items[j.jobID()] = j
	s.mu.Unlock()
	s.save(ctx, j)
}

// await marks the job the chat's next message answers.
func (s *jobs[T, P]) await(ctx context.Context, chat, id int64) {
	s.mu.Lock()
	s.awaiting[chat] = id
	s.mu.Unlock()
	if s.awaitAt == nil {
		return
	}
	if err := s.awaitAt.Save(ctx, chat, id); err != nil {
		s.log.WarnContext(ctx, "could not keep an awaited reply", slog.Any("error", err))
	}
}

// awaited returns the job the chat's next message answers, once: a second
// message is an ordinary message again.
func (s *jobs[T, P]) awaited(ctx context.Context, chat int64) (P, bool) {
	s.mu.Lock()
	id, ok := s.awaiting[chat]
	s.mu.Unlock()
	if !ok && s.awaitAt != nil {
		found, err := s.awaitAt.Load(ctx, chat, &id)
		if err != nil {
			s.log.WarnContext(ctx, "could not load an awaited reply", slog.Any("error", err))
		}
		ok = found
	}
	if !ok {
		return nil, false
	}
	s.forget(ctx, chat)
	return s.get(ctx, id)
}

// forget clears what the chat's next message answers.
func (s *jobs[T, P]) forget(ctx context.Context, chat int64) {
	s.mu.Lock()
	delete(s.awaiting, chat)
	s.mu.Unlock()
	if s.awaitAt == nil {
		return
	}
	if err := s.awaitAt.Delete(ctx, chat); err != nil {
		s.log.WarnContext(ctx, "could not clear an awaited reply", slog.Any("error", err))
	}
}
