package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadBotDefaults(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "123:abc")

	cfg, err := LoadBot()
	require.NoError(t, err)
	assert.Equal(t, defaultDSN, cfg.Postgres.DSN)
	assert.Equal(t, 256, cfg.MaxPending)
	assert.Equal(t, 20*time.Second, cfg.HandlerTimeout)
}

func TestLoadBotReportsEveryProblemAtOnce(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("BOT_MAX_PENDING", "lots")
	t.Setenv("LOG_LEVEL", "verbose")

	_, err := LoadBot()
	require.Error(t, err)
	assert.ErrorContains(t, err, "TELEGRAM_BOT_TOKEN")
	assert.ErrorContains(t, err, "BOT_MAX_PENDING")
	assert.ErrorContains(t, err, "LOG_LEVEL")
}
