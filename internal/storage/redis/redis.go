// Package redis keeps the short-lived state of the AI consultant: the
// conversation of each chat and the per-user budget of model calls. Both are
// fine to lose -- a lost conversation only costs context -- which is why they
// live here rather than in Postgres.
package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/aristodem96/ai_shop_bot/internal/agent"
	"github.com/aristodem96/ai_shop_bot/internal/llm"
)

// NewClient opens and verifies a connection.
func NewClient(ctx context.Context, addr, password string, db int) (*goredis.Client, error) {
	rdb := goredis.NewClient(&goredis.Options{Addr: addr, Password: password, DB: db})
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("redis: ping %s: %w", addr, err)
	}
	return rdb, nil
}

var _ agent.History = (*History)(nil)

// History stores each chat's conversation as one JSON value with a TTL. A
// buyer who comes back the next day starts fresh, which is what they expect
// from a shop assistant, and abandoned chats clean themselves up.
type History struct {
	rdb *goredis.Client
	ttl time.Duration
}

// NewHistory returns a History whose conversations expire ttl after their
// last message.
func NewHistory(rdb *goredis.Client, ttl time.Duration) *History {
	return &History{rdb: rdb, ttl: ttl}
}

func historyKey(chatID int64) string { return "agent:history:" + strconv.FormatInt(chatID, 10) }

// Load returns the conversation, empty if there is none.
func (h *History) Load(ctx context.Context, chatID int64) ([]llm.Message, error) {
	raw, err := h.rdb.Get(ctx, historyKey(chatID)).Bytes()
	if errors.Is(err, goredis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("redis: load history: %w", err)
	}
	var msgs []llm.Message
	if err := json.Unmarshal(raw, &msgs); err != nil {
		// A value written by an older, incompatible version: start over
		// rather than fail every message of this chat until the TTL runs out.
		return nil, nil //nolint:nilerr // an unreadable conversation is treated as none
	}
	return msgs, nil
}

// Save replaces the conversation and restarts its TTL. One SET rather than a
// list append: the agent hands over the already-trimmed conversation, and the
// chat's messages are handled one at a time, so there is no concurrent writer
// to merge with.
func (h *History) Save(ctx context.Context, chatID int64, msgs []llm.Message) error {
	raw, err := json.Marshal(msgs)
	if err != nil {
		return fmt.Errorf("redis: encode history: %w", err)
	}
	if err := h.rdb.Set(ctx, historyKey(chatID), raw, h.ttl).Err(); err != nil {
		return fmt.Errorf("redis: save history: %w", err)
	}
	return nil
}

// Clear forgets the conversation.
func (h *History) Clear(ctx context.Context, chatID int64) error {
	if err := h.rdb.Del(ctx, historyKey(chatID)).Err(); err != nil {
		return fmt.Errorf("redis: clear history: %w", err)
	}
	return nil
}

// RateLimiter caps how many messages a user may send to the model per window.
// Model calls cost money per token; without a cap one user -- or one script --
// could spend the month's budget in an afternoon. Buttons are not limited:
// they cost nothing.
type RateLimiter struct {
	rdb    *goredis.Client
	limit  int64
	window time.Duration
}

// NewRateLimiter allows limit messages per window per user.
func NewRateLimiter(rdb *goredis.Client, limit int, window time.Duration) *RateLimiter {
	return &RateLimiter{rdb: rdb, limit: int64(limit), window: window}
}

// fixedWindow counts a hit and starts the window on the first one. In Lua so
// that INCR and PEXPIRE are one atomic step: done as two commands, a crash in
// between would leave a counter with no expiry and lock the user out for good.
var fixedWindow = goredis.NewScript(`
local n = redis.call('INCR', KEYS[1])
if n == 1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
return {n, redis.call('PTTL', KEYS[1])}
`)

// Allow counts one message and reports whether it is within the limit and,
// when it is not, how long until the window resets.
func (l *RateLimiter) Allow(ctx context.Context, userID int64) (bool, time.Duration, error) {
	key := "agent:ratelimit:" + strconv.FormatInt(userID, 10)
	res, err := fixedWindow.Run(ctx, l.rdb, []string{key}, l.window.Milliseconds()).Int64Slice()
	if err != nil {
		return false, 0, fmt.Errorf("redis: rate limit: %w", err)
	}
	n, ttl := res[0], time.Duration(res[1])*time.Millisecond
	return n <= l.limit, ttl, nil
}
