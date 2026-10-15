package redis

import (
	"context"
	"flag"
	"fmt"
	"os"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"github.com/aristodem96/ai_shop_bot/internal/llm"
)

// Against a real Redis: what is under test is the Lua script and the TTLs,
// which a fake would only imitate. `go test -short` skips these.

var rdb *goredis.Client

func TestMain(m *testing.M) {
	flag.Parse()
	if testing.Short() {
		os.Exit(m.Run())
	}
	os.Exit(runWithRedis(m))
}

func runWithRedis(m *testing.M) int {
	ctx := context.Background()
	container, err := tcredis.Run(ctx, "redis:7-alpine")
	if err != nil {
		fmt.Fprintf(os.Stderr, "could not start redis: %v\n", err)
		return 1
	}
	defer func() { _ = testcontainers.TerminateContainer(container) }()

	uri, err := container.ConnectionString(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "could not get redis address: %v\n", err)
		return 1
	}
	opts, err := goredis.ParseURL(uri)
	if err != nil {
		fmt.Fprintf(os.Stderr, "could not parse redis address: %v\n", err)
		return 1
	}
	rdb, err = NewClient(ctx, opts.Addr, "", 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "could not connect: %v\n", err)
		return 1
	}
	defer rdb.Close()
	return m.Run()
}

func skipShort(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test: needs Docker")
	}
}

func TestHistoryRoundTrip(t *testing.T) {
	skipShort(t)
	ctx := context.Background()
	h := NewHistory(rdb, time.Hour)

	msgs, err := h.Load(ctx, 1)
	require.NoError(t, err)
	assert.Empty(t, msgs, "no conversation reads as an empty one")

	in := []llm.Message{
		{Role: llm.RoleUser, Text: "куртка"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "c1", Name: "search_products", Input: []byte(`{"category":"jackets"}`)}},
			Native: "provider-specific, must not be stored"},
		{Role: llm.RoleUser, ToolResults: []llm.ToolResult{{CallID: "c1", Content: "[]"}}},
		{Role: llm.RoleAssistant, Text: "Нічого немає."},
	}
	require.NoError(t, h.Save(ctx, 1, in))

	out, err := h.Load(ctx, 1)
	require.NoError(t, err)
	require.Len(t, out, 4)
	assert.Nil(t, out[1].Native, "native provider turns are never persisted")
	in[1].Native = nil
	assert.Equal(t, in, out)

	ttl, err := rdb.TTL(ctx, historyKey(1)).Result()
	require.NoError(t, err)
	assert.Greater(t, ttl, 59*time.Minute)

	require.NoError(t, h.Clear(ctx, 1))
	out, err = h.Load(ctx, 1)
	require.NoError(t, err)
	assert.Empty(t, out)
}

func TestHistoryIgnoresUnreadableValue(t *testing.T) {
	skipShort(t)
	ctx := context.Background()
	require.NoError(t, rdb.Set(ctx, historyKey(2), "not json", time.Minute).Err())

	msgs, err := NewHistory(rdb, time.Hour).Load(ctx, 2)
	require.NoError(t, err)
	assert.Empty(t, msgs)
}

func TestRateLimiter(t *testing.T) {
	skipShort(t)
	ctx := context.Background()
	l := NewRateLimiter(rdb, 3, time.Minute)

	for i := range 3 {
		ok, _, err := l.Allow(ctx, 10)
		require.NoError(t, err)
		assert.True(t, ok, "message %d is within the limit", i+1)
	}
	ok, retry, err := l.Allow(ctx, 10)
	require.NoError(t, err)
	assert.False(t, ok, "the fourth is not")
	assert.Greater(t, retry, 50*time.Second)

	ok, _, err = l.Allow(ctx, 11)
	require.NoError(t, err)
	assert.True(t, ok, "users have separate budgets")
}

func TestRateLimiterWindowExpires(t *testing.T) {
	skipShort(t)
	ctx := context.Background()
	l := NewRateLimiter(rdb, 1, 200*time.Millisecond)

	ok, _, _ := l.Allow(ctx, 20)
	assert.True(t, ok)
	ok, _, _ = l.Allow(ctx, 20)
	assert.False(t, ok)

	require.Eventually(t, func() bool {
		ok, _, err := l.Allow(ctx, 20)
		return err == nil && ok
	}, 2*time.Second, 50*time.Millisecond, "a new window opens after the old one expires")
}
