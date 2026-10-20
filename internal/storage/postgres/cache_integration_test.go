package postgres

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The catalogue's cache does not wait for the disk; nothing else changes.
func TestCacheWritesDoNotWaitForTheDisk(t *testing.T) {
	skipShort(t)
	ctx := context.Background()
	var inside, after string
	require.NoError(t, cacheTx(ctx, testPool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SHOW synchronous_commit`).Scan(&inside)
	}))
	require.NoError(t, testPool.QueryRow(ctx, `SHOW synchronous_commit`).Scan(&after))
	assert.Equal(t, "off", inside)
	assert.Equal(t, "on", after, "orders, payments and stock still wait for the disk")
}
