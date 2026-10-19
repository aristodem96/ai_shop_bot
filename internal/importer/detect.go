package importer

import (
	"sort"
	"strings"
)

// Field is a kind of column the shop reads from a price list.
type Field string

// Fields of a price list.
const (
	FieldName        Field = "name"
	FieldPrice       Field = "price"
	FieldCode        Field = "code"
	FieldArticle     Field = "article"
	FieldBrand       Field = "brand"
	FieldStock       Field = "stock"
	FieldGroup       Field = "group"
	FieldVehicle     Field = "vehicle"
	FieldYears       Field = "years"
	FieldPosition    Field = "position"
	FieldImage       Field = "image"
	FieldDescription Field = "description"
)

// FieldInfo describes a field for the owner.
type FieldInfo struct {
	Field Field
	Label string
	// Why says in a few words what the shop does with it.
	Why string
}

// Fields lists the fields in the order the owner is shown them.
var Fields = []FieldInfo{
	{FieldName, "Назва", "обов'язково"},
	{FieldPrice, "Ціна", "обов'язково"},
	{FieldCode, "Код товару", "код в обліку (1С); за ним оновлюється товар"},
	{FieldArticle, "Артикул", "номер деталі; для пошуку за номером"},
	{FieldBrand, "Бренд", "виробник деталі"},
	{FieldStock, "Залишок", "без нього все буде «під замовлення»"},
	{FieldGroup, "Група", "група товару в обліку; допомагає розкласти по категоріях"},
	{FieldVehicle, "Авто", "до яких машин підходить"},
	{FieldYears, "Роки авто", "напр. 2013-2020"},
	{FieldPosition, "Де на авто", "напр. «передня вісь», «ліва»"},
	{FieldImage, "Фото", "посилання на фото"},
	{FieldDescription, "Опис", ""},
}

// Get returns the header the field is read from, "" for none.
func (c *Columns) Get(f Field) string {
	if p := c.field(f); p != nil {
		return *p
	}
	return ""
}

// Set reads the field from the header, or from none for "".
func (c *Columns) Set(f Field, header string) {
	if p := c.field(f); p != nil {
		*p = header
	}
}

func (c *Columns) field(f Field) *string {
	switch f {
	case FieldName:
		return &c.Name
	case FieldPrice:
		return &c.Price
	case FieldCode:
		return &c.ExternalID
	case FieldArticle:
		return &c.PartNumber
	case FieldBrand:
		return &c.Brand
	case FieldStock:
		return &c.Stock
	case FieldGroup:
		return &c.Category
	case FieldVehicle:
		return &c.Vehicle
	case FieldYears:
		return &c.Years
	case FieldPosition:
		return &c.Position
	case FieldImage:
		return &c.Image
	case FieldDescription:
		return &c.Description
	}
	return nil
}

// synonyms are header texts each field goes by, lower case, in Ukrainian
// and Russian price lists and 1C exports. A header equal to one scores
// highest, one starting with it next, one containing it last.
var synonyms = map[Field][]string{
	FieldName: {"найменування", "наименование", "назва", "название", "номенклатура", "товар", "назва товару",
		"наименование товара", "name", "product"},
	FieldPrice: {"ціна роздріб", "цена розница", "роздрібна ціна", "розничная цена", "ціна", "цена", "вартість",
		"стоимость", "price"},
	FieldCode: {"код", "код товару", "код товара", "код номенклатури", "код номенклатуры", "ид", "id", "посилання"},
	FieldArticle: {"артикул", "кат. номер", "каталожний номер", "каталожный номер", "номер деталі", "номер детали",
		"part number", "oem", "номер"},
	FieldBrand: {"бренд", "виробник", "производитель", "торгова марка", "торговая марка", "brand", "марка товару"},
	FieldStock: {"залишок", "остаток", "наявність", "наличие", "кількість", "количество", "к-сть", "кол-во",
		"stock", "qty"},
	FieldGroup: {"група", "группа", "категорія", "категория", "вид номенклатури", "вид номенклатуры", "розділ", "раздел"},
	FieldVehicle: {"авто", "автомобіль", "автомобиль", "застосовність", "применяемость", "марка авто", "модель авто",
		"машина", "car"},
	FieldYears:       {"роки", "годы", "рік", "год", "year"},
	FieldPosition:    {"розташування", "расположение", "сторона", "положення", "положение", "вісь", "ось", "position", "side"},
	FieldImage:       {"фото", "зображення", "изображение", "картинка", "image", "photo"},
	FieldDescription: {"опис", "описание", "description", "характеристики"},
}

// avoid are headers that look like a field and are not: a purchase price
// is no price to sell at, a barcode no product code.
var avoid = map[Field][]string{
	FieldPrice: {"закуп", "вхідн", "входн", "собіварт", "себестоим"},
	FieldCode:  {"штрих", "уктзед", "вэд", "зед"},
	FieldName:  {"код"},
}

// score rates how well a header names a field: 0 not at all.
func score(f Field, header string) int {
	h := normHeader(header)
	if h == "" {
		return 0
	}
	for _, a := range avoid[f] {
		if strings.Contains(h, a) {
			return 0
		}
	}
	best := 0
	for _, s := range synonyms[f] {
		var sc int
		switch {
		case h == s:
			sc = 300
		case strings.HasPrefix(h, s):
			sc = 200
		case f != FieldCode && strings.Contains(h, s):
			// "код" inside a longer header is most often something else's code.
			sc = 100
		}
		// A longer synonym is the more specific one.
		if sc > 0 && sc+len(s) > best {
			best = sc + len(s)
		}
	}
	return best
}

// Detect reads which column holds which field from a header row. Each
// column serves one field, and the best scored pairs are taken first.
func Detect(header []string) Columns {
	type pair struct {
		f     Field
		col   string
		score int
	}
	var pairs []pair
	for _, cell := range header {
		col := clean(cell)
		if col == "" {
			continue
		}
		for _, fi := range Fields {
			if s := score(fi.Field, col); s > 0 {
				pairs = append(pairs, pair{fi.Field, col, s})
			}
		}
	}
	sort.SliceStable(pairs, func(i, j int) bool { return pairs[i].score > pairs[j].score })
	var c Columns
	usedCol := map[string]bool{}
	for _, p := range pairs {
		if c.Get(p.f) != "" || usedCol[normHeader(p.col)] {
			continue
		}
		c.Set(p.f, p.col)
		usedCol[normHeader(p.col)] = true
	}
	return c
}

// Inspection is what a file looks like before a supplier reads it.
type Inspection struct {
	// Header is the row that names the columns, its non-empty cells.
	Header []string
	Sheet  string
	Line   int
	// Detected is the reading of the header Detect proposes.
	Detected Columns
	// Supplier is the known supplier whose columns the file has, nil when
	// none: a new supplier, or a known one that changed its file.
	Supplier *Supplier
}

// Inspect finds the header row of a file and the supplier it belongs to.
// The row with the most recognisable columns is the header; a known
// supplier is one whose every column the file has, the one with the most
// columns when several have.
func Inspect(path string, known []Supplier) (*Inspection, error) {
	delims := []rune{0}
	if isCSV(path) {
		delims = csvDelimiters
	}
	var best *Inspection
	bestScore := -1
	for _, d := range delims {
		sheets, err := ReadFile(path, d)
		if err != nil {
			return nil, err
		}
		for _, sh := range sheets {
			for i, row := range sh.Rows[:min(len(sh.Rows), headerSearchDepth)] {
				cols := Detect(row)
				n := len(cols.all())
				if cols.Name != "" && cols.Price != "" {
					n += 10
				}
				if n > bestScore {
					bestScore = n
					best = &Inspection{Header: nonEmpty(row), Sheet: sh.Name, Line: i + 1, Detected: cols}
				}
			}
		}
		for i := range known {
			s := &known[i]
			for _, sh := range sheets {
				if _, _, miss := findHeader(sh, s.Columns); miss == nil {
					if best == nil {
						best = &Inspection{}
					}
					if best.Supplier == nil || len(s.Columns.all()) > len(best.Supplier.Columns.all()) {
						best.Supplier = s
					}
				}
			}
		}
	}
	if best == nil {
		best = &Inspection{}
	}
	if best.Supplier != nil {
		// A known supplier's file is read its way, whatever Detect says.
		best.Detected = best.Supplier.Columns
	}
	return best, nil
}
