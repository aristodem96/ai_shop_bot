package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// cacheTx runs f in a transaction whose commit does not wait for the disk.
//
// For what the bot keeps as a cache of the parts catalogue only: a car's
// parts, the sections' paths, a crawl's progress. A car brings hundreds of
// sections, each its own commit, and on a server whose disk takes tens of
// milliseconds a write, waiting for each to reach the disk held a crawl to
// minutes. Not waiting risks the last fraction of a second of writes if the
// server loses power -- and those are re-read from the catalogue on the next
// crawl. Orders, payments, stock and carts never go through here.
func cacheTx(ctx context.Context, pool *pgxpool.Pool, f func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL synchronous_commit = off`); err != nil {
			return fmt.Errorf("postgres: cache write: %w", err)
		}
		return f(tx)
	})
}
