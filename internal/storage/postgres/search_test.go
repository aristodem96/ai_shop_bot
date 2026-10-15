package postgres

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTSQuery(t *testing.T) {
	cases := map[string]string{
		"":                        "",
		"Пуховик":                 "пуховик:*",
		"тепл пухов":              "тепл:* | пухов:*",
		"a & b | !(c)":            "",
		"Nord's 90/10 ':* &&":     "nord:* | 90:* | 10:*",
		"вʼязана шапка":           "язана:* | шапка:*",
		"1 2 3 4 5 6 7 8 9 10 11": "10:* | 11:*",
	}
	for in, want := range cases {
		assert.Equal(t, want, tsQuery(in), "%q", in)
	}
}
