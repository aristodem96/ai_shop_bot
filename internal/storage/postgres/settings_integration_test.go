package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSettings(t *testing.T) {
	skipShort(t)
	ctx := context.Background()
	repo := NewSettingsRepository(testPool)
	t.Cleanup(func() { _, _ = testPool.Exec(ctx, `DELETE FROM bot_settings`) })

	v, err := repo.Get(ctx, "admin_chat")
	require.NoError(t, err)
	assert.Empty(t, v, "never set")

	require.NoError(t, repo.Set(ctx, "admin_chat", "42"))
	require.NoError(t, repo.Set(ctx, "admin_chat", "-100500"), "set again, replaced")
	v, err = repo.Get(ctx, "admin_chat")
	require.NoError(t, err)
	assert.Equal(t, "-100500", v)
}
