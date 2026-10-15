// Package config loads the configuration from the environment.
//
// It is hand rolled rather than pulled from a library: the surface is small,
// every field gets a documented default, and validation errors are collected
// so a misconfigured deployment reports everything that is wrong at once
// instead of one variable per restart.
package config

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	// The runtime image is distroless and ships no time zone database, so
	// LoadLocation("Europe/Kyiv") would fail there -- and only there.
	_ "time/tzdata"
)

// defaultDSN points at the docker compose database. Its password is the
// public one from docker-compose.yml, not a secret.
const defaultDSN = "postgres://shop:shop@localhost:5433/shop?sslmode=disable" //nolint:gosec // local development default

// Postgres configures the connection pool.
type Postgres struct {
	DSN      string
	MaxConns int32
}

// Redis configures the store of conversations and rate limits.
type Redis struct {
	Addr     string
	Password string
	DB       int
}

// LLM selects and configures the model behind the consultant.
type LLM struct {
	// Provider is "claude", or "none" to run the bot on buttons alone.
	Provider string
	APIKey   string
	Model    string
	// Effort is the reasoning effort: low, medium or high.
	Effort string
}

// Agent bounds the consultant.
type Agent struct {
	MaxSteps   int
	Timeout    time.Duration
	MaxHistory int
	HistoryTTL time.Duration
	// RateLimit messages per RateWindow per user go to the model.
	RateLimit  int
	RateWindow time.Duration
}

// Log configures the structured logger.
type Log struct {
	Level  string
	Format string
}

// Bot is the configuration of the bot process.
type Bot struct {
	Postgres Postgres
	Redis    Redis
	LLM      LLM
	Agent    Agent
	Log      Log

	TelegramToken string
	// TelegramAPIURL is the Bot API endpoint. Telegram publishes the server
	// itself, so a deployment may run its own.
	TelegramAPIURL string

	// MaxPending bounds the updates accepted but not yet handled. When it is
	// reached the bot stops polling until handlers catch up, so a slow
	// dependency -- the LLM from stage 2 -- turns into backpressure on
	// Telegram's side instead of an unbounded queue in memory.
	MaxPending int
	// HandlerTimeout bounds one update. It is what stops one hung call from
	// blocking a chat forever, since a chat's updates run one at a time.
	HandlerTimeout time.Duration

	// CartReminderAfter is how long a cart sits untouched before the buyer is
	// reminded; zero turns reminders off. CartReminderEvery is how often
	// carts are checked.
	CartReminderAfter time.Duration
	CartReminderEvery time.Duration
	// Timezone decides the hours reminders may be sent in.
	Timezone string
}

// LoadBot reads the bot configuration.
func LoadBot() (*Bot, error) {
	var errs []error
	collect := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}

	cfg := &Bot{
		Postgres: loadPostgres(collect),
		Log: Log{
			Level:  strings.ToLower(envString("LOG_LEVEL", "info")),
			Format: strings.ToLower(envString("LOG_FORMAT", "text")),
		},
		TelegramToken:     envString("TELEGRAM_BOT_TOKEN", ""),
		TelegramAPIURL:    envString("TELEGRAM_API_URL", "https://api.telegram.org"),
		MaxPending:        envInt("BOT_MAX_PENDING", 256, collect),
		HandlerTimeout:    envDuration("BOT_HANDLER_TIMEOUT", 60*time.Second, collect),
		CartReminderAfter: envDuration("CART_REMINDER_AFTER", 24*time.Hour, collect),
		CartReminderEvery: envDuration("CART_REMINDER_EVERY", 10*time.Minute, collect),
		Timezone:          envString("BOT_TIMEZONE", "Europe/Kyiv"),
		Redis: Redis{
			Addr:     envString("REDIS_ADDR", "localhost:6380"),
			Password: envString("REDIS_PASSWORD", ""),
			DB:       envInt("REDIS_DB", 0, collect),
		},
		LLM: LLM{
			Provider: strings.ToLower(envString("LLM_PROVIDER", "claude")),
			APIKey:   envString("ANTHROPIC_API_KEY", ""),
			Model:    envString("LLM_MODEL", "claude-haiku-5-5"),
			Effort:   strings.ToLower(envString("LLM_EFFORT", "low")),
		},
		Agent: Agent{
			MaxSteps:   envInt("AGENT_MAX_STEPS", 6, collect),
			Timeout:    envDuration("AGENT_TIMEOUT", 45*time.Second, collect),
			MaxHistory: envInt("AGENT_HISTORY_MESSAGES", 30, collect),
			HistoryTTL: envDuration("AGENT_HISTORY_TTL", 24*time.Hour, collect),
			RateLimit:  envInt("AGENT_RATE_LIMIT", 30, collect),
			RateWindow: envDuration("AGENT_RATE_WINDOW", time.Hour, collect),
		},
	}

	require := func(ok bool, problem string) {
		if !ok {
			errs = append(errs, errors.New(problem))
		}
	}
	require(cfg.TelegramToken != "", "TELEGRAM_BOT_TOKEN must be set")
	require(cfg.TelegramAPIURL != "", "TELEGRAM_API_URL must be set")
	require(cfg.MaxPending > 0, "BOT_MAX_PENDING must be positive")
	require(cfg.HandlerTimeout >= time.Second, "BOT_HANDLER_TIMEOUT must be at least 1s")
	require(cfg.CartReminderAfter >= 0, "CART_REMINDER_AFTER must not be negative")
	require(cfg.CartReminderEvery >= time.Second, "CART_REMINDER_EVERY must be at least 1s")
	if _, err := time.LoadLocation(cfg.Timezone); err != nil {
		errs = append(errs, fmt.Errorf("BOT_TIMEZONE: %q is not a known time zone", cfg.Timezone))
	}

	switch cfg.LLM.Provider {
	case "none":
	case "claude":
		require(cfg.LLM.APIKey != "", "ANTHROPIC_API_KEY must be set when LLM_PROVIDER=claude (or set LLM_PROVIDER=none)")
		require(cfg.LLM.Model != "", "LLM_MODEL must be set")
		require(slices.Contains([]string{"low", "medium", "high"}, cfg.LLM.Effort), "LLM_EFFORT must be low, medium or high")
		require(cfg.Redis.Addr != "", "REDIS_ADDR must be set")
		require(cfg.Agent.MaxSteps > 0, "AGENT_MAX_STEPS must be positive")
		require(cfg.Agent.MaxHistory > 0, "AGENT_HISTORY_MESSAGES must be positive")
		require(cfg.Agent.HistoryTTL > 0, "AGENT_HISTORY_TTL must be positive")
		require(cfg.Agent.RateLimit > 0, "AGENT_RATE_LIMIT must be positive")
		require(cfg.Agent.RateWindow > 0, "AGENT_RATE_WINDOW must be positive")
		// The agent must give up before the update handler does, so the buyer
		// gets "try again" rather than silence.
		require(cfg.Agent.Timeout > 0 && cfg.Agent.Timeout < cfg.HandlerTimeout,
			"AGENT_TIMEOUT must be positive and shorter than BOT_HANDLER_TIMEOUT")
	default:
		errs = append(errs, fmt.Errorf("LLM_PROVIDER must be claude or none, got %q", cfg.LLM.Provider))
	}
	errs = append(errs, cfg.Postgres.validate(), cfg.Log.validate())

	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return cfg, nil
}

// LoadDatabase reads only the database settings. The seed command needs a
// connection and nothing else; making it demand a bot token would be a barrier
// with no purpose.
func LoadDatabase() (*Postgres, error) {
	var errs []error
	cfg := loadPostgres(func(err error) { errs = append(errs, err) })
	errs = append(errs, cfg.validate())
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func loadPostgres(collect func(error)) Postgres {
	return Postgres{
		DSN:      envString("POSTGRES_DSN", defaultDSN),
		MaxConns: envInt32("POSTGRES_MAX_CONNS", 10, collect),
	}
}

func (p Postgres) validate() error {
	var errs []error
	if p.DSN == "" {
		errs = append(errs, errors.New("POSTGRES_DSN must be set"))
	}
	if p.MaxConns <= 0 {
		errs = append(errs, errors.New("POSTGRES_MAX_CONNS must be positive"))
	}
	return errors.Join(errs...)
}

func (l Log) validate() error {
	var errs []error
	switch l.Format {
	case "json", "text":
	default:
		errs = append(errs, fmt.Errorf("LOG_FORMAT must be json or text, got %q", l.Format))
	}
	switch l.Level {
	case "debug", "info", "warn", "error":
	default:
		errs = append(errs, fmt.Errorf("LOG_LEVEL must be debug, info, warn or error, got %q", l.Level))
	}
	return errors.Join(errs...)
}

func envString(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envInt(key string, def int, collect func(error)) int {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		collect(fmt.Errorf("%s: %q is not an integer", key, v))
		return def
	}
	return n
}

// envInt32 parses at 32 bits so an out-of-range value is rejected here rather
// than silently wrapping on conversion.
func envInt32(key string, def int32, collect func(error)) int32 {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 32)
	if err != nil {
		collect(fmt.Errorf("%s: %q is not a 32-bit integer", key, v))
		return def
	}
	return int32(n)
}

func envDuration(key string, def time.Duration, collect func(error)) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		collect(fmt.Errorf("%s: %q is not a duration (try 5s, 1m)", key, v))
		return def
	}
	return d
}
