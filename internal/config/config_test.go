package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadBotDefaults(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "123:abc")
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	cfg, err := LoadBot()
	require.NoError(t, err)
	assert.Equal(t, defaultDSN, cfg.Postgres.DSN)
	assert.Equal(t, 256, cfg.MaxPending)
	assert.Equal(t, 60*time.Second, cfg.HandlerTimeout)
	assert.Equal(t, "claude", cfg.LLM.Provider)
	assert.Equal(t, "claude-haiku-5-5", cfg.LLM.Model)
	assert.Equal(t, "low", cfg.LLM.Effort)
}

func TestLoadBotWithoutModel(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "123:abc")
	t.Setenv("LLM_PROVIDER", "none")
	t.Setenv("ANTHROPIC_API_KEY", "")

	_, err := LoadBot()
	require.NoError(t, err, "buttons alone need no API key")
}

func TestLoadBotAgentMustTimeOutFirst(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "123:abc")
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	t.Setenv("AGENT_TIMEOUT", "90s")

	_, err := LoadBot()
	assert.ErrorContains(t, err, "AGENT_TIMEOUT")
}

func TestLoadBotReportsEveryProblemAtOnce(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("BOT_MAX_PENDING", "lots")
	t.Setenv("LOG_LEVEL", "verbose")
	t.Setenv("ANTHROPIC_API_KEY", "")

	_, err := LoadBot()
	require.Error(t, err)
	assert.ErrorContains(t, err, "TELEGRAM_BOT_TOKEN")
	assert.ErrorContains(t, err, "ANTHROPIC_API_KEY")
	assert.ErrorContains(t, err, "BOT_MAX_PENDING")
	assert.ErrorContains(t, err, "LOG_LEVEL")
}
