// Command bot runs the shop's Telegram bot.
//
// This is the composition root: the only place that knows every concrete type.
// Everything below it depends on interfaces, which is what lets the tests
// swap Postgres and Telegram for in-memory fakes.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aristodem96/ai_shop_bot/internal/agent"
	"github.com/aristodem96/ai_shop_bot/internal/bot"
	"github.com/aristodem96/ai_shop_bot/internal/config"
	"github.com/aristodem96/ai_shop_bot/internal/llm/claude"
	"github.com/aristodem96/ai_shop_bot/internal/observability"
	"github.com/aristodem96/ai_shop_bot/internal/service"
	"github.com/aristodem96/ai_shop_bot/internal/storage/postgres"
	redisstore "github.com/aristodem96/ai_shop_bot/internal/storage/redis"
	"github.com/aristodem96/ai_shop_bot/internal/telegram"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadBot()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}

	log, err := observability.NewLogger(cfg.Log.Level, cfg.Log.Format)
	if err != nil {
		return fmt.Errorf("build logger: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	startupCtx, cancelStartup := context.WithTimeout(ctx, 30*time.Second)
	defer cancelStartup()

	if err := postgres.Migrate(startupCtx, cfg.Postgres.DSN, log); err != nil {
		return err
	}
	pool, err := postgres.NewPool(startupCtx, cfg.Postgres.DSN, cfg.Postgres.MaxConns)
	if err != nil {
		return err
	}
	defer pool.Close()

	catalogRepo := postgres.NewCatalogRepository(pool)
	catalog := service.NewCatalogService(catalogRepo)
	cart := service.NewCartService(postgres.NewCartRepository(pool), catalogRepo)
	vehicleRepo := postgres.NewVehicleRepository(pool)
	garage := service.NewGarageService(vehicleRepo, vehicleRepo)

	tg := telegram.NewWithBaseURL(cfg.TelegramAPIURL, cfg.TelegramToken)

	// The menu is a convenience; a bot that cannot set it still works.
	if err := tg.SetCommands(startupCtx, bot.Commands); err != nil {
		log.Warn("could not register the command menu", slog.Any("error", err))
	}

	b := bot.New(tg, tg, catalog, cart, garage, bot.Config{
		MaxPending:     cfg.MaxPending,
		HandlerTimeout: cfg.HandlerTimeout,
	}, log)

	if cfg.LLM.Provider != "none" {
		rdb, err := redisstore.NewClient(startupCtx, cfg.Redis.Addr, cfg.Redis.Password, cfg.Redis.DB)
		if err != nil {
			return err
		}
		defer func() { _ = rdb.Close() }()

		consultant, err := agent.New(startupCtx,
			claude.New(claude.Config{APIKey: cfg.LLM.APIKey, Model: cfg.LLM.Model, Effort: cfg.LLM.Effort}),
			catalog, cart, garage,
			redisstore.NewHistory(rdb, cfg.Agent.HistoryTTL),
			agent.Config{MaxSteps: cfg.Agent.MaxSteps, MaxHistory: cfg.Agent.MaxHistory, Timeout: cfg.Agent.Timeout},
			log)
		if err != nil {
			return err
		}
		b.UseConsultant(consultant, redisstore.NewRateLimiter(rdb, cfg.Agent.RateLimit, cfg.Agent.RateWindow))
	}

	log.Info("bot started",
		slog.Int("max_pending", cfg.MaxPending),
		slog.String("llm_provider", cfg.LLM.Provider),
		slog.String("llm_model", cfg.LLM.Model))
	if err := b.Run(ctx); err != nil {
		return err
	}
	log.Info("bot stopped")
	return nil
}
