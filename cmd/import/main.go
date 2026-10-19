// Command import loads a supplier's price list (CSV or Excel) into the
// catalogue, as its profile describes it, or the shop's vehicle directory.
//
//	import -profile supplier.json -file price.xlsx -dry-run
//	import -vehicles data/vehicles.csv
//
// Load vehicles first: a price list's fitments can only point at vehicles
// the catalogue knows.
//
// Run with -dry-run first: it reports what would change and every row it
// could not understand, and changes nothing.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/aristodem96/ai_shop_bot/internal/config"
	"github.com/aristodem96/ai_shop_bot/internal/importer"
	"github.com/aristodem96/ai_shop_bot/internal/storage/postgres"
)

// listed bounds each list in the printed report.
const listed = 15

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		profilePath = flag.String("profile", "", "supplier profile, JSON (required)")
		filePath    = flag.String("file", "", "price list, .csv or .xlsx (required)")
		vehicles    = flag.String("vehicles", "", "vehicle directory, .csv (instead of -profile and -file)")
		dryRun      = flag.Bool("dry-run", false, "report what would change and change nothing")
		force       = flag.Bool("force", false, "take off sale more of the supplier's products than its profile allows")
	)
	flag.Parse()
	if (*vehicles == "") == (*profilePath == "" || *filePath == "") {
		flag.Usage()
		return fmt.Errorf("give either -profile and -file, or -vehicles")
	}

	var profile *importer.Profile
	if *vehicles == "" {
		var err error
		if profile, err = importer.LoadProfile(*profilePath); err != nil {
			return err
		}
	}
	cfg, err := config.LoadDatabase()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	if err := postgres.Migrate(ctx, cfg.DSN, log); err != nil {
		return err
	}
	pool, err := postgres.NewPool(ctx, cfg.DSN, 2)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := postgres.SeedCategories(ctx, pool); err != nil {
		return err
	}

	started := time.Now()
	if *vehicles != "" {
		return importVehicles(ctx, postgres.NewImportRepository(pool), *vehicles, *dryRun)
	}
	report, res, err := importer.Run(ctx, postgres.NewImportRepository(pool), profile, *filePath,
		importer.Options{DryRun: *dryRun, Force: *force})
	var out strings.Builder
	if report != nil {
		printReport(&out, report)
	}
	var limit *importer.DeactivationError
	switch {
	case errors.As(err, &limit):
		fmt.Fprintf(&out, "\nStopped, nothing changed: the file would take %d of %d active products off sale, more than %d%%.\n"+
			"A truncated file looks just like this. If the supplier really dropped them, run again with -force.\n",
			limit.Would, limit.Active, limit.LimitPercent)
		err = errors.New("import stopped by the deactivation limit")
	case err == nil:
		printResult(&out, res, *dryRun, time.Since(started))
	}
	if _, werr := io.WriteString(os.Stdout, out.String()); werr != nil && err == nil {
		err = werr
	}
	return err
}

func importVehicles(ctx context.Context, store importer.VehicleStore, path string, dryRun bool) error {
	problems, res, err := importer.ImportVehicles(ctx, store, path, dryRun)
	var out strings.Builder
	if len(problems) > 0 {
		fmt.Fprintf(&out, "Skipped rows: %d\n", len(problems))
		for _, p := range problems {
			fmt.Fprintf(&out, "  line %d  %s\n", p.Row.Line, p.Reason)
		}
	}
	if err == nil {
		verb := "Applied"
		if dryRun {
			verb = "Dry run, nothing changed. Would apply"
		}
		fmt.Fprintf(&out, "%s: %d new vehicles, %d updated\n", verb, res.Inserted, res.Updated)
	}
	if _, werr := io.WriteString(os.Stdout, out.String()); werr != nil && err == nil {
		err = werr
	}
	return err
}

func printReport(w *strings.Builder, r *importer.Report) {
	fmt.Fprintf(w, "File %s: %d rows, %d offers\n", r.File, r.Rows, r.Offers)

	if r.Skipped > 0 {
		fmt.Fprintf(w, "\nSkipped rows: %d\n", r.Skipped)
		for _, p := range r.Problems {
			fmt.Fprintf(w, "  %s  %s\n", p.Row, p.Reason)
		}
		if r.Skipped > len(r.Problems) {
			fmt.Fprintf(w, "  ... and %d more\n", r.Skipped-len(r.Problems))
		}
	}

	fmt.Fprintln(w, "\nCategories:")
	for _, c := range r.Categories {
		fmt.Fprintf(w, "  %-14s %d\n", c.Name, c.N)
	}

	if len(r.NewBrands) > 0 {
		fmt.Fprintf(w, "\nNew brands: %d (a misspelling here belongs in brand_aliases)\n", len(r.NewBrands))
		printList(w, r.NewBrands)
	}

	if r.Unsorted > 0 {
		fmt.Fprintf(w, "\nProducts no category recognised: %d (filed in the default; a keyword from these names belongs in data/categories.csv)\n", r.Unsorted)
		printList(w, r.Unrecognised)
	}

	if r.WithoutFitment > 0 {
		fmt.Fprintf(w, "\nProducts that fit no known vehicle: %d (hidden from buyers with a car chosen)\n", r.WithoutFitment)
	}
	if len(r.UnmatchedVehicles) > 0 {
		fmt.Fprintf(w, "\nVehicle names matching no model: %d (map them in the profile's \"vehicles\")\n", len(r.UnmatchedVehicles))
		for _, c := range r.UnmatchedVehicles[:min(len(r.UnmatchedVehicles), listed)] {
			fmt.Fprintf(w, "  %-30s %d rows\n", c.Name, c.N)
		}
		if extra := len(r.UnmatchedVehicles) - listed; extra > 0 {
			fmt.Fprintf(w, "  ... and %d more\n", extra)
		}
	}
}

func printList(w *strings.Builder, items []string) {
	for _, s := range items[:min(len(items), listed)] {
		fmt.Fprintf(w, "  %s\n", s)
	}
	if extra := len(items) - listed; extra > 0 {
		fmt.Fprintf(w, "  ... and %d more\n", extra)
	}
}

func printResult(w *strings.Builder, res importer.Result, dryRun bool, took time.Duration) {
	verb := "Applied"
	if dryRun {
		verb = "Dry run, nothing changed. Would apply"
	}
	fmt.Fprintf(w, "\n%s: %d new, %d updated, %d taken off sale, %d fitments (%s)\n",
		verb, res.Inserted, res.Updated, res.Deactivated, res.Fitments, took.Round(time.Millisecond))
	if res.RunID != 0 {
		fmt.Fprintf(w, "Logged as import run #%d\n", res.RunID)
	}
}
