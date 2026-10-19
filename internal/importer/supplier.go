package importer

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// Supplier is where price lists come from -- a supplier, or the shop's own
// 1C -- and how its files are read: which column holds what. It is made in
// the managers' chat from the first file it sends, and recognised by its
// columns after that.
type Supplier struct {
	ID   int64
	Name string
	// Source is the key the supplier's products are matched on at every
	// import. Made from the name once, it never changes, even if the name
	// does.
	Source  string
	Columns Columns
	// LeadDays makes every row of a file without a stock column "on order,
	// delivered in this many days": such a file is a supplier's price list,
	// not the shop's shelf. Nil when the file has stock.
	LeadDays *int
	// MarkupPercent turns the file's prices into the shop's: 25 sells what
	// the file prices at 1000 for 1250.
	MarkupPercent float64
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

// defaultCategory takes the rows no word recognised.
const defaultCategory = "other"

// maxDeactivatePercent stops an import that would take more than this
// share of a supplier's products off sale: a truncated file, one sheet of
// six, looks exactly like a supplier dropping most of its range. Going past
// it takes an explicit force.
const maxDeactivatePercent = 20

// Errors of a supplier, worded for the owner who reads them in the chat.
var (
	ErrNoName       = errors.New("потрібна колонка з назвою товару")
	ErrNoPrice      = errors.New("потрібна колонка з ціною")
	ErrNoKey        = errors.New("потрібна колонка з кодом товару або артикулом: за нею наступний прайс знаходить товари, які оновлює")
	ErrNoStockOrDay = errors.New("у файлі немає залишків: вкажіть термін доставки, інакше всі товари будуть «немає в наявності»")
	ErrSupplierName = errors.New("назва постачальника має бути від 2 до 40 символів")
	ErrBadMarkup    = errors.New("націнка має бути від 0 до 500%")
)

// Validate reports every problem at once, so a supplier is fixed in one
// pass.
func (s *Supplier) Validate() error {
	var errs []error
	if n := len([]rune(strings.TrimSpace(s.Name))); n < 2 || n > 40 {
		errs = append(errs, ErrSupplierName)
	}
	if s.Columns.Name == "" {
		errs = append(errs, ErrNoName)
	}
	if s.Columns.Price == "" {
		errs = append(errs, ErrNoPrice)
	}
	if s.Columns.ExternalID == "" && s.Columns.PartNumber == "" {
		errs = append(errs, ErrNoKey)
	}
	if s.Columns.Stock == "" && (s.LeadDays == nil || *s.LeadDays < 0) {
		errs = append(errs, ErrNoStockOrDay)
	}
	if s.MarkupPercent < 0 || s.MarkupPercent > 500 {
		errs = append(errs, ErrBadMarkup)
	}
	return errors.Join(errs...)
}

// skuPrefix starts the shop's code of every product created from the
// supplier's files: "china-avto" gives "CH".
func (s *Supplier) skuPrefix() string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s.Source) {
		if r >= 'A' && r <= 'Z' {
			b.WriteRune(r)
			if b.Len() == 2 {
				break
			}
		}
	}
	if b.Len() < 2 {
		return "SP"
	}
	return b.String()
}

// SourceFor makes a supplier's key from its name: "China Avto" gives
// "china-avto". taken says whether a key is in use; a number is added until
// one is free.
func SourceFor(name string, taken func(string) bool) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch t, ok := translit[r]; {
		case ok:
			b.WriteString(t)
			dash = false
		case r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	base := strings.Trim(b.String(), "-")
	if base == "" || !unicode.IsLetter(rune(base[0])) {
		base = "supplier-" + base
	}
	base = strings.TrimSuffix(base[:min(len(base), 32)], "-")
	src := base
	for i := 2; taken(src); i++ {
		src = fmt.Sprintf("%s-%d", base, i)
	}
	return src
}

// translit follows the Ukrainian national transliteration, simplified to
// one form per letter; Russian letters are covered for Russian names.
var translit = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "h", 'ґ': "g", 'д': "d", 'е': "e", 'є': "ie", 'ж': "zh", 'з': "z",
	'и': "y", 'і': "i", 'ї': "i", 'й': "i", 'к': "k", 'л': "l", 'м': "m", 'н': "n", 'о': "o", 'п': "p",
	'р': "r", 'с': "s", 'т': "t", 'у': "u", 'ф': "f", 'х': "kh", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "shch",
	'ь': "", 'ю': "iu", 'я': "ia", '\'': "", 'ʼ': "", 'ы': "y", 'э': "e", 'ё': "e", 'ъ': "",
}
