package bot

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeAbandoned hands out the queued users in batches, the way the claim does.
type fakeAbandoned struct {
	queue  []int64
	claims int
}

func (f *fakeAbandoned) ClaimAbandoned(_ context.Context, _ time.Duration, limit int) ([]int64, error) {
	f.claims++
	n := min(limit, len(f.queue))
	got := f.queue[:n]
	f.queue = f.queue[n:]
	return got, nil
}

func newReminder(t *testing.T, hour int, queue ...int64) (*Reminder, testBot, *fakeAbandoned) {
	t.Helper()
	b := newTestBot()
	kyiv, err := time.LoadLocation("Europe/Kyiv")
	require.NoError(t, err)
	abandoned := &fakeAbandoned{queue: queue}
	r := NewReminder(abandoned, b.cart, b.tg, ReminderConfig{
		After: 24 * time.Hour, Every: time.Minute, Location: kyiv, FromHour: 9, ToHour: 21,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.now = func() time.Time { return time.Date(2026, 10, 15, hour, 0, 0, 0, kyiv) }
	return r, b, abandoned
}

func TestReminderSendsWithCurrentTotal(t *testing.T) {
	r, b, _ := newReminder(t, 12, chat)
	b.cart.qty[padsID] = 2

	sent, err := r.RunOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, sent)

	msg := b.tg.last("sendMessage")
	assert.Equal(t, chat, msg.chat)
	assert.Contains(t, msg.text, "2 шт. на суму <b>2 900 грн</b>")
	assert.Contains(t, callbacks(msg.markup), encode(actCart))
}

func TestReminderSkipsCartsEmptiedMeanwhile(t *testing.T) {
	r, b, _ := newReminder(t, 12, chat)

	sent, err := r.RunOnce(context.Background())
	require.NoError(t, err)
	assert.Zero(t, sent)
	assert.Empty(t, b.tg.calls)
}

func TestReminderRespectsQuietHours(t *testing.T) {
	r, b, abandoned := newReminder(t, 22, chat)
	b.cart.qty[padsID] = 1

	sent, err := r.RunOnce(context.Background())
	require.NoError(t, err)
	assert.Zero(t, sent)
	assert.Zero(t, abandoned.claims, "nothing is claimed at night, so nothing is lost")
	assert.Len(t, abandoned.queue, 1)
}

func TestReminderDrainsAllBatches(t *testing.T) {
	users := make([]int64, reminderBatch*2+3)
	for i := range users {
		users[i] = int64(1000 + i)
	}
	r, _, abandoned := newReminder(t, 12, users...)

	_, err := r.RunOnce(context.Background())
	require.NoError(t, err)
	assert.Empty(t, abandoned.queue)
	assert.Equal(t, 3, abandoned.claims)
}
