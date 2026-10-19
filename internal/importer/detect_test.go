package importer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetect(t *testing.T) {
	for name, tc := range map[string]struct {
		header []string
		want   Columns
	}{
		"a 1C export": {
			header: []string{"Код", "Артикул", "Номенклатура", "Производитель", "Вид номенклатуры", "Остаток", "Цена"},
			want: Columns{ExternalID: "Код", PartNumber: "Артикул", Name: "Номенклатура", Brand: "Производитель",
				Category: "Вид номенклатуры", Stock: "Остаток", Price: "Цена"},
		},
		"China Avto": {
			header: []string{"Авто", "Артикул", "Виробник", "Назва", "Ціна, грн", "Посилання"},
			want: Columns{Vehicle: "Авто", PartNumber: "Артикул", Brand: "Виробник", Name: "Назва",
				Price: "Ціна, грн", ExternalID: "Посилання"},
		},
		"two prices": {
			header: []string{"Найменування товару", "Ціна закупівельна", "Ціна роздріб, грн", "Штрих-код", "Код товару"},
			want:   Columns{Name: "Найменування товару", Price: "Ціна роздріб, грн", ExternalID: "Код товару"},
		},
		"the demo file": {
			header: []string{"Код", "Артикул", "Бренд", "Найменування", "Група", "Ціна, грн", "Залишок", "Авто", "Роки", "Розташування"},
			want: Columns{ExternalID: "Код", PartNumber: "Артикул", Brand: "Бренд", Name: "Найменування", Category: "Група",
				Price: "Ціна, грн", Stock: "Залишок", Vehicle: "Авто", Years: "Роки", Position: "Розташування"},
		},
	} {
		assert.Equal(t, tc.want, Detect(tc.header), name)
	}
}

func TestInspectFindsTheHeaderAndTheSupplier(t *testing.T) {
	path := filepath.Join(t.TempDir(), "price.csv")
	require.NoError(t, os.WriteFile(path, []byte("Прайс-лист на 19.10\n\nКод;Номенклатура;Цена;Остаток\n1;Колодки;1049;3\n"), 0o600))

	in, err := Inspect(path, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"Код", "Номенклатура", "Цена", "Остаток"}, in.Header, "under the title")
	assert.Equal(t, Columns{ExternalID: "Код", Name: "Номенклатура", Price: "Цена", Stock: "Остаток"}, in.Detected)
	assert.Nil(t, in.Supplier, "nobody has these columns yet")

	known := []Supplier{
		{ID: 1, Name: "Інший", Columns: Columns{Name: "Назва", Price: "Ціна"}},
		{ID: 2, Name: "Наша 1С", Columns: Columns{ExternalID: "Код", Name: "Номенклатура", Price: "Цена"}},
	}
	in, err = Inspect(path, known)
	require.NoError(t, err)
	require.NotNil(t, in.Supplier)
	assert.Equal(t, int64(2), in.Supplier.ID, "every column of the supplier's is in the file")
}
