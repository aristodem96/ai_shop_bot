package postgres

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/seed"
)

// These tests run against a real PostgreSQL in Docker rather than a mock,
// because what they verify is the SQL itself: the lateral stock sums, the
// in-stock-first ordering, the style overlap ranking and the CTE upserts. None
// of that can fail in a fake.
//
// `go test -short` skips them, so the unit suite stays runnable without Docker.

var testPool *pgxpool.Pool

func TestMain(m *testing.M) {
	// testing.Short panics if the flags have not been parsed yet, and TestMain
	// runs before the framework does it.
	flag.Parse()
	if testing.Short() {
		os.Exit(m.Run())
	}
	os.Exit(runWithPostgres(m))
}

func runWithPostgres(m *testing.M) int {
	ctx := context.Background()

	container, err := tcpostgres.Run(ctx, "postgres:17-alpine",
		tcpostgres.WithDatabase("shop"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "could not start postgres: %v\n", err)
		return 1
	}
	defer func() {
		if err := testcontainers.TerminateContainer(container); err != nil {
			fmt.Fprintf(os.Stderr, "could not terminate postgres: %v\n", err)
		}
	}()

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Fprintf(os.Stderr, "could not build dsn: %v\n", err)
		return 1
	}

	// Running the real migrations and the real seed is part of the test: a
	// broken migration or seed should fail here rather than on a fresh clone.
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := Migrate(ctx, dsn, log); err != nil {
		fmt.Fprintf(os.Stderr, "could not migrate: %v\n", err)
		return 1
	}
	testPool, err = NewPool(ctx, dsn, 8)
	if err != nil {
		fmt.Fprintf(os.Stderr, "could not connect: %v\n", err)
		return 1
	}
	defer testPool.Close()

	if _, err := seed.Run(ctx, testPool); err != nil {
		fmt.Fprintf(os.Stderr, "could not seed: %v\n", err)
		return 1
	}
	return m.Run()
}

func skipShort(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test: needs Docker")
	}
}

// productBySKU looks a seeded product up by its stable key; ids are assigned
// by the database and differ between runs.
func productBySKU(t *testing.T, sku string) *domain.Product {
	t.Helper()
	var id int64
	require.NoError(t, testPool.QueryRow(context.Background(),
		`SELECT id FROM products WHERE sku = $1`, sku).Scan(&id))
	p, err := NewCatalogRepository(testPool).Product(context.Background(), id)
	require.NoError(t, err)
	return p
}

func TestSeedIsIdempotent(t *testing.T) {
	skipShort(t)
	ctx := context.Background()

	count := func() (products, variants int) {
		require.NoError(t, testPool.QueryRow(ctx,
			`SELECT (SELECT count(*) FROM products), (SELECT count(*) FROM product_variants)`,
		).Scan(&products, &variants))
		return products, variants
	}

	p1, v1 := count()
	st, err := seed.Run(ctx, testPool)
	require.NoError(t, err)
	p2, v2 := count()

	assert.Equal(t, p1, p2, "a second run must not duplicate products")
	assert.Equal(t, v1, v2, "nor sizes")
	assert.Equal(t, st.Products, p2)
	assert.GreaterOrEqual(t, p2, 50)
}

func TestCategories(t *testing.T) {
	skipShort(t)

	cats, err := NewCatalogRepository(testPool).Categories(context.Background())
	require.NoError(t, err)
	require.Len(t, cats, 6)
	assert.Equal(t, "jackets", cats[0].Slug, "menu order follows position")

	_, err = NewCatalogRepository(testPool).Category(context.Background(), -1)
	require.ErrorIs(t, err, domain.ErrNotFound)
}

func TestProductsByCategoryPutsInStockFirst(t *testing.T) {
	skipShort(t)
	repo := NewCatalogRepository(testPool)
	ctx := context.Background()

	var catID int64
	require.NoError(t, testPool.QueryRow(ctx, `SELECT id FROM categories WHERE slug = 'accessories'`).Scan(&catID))

	all, total, err := repo.ProductsByCategory(ctx, catID, 100, 0)
	require.NoError(t, err)
	require.Len(t, all, total)

	// Stock is generated, so the test checks the rule rather than which SKUs
	// happen to be sold out -- except the belt, which the seed pins at zero.
	soldOut := false
	for _, p := range all {
		if p.TotalStock == 0 {
			soldOut = true
			continue
		}
		assert.False(t, soldOut, "%s is in stock but sorts after a sold-out product", p.SKU)
	}
	assert.Zero(t, all[len(all)-1].TotalStock, "the sold-out belt is among the last")

	page, total2, err := repo.ProductsByCategory(ctx, catID, 2, 2)
	require.NoError(t, err)
	assert.Equal(t, total, total2)
	assert.Equal(t, all[2:4], page, "pages are slices of the same order")
}

func TestProductHasSizesInChartOrder(t *testing.T) {
	skipShort(t)

	p := productBySKU(t, "JKT-008") // the coat comes without XXL
	sizes := make([]string, 0, len(p.Variants))
	sum := 0
	for _, v := range p.Variants {
		sizes = append(sizes, v.Size)
		sum += v.Stock
	}
	assert.Equal(t, []string{"XS", "S", "M", "L", "XL"}, sizes)
	assert.Equal(t, sum, p.TotalStock)
	assert.Equal(t, domain.UAH(4899), p.Price)

	_, err := NewCatalogRepository(testPool).Product(context.Background(), -1)
	require.ErrorIs(t, err, domain.ErrNotFound)
}

func TestSimilar(t *testing.T) {
	skipShort(t)
	p := productBySKU(t, "HOD-001")

	similar, err := NewCatalogRepository(testPool).Similar(context.Background(), p, 5)
	require.NoError(t, err)
	require.NotEmpty(t, similar)
	for _, s := range similar {
		assert.NotEqual(t, p.ID, s.ID, "a product is not similar to itself")
		assert.Equal(t, p.CategoryID, s.CategoryID)
		assert.Positive(t, s.TotalStock)
	}
	// The same model in another colour shares every style tag and the price.
	assert.Contains(t, []string{"HOD-002", "HOD-003"}, similar[0].SKU)
}

func TestCartLifecycle(t *testing.T) {
	skipShort(t)
	ctx := context.Background()
	carts := NewCartRepository(testPool)
	const user = int64(1001)
	t.Cleanup(func() { _ = carts.Clear(ctx, user) })

	jacket := productBySKU(t, "JKT-002")
	tee := productBySKU(t, "TSH-001")
	vJacket, vTee := jacket.Variants[0], tee.Variants[0]

	cart, err := carts.Cart(ctx, user)
	require.NoError(t, err)
	assert.Empty(t, cart.Lines, "no cart reads as an empty one")

	require.NoError(t, carts.SetQty(ctx, user, vJacket.ID, 1))
	require.NoError(t, carts.SetQty(ctx, user, vTee.ID, 2))
	require.NoError(t, carts.SetQty(ctx, user, vJacket.ID, 3), "setting again updates the line")

	cart, err = carts.Cart(ctx, user)
	require.NoError(t, err)
	require.Len(t, cart.Lines, 2)
	assert.Equal(t, vJacket.ID, cart.Lines[0].VariantID, "lines keep the order they were added in")
	assert.Equal(t, 3, cart.Lines[0].Qty)
	assert.Equal(t, jacket.Price, cart.Lines[0].Price, "the price comes from the catalogue")
	assert.Equal(t, vJacket.Stock, cart.Lines[0].Stock)
	assert.Equal(t, jacket.Price*3+tee.Price*2, cart.Total())

	require.NoError(t, carts.SetQty(ctx, user, vJacket.ID, 0))
	cart, err = carts.Cart(ctx, user)
	require.NoError(t, err)
	require.Len(t, cart.Lines, 1)

	require.NoError(t, carts.Clear(ctx, user))
	cart, err = carts.Cart(ctx, user)
	require.NoError(t, err)
	assert.Empty(t, cart.Lines)
}

func TestCartUnknownVariant(t *testing.T) {
	skipShort(t)
	err := NewCartRepository(testPool).SetQty(context.Background(), 1002, -5, 1)
	require.ErrorIs(t, err, domain.ErrNotFound)
}

func TestCartShowsDeactivatedProductAsOutOfStock(t *testing.T) {
	skipShort(t)
	ctx := context.Background()
	carts := NewCartRepository(testPool)
	const user = int64(1003)

	p := productBySKU(t, "SHO-007")
	require.NoError(t, carts.SetQty(ctx, user, p.Variants[0].ID, 1))
	_, err := testPool.Exec(ctx, `UPDATE products SET is_active = false WHERE id = $1`, p.ID)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `UPDATE products SET is_active = true WHERE id = $1`, p.ID)
		_ = carts.Clear(ctx, user)
	})

	cart, err := carts.Cart(ctx, user)
	require.NoError(t, err)
	require.Len(t, cart.Lines, 1)
	assert.Zero(t, cart.Lines[0].Stock)
	assert.True(t, cart.Lines[0].Short())
}
