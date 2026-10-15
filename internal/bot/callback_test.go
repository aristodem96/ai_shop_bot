package bot

import (
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCallbackRoundTrip(t *testing.T) {
	for act, n := range arity {
		args := make([]int64, n)
		for i := range args {
			// The largest ids that can ever occur still have to fit.
			args[i] = math.MaxInt64
		}
		data := encode(act, args...)
		assert.LessOrEqual(t, len(data), maxCallbackData, "%s overflows Telegram's limit", act)

		cb, err := parseCallback(data)
		require.NoError(t, err, data)
		assert.Equal(t, act, cb.act)
		assert.Equal(t, args, cb.args)
	}
}

func TestParseCallbackRejectsGarbage(t *testing.T) {
	for _, data := range []string{
		"",
		"zzz",
		"p",      // missing argument
		"p:1:2",  // too many
		"p:abc",  // not a number
		"a:1",    // too few
		"cats:1", // takes none
		"p:" + strings.Repeat("9", 70),
	} {
		_, err := parseCallback(data)
		assert.ErrorIs(t, err, errBadCallback, "%q", data)
	}
}
