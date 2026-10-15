// Command seed migrates the database and loads the demo catalogue. Running it
// again resets the catalogue, stock included, to its initial state.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aristodem96/ai_shop_bot/internal/config"
	"github.com/aristodem96/ai_shop_bot/internal/seed"
	"github.com/aristodem96/ai_shop_bot/internal/storage/postgres"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadDatabase()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()

	if err := postgres.Migrate(ctx, cfg.DSN, log); err != nil {
		return err
	}
	pool, err := postgres.NewPool(ctx, cfg.DSN, 2)
	if err != nil {
		return err
	}
	defer pool.Close()

	st, err := seed.Run(ctx, pool)
	if err != nil {
		return err
	}
	log.Info("catalogue seeded",
		slog.Int("categories", st.Categories),
		slog.Int("vehicles", st.Vehicles),
		slog.Int("products", st.Products),
		slog.Int("fitments", st.Fitments))
	return nil
}
