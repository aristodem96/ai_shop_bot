// Command bot runs the shop's Telegram bot, with the payment webhook, the
// payment poll, the cart reminder and the catalogue's background work
// alongside it.
//
// This is the composition root: the only place that knows every concrete type.
// Everything below it depends on interfaces, which is what lets the tests
// swap Postgres, Redis, Telegram, Claude, Monobank and Auto-DB for fakes.
//
// Each part the configuration may leave out -- Auto-DB, payments, the
// consultant, the managers' chat -- is wired by a function of its own, and
// every background job is started the same way, so what runs is read off
// run() at a glance.
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

	"github.com/jackc/pgx/v5/pgxpool"
	goredis "github.com/redis/go-redis/v9"
	"golang.org/x/sync/errgroup"

	"github.com/aristodem96/ai_shop_bot/internal/agent"
	"github.com/aristodem96/ai_shop_bot/internal/autodb"
	"github.com/aristodem96/ai_shop_bot/internal/bot"
	"github.com/aristodem96/ai_shop_bot/internal/categories"
	"github.com/aristodem96/ai_shop_bot/internal/catmatch"
	"github.com/aristodem96/ai_shop_bot/internal/config"
	"github.com/aristodem96/ai_shop_bot/internal/crawler"
	"github.com/aristodem96/ai_shop_bot/internal/directory"
	"github.com/aristodem96/ai_shop_bot/internal/enrich"
	"github.com/aristodem96/ai_shop_bot/internal/grouping"
	"github.com/aristodem96/ai_shop_bot/internal/importer"
	"github.com/aristodem96/ai_shop_bot/internal/llm"
	"github.com/aristodem96/ai_shop_bot/internal/llm/claude"
	"github.com/aristodem96/ai_shop_bot/internal/observability"
	"github.com/aristodem96/ai_shop_bot/internal/partnames"
	"github.com/aristodem96/ai_shop_bot/internal/payment/monobank"
	"github.com/aristodem96/ai_shop_bot/internal/photos"
	"github.com/aristodem96/ai_shop_bot/internal/reports"
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

// app is what the wiring functions share.
type app struct {
	cfg  *config.Bot
	log  *slog.Logger
	loc  *time.Location // the shop's days: reminders' hours, reports' periods
	pool *pgxpool.Pool
	rdb  *goredis.Client
	tg   *telegram.Client
	bot  *bot.Bot
	mux  *http.ServeMux

	// startup bounds the calls made while wiring; group and ctx run the
	// background jobs until shutdown.
	startup context.Context
	group   *errgroup.Group
	ctx     context.Context

	catalogRepo *postgres.CatalogRepository
	vehicleRepo *postgres.VehicleRepository
	cartRepo    *postgres.CartRepository
	orderRepo   *postgres.OrderRepository
	catalog     *service.CatalogService
	cart        *service.CartService
	garage      *service.GarageService
	kits        *service.MaintenanceService

	// Set by the optional parts; nil when left out.
	catalogue *catalogue
	model     llm.Client
	payments  bool
}

// catalogue is the Auto-DB side of the shop.
type catalogue struct {
	dir      *directory.Directory
	crawl    *crawler.Crawler
	enricher *enrich.Enricher
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
	startup, cancelStartup := context.WithTimeout(ctx, 30*time.Second)
	defer cancelStartup()

	if err := postgres.Migrate(startup, cfg.Postgres.DSN, log); err != nil {
		return err
	}
	pool, err := postgres.NewPool(startup, cfg.Postgres.DSN, cfg.Postgres.MaxConns)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := postgres.SeedCategories(startup, pool); err != nil {
		return err
	}
	rdb, err := redisstore.NewClient(startup, cfg.Redis.Addr, cfg.Redis.Password, cfg.Redis.DB)
	if err != nil {
		return err
	}
	defer func() { _ = rdb.Close() }()

	group, gctx := errgroup.WithContext(ctx)
	a := &app{cfg: cfg, log: log, loc: loc, pool: pool, rdb: rdb, startup: startup, group: group, ctx: gctx,
		tg: telegram.NewWithBaseURL(cfg.TelegramAPIURL, cfg.TelegramToken), mux: http.NewServeMux()}

	a.wireShop()
	if err := a.wirePayments(); err != nil {
		return err
	}
	if err := a.wireConsultant(); err != nil {
		return err
	}
	if err := a.wireManagers(); err != nil {
		return err
	}

	group.Go(func() error { return a.bot.Run(gctx) })
	if cfg.CartReminderAfter > 0 {
		reminder := bot.NewReminder(a.cartRepo, a.cart, a.tg, bot.ReminderConfig{
			After: cfg.CartReminderAfter, Every: cfg.CartReminderEvery,
			Location: loc, FromHour: 9, ToHour: 21,
		}, log)
		a.background(reminder.Run)
	}
	a.serveHTTP(ctx)

	log.Info("bot started",
		slog.String("llm_provider", cfg.LLM.Provider),
		slog.String("llm_model", cfg.LLM.Model),
		slog.Bool("payments", a.payments),
		slog.Bool("payment_webhook", cfg.Payments.PublicURL != ""),
		slog.Bool("autodb", a.catalogue != nil),
		slog.Duration("cart_reminder_after", cfg.CartReminderAfter))

	if err := group.Wait(); err != nil {
		return err
	}
	log.Info("bot stopped")
	return nil
}

// background runs a job until shutdown.
func (a *app) background(job func(context.Context) error) {
	a.group.Go(func() error { return job(a.ctx) })
}

// every runs a job on an interval until shutdown. A failed run is logged
// and the next one tried: none of these jobs is worth stopping the bot.
func (a *app) every(interval time.Duration, what string, job func(context.Context) error) {
	a.background(func(ctx context.Context) error {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-t.C:
				if err := job(ctx); err != nil && ctx.Err() == nil {
					a.log.Warn(what+" failed", slog.Any("error", err))
				}
			}
		}
	})
}

// wireShop builds the catalogue, cart and garage, and the bot that serves
// them: what works with no optional part at all.
func (a *app) wireShop() {
	a.catalogRepo = postgres.NewCatalogRepository(a.pool)
	a.vehicleRepo = postgres.NewVehicleRepository(a.pool)
	a.cartRepo = postgres.NewCartRepository(a.pool)
	a.orderRepo = postgres.NewOrderRepository(a.pool)
	a.catalog = service.NewCatalogService(a.catalogRepo)
	a.cart = service.NewCartService(a.cartRepo, a.catalogRepo)
	a.kits = service.NewMaintenanceService(a.catalogRepo, a.vehicleRepo)

	// With an Auto-DB account, buyers' cars come from its catalogue of every
	// vehicle, and the shop's own list covers what it lacks; without one,
	// from the shop's own list.
	var vehicles service.VehicleRepository = a.vehicleRepo
	if a.cfg.AutoDB.User != "" {
		a.catalogue = a.newCatalogue()
		vehicles = a.catalogue.dir
	}
	a.garage = service.NewGarageService(vehicles, a.vehicleRepo)

	// The menu is a convenience; a bot that cannot set it still works.
	if err := a.tg.SetCommands(a.startup, bot.Commands); err != nil {
		a.log.Warn("could not register the command menu", slog.Any("error", err))
	}
	a.bot = bot.New(a.tg, a.tg, a.catalog, a.cart, a.garage, a.kits, bot.Config{
		MaxPending:     a.cfg.MaxPending,
		HandlerTimeout: a.cfg.HandlerTimeout,
		PickupAddress:  a.cfg.PickupAddress,
	}, a.log)
	// Photos go to Telegram converted and are sent again by Telegram's id.
	a.bot.UsePhotos(photos.New(a.tg, postgres.NewPhotoRepository(a.pool), a.log))
	// Managers' dialogs -- an upload waiting for Apply, a matching session --
	// are kept in Redis, so a restart halfway through loses nothing.
	a.bot.KeepDialogs(func(prefix string, ttl time.Duration) bot.Sessions {
		return redisstore.NewSessions(a.rdb, prefix, ttl)
	})

	if c := a.catalogue; c != nil {
		// A car chosen is a car whose parts are fetched now, in the
		// background, so they are there when the buyer asks for them.
		a.garage.OnChoose(func(ctx context.Context, vehicleID int64) {
			if err := c.crawl.Enqueue(ctx, vehicleID); err != nil {
				a.log.WarnContext(ctx, "could not queue a crawl", slog.Int64("vehicle_id", vehicleID), slog.Any("error", err))
			}
		})
		// Photos, carmakers' numbers and analogues: for a card as it is
		// opened, and for the whole stock in the background.
		a.bot.UseEnrichment(c.enricher)
		// Makes and models in the cache before buyers ask; the API takes
		// seconds a call, and a cold lookup would keep a buyer waiting.
		a.background(func(ctx context.Context) error {
			c.dir.Warm(ctx)
			return nil
		})
		a.background(c.crawl.Run)
		a.background(c.enricher.Run)
	}
}

// newCatalogue builds the Auto-DB side: one client, paced for all of it.
func (a *app) newCatalogue() *catalogue {
	api := autodb.New(autodb.Config{BaseURL: a.cfg.AutoDB.URL, User: a.cfg.AutoDB.User, Password: a.cfg.AutoDB.Password,
		RPS: a.cfg.AutoDB.RPS, BackgroundRPS: a.cfg.AutoDB.BackgroundRPS})
	return &catalogue{
		dir:      directory.New(api, postgres.NewDirectoryRepository(a.pool), a.log).WithFallback(a.vehicleRepo),
		crawl:    crawler.New(api, postgres.NewCrawlRepository(a.pool), crawler.Config{}, a.log),
		enricher: enrich.New(api, postgres.NewEnrichRepository(a.pool), enrich.Config{}, a.log),
	}
}

// wirePayments turns on checkout with Monobank.
func (a *app) wirePayments() error {
	cfg := a.cfg.Payments
	if cfg.MonobankToken == "" {
		a.log.Warn("MONOBANK_TOKEN is not set: checkout is off")
		return nil
	}
	me, err := a.tg.GetMe(a.startup)
	if err != nil {
		return fmt.Errorf("telegram getMe: %w", err)
	}
	monoCfg := monobank.Config{
		BaseURL: cfg.MonobankAPIURL,
		Token:   cfg.MonobankToken,
		// After paying, the buyer lands back in the chat.
		RedirectURL: "https://t.me/" + me.Username,
	}
	if cfg.PublicURL != "" {
		monoCfg.WebhookURL = cfg.PublicURL + "/webhooks/monobank"
	}
	mono := monobank.New(monoCfg)

	orders := service.NewOrderService(a.orderRepo, mono, bot.NewNotifier(a.tg, cfg.AdminChatID, a.log), a.log)
	a.bot.UseOrders(orders, redisstore.NewSessions(a.rdb, "checkout:", 30*time.Minute), cfg.AdminChatID)
	a.mux.Handle("/webhooks/monobank", mono.WebhookHandler(orders.HandlePayment, a.log))
	// The poll is what makes payments land without a public URL, and what
	// catches a webhook that never arrived.
	a.every(cfg.ReconcileEvery, "payment reconciliation", orders.Reconcile)
	if cfg.AdminChatID == 0 {
		a.log.Warn("ADMIN_CHAT_ID is not set: paid orders reach no manager")
	}
	a.payments = true
	return nil
}

// wireConsultant turns on the AI consultant and, with Auto-DB, parts on
// order: requested by buyers, priced by managers, paid at checkout.
func (a *app) wireConsultant() error {
	if a.cfg.LLM.Provider == "none" {
		return nil
	}
	// One model client serves the consultant, vehicle matching and part
	// names.
	a.model = claude.New(claude.Config{APIKey: a.cfg.LLM.APIKey, Model: a.cfg.LLM.Model, Effort: a.cfg.LLM.Effort})
	consultant, err := agent.New(a.startup, a.model,
		a.catalog, a.cart, a.garage, a.kits, a.orderRepo,
		redisstore.NewHistory(a.rdb, a.cfg.Agent.HistoryTTL),
		agent.Config{MaxSteps: a.cfg.Agent.MaxSteps, MaxHistory: a.cfg.Agent.MaxHistory, Timeout: a.cfg.Agent.Timeout},
		a.log)
	if err != nil {
		return err
	}
	a.bot.UseConsultant(consultant, redisstore.NewRateLimiter(a.rdb, a.cfg.Agent.RateLimit, a.cfg.Agent.RateWindow), a.tg)

	c := a.catalogue
	if c == nil {
		return nil
	}
	// get_part tells the model which cars the catalogue lists a part for.
	consultant.UseCatalogFits(c.enricher)
	// Catalogue parts carry Russian names; each is translated once, as
	// crawls bring them.
	catalogParts := postgres.NewCatalogPartRepository(a.pool)
	cats, err := a.catalog.Categories(a.startup)
	if err != nil {
		return err
	}
	names := partnames.New(a.model, catalogParts, cats, a.log)
	c.crawl.OnDone(func(int64) { names.Wake() })
	a.background(names.Run)

	// A part request goes to the managers and is paid through the checkout,
	// so offering parts on order needs both.
	if !a.payments || a.cfg.Payments.AdminChatID == 0 {
		return nil
	}
	requests := postgres.NewPartRequestRepository(a.pool)
	consultant.UseOrderCatalog(catalogParts)
	a.bot.UseRequests(requests, catalogParts)
	a.every(10*time.Minute, "expiring quotes", func(ctx context.Context) error {
		_, err := requests.Expire(ctx)
		return err
	})
	return nil
}

// wireManagers turns on the managers' chat tools: price lists, the vehicle
// directory, category words and vehicle names.
func (a *app) wireManagers() error {
	chat := a.cfg.Payments.AdminChatID
	if chat == 0 {
		a.log.Warn("ADMIN_CHAT_ID is not set: price lists cannot be uploaded in Telegram")
		return nil
	}
	importRepo := postgres.NewImportRepository(a.pool)
	deps := importer.Deps{Prices: importRepo, Vehicles: importRepo, Aliases: importRepo, Suppliers: importRepo}
	if a.model != nil {
		matcher := vehiclematch.New(a.model)
		deps.Matcher, deps.Proposer = matcher, matcher
	}
	imports := importer.NewService(deps)
	if err := a.bot.UseImports(imports, a.tg, a.cfg.ImportDir, chat); err != nil {
		return err
	}
	cats := categories.New(postgres.NewCategoryRepository(a.pool))
	a.bot.UseCategories(cats)
	a.bot.UseReports(reports.New(postgres.NewReportRepository(a.pool), a.loc), a.tg)
	// Suppliers' groups are filed by the model where it is sure, by the
	// owner where it is not; without a model, by the owner alone.
	var matcher grouping.Matcher
	if a.model != nil {
		matcher = catmatch.New(a.model)
	}
	a.bot.UseGrouping(grouping.New(postgres.NewGroupRepository(a.pool), cats, matcher))
	a.bot.UseVehicleMatching(imports)
	if err := a.tg.SetChatCommands(a.startup, chat, bot.AdminMenu(chat)); err != nil {
		a.log.Warn("could not register the managers' command menu", slog.Any("error", err))
	}
	return nil
}

// serveHTTP serves /healthz and the payment webhook until shutdown.
func (a *app) serveHTTP(ctx context.Context) {
	a.mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	server := &http.Server{
		Addr:              a.cfg.Payments.HTTPAddr,
		Handler:           a.mux,
		ReadHeaderTimeout: 3 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
	}
	a.background(func(ctx context.Context) error {
		var lc net.ListenConfig
		listener, err := lc.Listen(ctx, "tcp", server.Addr)
		if err != nil {
			return fmt.Errorf("http server: listen on %s: %w", server.Addr, err)
		}
		a.log.Info("http server listening", slog.String("addr", listener.Addr().String()))
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http server: %w", err)
		}
		return nil
	})
	a.background(func(gctx context.Context) error {
		<-gctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	})
}
