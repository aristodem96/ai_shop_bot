package importer

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/xuri/excelize/v2"
	"golang.org/x/text/encoding/charmap"
)

// Sheet is one table of a file: a CSV file has one, a workbook one per sheet.
type Sheet struct {
	Name string
	Rows [][]string
}

// csvDelimiters are tried in turn when a profile does not name one: Excel in
// a Ukrainian locale writes ";", most exports ",", some tabs or pipes.
var csvDelimiters = []rune{';', ',', '\t', '|'}

func isCSV(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".csv", ".txt":
		return true
	}
	return false
}

// readAndParse reads a file and parses it with the profile. A CSV file
// whose delimiter the profile leaves open is read with each candidate until
// one gives the header row: a wrong delimiter leaves each line one cell, so
// the header is found with the right one only.
func readAndParse(path string, p *Supplier) (*Parsed, error) {
	delims := []rune{0}
	if isCSV(path) {
		delims = csvDelimiters
	}
	var best *HeaderError
	for _, d := range delims {
		sheets, err := ReadFile(path, d)
		if err != nil {
			return nil, err
		}
		parsed, err := Parse(sheets, p)
		var he *HeaderError
		if !errors.As(err, &he) {
			return parsed, err
		}
		if best == nil || len(he.Missing) < len(best.Missing) {
			best = he
		}
	}
	return nil, best
}

// ReadFile reads every sheet of a .csv, .xlsx or .xlsm file as text.
// csvDelimiter separates the fields of a CSV file.
func ReadFile(path string, csvDelimiter rune) ([]Sheet, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".csv", ".txt":
		f, err := os.Open(path) //nolint:gosec // the path is the operator's own argument
		if err != nil {
			return nil, fmt.Errorf("importer: open %s: %w", path, err)
		}
		defer f.Close() //nolint:errcheck // read-only
		rows, err := readCSV(f, csvDelimiter)
		if err != nil {
			return nil, fmt.Errorf("importer: read %s: %w", path, err)
		}
		return []Sheet{{Name: filepath.Base(path), Rows: rows}}, nil
	case ".xlsx", ".xlsm":
		return readWorkbook(path)
	default:
		return nil, fmt.Errorf("importer: %s: only .csv and .xlsx files are supported (save .xls as .xlsx)", path)
	}
}

// readCSV reads a CSV file as written by Excel or by a supplier's export:
// with or without a byte order mark, with ragged rows, in UTF-8 or in
// Windows-1251.
//
// Excel on a Ukrainian Windows and 1C save CSV in Windows-1251, and nothing
// in the file says so. Valid UTF-8 is read as UTF-8; anything else is taken
// for Windows-1251, the only other encoding a Ukrainian file comes in.
// Cyrillic in Windows-1251 is practically never valid UTF-8 by accident, so
// the guess does not misfire on real files.
func readCSV(r io.Reader, delimiter rune) ([][]string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	if !utf8.Valid(data) {
		if data, err = charmap.Windows1251.NewDecoder().Bytes(data); err != nil {
			return nil, fmt.Errorf("decode Windows-1251: %w", err)
		}
	}
	cr := csv.NewReader(bytes.NewReader(data))
	cr.Comma = delimiter
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	return cr.ReadAll()
}

// readWorkbook reads cell values as stored, not as displayed: a price kept as
// 1049.5 and shown as "1 049,50 ₴" arrives as 1049.5.
func readWorkbook(path string) ([]Sheet, error) {
	f, err := excelize.OpenFile(path)
	if err != nil {
		return nil, fmt.Errorf("importer: open %s: %w", path, err)
	}
	defer f.Close() //nolint:errcheck // read-only

	var sheets []Sheet
	for _, name := range f.GetSheetList() {
		rows, err := f.GetRows(name, excelize.Options{RawCellValue: true})
		if err != nil {
			return nil, fmt.Errorf("importer: read sheet %q of %s: %w", name, path, err)
		}
		sheets = append(sheets, Sheet{Name: name, Rows: rows})
	}
	if len(sheets) == 0 {
		return nil, errors.New("importer: the workbook has no sheets")
	}
	return sheets, nil
}
