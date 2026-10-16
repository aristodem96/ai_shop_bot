package postgres

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

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

	if err := SyncCategories(ctx, testPool); err != nil {
		fmt.Fprintf(os.Stderr, "could not sync categories: %v\n", err)
		return 1
	}
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

	corolla, err := repo.Vehicle(ctx, vehicleByCode(t, "corolla-e210-16"))
	require.NoError(t, err)
	assert.Zero(t, corolla.YearTo, "still produced")
}

func TestFindVehicles(t *testing.T) {
	skipShort(t)
	repo := NewVehicleRepository(testPool)
	ctx := context.Background()

	got, err := repo.FindVehicles(ctx, service.VehicleQuery{Make: "skoda", Model: "octavia", Year: 2015, Engine: "1.4"}, 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "1.4 TSI", got[0].Engine)

	got, err = repo.FindVehicles(ctx, service.VehicleQuery{Model: "Octavia"}, 10)
	require.NoError(t, err)
	assert.Len(t, got, 2, "both engines when the engine is not given")

	got, err = repo.FindVehicles(ctx, service.VehicleQuery{Make: "Toyota", Year: 2026}, 10)
	require.NoError(t, err)
	require.Len(t, got, 1, "a model still in production matches any later year")
	assert.Equal(t, "Corolla E210", got[0].Model)

	got, err = repo.FindVehicles(ctx, service.VehicleQuery{}, 3)
	require.NoError(t, err)
	assert.Len(t, got, 3, "never the whole list")

	got, err = repo.FindVehicles(ctx, service.VehicleQuery{Make: "%"}, 10)
	require.NoError(t, err)
	assert.Empty(t, got, "a wildcard from the buyer is a literal character")

	makes, err := repo.Makes(ctx)
	require.NoError(t, err)
	assert.Len(t, makes, 8)
	assert.Equal(t, "Daewoo", makes[0])
}

func TestMaintenanceKit(t *testing.T) {
	skipShort(t)
	ctx := context.Background()
	vehicles := NewVehicleRepository(testPool)
	kits := service.NewMaintenanceService(NewCatalogRepository(testPool), vehicles)

	kit, err := kits.Kit(ctx, vehicleByCode(t, "octavia-a7-14tsi"))
	require.NoError(t, err)
	assert.Equal(t, "5W-30", kit.Vehicle.OilViscosity)
	assert.InDelta(t, 3.6, kit.Vehicle.OilVolume, 0.001)

	got := map[string][]string{}
	for _, i := range kit.Items {
		got[i.PartType] = append(got[i.PartType], fmt.Sprintf("%s x%d", i.Product.SKU, i.Qty))
	}
	assert.Equal(t, map[string][]string{
		domain.PartEngineOil:   {"OIL-001 x1"}, // 3.6 l: one 4 l can of 5W-30
		domain.PartOilFilter:   {"FLT-002 x1"}, // the cheaper of two with the same OEM number
		domain.PartAirFilter:   {"FLT-007 x1"},
		domain.PartCabinFilter: {"FLT-009 x1"},
		domain.PartSparkPlug:   {"ENG-002 x4"},
	}, got)
	assert.Empty(t, kit.Missing)

	diesel, err := kits.Kit(ctx, vehicleByCode(t, "passat-b8-20tdi"))
	require.NoError(t, err)
	for _, i := range diesel.Items {
		assert.NotEqual(t, domain.PartSparkPlug, i.PartType, "a diesel has no spark plugs")
	}

	camry, err := kits.Kit(ctx, vehicleByCode(t, "camry-xv70-25"))
	require.NoError(t, err)
	for _, i := range camry.Items {
		if i.PartType == domain.PartEngineOil {
			assert.Equal(t, "OIL-003", i.Product.SKU, "0W-20 for the Camry, not the cheaper 10W-40")
		}
	}
	assert.Contains(t, camry.Missing, domain.PartAirFilter, "the shop lists no Camry air filter")
}

func TestClaimAbandonedCarts(t *testing.T) {
	skipShort(t)
	ctx := context.Background()
	carts := NewCartRepository(testPool)
	oil := productBySKU(t, "OIL-001")

	const (
		abandoned = int64(3001)
		recent    = int64(3002)
		emptied   = int64(3003)
	)
	for _, u := range []int64{abandoned, recent, emptied} {
		require.NoError(t, carts.SetQty(ctx, u, oil.ID, 1))
		t.Cleanup(func() { _ = carts.Clear(ctx, u) })
	}
	require.NoError(t, carts.SetQty(ctx, emptied, oil.ID, 0))
	backdate := func(u int64) {
		_, err := testPool.Exec(ctx, `UPDATE carts SET updated_at = now() - interval '25 hours' WHERE user_id = $1`, u)
		require.NoError(t, err)
	}
	backdate(abandoned)
	backdate(emptied)

	got, err := carts.ClaimAbandoned(ctx, 24*time.Hour, 100)
	require.NoError(t, err)
	assert.Contains(t, got, abandoned)
	assert.NotContains(t, got, recent, "touched within the day")
	assert.NotContains(t, got, emptied, "nothing left in it to remind about")

	got, err = carts.ClaimAbandoned(ctx, 24*time.Hour, 100)
	require.NoError(t, err)
	assert.NotContains(t, got, abandoned, "a cart is reminded once")

	// Changing the cart starts a new abandonment, worth a new reminder.
	require.NoError(t, carts.SetQty(ctx, abandoned, oil.ID, 2))
	backdate(abandoned)
	got, err = carts.ClaimAbandoned(ctx, 24*time.Hour, 100)
	require.NoError(t, err)
	assert.Contains(t, got, abandoned)
}

func TestClaimAbandonedConcurrently(t *testing.T) {
	skipShort(t)
	ctx := context.Background()
	carts := NewCartRepository(testPool)
	oil := productBySKU(t, "OIL-002")

	const n = 40
	for i := range int64(n) {
		u := 4000 + i
		require.NoError(t, carts.SetQty(ctx, u, oil.ID, 1))
		t.Cleanup(func() { _ = carts.Clear(ctx, u) })
	}
	_, err := testPool.Exec(ctx, `UPDATE carts SET updated_at = now() - interval '2 days' WHERE user_id BETWEEN 4000 AND 4039`)
	require.NoError(t, err)

	// Two "bot instances" claim in small batches at the same time.
	var (
		mu      sync.Mutex
		claimed = map[int64]int{}
		wg      sync.WaitGroup
	)
	for range 2 {
		wg.Go(func() {
			for {
				got, err := carts.ClaimAbandoned(ctx, 24*time.Hour, 3)
				if !assert.NoError(t, err) || len(got) == 0 {
					return
				}
				mu.Lock()
				for _, u := range got {
					claimed[u]++
				}
				mu.Unlock()
			}
		})
	}
	wg.Wait()

	for i := range int64(n) {
		assert.Equal(t, 1, claimed[4000+i], "user %d claimed exactly once", 4000+i)
	}
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
