// Command bot runs the shop's Telegram bot, with the payment webhook, the
// payment poll and the cart reminder alongside it.
//
// This is the composition root: the only place that knows every concrete type.
// Everything below it depends on interfaces, which is what lets the tests
// swap Postgres, Redis, Telegram, Claude and Monobank for in-memory fakes.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/aristodem96/ai_shop_bot/internal/agent"
	"github.com/aristodem96/ai_shop_bot/internal/autodb"
	"github.com/aristodem96/ai_shop_bot/internal/bot"
	"github.com/aristodem96/ai_shop_bot/internal/config"
	"github.com/aristodem96/ai_shop_bot/internal/crawler"
	"github.com/aristodem96/ai_shop_bot/internal/directory"
	"github.com/aristodem96/ai_shop_bot/internal/importer"
	"github.com/aristodem96/ai_shop_bot/internal/llm"
	"github.com/aristodem96/ai_shop_bot/internal/llm/claude"
	"github.com/aristodem96/ai_shop_bot/internal/observability"
	"github.com/aristodem96/ai_shop_bot/internal/partnames"
	"github.com/aristodem96/ai_shop_bot/internal/payment/monobank"
	"github.com/aristodem96/ai_shop_bot/internal/service"
	"github.com/aristodem96/ai_shop_bot/internal/storage/postgres"
	redisstore "github.com/aristodem96/ai_shop_bot/internal/storage/redis"
	"github.com/aristodem96/ai_shop_bot/internal/telegram"
	"github.com/aristodem96/ai_shop_bot/internal/vehiclematch"
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
	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		return fmt.Errorf("load time zone %q: %w", cfg.Timezone, err)
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
	if err := postgres.SyncCategories(startupCtx, pool); err != nil {
		return err
	}

	rdb, err := redisstore.NewClient(startupCtx, cfg.Redis.Addr, cfg.Redis.Password, cfg.Redis.DB)
	if err != nil {
		return err
	}
	defer func() { _ = rdb.Close() }()

	// --- the shop ------------------------------------------------------------
	catalogRepo := postgres.NewCatalogRepository(pool)
	vehicleRepo := postgres.NewVehicleRepository(pool)
	cartRepo := postgres.NewCartRepository(pool)
	orderRepo := postgres.NewOrderRepository(pool)

	catalog := service.NewCatalogService(catalogRepo)
	cart := service.NewCartService(cartRepo, catalogRepo)
	// With an Auto-DB account, buyers' cars come from its catalogue of every
	// vehicle; without one, from the shop's own list.
	var (
		vehicles service.VehicleRepository = vehicleRepo
		dir      *directory.Directory
		crawl    *crawler.Crawler
	)
	if cfg.AutoDB.User != "" {
		api := autodb.New(autodb.Config{BaseURL: cfg.AutoDB.URL, User: cfg.AutoDB.User, Password: cfg.AutoDB.Password})
		dir = directory.New(api, postgres.NewDirectoryRepository(pool), log).WithFallback(vehicleRepo)
		vehicles = dir
		crawl = crawler.New(api, postgres.NewCrawlRepository(pool), crawler.Config{}, log)
	}
	garage := service.NewGarageService(vehicles, vehicleRepo)
	if crawl != nil {
		// A car chosen is a car whose parts are fetched now, in the
		// background, so they are there when the buyer asks for them.
		garage.OnChoose(func(ctx context.Context, vehicleID int64) {
			if err := crawl.Enqueue(ctx, vehicleID); err != nil {
				log.WarnContext(ctx, "could not queue a crawl", slog.Int64("vehicle_id", vehicleID), slog.Any("error", err))
			}
		})
	}
	kits := service.NewMaintenanceService(catalogRepo, vehicleRepo)

	tg := telegram.NewWithBaseURL(cfg.TelegramAPIURL, cfg.TelegramToken)
	// The menu is a convenience; a bot that cannot set it still works.
	if err := tg.SetCommands(startupCtx, bot.Commands); err != nil {
		log.Warn("could not register the command menu", slog.Any("error", err))
	}

	b := bot.New(tg, tg, catalog, cart, garage, kits, bot.Config{
		MaxPending:     cfg.MaxPending,
		HandlerTimeout: cfg.HandlerTimeout,
		PickupAddress:  cfg.PickupAddress,
	}, log)

	group, gctx := errgroup.WithContext(ctx)
	if dir != nil {
		// Makes and models in the cache before buyers ask; the API takes
		// seconds a call, and a cold lookup would keep a buyer waiting.
		group.Go(func() error {
			dir.Warm(gctx)
			return nil
		})
		group.Go(func() error { return crawl.Run(gctx) })
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	// --- payments --------------------------------------------------------------
	if cfg.Payments.MonobankToken != "" {
		me, err := tg.GetMe(startupCtx)
		if err != nil {
			return fmt.Errorf("telegram getMe: %w", err)
		}
		monoCfg := monobank.Config{
			BaseURL: cfg.Payments.MonobankAPIURL,
			Token:   cfg.Payments.MonobankToken,
			// After paying, the buyer lands back in the chat.
			RedirectURL: "https://t.me/" + me.Username,
		}
		if cfg.Payments.PublicURL != "" {
			monoCfg.WebhookURL = cfg.Payments.PublicURL + "/webhooks/monobank"
		}
		mono := monobank.New(monoCfg)

		orders := service.NewOrderService(orderRepo, mono, bot.NewNotifier(tg, cfg.Payments.AdminChatID, log), log)
		b.UseOrders(orders, redisstore.NewSessions(rdb, "checkout:", 30*time.Minute), cfg.Payments.AdminChatID)
		mux.Handle("/webhooks/monobank", mono.WebhookHandler(orders.HandlePayment, log))

		// The poll is what makes payments land without a public URL, and
		// what catches a webhook that never arrived.
		group.Go(func() error {
			t := time.NewTicker(cfg.Payments.ReconcileEvery)
			defer t.Stop()
			for {
				select {
				case <-gctx.Done():
					return nil
				case <-t.C:
					if err := orders.Reconcile(gctx); err != nil && gctx.Err() == nil {
						log.Warn("payment reconciliation failed", slog.Any("error", err))
					}
				}
			}
		})
		if cfg.Payments.AdminChatID == 0 {
			log.Warn("ADMIN_CHAT_ID is not set: paid orders reach no manager")
		}
	} else {
		log.Warn("MONOBANK_TOKEN is not set: checkout is off")
	}

	// --- the consultant ----------------------------------------------------
	// One model client serves the consultant and vehicle matching; nil runs
	// the bot without either.
	var model llm.Client
	if cfg.LLM.Provider != "none" {
		model = claude.New(claude.Config{APIKey: cfg.LLM.APIKey, Model: cfg.LLM.Model, Effort: cfg.LLM.Effort})
		consultant, err := agent.New(startupCtx, model,
			catalog, cart, garage, kits, orderRepo,
			redisstore.NewHistory(rdb, cfg.Agent.HistoryTTL),
			agent.Config{MaxSteps: cfg.Agent.MaxSteps, MaxHistory: cfg.Agent.MaxHistory, Timeout: cfg.Agent.Timeout},
			log)
		if err != nil {
			return err
		}
		b.UseConsultant(consultant, redisstore.NewRateLimiter(rdb, cfg.Agent.RateLimit, cfg.Agent.RateWindow), tg)

		if crawl != nil {
			// The consultant offers what the catalogue lists for the car
			// beyond the stock, under names translated as crawls bring them.
			catalogParts := postgres.NewCatalogPartRepository(pool)
			if cfg.Payments.MonobankToken != "" && cfg.Payments.AdminChatID != 0 {
				// A request goes to the managers and is paid through the
				// checkout, so it needs both.
				requests := postgres.NewPartRequestRepository(pool)
				consultant.UseOrderCatalog(catalogParts)
				b.UseRequests(requests, catalogParts)
				group.Go(func() error {
					t := time.NewTicker(10 * time.Minute)
					defer t.Stop()
					for {
						select {
						case <-gctx.Done():
							return nil
						case <-t.C:
							if _, err := requests.Expire(gctx); err != nil && gctx.Err() == nil {
								log.Warn("could not expire quotes", slog.Any("error", err))
							}
						}
					}
				})
			}
			cats, err := catalog.Categories(startupCtx)
			if err != nil {
				return err
			}
			names := partnames.New(model, catalogParts, cats, log)
			crawl.OnDone(func(int64) { names.Wake() })
			group.Go(func() error { return names.Run(gctx) })
		}
	}

	// --- catalogue imports from the managers' chat ----------------------------
	if cfg.Payments.AdminChatID != 0 {
		importRepo := postgres.NewImportRepository(pool)
		deps := importer.Deps{Prices: importRepo, Vehicles: importRepo, Keywords: importRepo, Aliases: importRepo}
		if model != nil {
			matcher := vehiclematch.New(model)
			deps.Matcher, deps.Proposer = matcher, matcher
		}
		imports := importer.NewService(deps, cfg.ImportProfilesDir)
		if err := b.UseImports(imports, tg, cfg.ImportDir, cfg.Payments.AdminChatID); err != nil {
			return err
		}
		b.UseWords(imports)
		b.UseVehicleMatching(imports)
		if err := tg.SetChatCommands(startupCtx, cfg.Payments.AdminChatID, bot.AdminMenu(cfg.Payments.AdminChatID)); err != nil {
			log.Warn("could not register the managers' command menu", slog.Any("error", err))
		}
	} else {
		log.Warn("ADMIN_CHAT_ID is not set: price lists cannot be uploaded in Telegram")
	}

	// --- run -----------------------------------------------------------------
	group.Go(func() error { return b.Run(gctx) })

	if cfg.CartReminderAfter > 0 {
		reminder := bot.NewReminder(cartRepo, cart, tg, bot.ReminderConfig{
			After: cfg.CartReminderAfter, Every: cfg.CartReminderEvery,
			Location: loc, FromHour: 9, ToHour: 21,
		}, log)
		group.Go(func() error { return reminder.Run(gctx) })
	}

	server := &http.Server{
		Addr:              cfg.Payments.HTTPAddr,
		Handler:           mux,
		ReadHeaderTimeout: 3 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
	}
	group.Go(func() error {
		var lc net.ListenConfig
		listener, err := lc.Listen(gctx, "tcp", server.Addr)
		if err != nil {
			return fmt.Errorf("http server: listen on %s: %w", server.Addr, err)
		}
		log.Info("http server listening", slog.String("addr", listener.Addr().String()))
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http server: %w", err)
		}
		return nil
	})
	group.Go(func() error {
		<-gctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	})

	log.Info("bot started",
		slog.String("llm_provider", cfg.LLM.Provider),
		slog.String("llm_model", cfg.LLM.Model),
		slog.Bool("payments", cfg.Payments.MonobankToken != ""),
		slog.Bool("payment_webhook", cfg.Payments.PublicURL != ""),
		slog.Duration("cart_reminder_after", cfg.CartReminderAfter))

	if err := group.Wait(); err != nil {
		return err
	}
	log.Info("bot stopped")
	return nil
}
