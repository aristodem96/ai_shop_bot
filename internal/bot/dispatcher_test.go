package bot

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aristodem96/ai_shop_bot/internal/telegram"
)

func TestDispatcherKeepsOrderWithinAChat(t *testing.T) {
	var (
		mu  sync.Mutex
		got = map[int64][]int64{}
	)
	d := newDispatcher(1000, func(u telegram.Update) {
		chat := u.Message.Chat.ID
		mu.Lock()
		got[chat] = append(got[chat], u.UpdateID)
		mu.Unlock()
	})

	const chats, perChat = 20, 50
	for i := range int64(perChat) {
		for chat := range int64(chats) {
			require.True(t, d.Dispatch(context.Background(), chat, msgUpdate(i, chat)))
		}
	}
	d.Wait()

	for chat := range int64(chats) {
		require.Len(t, got[chat], perChat)
		for i, id := range got[chat] {
			assert.Equal(t, int64(i), id, "chat %d handled update %d out of order", chat, id)
		}
	}
	assert.Empty(t, d.queues, "drained chats leave no queue behind")
}

func TestDispatcherRunsChatsInParallel(t *testing.T) {
	// Two chats each block until the other has started. Run one after the
	// other, they would deadlock; the timeout turns that into a failure.
	started := map[int64]chan struct{}{1: make(chan struct{}), 2: make(chan struct{})}
	d := newDispatcher(10, func(u telegram.Update) {
		chat := u.Message.Chat.ID
		close(started[chat])
		select {
		case <-started[3-chat]:
		case <-time.After(5 * time.Second):
			t.Errorf("chat %d never saw chat %d start: chats are serialised", chat, 3-chat)
		}
	})

	d.Dispatch(context.Background(), 1, msgUpdate(1, 1))
	d.Dispatch(context.Background(), 2, msgUpdate(2, 2))
	d.Wait()
}

func TestDispatcherAppliesBackpressure(t *testing.T) {
	release := make(chan struct{})
	d := newDispatcher(2, func(telegram.Update) { <-release })

	ctx := context.Background()
	require.True(t, d.Dispatch(ctx, 1, msgUpdate(1, 1)))
	require.True(t, d.Dispatch(ctx, 2, msgUpdate(2, 2)))

	// Both slots are taken by handlers that have not returned.
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	assert.False(t, d.Dispatch(short, 3, msgUpdate(3, 3)), "a full dispatcher must block, not queue")

	close(release)
	d.Wait()
}

func msgUpdate(id, chat int64) telegram.Update {
	return telegram.Update{UpdateID: id, Message: &telegram.Message{Chat: telegram.Chat{ID: chat}}}
}
