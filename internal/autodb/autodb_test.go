package autodb

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
)

// The answers below are shaped like the live API's: a doubled byte order
// mark before the JSON, ids as strings in some methods and numbers in
// others.
const bom2 = "\xEF\xBB\xBF\xEF\xBB\xBF"

func server(t *testing.T, answers map[string]string) (*Client, *[]string) {
	t.Helper()
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "user_x" || pass != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, bom2+`{"error":"User not found","code":401}`)
			return
		}
		asked = append(asked, r.URL.RawQuery)
		body, ok := answers[r.URL.Query().Get("action")]
		if !ok {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, bom2+`{"error":"Access denied from IP: 77.75.148.150","code":403}`)
			return
		}
		_, _ = io.WriteString(w, bom2+body)
	}))
	t.Cleanup(srv.Close)
	return New(Config{BaseURL: srv.URL + "/api/v1/", User: "user_x", Password: "secret"}), &asked
}

func TestVehicleSide(t *testing.T) {
	c, asked := server(t, map[string]string{
		"getMakes":  `[{"id":"138","name":"CHEVROLET","matchcode":"CHEVR"}]`,
		"getModels": `[{"id":"5403","name":"LACETTI (J200)","constructioninterval":"03.2003 - "}]`,
		"getModifications": `[{"id":"18537","name":"CHEVROLET LACETTI (J200) 1.6",
			"attrs":"ConstructionInterval:03.2005 - |Power:80 kW|Power:109 PS|Capacity:1.6 l|FuelType:Бензин"}]`,
		"getSections":     `[{"id":"100006","description":"тормозная система","havechild":"1"}]`,
		"getSectionParts": `[{"supplierid":"65","part_number":"S 59 513","supplier_name":"BREMBO","product_name":"Комплект тормозных колодок"}]`,
	})
	ctx := context.Background()

	makes, err := c.Makes(ctx, Passenger)
	require.NoError(t, err)
	assert.Equal(t, []Make{{ID: 138, Name: "CHEVROLET"}}, makes, "a byte order mark and a string id are read")

	models, err := c.Models(ctx, Passenger, 138)
	require.NoError(t, err)
	assert.Equal(t, []Model{{ID: 5403, Name: "LACETTI (J200)", Years: "03.2003 -"}}, models)

	mods, err := c.Modifications(ctx, Passenger, 5403)
	require.NoError(t, err)
	require.Len(t, mods, 1)
	assert.Equal(t, int64(18537), mods[0].ID)
	assert.Equal(t, []string{"80 kW", "109 PS"}, mods[0].Attrs["Power"], "a repeated attribute keeps every value")
	assert.Equal(t, []string{"03.2005 -"}, mods[0].Attrs["ConstructionInterval"])

	secs, err := c.Sections(ctx, Passenger, 18537, 0)
	require.NoError(t, err)
	assert.Equal(t, []Section{{ID: 100006, Name: "тормозная система", HasChildren: true}}, secs)

	parts, err := c.SectionParts(ctx, Passenger, 18537, 100006)
	require.NoError(t, err)
	assert.Equal(t, []Part{{SupplierID: 65, Number: "S 59 513", Brand: "BREMBO", Name: "Комплект тормозных колодок"}}, parts)

	assert.Contains(t, (*asked)[0], "group=passenger")
	assert.Contains(t, (*asked)[4], "mod_id=18537")
	assert.Contains(t, (*asked)[4], "sec_id=100006")
}

func TestPartSide(t *testing.T) {
	c, asked := server(t, map[string]string{
		"search": `[{"supplierId":"34","DataSupplierArticleNumber":"OC 90","NormalizedDescription":"Фильтр","ArticleStateDisplayValue":"Нормальный"}]`,
		"getArtInfo": `{"status":{"NormalizedDescription":"Фильтр"},"specs":[{"description":"для артикула №","displayvalue":"OC 90"},
			{"description":"Высота [мм]","displayvalue":"142"},{"description":"Высота [мм]","displayvalue":"142"}],
			"images":[{"url":"https://image.auto-db.pro/OC90.webp"},{"url":"https://image.auto-db.pro/images/B.webp"}],"oem":[{"description":"FORD","OENbr":"5009285"}],
			"cross":[{"description":"MANN-FILTER","PartsDataSupplierArticleNumber":"W 712/22"}],
			"vehicles":[{"type":"PassengerCar","car_id":"18537","make":"CHEVROLET","model":"LACETTI (J200)","constructioninterval":"03.2005 - ","description":"1.6"}]}`,
	})
	ctx := context.Background()

	arts, err := c.Search(ctx, "OC 90")
	require.NoError(t, err)
	assert.Equal(t, []Article{{SupplierID: 34, Number: "OC 90", Name: "Фильтр", State: "Нормальный"}}, arts)
	assert.Contains(t, (*asked)[0], "q=OC+90")

	info, err := c.Info(ctx, 34, "OC 90")
	require.NoError(t, err)
	assert.Equal(t, &ArticleInfo{Name: "Фильтр",
		Specs:    []Spec{{Name: "Высота [мм]", Value: "142"}},
		Images:   []Image{{URL: "https://image.auto-db.pro/images/OC90.webp"}, {URL: "https://image.auto-db.pro/images/B.webp"}},
		OEM:      []OEM{{Make: "FORD", Number: "5009285"}},
		Cross:    []Cross{{Brand: "MANN-FILTER", Number: "W 712/22"}},
		Vehicles: []ArticleVehicle{{ModID: 18537, Make: "CHEVROLET", Model: "LACETTI (J200)", Years: "03.2005 - ", Engine: "1.6"}},
	}, info, "photos where they are, each characteristic once")
	assert.Contains(t, (*asked)[1], "brand=34")
}

func TestErrors(t *testing.T) {
	c, _ := server(t, map[string]string{})
	_, err := c.Makes(context.Background(), Passenger)
	require.ErrorIs(t, err, ErrIPNotAllowed)
	assert.ErrorContains(t, err, "77.75.148.150", "the refused address is named, for the dashboard")

	bad := New(Config{BaseURL: c.base, User: "user_x", Password: "wrong"})
	_, err = bad.Makes(context.Background(), Passenger)
	require.ErrorIs(t, err, ErrUnauthorized)
}

func TestBackgroundKeepsToItsShare(t *testing.T) {
	c, _ := server(t, map[string]string{"search": `[]`})
	c.all, c.background = rate.NewLimiter(rate.Limit(1000), 1), rate.NewLimiter(rate.Limit(20), 1)
	ctx := context.Background()

	start := time.Now()
	for range 5 {
		_, err := c.Search(Background(ctx), "x")
		require.NoError(t, err)
	}
	bg := time.Since(start)

	start = time.Now()
	for range 5 {
		_, err := c.Search(ctx, "x")
		require.NoError(t, err)
	}
	fg := time.Since(start)
	assert.GreaterOrEqual(t, bg, 180*time.Millisecond, "five background calls at 20 a second")
	assert.Less(t, fg, bg/2, "a buyer's call does not wait for the background share")
}
