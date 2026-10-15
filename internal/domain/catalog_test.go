package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeOEM(t *testing.T) {
	for in, want := range map[string]string{
		"1K0 698 151 A": "1K0698151A",
		"1k0-698-151-a": "1K0698151A",
		"04E.115.561.H": "04E115561H",
		" 90915-10003 ": "9091510003",
		"":              "",
	} {
		assert.Equal(t, want, NormalizeOEM(in), "%q", in)
	}
}

func TestVehicleLabel(t *testing.T) {
	v := Vehicle{Make: "Skoda", Model: "Octavia A7", Engine: "1.4 TSI", YearFrom: 2013, YearTo: 2020}
	assert.Equal(t, "Skoda Octavia A7 1.4 TSI (2013–2020)", v.Label())

	v.YearTo = 0
	assert.Equal(t, "2013–н.ч.", v.Years(), "a model still in production")
}

func TestFitsVehicle(t *testing.T) {
	p := Product{Fits: []Vehicle{{ID: 3}}}
	assert.True(t, p.FitsVehicle(3))
	assert.False(t, p.FitsVehicle(4))

	p.Universal = true
	assert.True(t, p.FitsVehicle(4), "a universal part fits every car")
}
