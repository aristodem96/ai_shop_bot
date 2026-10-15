package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMoneyString(t *testing.T) {
	const nb = " "
	cases := []struct {
		in   Money
		want string
	}{
		{0, "0 грн"},
		{UAH(399), "399 грн"},
		{UAH(2899), "2" + nb + "899 грн"},
		{UAH(1234567), "1" + nb + "234" + nb + "567 грн"},
		{Money(289950), "2" + nb + "899,50 грн"},
		{Money(105), "1,05 грн"},
		{-UAH(1500), "-1" + nb + "500 грн"},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, c.in.String(), "Money(%d)", int64(c.in))
	}
}

func TestCartTotals(t *testing.T) {
	c := Cart{Lines: []CartLine{
		{VariantID: 1, Price: UAH(1000), Qty: 2, Stock: 5},
		{VariantID: 2, Price: UAH(399), Qty: 1, Stock: 0},
	}}

	assert.Equal(t, UAH(2399), c.Total())
	assert.Equal(t, 3, c.Items())
	assert.False(t, c.Lines[0].Short())
	assert.True(t, c.Lines[1].Short(), "stock dropped below quantity")

	_, ok := c.Line(2)
	assert.True(t, ok)
	_, ok = c.Line(3)
	assert.False(t, ok)
}
