package bot

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDialogsSurviveARestart(t *testing.T) {
	ctx := context.Background()
	stores := map[string]*fakeSessions{}
	keep := func(prefix string, _ time.Duration) Sessions {
		if stores[prefix] == nil {
			stores[prefix] = &fakeSessions{data: map[int64][]byte{}}
		}
		return stores[prefix]
	}
	dir := filepath.Join(t.TempDir(), "imports")
	start := func() (testBot, *fakeImports, *fakeMatching) {
		b := newTestBot()
		b.KeepDialogs(keep)
		imports := &fakeImports{profiles: []string{"chinaavto"}}
		files := &fakeFiles{data: map[string][]byte{"doc": []byte("Назва;Ціна\nКолодки;1049\n")}}
		require.NoError(t, b.UseImports(imports, files, dir, managers))
		m := &fakeMatching{canSuggest: true}
		b.UseVehicleMatching(m)
		return b, imports, m
	}

	b, _, _ := start()
	b.Handle(ctx, upload(managers, "price.xlsx", ""))
	b.Handle(ctx, pressIn(managers, encode(actMatchStart, 1)))

	b, imports, m := start() // the bot restarts mid-session
	b.Handle(ctx, pressIn(managers, encode(actMatchPick, 1, 0, 0)))
	assert.Equal(t, []string{"chinaavto:Lada Priora→11→12"}, m.aliases, "the session goes on where it was")
	require.NotEmpty(t, imports.runs, "and the upload, still on disk, is checked again")
	assert.Contains(t, callbacks(b.tg.last("sendMessage").markup), encode(actImportApply, 1, 0))
}
