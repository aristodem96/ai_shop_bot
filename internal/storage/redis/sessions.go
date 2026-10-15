package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// Sessions keeps short-lived per-chat state as JSON with a TTL, such as a
// checkout half filled in. A buyer who walks away mid-checkout comes back to
// a clean chat after the TTL instead of a stale question.
type Sessions struct {
	rdb    *goredis.Client
	prefix string
	ttl    time.Duration
}

// NewSessions returns a store whose keys start with prefix.
func NewSessions(rdb *goredis.Client, prefix string, ttl time.Duration) *Sessions {
	return &Sessions{rdb: rdb, prefix: prefix, ttl: ttl}
}

func (s *Sessions) key(id int64) string { return s.prefix + strconv.FormatInt(id, 10) }

// Load reads the state into v and reports whether there was any.
func (s *Sessions) Load(ctx context.Context, id int64, v any) (bool, error) {
	raw, err := s.rdb.Get(ctx, s.key(id)).Bytes()
	if errors.Is(err, goredis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("redis: load session: %w", err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return false, nil //nolint:nilerr // an unreadable session is treated as none
	}
	return true, nil
}

// Save stores v and restarts the TTL.
func (s *Sessions) Save(ctx context.Context, id int64, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("redis: encode session: %w", err)
	}
	if err := s.rdb.Set(ctx, s.key(id), raw, s.ttl).Err(); err != nil {
		return fmt.Errorf("redis: save session: %w", err)
	}
	return nil
}

// Delete forgets the state.
func (s *Sessions) Delete(ctx context.Context, id int64) error {
	if err := s.rdb.Del(ctx, s.key(id)).Err(); err != nil {
		return fmt.Errorf("redis: delete session: %w", err)
	}
	return nil
}
