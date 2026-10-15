package importer

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
)

// headerSearchDepth is how far down a sheet the header row is looked for.
// Price lists open with the shop's logo, phones and opening hours.
const headerSearchDepth = 30

// Offer is one product as the file describes it, before anything is matched
// against the catalogue. Rows that repeat the same offer for another vehicle
// are merged into one, with one Fit per row.
type Offer struct {
	Key        string // the external id, or brand and part number
	PartNumber string
	Brand      string // as written
	Name       string
	Desc       string
	Image      string
	Category   string // the supplier's own, as written
	Price      domain.Money
	Stock      *int // nil without a stock column
	Fits       []Fit
	Row        RowRef
}

// Fit is a vehicle as the file names it, with what the row says about where
// and when the part fits it.
type Fit struct {
	Vehicle          string
	YearFrom, YearTo int // 0: open
	Position, Note   string
}

// RowRef points at a row the way a person finds it in Excel: sheet and line.
type RowRef struct {
	Sheet string
	Line  int
}

func (r RowRef) String() string { return fmt.Sprintf("%s:%d", r.Sheet, r.Line) }

// Problem is a row that could not be used, and why. The reason is in
// Ukrainian: it is read by the shop's staff, in the terminal or in the
// managers' chat.
type Problem struct {
	Row    RowRef
	Reason string
}

// Parsed is the result of reading a file.
type Parsed struct {
	Offers   []*Offer
	Rows     int // data rows seen, empty ones excluded
	Problems []Problem
}

// columnIndex maps the profile's columns to positions in a sheet.
type columnIndex map[string]int

func (c columnIndex) get(row []string, header string) string {
	i, ok := c[normHeader(header)]
	if header == "" || !ok || i >= len(row) {
		return ""
	}
	return clean(row[i])
}

// Parse turns sheets into offers. It never fails on a bad row: the row is
// reported and the rest of the file still loads, because one typo in a price
// list of eighty thousand rows must not block the other 79 999.
func Parse(sheets []Sheet, p *Profile) (*Parsed, error) {
	out := &Parsed{}
	byKey := make(map[string]*Offer)
	headers := 0
	var best *HeaderError

	for _, sh := range sheets {
		idx, start, miss := findHeader(sh, p.Columns)
		if miss != nil {
			// A workbook may hold a cover sheet or notes; only sheets with
			// the header are price list. The closest miss explains a file
			// where no sheet has it.
			if best == nil || len(miss.Missing) < len(best.Missing) {
				best = miss
			}
			continue
		}
		headers++
		for i := start; i < len(sh.Rows); i++ {
			row := sh.Rows[i]
			if isBlank(row) {
				continue
			}
			out.Rows++
			ref := RowRef{Sheet: sh.Name, Line: i + 1}
			o, err := parseRow(row, idx, p)
			if err != nil {
				out.Problems = append(out.Problems, Problem{Row: ref, Reason: err.Error()})
				continue
			}
			o.Row = ref
			if prev, ok := byKey[o.Key]; ok {
				prev.Fits = append(prev.Fits, o.Fits...)
				continue
			}
			byKey[o.Key] = o
			out.Offers = append(out.Offers, o)
		}
	}
	if headers == 0 {
		if best == nil {
			best = &HeaderError{Missing: p.Columns.all()}
		}
		return nil, best
	}
	return out, nil
}

// HeaderError means no sheet has a row with every column the profile names:
// the supplier renamed or dropped a column, or the profile belongs to another
// file. It names what is missing and what the closest row has instead, which
// is usually enough to see the renamed column at a glance.
type HeaderError struct {
	Missing []string // the profile's columns that the closest row lacks
	Sheet   string   // where the closest row is
	Line    int
	Found   []string // that row's cells
}

func (e *HeaderError) Error() string {
	if e.Line == 0 {
		return fmt.Sprintf("importer: no header row with the columns %q", e.Missing)
	}
	return fmt.Sprintf("importer: no header row has the columns %q; the closest, %s:%d, has %q",
		e.Missing, e.Sheet, e.Line, e.Found)
}

// findHeader finds the first row that holds every column the profile names,
// or reports the row that came closest.
func findHeader(sh Sheet, c Columns) (columnIndex, int, *HeaderError) {
	want := c.all()
	var best *HeaderError
	for i, row := range sh.Rows[:min(len(sh.Rows), headerSearchDepth)] {
		idx := make(columnIndex, len(row))
		for j, cell := range row {
			if h := normHeader(cell); h != "" {
				if _, dup := idx[h]; !dup {
					idx[h] = j
				}
			}
		}
		var missing []string
		for _, h := range want {
			if _, ok := idx[normHeader(h)]; !ok {
				missing = append(missing, h)
			}
		}
		if len(missing) == 0 {
			return idx, i + 1, nil
		}
		// A row matching nothing is a title or a blank, not a candidate.
		if len(missing) < len(want) && (best == nil || len(missing) < len(best.Missing)) {
			best = &HeaderError{Missing: missing, Sheet: sh.Name, Line: i + 1, Found: nonEmpty(row)}
		}
	}
	if best == nil {
		best = &HeaderError{Missing: want}
	}
	return nil, 0, best
}

func nonEmpty(row []string) []string {
	var out []string
	for _, c := range row {
		if c = clean(c); c != "" {
			out = append(out, c)
		}
	}
	return out
}

func (c Columns) all() []string {
	var out []string
	for _, h := range []string{c.ExternalID, c.PartNumber, c.Brand, c.Name, c.Price, c.Stock, c.Description,
		c.Image, c.Category, c.Vehicle, c.Years, c.YearFrom, c.YearTo, c.Position, c.Note} {
		if h != "" {
			out = append(out, h)
		}
	}
	return out
}

func parseRow(row []string, idx columnIndex, p *Profile) (*Offer, error) {
	c := p.Columns
	o := &Offer{
		PartNumber: idx.get(row, c.PartNumber),
		Brand:      idx.get(row, c.Brand),
		Name:       idx.get(row, c.Name),
		Desc:       idx.get(row, c.Description),
		Image:      idx.get(row, c.Image),
		Category:   idx.get(row, c.Category),
	}
	if o.Name == "" {
		return nil, errors.New("немає назви")
	}

	price, err := parsePrice(idx.get(row, c.Price))
	if err != nil {
		return nil, err
	}
	if p.MarkupPercent > 0 {
		price = domain.Money(math.Round(float64(price) * (1 + p.MarkupPercent/100)))
	}
	o.Price = price

	if c.Stock != "" {
		n, err := parseStock(idx.get(row, c.Stock))
		if err != nil {
			return nil, err
		}
		o.Stock = &n
	}

	if ext := idx.get(row, c.ExternalID); ext != "" {
		o.Key = ext
	} else if c.ExternalID != "" {
		return nil, fmt.Errorf("порожня колонка «%s»", c.ExternalID)
	} else {
		pn := domain.NormalizeOEM(o.PartNumber)
		if pn == "" {
			return nil, errors.New("немає артикула")
		}
		// Without the supplier's id, an offer is a part number from a
		// brand: the same number from two brands is two products.
		o.Key = domain.NormalizeOEM(o.Brand) + "|" + pn
	}

	if c.Vehicle != "" {
		from, to, err := parseYears(idx.get(row, c.Years), idx.get(row, c.YearFrom), idx.get(row, c.YearTo))
		if err != nil {
			return nil, err
		}
		position, note := idx.get(row, c.Position), idx.get(row, c.Note)
		// One cell may name several vehicles: "Chery Tiggo 2, Chery Tiggo 4".
		for _, v := range strings.FieldsFunc(idx.get(row, c.Vehicle), func(r rune) bool { return r == ',' || r == ';' }) {
			if v = clean(v); v != "" {
				o.Fits = append(o.Fits, Fit{Vehicle: v, YearFrom: from, YearTo: to, Position: position, Note: note})
			}
		}
	}
	return o, nil
}

// parsePrice reads a price in hryvnias as people write it: "1049", "1049.5",
// "1 049,50", "1049 грн". Money is counted in kopiyky from here on; the
// conversion is the only place a fraction is read, and it reads digits, not a
// float.
func parsePrice(s string) (domain.Money, error) {
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsDigit(r):
			b.WriteRune(r)
		case r == '.' || r == ',':
			b.WriteRune('.')
		}
	}
	num := b.String()
	// "1,049.50": the first separator groups thousands.
	if strings.Count(num, ".") > 1 {
		last := strings.LastIndex(num, ".")
		num = strings.ReplaceAll(num[:last], ".", "") + num[last:]
	}
	whole, frac, _ := strings.Cut(num, ".")
	// "1.049" or "1,049": three digits after the only separator are
	// thousands, not a fraction -- prices have kopiyky, not mills.
	if len(frac) == 3 {
		whole, frac = whole+frac, ""
	}
	if whole == "" {
		return 0, fmt.Errorf("незрозуміла ціна %q", s)
	}
	frac = (frac + "00")[:2]
	n, err := strconv.ParseInt(whole+frac, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("незрозуміла ціна %q", s)
	}
	return domain.Money(n), nil
}

// parseStock reads a quantity. Suppliers write ">10" or "10+" for "plenty";
// that counts as the number. An empty cell means none.
func parseStock(s string) (int, error) {
	s = strings.Trim(s, "<>+ ")
	if s == "" {
		return 0, nil
	}
	// A whole number stored by Excel as 5.0.
	s, _, _ = strings.Cut(s, ".")
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("незрозумілий залишок %q", s)
	}
	return n, nil
}

// parseYears reads "2013-2020", "2017-", "2013–н.в." from one column or two.
func parseYears(span, from, to string) (int, int, error) {
	if span != "" {
		f, t, _ := strings.Cut(strings.NewReplacer("–", "-", "—", "-").Replace(span), "-")
		from, to = f, t
	}
	yf, err := parseYear(from)
	if err != nil {
		return 0, 0, err
	}
	yt, err := parseYear(to)
	if err != nil {
		return 0, 0, err
	}
	if yf != 0 && yt != 0 && yt < yf {
		return 0, 0, fmt.Errorf("роки %d–%d йдуть навпаки", yf, yt)
	}
	return yf, yt, nil
}

// parseYear takes the four digits of a year and ignores the rest ("н.в.",
// "р."). Anything without them is an open end.
func parseYear(s string) (int, error) {
	var digits strings.Builder
	for _, r := range s {
		if unicode.IsDigit(r) {
			digits.WriteRune(r)
		}
	}
	if digits.Len() == 0 {
		return 0, nil
	}
	y, err := strconv.Atoi(digits.String())
	if err != nil || y < 1900 || y > 2100 {
		return 0, fmt.Errorf("незрозумілий рік %q", s)
	}
	return y, nil
}

// clean trims a cell and collapses runs of spaces, which spreadsheets are
// full of.
func clean(s string) string { return strings.Join(strings.Fields(s), " ") }

// normHeader compares headers without case or stray spaces.
func normHeader(s string) string { return strings.ToLower(clean(s)) }

func isBlank(row []string) bool {
	for _, c := range row {
		if strings.TrimSpace(c) != "" {
			return false
		}
	}
	return true
}
