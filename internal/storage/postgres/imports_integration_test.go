package postgres

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
	"github.com/aristodem96/ai_shop_bot/internal/importer"
)

// The example in docs/import is the test input, so the file a reader of the
// README tries is the file CI proves works.
const (
	exampleProfile = "../../../profiles/demo-stock.json"
	exampleFile    = "../../../docs/import/example-price.csv"
)

func importExample(t *testing.T, file string, dryRun bool) (*importer.Report, importer.Result) {
	t.Helper()
	p, err := importer.LoadProfile(exampleProfile)
	require.NoError(t, err)
	rep, res, err := importer.Run(context.Background(), NewImportRepository(testPool), p, file, importer.Options{DryRun: dryRun})
	require.NoError(t, err)
	return rep, res
}

func cleanImports(t *testing.T) {
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = testPool.Exec(ctx, `DELETE FROM orders WHERE id IN (
			SELECT order_id FROM order_items oi JOIN products p ON p.id = oi.product_id WHERE p.source = 'demo-stock')`)
		_, _ = testPool.Exec(ctx, `DELETE FROM products WHERE source = 'demo-stock'`)
		_, _ = testPool.Exec(ctx, `DELETE FROM import_runs`)
	})
}

func imported(t *testing.T, code string) *domain.Product {
	t.Helper()
	var id int64
	require.NoError(t, testPool.QueryRow(context.Background(),
		`SELECT id FROM products WHERE source = 'demo-stock' AND external_id = $1`, code).Scan(&id))
	p, err := NewCatalogRepository(testPool).Product(context.Background(), id)
	require.NoError(t, err)
	return p
}

func TestImportExample(t *testing.T) {
	skipShort(t)
	cleanImports(t)
	ctx := context.Background()

	rep, res := importExample(t, exampleFile, true)
	assert.Equal(t, importer.Result{Inserted: 7, Fitments: 7}, res, "a dry run counts exactly")
	var n int
	require.NoError(t, testPool.QueryRow(ctx, `SELECT count(*) FROM products WHERE source = 'demo-stock'`).Scan(&n))
	assert.Zero(t, n, "and keeps nothing")

	assert.Equal(t, 9, rep.Rows)
	assert.Equal(t, 1, rep.Skipped, "the price \"за запитом\" is reported, not guessed")
	assert.Equal(t, []string{"Rivne Gum"}, rep.NewBrands, "\"Auto Line\" and \"STAL-PRO\" are brands the shop has")
	assert.Equal(t, []importer.Count{{Name: "Lada Priora", N: 1}}, rep.UnmatchedVehicles)

	_, res = importExample(t, exampleFile, false)
	assert.Equal(t, 7, res.Inserted)
	assert.NotZero(t, res.RunID)

	pads := imported(t, "10001")
	assert.Equal(t, "AutoLine", pads.Brand)
	assert.Equal(t, []string{"5Q0698151B"}, pads.OEMNumbers)
	assert.Equal(t, domain.UAH(1390), pads.Price)
	assert.Equal(t, 6, pads.Stock)
	assert.False(t, pads.OnOrder, "a file with a stock column is the shop's own shelf")
	octavia := vehicleByCode(t, "octavia-a7-20tdi")
	assert.True(t, pads.FitsVehicle(octavia), "\"Skoda Octavia A7\" covers every engine of it")
	assert.True(t, pads.FitsVehicle(vehicleByCode(t, "golf-7-16tdi")), "\"VW Golf VII\" goes through the profile's map")
	assert.Equal(t, "передня вісь", pads.FitNotes[octavia])

	assert.True(t, imported(t, "10005").FitsVehicle(vehicleByCode(t, "logan-2-16")), "a bare model name is enough when unique")
	assert.Equal(t, "Rivne Gum", imported(t, "10005").Brand)
	assert.True(t, imported(t, "10003").FitsVehicle(vehicleByCode(t, "corolla-e210-16")), "2019- overlaps a car made from 2019")
	brush := imported(t, "10008")
	assert.True(t, brush.Universal)
	assert.Equal(t, categoryBySlug(t, "mirrors-glass"), brush.CategoryID, "the profile's rule")
	assert.Equal(t, categoryBySlug(t, "brake-pads"), pads.CategoryID, "the tree's keywords")
	assert.Equal(t, categoryBySlug(t, "arms"), imported(t, "10005").CategoryID)

	// A second run of the same file changes nothing but the timestamps.
	_, res = importExample(t, exampleFile, false)
	assert.Equal(t, 0, res.Inserted)
	assert.Equal(t, 7, res.Updated)
}

func TestImportKeepsHeldStock(t *testing.T) {
	skipShort(t)
	cleanImports(t)
	importExample(t, exampleFile, false)

	// A buyer holds two of the six pads, then the shop uploads the same
	// count again: six are on the shelf, four are free to sell.
	const user = int64(5501)
	pads := imported(t, "10001")
	buyerCart(t, user, pads.ID, 2)
	place(t, user)
	require.Equal(t, 4, stockOf(t, pads.ID))

	importExample(t, exampleFile, false)
	assert.Equal(t, 4, stockOf(t, pads.ID), "units held by an open order are not sold twice")
}

func TestImportTakesMissingOffSale(t *testing.T) {
	skipShort(t)
	cleanImports(t)
	importExample(t, exampleFile, false)

	data, err := os.ReadFile(exampleFile)
	require.NoError(t, err)
	var kept []string
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "10004;") {
			kept = append(kept, line)
		}
	}
	shorter := filepath.Join(t.TempDir(), "price.csv")
	require.NoError(t, os.WriteFile(shorter, []byte(strings.Join(kept, "\n")), 0o600))

	_, res := importExample(t, shorter, false)
	assert.Equal(t, 1, res.Deactivated)
	var active bool
	require.NoError(t, testPool.QueryRow(context.Background(),
		`SELECT is_active FROM products WHERE source = 'demo-stock' AND external_id = '10004'`).Scan(&active))
	assert.False(t, active, "a part the supplier dropped is off sale, not deleted: orders still point at it")
}

// The shipped directory is test data too: a typo in it fails CI, not a
// shop's first import.
func TestImportVehicleDirectory(t *testing.T) {
	skipShort(t)
	ctx := context.Background()
	var before []string
	require.NoError(t, testPool.QueryRow(ctx, `SELECT array_agg(code) FROM vehicles`).Scan(&before))
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM vehicles WHERE code <> ALL($1)`, before)
		_, _ = testPool.Exec(ctx, `DELETE FROM vehicle_models m WHERE NOT EXISTS (
			SELECT 1 FROM vehicles v WHERE v.make = m.make AND v.model = m.model)`)
	})
	repo := NewImportRepository(testPool)
	problems, res, err := importer.ImportVehicles(ctx, repo, "../../../data/vehicles.csv", false)
	require.NoError(t, err)
	assert.Empty(t, problems)
	assert.Equal(t, len(before), res.Updated, "the seed's cars are in the file and are matched, not doubled")
	assert.Positive(t, res.Inserted)

	camry := vehicleByCode(t, "camry-xv70-25")
	v, err := NewVehicleRepository(testPool).Vehicle(ctx, camry)
	require.NoError(t, err)
	assert.Equal(t, "0W-20", v.OilViscosity, "service data is kept")

	_, res, err = importer.ImportVehicles(ctx, repo, "../../../data/vehicles.csv", false)
	require.NoError(t, err)
	assert.Zero(t, res.Inserted, "a second load adds nothing")
}

// A file cut short -- one sheet of six, an export that stopped halfway --
// looks exactly like a supplier dropping most of its range. The limit tells
// them apart by asking.
func TestImportStopsOnTruncatedFile(t *testing.T) {
	skipShort(t)
	cleanImports(t)
	ctx := context.Background()
	importExample(t, exampleFile, false)

	data, err := os.ReadFile(exampleFile)
	require.NoError(t, err)
	lines := strings.Split(string(data), "\n")
	truncated := filepath.Join(t.TempDir(), "price.csv")
	require.NoError(t, os.WriteFile(truncated, []byte(strings.Join(lines[:6], "\n")), 0o600)) // the header and two offers

	p, err := importer.LoadProfile(exampleProfile)
	require.NoError(t, err)
	repo := NewImportRepository(testPool)
	_, res, err := importer.Run(ctx, repo, p, truncated, importer.Options{})
	var limit *importer.DeactivationError
	require.ErrorAs(t, err, &limit)
	assert.Equal(t, importer.DeactivationError{Would: 5, Active: 7, LimitPercent: 20}, *limit)
	assert.Equal(t, 5, res.Deactivated, "the report still says what the file would do")
	var active int
	require.NoError(t, testPool.QueryRow(ctx,
		`SELECT count(*) FROM products WHERE source = 'demo-stock' AND is_active`).Scan(&active))
	assert.Equal(t, 7, active, "and nothing changed")

	_, res, err = importer.Run(ctx, repo, p, truncated, importer.Options{Force: true})
	require.NoError(t, err, "a supplier that really dropped them is one explicit step away")
	assert.Equal(t, 5, res.Deactivated)
}

func TestVehicleAliasMakesAndTakesFitments(t *testing.T) {
	skipShort(t)
	cleanImports(t)
	ctx := context.Background()
	repo := NewImportRepository(testPool)
	t.Cleanup(func() { _, _ = testPool.Exec(ctx, `DELETE FROM vehicle_aliases`) })

	rep, _ := importExample(t, exampleFile, false)
	require.Contains(t, rep.UnmatchedVehicles, importer.Count{Name: "Lada Priora", N: 1})
	assert.Equal(t, []string{"Тяга стабілізатора"}, rep.VehicleExamples["Lada Priora"])
	tie := imported(t, "10006")
	lanos := vehicleByCode(t, "lanos-15")
	require.False(t, tie.FitsVehicle(lanos))

	// The owner maps the supplier's "Lada Priora" in the chat. (To the
	// Lanos: the test catalogue has no Lada, and any model shows the path.)
	var lanosModel int64
	require.NoError(t, testPool.QueryRow(ctx,
		`SELECT id FROM vehicle_models WHERE make = 'Daewoo' AND model = 'Lanos'`).Scan(&lanosModel))
	require.NoError(t, repo.AddVehicleAlias(ctx, "demo-stock", "Lada Priora", "ladapriora", []int64{lanosModel}))
	require.NoError(t, repo.AddVehicleAlias(ctx, "demo-stock", "Lada Priora", "ladapriora", []int64{lanosModel}),
		"confirming twice changes nothing")

	rep, _ = importExample(t, exampleFile, false)
	assert.Empty(t, rep.UnmatchedVehicles)
	assert.True(t, imported(t, "10006").FitsVehicle(lanos), "the next import turns the mapping into a fitment")

	aliases, err := repo.ListVehicleAliases(ctx)
	require.NoError(t, err)
	require.Len(t, aliases, 1)
	assert.Equal(t, importer.VehicleAlias{ID: aliases[0].ID, Source: "demo-stock", Name: "Lada Priora", Model: "Daewoo Lanos"}, aliases[0])

	removed, err := repo.RemoveVehicleAlias(ctx, aliases[0].ID)
	require.NoError(t, err)
	assert.Equal(t, 1, removed.Fitments)
	assert.False(t, imported(t, "10006").FitsVehicle(lanos), "and removing it takes the fitment away at once")
	assert.True(t, imported(t, "10001").FitsVehicle(vehicleByCode(t, "golf-7-16tdi")), "fitments from other names stay")

	_, err = repo.RemoveVehicleAlias(ctx, aliases[0].ID)
	require.ErrorIs(t, err, importer.ErrNoAlias)
}
