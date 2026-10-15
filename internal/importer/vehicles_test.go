package importer

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVehicleCode(t *testing.T) {
	v := Vehicle{Make: "Geely", Model: "Emgrand EC7", Engine: "1.8", YearFrom: 2009}
	assert.Equal(t, "geely-emgrand-ec7-1-8-2009", v.Code())
	v = Vehicle{Make: "Mercedes-Benz", Model: "C W204", Engine: "C220 CDI", YearFrom: 2007}
	assert.Equal(t, "mercedes-benz-c-w204-c220-cdi-2007", v.Code())
}

func TestParseVehicles(t *testing.T) {
	rows := [][]string{
		{"# a comment, with commas"},
		{"make", "model", "year_from", "year_to", "engine", "fuel", "oil_viscosity", "oil_volume_l"},
		{"Geely", "Emgrand EC7", "2009", "2018", "1.8", "petrol", "", ""},
		{"# comments between rows keep line numbers right"},
		{"Nissan", "Leaf ZE1", "2017", "", "EV", "Electric", "", ""},
		{"Skoda", "Octavia A7", "2013", "2020", "1.4 TSI", "petrol", "5W-30", "3,6"},
		{"Skoda", "Octavia A7", "2013", "2020", "1.4 TSI", "petrol", "", ""},
		{"Lada", "2107", "1982", "2012", "1.6", "gas", "", ""},
		{"Lada", "Priora", "2018", "2007", "1.6", "petrol", "", ""},
		{"", "Priora", "2007", "", "1.6", "petrol", "", ""},
	}
	vs, problems, err := ParseVehicles(rows)
	require.NoError(t, err)

	require.Len(t, vs, 3)
	assert.Equal(t, Vehicle{Make: "Nissan", Model: "Leaf ZE1", Engine: "EV", Fuel: "electric", YearFrom: 2017}, vs[1],
		"an empty year_to is a car still produced")
	assert.InDelta(t, 3.6, vs[2].OilVolume, 0.001, "a decimal comma is read")

	require.Len(t, problems, 4)
	assert.Equal(t, 7, problems[0].Row.Line, "the line a text editor shows")
	assert.Equal(t, "repeats line 6", problems[0].Reason)
	assert.Contains(t, problems[1].Reason, `fuel "gas"`)
	assert.Contains(t, problems[2].Reason, "run backwards")
	assert.Contains(t, problems[3].Reason, "required")
}

func TestParseVehiclesNeedsColumns(t *testing.T) {
	_, _, err := ParseVehicles([][]string{{"make", "model", "engine"}})
	require.ErrorContains(t, err, `no column "year_from"`)
}
