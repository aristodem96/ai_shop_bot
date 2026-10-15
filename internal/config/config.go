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
	"strconv"
	"strings"
	"time"
)

// defaultDSN points at the docker compose database. Its password is the
// public one from docker-compose.yml, not a secret.
const defaultDSN = "postgres://shop:shop@localhost:5433/shop?sslmode=disable" //nolint:gosec // local development default

// Postgres configures the connection pool.
type Postgres struct {
	DSN      string
	MaxConns int32
}

// Log configures the structured logger.
type Log struct {
	Level  string
	Format string
}

// Bot is the configuration of the bot process.
type Bot struct {
	Postgres Postgres
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
		TelegramToken:  envString("TELEGRAM_BOT_TOKEN", ""),
		TelegramAPIURL: envString("TELEGRAM_API_URL", "https://api.telegram.org"),
		MaxPending:     envInt("BOT_MAX_PENDING", 256, collect),
		HandlerTimeout: envDuration("BOT_HANDLER_TIMEOUT", 20*time.Second, collect),
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
