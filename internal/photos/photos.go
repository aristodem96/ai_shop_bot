// Package photos sends product photos to Telegram from wherever they are.
//
// Telegram fetches a photo by URL itself, but not every URL suits it: the
// Auto-DB catalogue serves WebP. So a photo is downloaded once, turned into
// a JPEG, and uploaded; Telegram answers with a file_id, which is kept and
// sends the same photo from then on with no download at all.
package photos

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png" // decoders: price lists link PNGs
	"io"
	"log/slog"
	"net/http"
	"time"

	_ "golang.org/x/image/webp" // the catalogue's photos

	"github.com/aristodem96/ai_shop_bot/internal/telegram"
)

// Sender is the part of the Telegram client photos are sent with.
type Sender interface {
	SendPhoto(ctx context.Context, chatID int64, photo, captionHTML string, markup telegram.ReplyMarkup) error
	UploadPhoto(ctx context.Context, chatID int64, filename string, data []byte, captionHTML string, markup telegram.ReplyMarkup) (string, error)
}

// Cache keeps file ids by source URL.
type Cache interface {
	FileID(ctx context.Context, url string) (string, bool, error)
	SaveFileID(ctx context.Context, url, fileID string) error
}

// maxBytes bounds a photo download; Telegram takes up to 10 MB.
const maxBytes = 8 << 20

// Photos sends photos by URL.
type Photos struct {
	tg    Sender
	cache Cache
	http  *http.Client
	log   *slog.Logger
}

// New returns Photos.
func New(tg Sender, cache Cache, log *slog.Logger) *Photos {
	return &Photos{tg: tg, cache: cache, http: &http.Client{Timeout: 20 * time.Second}, log: log}
}

// Send sends the photo at url with a caption. A photo sent before goes by
// its file_id; a new one is fetched, converted and uploaded.
func (p *Photos) Send(ctx context.Context, chatID int64, url, captionHTML string, markup telegram.ReplyMarkup) error {
	id, ok, err := p.cache.FileID(ctx, url)
	if err != nil {
		p.log.WarnContext(ctx, "could not read the photo cache", slog.Any("error", err))
	}
	if ok {
		err := p.tg.SendPhoto(ctx, chatID, id, captionHTML, markup)
		if err == nil || ctx.Err() != nil {
			return err
		}
		// A file id from another bot token, say, is no good: upload anew.
		p.log.WarnContext(ctx, "a kept photo id failed, uploading again", slog.Any("error", err))
	}
	data, err := p.fetch(ctx, url)
	if err != nil {
		return err
	}
	if id, err = p.tg.UploadPhoto(ctx, chatID, "photo.jpg", data, captionHTML, markup); err != nil {
		return err
	}
	if err := p.cache.SaveFileID(ctx, url, id); err != nil {
		p.log.WarnContext(ctx, "could not keep a photo id", slog.Any("error", err))
	}
	return nil
}

// ErrNotAnImage means the URL gave something that is no picture.
var ErrNotAnImage = errors.New("photos: not an image")

// fetch downloads a photo as a JPEG.
func (p *Photos) fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("photos: %w", err)
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("photos: get %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("photos: get %s: HTTP %d", url, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("photos: read %s: %w", url, err)
	}
	if len(data) > maxBytes {
		return nil, fmt.Errorf("photos: %s is over %d bytes", url, maxBytes)
	}
	return toJPEG(data)
}

// toJPEG converts a picture to a JPEG; one already is goes as it is.
func toJPEG(data []byte) ([]byte, error) {
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotAnImage, err) //nolint:errorlint // the decoder's reason is detail
	}
	if format == "jpeg" {
		return data, nil
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, flatten(img), &jpeg.Options{Quality: 90}); err != nil {
		return nil, fmt.Errorf("photos: encode: %w", err)
	}
	return out.Bytes(), nil
}

// flatten puts a picture with transparency on white: a JPEG has no alpha,
// and transparent pixels would come out black.
func flatten(img image.Image) image.Image {
	b := img.Bounds()
	out := image.NewRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := img.At(x, y).RGBA()
			// Premultiplied colour over white: c + (1 - a).
			w := 0xffff - a
			out.Set(x, y, rgba64(r+w, g+w, bl+w))
		}
	}
	return out
}

func rgba64(r, g, b uint32) color.RGBA64 {
	return color.RGBA64{R: uint16(r), G: uint16(g), B: uint16(b), A: 0xffff} //nolint:gosec // each is at most 0xffff
}
