// Package importer loads a supplier's price list -- CSV or Excel -- into the
// catalogue.
//
// No two suppliers lay out a file the same way, so the layout is data, not
// code: a profile names the columns and the rules that sort rows into
// categories. A shop adds a supplier by writing a profile, without a release.
//
// An import is a pipeline: read the sheets, parse rows into offers, resolve
// brands, vehicles and categories against the database, then apply everything
// in one transaction. A dry run does all of it and rolls back, so the report
// it prints -- what would be added, updated, switched off, and what could not
// be understood -- is exact, not an estimate.
package importer

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
)

// Profile describes one supplier's file and how to read it.
type Profile struct {
	// Source names the supplier. With the offer's external id it is the key
	// a re-import matches products on, so it must not change once used.
	Source string `json:"source"`
	// SKUPrefix starts the shop's own code of every product created from
	// this file: "CA" gives "CA-7KQ2M9XW4P".
	SKUPrefix string  `json:"sku_prefix"`
	Columns   Columns `json:"columns"`
	// CSVDelimiter is "," by default; Excel in a Ukrainian locale saves ";".
	CSVDelimiter string `json:"csv_delimiter,omitempty"`
	// LeadTimeDays makes every offer of a file without a stock column "on
	// order, delivered in this many days". Such a file is a supplier's
	// price list, not the shop's shelf.
	LeadTimeDays *int `json:"lead_time_days,omitempty"`
	// MarkupPercent turns a purchase price into a retail one.
	MarkupPercent float64 `json:"markup_percent,omitempty"`
	// DefaultCategory takes the rows no rule matched.
	DefaultCategory string `json:"default_category"`
	// CreateCategories are made if missing, slug to title, so a profile can
	// bring its own "other" section.
	CreateCategories map[string]string `json:"create_categories,omitempty"`
	// Rules are tried in order; the first that matches a row decides it.
	Rules []Rule `json:"rules,omitempty"`
	// Vehicles maps how the supplier names a vehicle to the catalogue's
	// models, written "Make Model": {"Chery Tiggo": ["Chery Tiggo 7"]}. A
	// name that already reads like a model of the catalogue needs no entry.
	Vehicles map[string][]string `json:"vehicles,omitempty"`
	// DeactivateMissing takes off sale this supplier's products that the
	// file no longer lists. Right for a full price list, wrong for a partial
	// one, hence a choice.
	DeactivateMissing bool `json:"deactivate_missing,omitempty"`
}

// Columns are header texts, matched without regard to case or surrounding
// spaces. Empty means the file has no such column.
type Columns struct {
	ExternalID  string `json:"external_id,omitempty"`
	PartNumber  string `json:"part_number,omitempty"`
	Brand       string `json:"brand,omitempty"`
	Name        string `json:"name"`
	Price       string `json:"price"`
	Stock       string `json:"stock,omitempty"`
	Description string `json:"description,omitempty"`
	Image       string `json:"image,omitempty"`
	Category    string `json:"category,omitempty"`
	Vehicle     string `json:"vehicle,omitempty"`
	// Years is one column like "2013-2020" or "2017-"; YearFrom and YearTo
	// are the same split in two.
	Years    string `json:"years,omitempty"`
	YearFrom string `json:"year_from,omitempty"`
	YearTo   string `json:"year_to,omitempty"`
	Position string `json:"position,omitempty"`
	Note     string `json:"note,omitempty"`
}

// Rule sorts rows into a category by words in the name or the supplier's own
// category.
type Rule struct {
	// Contains are lower-case fragments; any one of them matching is enough.
	Contains []string `json:"contains"`
	Category string   `json:"category"`
	// PartType feeds the maintenance kit ("oil_filter"); optional.
	PartType string `json:"part_type,omitempty"`
	// Universal marks parts that fit any car, such as oil or bulbs.
	Universal bool `json:"universal,omitempty"`
}

var sourcePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// LoadProfile reads and checks a profile file.
func LoadProfile(path string) (*Profile, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the path is the operator's own argument
	if err != nil {
		return nil, fmt.Errorf("importer: read profile: %w", err)
	}
	var p Profile
	dec := json.NewDecoder(bytes.NewReader(data))
	// A misspelt key would otherwise be dropped in silence and the column
	// it names ignored.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("importer: parse profile %s: %w", path, err)
	}
	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("importer: profile %s: %w", path, err)
	}
	return &p, nil
}

// Validate reports every problem at once, so a profile is fixed in one pass.
func (p *Profile) Validate() error {
	var errs []error
	if !sourcePattern.MatchString(p.Source) {
		errs = append(errs, errors.New("source must be lower-case letters, digits and dashes"))
	}
	if p.SKUPrefix == "" {
		errs = append(errs, errors.New("sku_prefix is required"))
	}
	if p.Columns.Name == "" || p.Columns.Price == "" {
		errs = append(errs, errors.New("columns.name and columns.price are required"))
	}
	if p.Columns.ExternalID == "" && p.Columns.PartNumber == "" {
		errs = append(errs, errors.New("columns.external_id or columns.part_number is required: without one a re-import cannot find the product it updates"))
	}
	if p.Columns.Stock == "" && p.LeadTimeDays == nil {
		errs = append(errs, errors.New("a file without a stock column needs lead_time_days, or every part would be sold out"))
	}
	if p.LeadTimeDays != nil && *p.LeadTimeDays < 0 {
		errs = append(errs, errors.New("lead_time_days cannot be negative"))
	}
	if p.MarkupPercent < 0 {
		errs = append(errs, errors.New("markup_percent cannot be negative"))
	}
	if p.DefaultCategory == "" {
		errs = append(errs, errors.New("default_category is required"))
	}
	if p.CSVDelimiter != "" && len([]rune(p.CSVDelimiter)) != 1 {
		errs = append(errs, errors.New("csv_delimiter must be one character"))
	}
	for i, r := range p.Rules {
		if len(r.Contains) == 0 || r.Category == "" {
			errs = append(errs, fmt.Errorf("rules[%d] needs contains and category", i))
		}
	}
	return errors.Join(errs...)
}

// delimiter is the CSV field separator.
func (p *Profile) delimiter() rune {
	if p.CSVDelimiter == "" {
		return ','
	}
	return []rune(p.CSVDelimiter)[0]
}
