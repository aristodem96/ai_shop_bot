package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseVIN(t *testing.T) {
	v, err := ParseVIN(" tmb jg7ne 0 d0123456 ", 2026)
	require.NoError(t, err)
	assert.Equal(t, "TMBJG7NE0D0123456", v.Code, "spaces removed, upper-cased")
	assert.Equal(t, "Skoda", v.Make)
	assert.Equal(t, []int{2013, 1983}, v.ModelYears, "D repeats every 30 years, latest first")

	v, err = ParseVIN("Y6DTF69YD7W123456", 2026)
	require.NoError(t, err)
	assert.Equal(t, "Daewoo", v.Make, "a Lanos built by ZAZ")
	assert.Equal(t, []int{2007}, v.ModelYears, "2037 has not happened yet")

	v, err = ParseVIN("ZFA223000A5567770", 2026)
	require.NoError(t, err)
	assert.Empty(t, v.Make, "a maker the shop does not list")
	assert.Equal(t, []int{2010, 1980}, v.ModelYears)

	v, err = ParseVIN("ZFA22300005567770", 2026)
	require.NoError(t, err)
	assert.Empty(t, v.ModelYears, "0 encodes no year: European makers may leave it out")
}

func TestParseVINRejects(t *testing.T) {
	for _, s := range []string{
		"",
		"TMBJG7NE0D012345",   // 16 characters
		"TMBJG7NE0D01234567", // 18
		"TMBJG7NE0DO123456",  // O is never used
		"TMBJG7NE0D012345!",
	} {
		_, err := ParseVIN(s, 2026)
		assert.ErrorIs(t, err, ErrInvalidVIN, "%q", s)
	}
}
