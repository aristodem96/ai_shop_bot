package importer

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/xuri/excelize/v2"
)

// Sheet is one table of a file: a CSV file has one, a workbook one per sheet.
type Sheet struct {
	Name string
	Rows [][]string
}

// ReadFile reads every sheet of a .csv, .xlsx or .xlsm file as text.
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
// with or without a byte order mark, with ragged rows.
func readCSV(r io.Reader, delimiter rune) ([][]string, error) {
	br := bufio.NewReader(r)
	if bom, err := br.Peek(3); err == nil && bytes.Equal(bom, []byte{0xEF, 0xBB, 0xBF}) {
		_, _ = br.Discard(3)
	}
	cr := csv.NewReader(br)
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
