package photos

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aristodem96/ai_shop_bot/internal/telegram"
)

type fakeSender struct {
	byID    []string
	uploads [][]byte
}

func (f *fakeSender) SendPhoto(_ context.Context, _ int64, photo, _ string, _ telegram.ReplyMarkup) error {
	f.byID = append(f.byID, photo)
	return nil
}

func (f *fakeSender) UploadPhoto(_ context.Context, _ int64, _ string, data []byte, _ string, _ telegram.ReplyMarkup) (string, error) {
	f.uploads = append(f.uploads, data)
	return "file-1", nil
}

type memCache map[string]string

func (m memCache) FileID(_ context.Context, url string) (string, bool, error) {
	id, ok := m[url]
	return id, ok, nil
}

func (m memCache) SaveFileID(_ context.Context, url, id string) error {
	m[url] = id
	return nil
}

// transparentPNG is 32x16: red on the left, transparent on the right.
func transparentPNG(t *testing.T) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, 32, 16))
	for y := range 16 {
		for x := range 16 {
			img.Set(x, y, color.NRGBA{R: 255, A: 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

func TestSendUploadsOnceThenByID(t *testing.T) {
	served := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served++
		_, _ = w.Write(transparentPNG(t))
	}))
	t.Cleanup(srv.Close)
	tg, cache := &fakeSender{}, memCache{}
	p := New(tg, cache, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := context.Background()

	require.NoError(t, p.Send(ctx, 5, srv.URL+"/a.png", "Колодки", nil))
	require.Len(t, tg.uploads, 1)
	img, err := jpeg.Decode(bytes.NewReader(tg.uploads[0]))
	require.NoError(t, err, "uploaded as a JPEG")
	r, g, b, _ := img.At(28, 8).RGBA()
	assert.Greater(t, min(r, g, b), uint32(0xf000), "transparency becomes white, not black")

	require.NoError(t, p.Send(ctx, 6, srv.URL+"/a.png", "Колодки", nil))
	assert.Equal(t, []string{"file-1"}, tg.byID, "the second time by file id")
	assert.Equal(t, 1, served, "with no download")
}

func TestSendRefusesWhatIsNoPicture(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "<html>not found</html>")
	}))
	t.Cleanup(srv.Close)
	p := New(&fakeSender{}, memCache{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.ErrorIs(t, p.Send(context.Background(), 5, srv.URL, "", nil), ErrNotAnImage)
}

func TestJPEGGoesAsItIs(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 1, 1)), nil))
	out, err := toJPEG(buf.Bytes())
	require.NoError(t, err)
	assert.Equal(t, buf.Bytes(), out)
}
