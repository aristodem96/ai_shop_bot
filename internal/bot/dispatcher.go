package bot

import (
	"context"
	"sync"

	"github.com/aristodem96/ai_shop_bot/internal/telegram"
)

// dispatcher runs updates of one chat strictly in order and updates of
// different chats in parallel.
//
// In order, because a chat is a conversation: "+1" then "+1" must leave two
// units, and from stage 2 a message must see the agent's answer to the one
// before it. In parallel, because one buyer waiting seconds for the LLM must
// not hold up everyone else.
//
// A plain mutex per chat would not do: goroutines started for two updates of
// the same chat race for the lock, and the second tap can win. Instead each
// busy chat has a FIFO queue drained by exactly one goroutine, which exits
// when the queue empties, so idle chats cost nothing.
type dispatcher struct {
	handle func(telegram.Update)

	// slots bounds updates accepted but not yet handled. Dispatch blocks when
	// it is full, which stops the poll loop: backpressure lands on Telegram,
	// which keeps the updates for us, instead of on our memory.
	slots chan struct{}

	mu     sync.Mutex
	queues map[int64][]telegram.Update
	wg     sync.WaitGroup
}

func newDispatcher(maxPending int, handle func(telegram.Update)) *dispatcher {
	return &dispatcher{
		handle: handle,
		slots:  make(chan struct{}, maxPending),
		queues: make(map[int64][]telegram.Update),
	}
}

// Dispatch queues u behind the chat's earlier updates. It reports false when
// ctx was cancelled while waiting for room, in which case u was not queued.
func (d *dispatcher) Dispatch(ctx context.Context, chatID int64, u telegram.Update) bool {
	select {
	case d.slots <- struct{}{}:
	case <-ctx.Done():
		return false
	}

	d.mu.Lock()
	q, busy := d.queues[chatID]
	d.queues[chatID] = append(q, u)
	d.mu.Unlock()

	// The chat already has a goroutine draining its queue; it will get here.
	if !busy {
		d.wg.Go(func() { d.drain(chatID) })
	}
	return true
}

func (d *dispatcher) drain(chatID int64) {
	for {
		d.mu.Lock()
		u := d.queues[chatID][0]
		d.mu.Unlock()

		d.handle(u)
		<-d.slots

		d.mu.Lock()
		rest := d.queues[chatID][1:]
		if len(rest) == 0 {
			// Deleting under the same lock Dispatch takes is what makes the
			// hand-over safe: an update arriving now sees no queue and starts
			// a fresh goroutine rather than appending to one that is leaving.
			delete(d.queues, chatID)
			d.mu.Unlock()
			return
		}
		d.queues[chatID] = rest
		d.mu.Unlock()
	}
}

// Wait blocks until every queued update has been handled. It must not run
// concurrently with Dispatch.
func (d *dispatcher) Wait() { d.wg.Wait() }
