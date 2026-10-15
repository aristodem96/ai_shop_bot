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
	"github.com/aristodem96/ai_shop_bot/internal/service"
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

// productBySKU and vehicleByCode look seeded rows up by their stable keys; ids
// are assigned by the database and differ between runs.
func productBySKU(t *testing.T, sku string) *domain.Product {
	t.Helper()
	var id int64
	require.NoError(t, testPool.QueryRow(context.Background(),
		`SELECT id FROM products WHERE sku = $1`, sku).Scan(&id))
	p, err := NewCatalogRepository(testPool).Product(context.Background(), id)
	require.NoError(t, err)
	return p
}

func vehicleByCode(t *testing.T, code string) int64 {
	t.Helper()
	var id int64
	require.NoError(t, testPool.QueryRow(context.Background(),
		`SELECT id FROM vehicles WHERE code = $1`, code).Scan(&id))
	return id
}

func categoryBySlug(t *testing.T, slug string) int64 {
	t.Helper()
	var id int64
	require.NoError(t, testPool.QueryRow(context.Background(),
		`SELECT id FROM categories WHERE slug = $1`, slug).Scan(&id))
	return id
}

func TestSeedIsIdempotent(t *testing.T) {
	skipShort(t)
	ctx := context.Background()

	count := func() (products, fitments int) {
		require.NoError(t, testPool.QueryRow(ctx,
			`SELECT (SELECT count(*) FROM products), (SELECT count(*) FROM product_fitments)`,
		).Scan(&products, &fitments))
		return products, fitments
	}

	p1, f1 := count()
	st, err := seed.Run(ctx, testPool)
	require.NoError(t, err)
	p2, f2 := count()

	assert.Equal(t, p1, p2, "a second run must not duplicate products")
	assert.Equal(t, f1, f2, "nor fitments")
	assert.Equal(t, st.Products, p2)
	assert.Equal(t, 12, st.Vehicles)
	assert.GreaterOrEqual(t, p2, 50)
}

func TestProductCard(t *testing.T) {
	skipShort(t)

	p := productBySKU(t, "FLT-003") // a diesel VAG oil filter
	assert.Equal(t, []string{"03N115562"}, p.OEMNumbers, "OEM numbers are stored normalised")
	assert.Equal(t, []domain.Spec{{Name: "Тип", Value: "картридж"}}, p.Specs)
	assert.Equal(t, domain.UAH(340), p.Price)
	require.Len(t, p.Fits, 3)
	assert.True(t, p.FitsVehicle(vehicleByCode(t, "golf-7-16tdi")))
	assert.False(t, p.FitsVehicle(vehicleByCode(t, "octavia-a7-14tsi")), "a petrol engine takes another filter")

	oil := productBySKU(t, "OIL-001")
	assert.True(t, oil.Universal)
	assert.Empty(t, oil.Fits)

	_, err := NewCatalogRepository(testPool).Product(context.Background(), -1)
	require.ErrorIs(t, err, domain.ErrNotFound)
}

func TestCategoryForMyCar(t *testing.T) {
	skipShort(t)
	repo := NewCatalogRepository(testPool)
	ctx := context.Background()
	brakes := categoryBySlug(t, "brakes")
	camry := vehicleByCode(t, "camry-xv70-25")

	all, total, err := repo.ProductsByCategory(ctx, brakes, 0, 100, 0)
	require.NoError(t, err)
	assert.Len(t, all, total)
	assert.Equal(t, "BRK-009", all[len(all)-1].SKU, "the sold-out Lanos pads sort last")

	mine, _, err := repo.ProductsByCategory(ctx, brakes, camry, 100, 0)
	require.NoError(t, err)
	require.NotEmpty(t, mine)
	assert.Less(t, len(mine), len(all))
	for _, p := range mine {
		full := productBySKU(t, p.SKU)
		assert.True(t, full.FitsVehicle(camry), "%s must fit the Camry", p.SKU)
	}

	oils, _, err := repo.ProductsByCategory(ctx, categoryBySlug(t, "oils"), camry, 100, 0)
	require.NoError(t, err)
	assert.Len(t, oils, 8, "universal products fit every car")
}

func TestAnalogues(t *testing.T) {
	skipShort(t)
	repo := NewCatalogRepository(testPool)
	ctx := context.Background()

	pads := productBySKU(t, "BRK-001")
	got, err := repo.Analogues(ctx, pads, 0, 5)
	require.NoError(t, err)
	require.NotEmpty(t, got)
	assert.Equal(t, "BRK-002", got[0].SKU, "the same OEM number from another brand comes first")
	for _, p := range got {
		assert.NotEqual(t, pads.ID, p.ID, "a part is not its own analogue")
		assert.Positive(t, p.Stock)
	}

	filter := productBySKU(t, "FLT-001")
	got, err = repo.Analogues(ctx, filter, vehicleByCode(t, "octavia-a7-14tsi"), 5)
	require.NoError(t, err)
	require.NotEmpty(t, got)
	assert.Equal(t, "FLT-002", got[0].SKU)

	got, err = repo.Analogues(ctx, filter, vehicleByCode(t, "lanos-15"), 5)
	require.NoError(t, err)
	assert.Empty(t, got, "nothing interchangeable fits a Lanos")
}

func TestSearch(t *testing.T) {
	skipShort(t)
	repo := NewCatalogRepository(testPool)
	ctx := context.Background()
	search := func(f service.SearchFilter) []domain.Product {
		t.Helper()
		if f.Limit == 0 {
			f.Limit = 50
		}
		got, err := repo.Search(ctx, f)
		require.NoError(t, err)
		return got
	}

	t.Run("by part number in any spelling", func(t *testing.T) {
		got := search(service.SearchFilter{PartNumber: domain.NormalizeOEM("04e-115 561h")})
		require.Len(t, got, 2)
		assert.Equal(t, "FLT-002", got[0].SKU, "cheapest first")
		assert.Equal(t, "FLT-001", got[1].SKU)
	})

	t.Run("by shop SKU", func(t *testing.T) {
		got := search(service.SearchFilter{PartNumber: domain.NormalizeOEM("eng-001")})
		require.Len(t, got, 1)
		assert.Equal(t, "ENG-001", got[0].SKU)
	})

	t.Run("for a car within a budget", func(t *testing.T) {
		logan := vehicleByCode(t, "logan-2-16")
		got := search(service.SearchFilter{Category: "brakes", VehicleID: logan, MaxPrice: domain.UAH(1000)})
		require.Len(t, got, 1)
		assert.Equal(t, "BRK-004", got[0].SKU)
	})

	t.Run("by keyword stem", func(t *testing.T) {
		got := search(service.SearchFilter{Query: "аморт"})
		require.NotEmpty(t, got)
		for _, p := range got {
			assert.Contains(t, p.Name, "Амортизатор")
		}
	})

	t.Run("never sold out", func(t *testing.T) {
		got := search(service.SearchFilter{VehicleID: vehicleByCode(t, "lanos-15"), Category: "brakes"})
		assert.Empty(t, got, "the only Lanos pads are sold out")
	})

	t.Run("hostile input is data", func(t *testing.T) {
		assert.Empty(t, search(service.SearchFilter{Category: "x' OR '1'='1"}))
		assert.Empty(t, search(service.SearchFilter{Brand: "a'); DROP TABLE products; --"}))
		_ = search(service.SearchFilter{Query: "&|!():*'\\"})
		var n int
		require.NoError(t, testPool.QueryRow(ctx, `SELECT count(*) FROM products`).Scan(&n))
		assert.Positive(t, n)
	})
}

func TestGarage(t *testing.T) {
	skipShort(t)
	ctx := context.Background()
	repo := NewVehicleRepository(testPool)
	const user = int64(2001)

	v, err := repo.Garage(ctx, user)
	require.NoError(t, err)
	assert.Nil(t, v)

	require.NoError(t, repo.SetGarage(ctx, user, vehicleByCode(t, "logan-2-16")))
	require.NoError(t, repo.SetGarage(ctx, user, vehicleByCode(t, "focus-3-16")), "choosing again replaces")
	v, err = repo.Garage(ctx, user)
	require.NoError(t, err)
	assert.Equal(t, "Focus III", v.Model)

	require.ErrorIs(t, repo.SetGarage(ctx, user, -1), domain.ErrNotFound)

	require.NoError(t, repo.ClearGarage(ctx, user))
	v, err = repo.Garage(ctx, user)
	require.NoError(t, err)
	assert.Nil(t, v)

	all, err := repo.Vehicles(ctx)
	require.NoError(t, err)
	assert.Len(t, all, 12)
	assert.Equal(t, "Daewoo", all[0].Make, "ordered by make")
	corolla, err := repo.Vehicle(ctx, vehicleByCode(t, "corolla-e210-16"))
	require.NoError(t, err)
	assert.Zero(t, corolla.YearTo, "still produced")
}

func TestCartLifecycle(t *testing.T) {
	skipShort(t)
	ctx := context.Background()
	carts := NewCartRepository(testPool)
	const user = int64(1001)
	t.Cleanup(func() { _ = carts.Clear(ctx, user) })

	pads := productBySKU(t, "BRK-002")
	oil := productBySKU(t, "OIL-001")

	cart, err := carts.Cart(ctx, user)
	require.NoError(t, err)
	assert.Empty(t, cart.Lines, "no cart reads as an empty one")

	require.NoError(t, carts.SetQty(ctx, user, pads.ID, 1))
	require.NoError(t, carts.SetQty(ctx, user, oil.ID, 2))
	require.NoError(t, carts.SetQty(ctx, user, pads.ID, 3), "setting again updates the line")

	cart, err = carts.Cart(ctx, user)
	require.NoError(t, err)
	require.Len(t, cart.Lines, 2)
	assert.Equal(t, pads.ID, cart.Lines[0].ProductID, "lines keep the order they were added in")
	assert.Equal(t, 3, cart.Lines[0].Qty)
	assert.Equal(t, "Stal Pro", cart.Lines[0].Brand)
	assert.Equal(t, pads.Price, cart.Lines[0].Price, "the price comes from the catalogue")
	assert.Equal(t, pads.Price*3+oil.Price*2, cart.Total())

	require.NoError(t, carts.SetQty(ctx, user, pads.ID, 0))
	cart, err = carts.Cart(ctx, user)
	require.NoError(t, err)
	require.Len(t, cart.Lines, 1)

	require.NoError(t, carts.Clear(ctx, user))
	cart, err = carts.Cart(ctx, user)
	require.NoError(t, err)
	assert.Empty(t, cart.Lines)
}

func TestCartUnknownProduct(t *testing.T) {
	skipShort(t)
	err := NewCartRepository(testPool).SetQty(context.Background(), 1002, -5, 1)
	require.ErrorIs(t, err, domain.ErrNotFound)
}

func TestCartShowsDeactivatedProductAsOutOfStock(t *testing.T) {
	skipShort(t)
	ctx := context.Background()
	carts := NewCartRepository(testPool)
	const user = int64(1003)

	p := productBySKU(t, "SUS-007")
	require.NoError(t, carts.SetQty(ctx, user, p.ID, 1))
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
