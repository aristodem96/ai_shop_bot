package importer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/text/encoding/charmap"

	"github.com/aristodem96/ai_shop_bot/internal/domain"
)

func TestParsePrice(t *testing.T) {
	for in, want := range map[string]domain.Money{
		"1049":       104900,
		"1049.5":     104950,
		"1 049,50":   104950,
		"1049 грн":   104900,
		"1,049.50":   104950,
		"1.049":      104900, // a thousands separator, not 1 hryvnia 4.9 kopiyky
		"0.99":       99,
		"12 345 678": 1234567800,
	} {
		got, err := parsePrice(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
	for _, bad := range []string{"", "ціна за запитом", "0", "-"} {
		_, err := parsePrice(bad)
		assert.Error(t, err, bad)
	}
}

func TestParseYears(t *testing.T) {
	for _, tc := range []struct {
		span, from, to string
		yf, yt         int
	}{
		{span: "2013-2020", yf: 2013, yt: 2020},
		{span: "2017-", yf: 2017},
		{span: "2013–н.в.", yf: 2013},
		{from: "2009", to: "2018 р.", yf: 2009, yt: 2018},
		{},
	} {
		yf, yt, err := parseYears(tc.span, tc.from, tc.to)
		require.NoError(t, err)
		assert.Equal(t, [2]int{tc.yf, tc.yt}, [2]int{yf, yt}, tc)
	}
	_, _, err := parseYears("2020-2013", "", "")
	assert.Error(t, err, "years that run backwards are a typo")
}

func days(n int) *int { return &n }

func priceList() *Profile {
	return &Profile{
		Source: "supplier", SKUPrefix: "SP", LeadTimeDays: days(3), DefaultCategory: "other",
		Columns: Columns{ExternalID: "Посилання", PartNumber: "Артикул", Brand: "Виробник",
			Name: "Назва", Price: "Ціна, грн", Vehicle: "Авто"},
		Rules: []Rule{{Contains: []string{"колодк"}, Category: "brakes"}},
	}
}

// The layout of a real Ukrainian price list: contacts above the header, one
// row per offer and vehicle, a brand in several spellings, holes.
var priceSheet = Sheet{Name: "Chery", Rows: [][]string{
	{"", "", "(095) 000-00-00"},
	{"", "", "Пн-Пт 9:00-18:00"},
	{},
	{"Авто", "Артикул", "Виробник", "Назва", "Ціна, грн", "Посилання"},
	{"Skoda Octavia A7", "5Q0-698-151", "KYB", "Колодки передні", "1049", "https://shop/1"},
	{"Volkswagen Golf VII", "5Q0-698-151", "KYB", "Колодки передні", "1049", "https://shop/1"},
	{"Skoda  Octavia A7 ", "5Q0698151", "kyb", "Колодки передні", "990,50", "https://shop/2"},
	{"Chery Tiggo", "T11-2915010", "", "Амортизатор задній", "1424", "https://shop/3"},
	{"Skoda Octavia A7", "X1", "Mogen", "", "100", "https://shop/4"},
	{"Skoda Octavia A7", "X2", "Mogen", "Ремінь", "за запитом", "https://shop/5"},
}}

func TestParse(t *testing.T) {
	parsed, err := Parse([]Sheet{{Name: "cover", Rows: [][]string{{"Прайс"}}}, priceSheet}, priceList())
	require.NoError(t, err)

	assert.Equal(t, 6, parsed.Rows, "blank and preamble rows are not data")
	require.Len(t, parsed.Offers, 3, "rows repeating an offer for another car merge into it")
	pads := parsed.Offers[0]
	assert.Equal(t, "https://shop/1", pads.Key)
	assert.Equal(t, domain.Money(104900), pads.Price)
	assert.Equal(t, []Fit{{Vehicle: "Skoda Octavia A7"}, {Vehicle: "Volkswagen Golf VII"}}, pads.Fits)
	assert.Equal(t, "Skoda Octavia A7", parsed.Offers[1].Fits[0].Vehicle, "cells are trimmed and spaces collapsed")

	require.Len(t, parsed.Problems, 2)
	assert.Equal(t, RowRef{Sheet: "Chery", Line: 9}, parsed.Problems[0].Row, "a problem points at the line Excel shows")
	assert.Equal(t, "немає назви", parsed.Problems[0].Reason)
	assert.Contains(t, parsed.Problems[1].Reason, "незрозуміла ціна")
}

func TestParseWithoutHeader(t *testing.T) {
	_, err := Parse([]Sheet{{Name: "x", Rows: [][]string{{"a", "b"}}}}, priceList())
	require.Error(t, err)
}

func TestParseKeyByBrandAndPartNumber(t *testing.T) {
	p := priceList()
	p.Columns.ExternalID = ""
	parsed, err := Parse([]Sheet{priceSheet}, p)
	require.NoError(t, err)
	// Without the supplier's id, "KYB 5Q0-698-151" and "kyb 5Q0698151" are
	// one part however the rows spell it.
	require.Len(t, parsed.Offers, 2)
	assert.Equal(t, "KYB|5Q0698151", parsed.Offers[0].Key)
	assert.Len(t, parsed.Offers[0].Fits, 3)
}

type fakeStore struct {
	aliases map[string]string
	models  []Model
	slugs   []string
}

func (f *fakeStore) BrandAliases(context.Context) (map[string]string, error) {
	out := make(map[string]string, len(f.aliases))
	for k, v := range f.aliases {
		out[k] = v
	}
	return out, nil
}
func (f *fakeStore) VehicleModels(context.Context) ([]Model, error)  { return f.models, nil }
func (f *fakeStore) CategorySlugs(context.Context) ([]string, error) { return f.slugs, nil }
func (f *fakeStore) Apply(context.Context, *Batch, bool) (Result, error) {
	return Result{}, nil
}

func catalogue() *fakeStore {
	return &fakeStore{
		aliases: map[string]string{"KYB": "KYB", "KAYABA": "KYB"},
		models: []Model{
			{ID: 1, Make: "Skoda", Model: "Octavia A7"},
			{ID: 2, Make: "Volkswagen", Model: "Golf VII"},
			{ID: 3, Make: "Chery", Model: "Tiggo 7"},
			{ID: 4, Make: "Chery", Model: "Tiggo 4"},
		},
		slugs: []string{"brakes", "suspension", "other"},
	}
}

func resolveSheet(t *testing.T, p *Profile, sh Sheet) *Batch {
	t.Helper()
	parsed, err := Parse([]Sheet{sh}, p)
	require.NoError(t, err)
	b, err := Resolve(context.Background(), catalogue(), p, parsed)
	require.NoError(t, err)
	return b
}

func TestResolve(t *testing.T) {
	b := resolveSheet(t, priceList(), priceSheet)
	require.Len(t, b.Products, 3)

	pads := b.Products[0]
	assert.Equal(t, "KYB", pads.Brand)
	assert.Equal(t, "KYB", b.Products[1].Brand, "\"kyb\" is a known brand spelt differently")
	assert.Empty(t, b.Products[2].Brand, "a missing brand stays missing, it is not invented")
	assert.Equal(t, []string{"5Q0698151"}, pads.OEM)
	assert.Equal(t, "brakes", pads.Category)
	assert.Equal(t, "other", b.Products[2].Category, "no rule matched")
	assert.Nil(t, pads.Stock)
	assert.Equal(t, 3, *pads.LeadDays, "a file without stock is a supplier's list: everything on order")
	assert.Equal(t, []ModelFit{{ModelID: 1}, {ModelID: 2}}, pads.Fits)
	assert.True(t, strings.HasPrefix(pads.SKU, "SP-"))
	assert.Equal(t, pads.SKU, sku(priceList(), "https://shop/1"), "the code is stable across runs")

	assert.Equal(t, []Count{{Name: "Chery Tiggo", N: 1}}, b.Report.UnmatchedVehicles,
		"a family name is not guessed into a model")
	assert.Equal(t, 1, b.Report.WithoutFitment)
	assert.Equal(t, 2, b.Report.Skipped)
}

func TestResolveVehicleMap(t *testing.T) {
	p := priceList()
	p.Vehicles = map[string][]string{"Chery Tiggo": {"Chery Tiggo 7", "Chery Tiggo 4"}}
	b := resolveSheet(t, p, priceSheet)

	shock := b.Products[2]
	assert.Equal(t, []ModelFit{{ModelID: 3}, {ModelID: 4}}, shock.Fits)
	assert.Empty(t, b.Report.UnmatchedVehicles)
}

func TestResolveNewBrandTakesCommonSpelling(t *testing.T) {
	sh := Sheet{Name: "s", Rows: [][]string{
		{"Назва", "Ціна, грн", "Артикул", "Виробник"},
		{"Фільтр", "100", "A1", "mogen"},
		{"Фільтр", "100", "A2", "Mogen"},
		{"Фільтр", "100", "A3", "Mogen"},
	}}
	p := priceList()
	p.Columns = Columns{Name: "Назва", Price: "Ціна, грн", PartNumber: "Артикул", Brand: "Виробник"}
	b := resolveSheet(t, p, sh)

	assert.Equal(t, []NewBrand{{Name: "Mogen", Alias: "MOGEN"}}, b.NewBrands)
	for _, prod := range b.Products {
		assert.Equal(t, "Mogen", prod.Brand)
	}
}

func TestResolveRejectsUnknownCategory(t *testing.T) {
	p := priceList()
	p.Rules = append(p.Rules, Rule{Contains: []string{"фільтр"}, Category: "filtres"})
	parsed, err := Parse([]Sheet{priceSheet}, p)
	require.NoError(t, err)
	_, err = Resolve(context.Background(), catalogue(), p, parsed)
	require.ErrorContains(t, err, `"filtres" does not exist`)
}

func TestProfileValidate(t *testing.T) {
	require.NoError(t, priceList().Validate())

	err := (&Profile{Source: "Bad Source"}).Validate()
	require.Error(t, err)
	for _, want := range []string{"source", "sku_prefix", "columns.name", "external_id or", "lead_time_days", "default_category"} {
		assert.ErrorContains(t, err, want, "every problem is reported at once")
	}
}

func TestReadCSV(t *testing.T) {
	rows, err := readCSV(strings.NewReader("\xEF\xBB\xBFНазва;Ціна\n\"Колодки; передні\";1049\nкороткий\n"), ';')
	require.NoError(t, err)
	assert.Equal(t, [][]string{{"Назва", "Ціна"}, {"Колодки; передні", "1049"}, {"короткий"}}, rows,
		"the byte order mark Excel writes is dropped, ragged rows are kept")
}

func TestHeaderErrorNamesTheRenamedColumn(t *testing.T) {
	sh := Sheet{Name: "Прайс", Rows: [][]string{
		{"Прайс-лист"},
		{"Авто", "Артикул", "Виробник", "Найменування", "Ціна, грн", "Посилання"},
	}}
	_, err := Parse([]Sheet{sh}, priceList())
	var he *HeaderError
	require.ErrorAs(t, err, &he)
	assert.Equal(t, []string{"Назва"}, he.Missing, "only the renamed column is missing")
	assert.Equal(t, 2, he.Line)
	assert.Contains(t, he.Found, "Найменування", "and the row shows what it is called now")
}

// writeTemp puts a file with the given bytes where readAndParse can read it.
func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, data, 0o600))
	return path
}

const csvPrice = "Авто;Артикул;Виробник;Назва;Ціна, грн;Посилання\n" +
	"Skoda Octavia A7;5Q0698151;KYB;Колодки передні;1 049,50;https://shop/1\n"

func TestReadsWindows1251(t *testing.T) {
	cp1251, err := charmap.Windows1251.NewEncoder().Bytes([]byte(csvPrice))
	require.NoError(t, err)
	parsed, err := readAndParse(writeTemp(t, "price.csv", cp1251), priceList())
	require.NoError(t, err, "a file saved by Excel on Windows is read without telling it the encoding")
	require.Len(t, parsed.Offers, 1)
	assert.Equal(t, "Колодки передні", parsed.Offers[0].Name)
}

func TestDetectsDelimiter(t *testing.T) {
	for name, delim := range map[string]string{"semicolon": ";", "comma": ",", "tab": "\t"} {
		t.Run(name, func(t *testing.T) {
			data := strings.ReplaceAll(csvPrice, ";", delim)
			// A comma inside a field must be quoted in a comma file.
			if delim == "," {
				data = strings.Replace(data, "Ціна, грн", `"Ціна, грн"`, 1)
				data = strings.Replace(data, "1 049,50", `"1 049,50"`, 1)
			}
			parsed, err := readAndParse(writeTemp(t, "price.csv", []byte(data)), priceList())
			require.NoError(t, err)
			require.Len(t, parsed.Offers, 1)
			assert.Equal(t, domain.Money(104950), parsed.Offers[0].Price)
		})
	}
}

func TestExceeded(t *testing.T) {
	assert.False(t, Exceeded(20, 100, 20), "exactly the limit is allowed")
	assert.True(t, Exceeded(21, 100, 20))
	assert.False(t, Exceeded(5, 0, 20), "nothing on sale, nothing to protect")
	assert.True(t, Exceeded(1, 1, 0), "a zero limit allows none")
}
