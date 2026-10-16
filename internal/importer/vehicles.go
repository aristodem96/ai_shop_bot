package importer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"unicode"
)

// Vehicle is one row of a vehicle file: a make, model and engine, the level
// at which parts differ.
type Vehicle struct {
	Make, Model, Engine, Fuel string
	YearFrom, YearTo          int // YearTo 0: still produced
	// Service data, optional: empty and zero when the file does not say.
	OilViscosity string
	OilVolume    float64
}

// Code is the vehicle's stable key: "geely-emgrand-ec7-1-8-2009".
func (v Vehicle) Code() string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(fmt.Sprintf("%s %s %s %d", v.Make, v.Model, v.Engine, v.YearFrom)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}

// VehicleStore writes a vehicle directory.
type VehicleStore interface {
	ApplyVehicles(ctx context.Context, vs []Vehicle, dryRun bool) (VehicleResult, error)
}

// VehicleResult is what ApplyVehicles wrote.
type VehicleResult struct {
	Inserted, Updated int
}

// The columns of a vehicle file. Unlike a price list, this file is the
// shop's own, so its layout is fixed rather than described by a profile.
var (
	vehicleRequired = []string{"make", "model", "year_from", "year_to", "engine", "fuel"}
	vehicleOptional = []string{"oil_viscosity", "oil_volume_l"}
	fuels           = map[string]bool{"petrol": true, "diesel": true, "hybrid": true, "electric": true}
)

// ImportVehicles loads a vehicle file. Lines starting with # are comments.
func ImportVehicles(ctx context.Context, store VehicleStore, path string, dryRun bool) ([]Problem, VehicleResult, error) {
	f, err := os.Open(path) //nolint:gosec // the path is the operator's own argument
	if err != nil {
		return nil, VehicleResult{}, fmt.Errorf("importer: open %s: %w", path, err)
	}
	defer f.Close() //nolint:errcheck // read-only
	rows, err := readCSV(f, ',')
	if err != nil {
		return nil, VehicleResult{}, fmt.Errorf("importer: read %s: %w", path, err)
	}
	vs, problems, err := ParseVehicles(rows)
	if err != nil {
		return nil, VehicleResult{}, fmt.Errorf("importer: %s: %w", path, err)
	}
	res, err := store.ApplyVehicles(ctx, vs, dryRun)
	return problems, res, err
}

// ParseVehicles reads the rows of a vehicle file. As with a price list, a
// bad row is reported and skipped; a missing column fails the whole file.
func ParseVehicles(rows [][]string) ([]Vehicle, []Problem, error) {
	// Comments come first; the header is the first row that is not one.
	start := 0
	for start < len(rows) && (isBlank(rows[start]) || isComment(rows[start])) {
		start++
	}
	if start == len(rows) {
		return nil, nil, errors.New("файл порожній")
	}
	idx := make(map[string]int)
	for i, h := range rows[start] {
		idx[normHeader(h)] = i
	}
	for _, h := range vehicleRequired {
		if _, ok := idx[h]; !ok {
			return nil, nil, fmt.Errorf("немає колонки %q; шапка має бути такою: %s", h,
				strings.Join(append(vehicleRequired, vehicleOptional...), ","))
		}
	}
	get := func(row []string, h string) string {
		if i, ok := idx[h]; ok && i < len(row) {
			return clean(row[i])
		}
		return ""
	}

	var (
		out      []Vehicle
		problems []Problem
		seen     = make(map[string]int)
	)
	for i := start + 1; i < len(rows); i++ {
		row := rows[i]
		if isBlank(row) || isComment(row) {
			continue
		}
		ref := RowRef{Sheet: "vehicles", Line: i + 1}
		v, err := parseVehicle(get, row)
		if err != nil {
			problems = append(problems, Problem{Row: ref, Reason: err.Error()})
			continue
		}
		if first, dup := seen[v.Code()]; dup {
			problems = append(problems, Problem{Row: ref, Reason: fmt.Sprintf("повторює рядок %d", first)})
			continue
		}
		seen[v.Code()] = ref.Line
		out = append(out, v)
	}
	return out, problems, nil
}

// isComment reports a row whose first cell starts with #. Comments are read
// as rows rather than dropped by the CSV reader, which would make every line
// number after them wrong.
func isComment(row []string) bool {
	return len(row) > 0 && strings.HasPrefix(strings.TrimSpace(row[0]), "#")
}

func parseVehicle(get func([]string, string) string, row []string) (Vehicle, error) {
	v := Vehicle{
		Make: get(row, "make"), Model: get(row, "model"), Engine: get(row, "engine"),
		Fuel: get(row, "fuel"), OilViscosity: get(row, "oil_viscosity"),
	}
	var err error
	if v.YearFrom, err = parseYear(get(row, "year_from")); err != nil || v.YearFrom == 0 {
		return v, fmt.Errorf("незрозумілий year_from %q", get(row, "year_from"))
	}
	if v.YearTo, err = parseYear(get(row, "year_to")); err != nil {
		return v, err
	}
	if s := get(row, "oil_volume_l"); s != "" {
		v.OilVolume, err = strconv.ParseFloat(strings.ReplaceAll(s, ",", "."), 64)
		if err != nil || v.OilVolume <= 0 || v.OilVolume >= 100 {
			return v, fmt.Errorf("незрозумілий oil_volume_l %q", s)
		}
	}
	return v, v.Normalize()
}

// Normalize tidies a vehicle and checks it is one the directory can hold.
// The file, the owner's typing and a model's proposal all pass through it,
// so the directory has one standard whatever wrote the row.
func (v *Vehicle) Normalize() error {
	v.Make, v.Model, v.Engine = clean(v.Make), clean(v.Model), clean(v.Engine)
	v.Fuel = strings.ToLower(clean(v.Fuel))
	if f, ok := fuelWords[v.Fuel]; ok {
		v.Fuel = f
	}
	switch {
	case v.Make == "" || v.Model == "" || v.Engine == "":
		return errors.New("потрібні марка, модель і двигун")
	case !fuels[v.Fuel]:
		return fmt.Errorf("пальне %q — не бензин, дизель, гібрид чи електро", v.Fuel)
	case v.YearFrom < 1950 || v.YearFrom > 2100:
		return fmt.Errorf("незрозумілий рік початку %d", v.YearFrom)
	case v.YearTo != 0 && v.YearTo < v.YearFrom:
		return fmt.Errorf("роки %d–%d йдуть навпаки", v.YearFrom, v.YearTo)
	}
	return nil
}

// fuelWords lets people write fuel as they say it.
var fuelWords = map[string]string{
	"бензин": "petrol", "дизель": "diesel", "гібрид": "hybrid", "електро": "electric",
	"електричний": "electric", "газ/бензин": "petrol", "бензиновий": "petrol", "дизельний": "diesel",
}

// FuelName is the Ukrainian name of a fuel, for messages.
func FuelName(fuel string) string {
	switch fuel {
	case "petrol":
		return "бензин"
	case "diesel":
		return "дизель"
	case "hybrid":
		return "гібрид"
	case "electric":
		return "електро"
	}
	return fuel
}

// ParseVehicleLine reads one vehicle typed by hand, comma-separated: "Peugeot,
// 308 T7, 2007, 2013, 1.6 HDi, дизель". The second year may be empty or
// "н.ч." for a car still produced. It is the fallback when no model is there
// to read free text.
func ParseVehicleLine(s string) (Vehicle, error) {
	parts := strings.Split(s, ",")
	if len(parts) != 6 {
		return Vehicle{}, errors.New("потрібно 6 частин через кому: марка, модель, рік від, рік до, двигун, пальне")
	}
	v := Vehicle{Make: parts[0], Model: parts[1], Engine: parts[4], Fuel: parts[5]}
	var err error
	if v.YearFrom, err = parseYear(parts[2]); err != nil || v.YearFrom == 0 {
		return v, fmt.Errorf("незрозумілий рік початку %q", clean(parts[2]))
	}
	if v.YearTo, err = parseYear(parts[3]); err != nil {
		return v, err
	}
	return v, v.Normalize()
}
