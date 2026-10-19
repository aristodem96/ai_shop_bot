package bot

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/stretchr/testify/assert"

	"github.com/aristodem96/ai_shop_bot/internal/agent"
	"github.com/aristodem96/ai_shop_bot/internal/llm"
	"github.com/aristodem96/ai_shop_bot/internal/telegram"
)

type fakeConsultant struct {
	reply  *agent.Reply
	err    error
	asked  []string
	images []llm.Image
	resets int
}

func (f *fakeConsultant) Reply(_ context.Context, _, _ int64, text string, images ...llm.Image) (*agent.Reply, error) {
	f.asked = append(f.asked, text)
	f.images = append(f.images, images...)
	if f.reply == nil {
		return &agent.Reply{Text: "ok"}, f.err
	}
	return f.reply, f.err
}

// fakeFiles serves one downloadable photo per file id.
type fakeFiles struct {
	data       map[string][]byte
	downloaded []string
}

func (f *fakeFiles) GetFile(_ context.Context, id string) (*telegram.File, error) {
	if _, ok := f.data[id]; !ok {
		return nil, errors.New("telegram: getFile failed (400): file not found")
	}
	return &telegram.File{FileID: id, FilePath: "photos/" + id}, nil
}

func (f *fakeFiles) DownloadFile(_ context.Context, path string, _ int64) ([]byte, error) {
	id := strings.TrimPrefix(path, "photos/")
	f.downloaded = append(f.downloaded, id)
	return f.data[id], nil
}

// jpeg is enough of a JPEG for content sniffing.
var jpeg = []byte{0xff, 0xd8, 0xff, 0xe0, 0, 0x10, 'J', 'F', 'I', 'F', 0}

func photo(caption string, sizes ...telegram.PhotoSize) telegram.Update {
	u := text("")
	u.Message.Caption = caption
	u.Message.Photo = sizes
	return u
}

func (f *fakeConsultant) Reset(context.Context, int64) error {
	f.resets++
	return nil
}

type fakeLimiter struct {
	allow bool
	err   error
}

func (f fakeLimiter) Allow(context.Context, int64) (bool, time.Duration, error) {
	return f.allow, 90 * time.Second, f.err
}

func withConsultant(c *fakeConsultant, l Limiter) (testBot, *fakeTelegram) {
	b := newTestBot()
	b.UseConsultant(c, l, &fakeFiles{})
	return b, b.tg
}

func TestConsultantAnswerWithCards(t *testing.T) {
	c := &fakeConsultant{reply: &agent.Reply{
		Text:       "Ось <колодки> & ще дещо",
		ProductIDs: []int64{padsID, 404}, // 404 left the catalogue meanwhile
	}}
	b, tg := withConsultant(c, fakeLimiter{allow: true})

	b.Handle(context.Background(), text("колодки на октавію"))

	assert.Equal(t, []string{"колодки на октавію"}, c.asked)
	msg := tg.last("sendMessage")
	assert.True(t, strings.HasPrefix(msg.text, "Ось &lt;колодки&gt; &amp; ще дещо\n\n"), "model output is escaped, never trusted as HTML")
	assert.Contains(t, msg.text, "<b>1.</b> Колодки гальмівні передні · AutoLine — <b>1\u202f450 грн</b> ⚠️ останні 2 шт.",
		"the products listed in full under the answer")
	assert.Equal(t, []string{encode(actProduct, padsID)}, callbacks(msg.markup),
		"a card per product that still exists")
	assert.Contains(t, tg.methods(), "sendChatAction", "the buyer sees typing while the model works")
}

func TestConsultantCatalogueButtonAfterSavingTheCar(t *testing.T) {
	c := &fakeConsultant{reply: &agent.Reply{Text: "Запам'ятав вашу Octavia.", CarSaved: true}}
	b, tg := withConsultant(c, fakeLimiter{allow: true})

	b.Handle(context.Background(), text("у мене октавія 2015 1.4"))
	assert.Contains(t, callbacks(tg.last("sendMessage").markup), encode(actCategories))
}

func TestConsultantCartButtonAfterAdding(t *testing.T) {
	c := &fakeConsultant{reply: &agent.Reply{Text: "Додав.", CartChanged: true}}
	b, tg := withConsultant(c, fakeLimiter{allow: true})

	b.Handle(context.Background(), text("додай у кошик"))
	assert.Contains(t, callbacks(tg.last("sendMessage").markup), encode(actCart))
}

func TestConsultantRateLimited(t *testing.T) {
	c := &fakeConsultant{}
	b, tg := withConsultant(c, fakeLimiter{allow: false})

	b.Handle(context.Background(), text("ще одне питання"))

	assert.Empty(t, c.asked, "the model is not called over the limit")
	assert.Contains(t, tg.last("sendMessage").text, "за 2 хв", "the wait is rounded up")
}

func TestConsultantFailsClosedWithoutLimiter(t *testing.T) {
	c := &fakeConsultant{}
	b, tg := withConsultant(c, fakeLimiter{err: errors.New("redis: connection refused")})

	b.Handle(context.Background(), text("колодки"))
	assert.Empty(t, c.asked)
	assert.Equal(t, msgConsultantDown, tg.last("sendMessage").text)
}

func TestConsultantProviderOutage(t *testing.T) {
	c := &fakeConsultant{err: llm.ErrUnavailable}
	b, tg := withConsultant(c, fakeLimiter{allow: true})

	b.Handle(context.Background(), text("колодки"))
	assert.Equal(t, msgConsultantDown, tg.last("sendMessage").text)
}

func TestConsultantRejectsLongMessages(t *testing.T) {
	c := &fakeConsultant{}
	b, tg := withConsultant(c, fakeLimiter{allow: true})

	b.Handle(context.Background(), text(strings.Repeat("я", maxQuestionRunes+1)))
	assert.Empty(t, c.asked)
	assert.Equal(t, msgTooLong, tg.last("sendMessage").text)
}

func TestPhotoSearch(t *testing.T) {
	c := &fakeConsultant{}
	files := &fakeFiles{data: map[string][]byte{"small": jpeg, "big": jpeg}}
	b := newTestBot()
	b.UseConsultant(c, fakeLimiter{allow: true}, files)

	b.Handle(context.Background(), photo("",
		telegram.PhotoSize{FileID: "small", Width: 90, Height: 67},
		telegram.PhotoSize{FileID: "big", Width: 1280, Height: 960},
		telegram.PhotoSize{FileID: "huge", Width: 4000, Height: 3000, FileSize: maxPhotoBytes + 1},
	))

	assert.Equal(t, []string{"big"}, files.downloaded, "the largest size within the limit")
	assert.Equal(t, []string{photoQuestion}, c.asked, "a photo without a caption gets a default question")
	require.Len(t, c.images, 1)
	assert.Equal(t, "image/jpeg", c.images[0].MediaType, "the type is read from the bytes")
}

func TestPhotoCaptionIsTheQuestion(t *testing.T) {
	c := &fakeConsultant{}
	b := newTestBot()
	b.UseConsultant(c, fakeLimiter{allow: true}, &fakeFiles{data: map[string][]byte{"p": jpeg}})

	b.Handle(context.Background(), photo("це з передньої осі", telegram.PhotoSize{FileID: "p", Width: 800, Height: 600}))
	assert.Equal(t, []string{"це з передньої осі"}, c.asked)
}

func TestPhotoThatIsNotAnImage(t *testing.T) {
	c := &fakeConsultant{}
	b := newTestBot()
	b.UseConsultant(c, fakeLimiter{allow: true}, &fakeFiles{data: map[string][]byte{"p": []byte("%PDF-1.7")}})

	b.Handle(context.Background(), photo("", telegram.PhotoSize{FileID: "p", Width: 800, Height: 600}))
	assert.Empty(t, c.asked, "the model never sees it")
	assert.Equal(t, msgBadPhoto, b.tg.last("sendMessage").text)
}

func TestPhotoOverTheLimitIsNotDownloaded(t *testing.T) {
	c := &fakeConsultant{}
	files := &fakeFiles{data: map[string][]byte{"p": jpeg}}
	b := newTestBot()
	b.UseConsultant(c, fakeLimiter{allow: false}, files)

	b.Handle(context.Background(), photo("", telegram.PhotoSize{FileID: "p", Width: 800, Height: 600}))
	assert.Empty(t, files.downloaded, "the rate limit is checked before the download")
	assert.Empty(t, c.asked)
}

func TestPhotoWithoutConsultant(t *testing.T) {
	b := newTestBot()
	b.Handle(context.Background(), photo("", telegram.PhotoSize{FileID: "p", Width: 800, Height: 600}))
	assert.Equal(t, msgPhotoNeedsConsultant, b.tg.last("sendMessage").text)
}

func TestNonTextMessage(t *testing.T) {
	c := &fakeConsultant{}
	b, tg := withConsultant(c, fakeLimiter{allow: true})

	b.Handle(context.Background(), text(""))
	assert.Empty(t, c.asked)
	assert.Equal(t, msgTextOnly, tg.last("sendMessage").text)
}

func TestNewConversation(t *testing.T) {
	c := &fakeConsultant{}
	b, tg := withConsultant(c, fakeLimiter{allow: true})

	b.Handle(context.Background(), text("/new"))
	assert.Equal(t, 1, c.resets)
	assert.Equal(t, msgConversationReset, tg.last("sendMessage").text)
}

func TestWithoutConsultantFreeTextShowsCatalogue(t *testing.T) {
	b := newTestBot()
	b.Handle(context.Background(), text("колодки на октавію"))

	assert.Equal(t, msgFreeTextSoon, b.tg.calls[0].text)
	assert.False(t, slices.Contains(b.tg.methods(), "sendChatAction"))
}

func TestConsultantSmallTalkHasNoKeyboard(t *testing.T) {
	c := &fakeConsultant{reply: &agent.Reply{Text: "Дякую, все добре! Що шукаєте?"}}
	b, tg := withConsultant(c, fakeLimiter{allow: true})

	b.Handle(context.Background(), text("як справи"))
	msg := tg.last("sendMessage")
	assert.Equal(t, "Дякую, все добре! Що шукаєте?", msg.text)
	assert.Nil(t, msg.markup, "no empty keyboard for Telegram to reject")
}

func TestConsultantCardsWithoutText(t *testing.T) {
	c := &fakeConsultant{reply: &agent.Reply{Text: "  ", ProductIDs: []int64{padsID}}}
	b, tg := withConsultant(c, fakeLimiter{allow: true})

	b.Handle(context.Background(), text("колодки"))
	assert.True(t, strings.HasPrefix(tg.last("sendMessage").text, msgHereYouGo+"\n\n"), "Telegram rejects an empty message")
}

func TestCatalogueAsksForACarFirst(t *testing.T) {
	b, tg := withConsultant(&fakeConsultant{}, fakeLimiter{allow: true})
	ctx := context.Background()

	b.Handle(ctx, text(btnCatalog))
	assert.Equal(t, msgAskCar, tg.last("sendMessage").text, "every part of every car is too much to browse")
	b.Handle(ctx, press(encode(actCategory, 1, 0)))
	assert.Equal(t, msgAskCar, tg.last("editMessageText").text, "a stale button too")

	b.garage.car = &octavia
	b.Handle(ctx, text(btnCatalog))
	menu := tg.last("sendMessage")
	assert.Contains(t, menu.text, "Для: Skoda Octavia")
	assert.NotContains(t, callbacks(menu.markup), encode(actForgetCar), "no way back to the whole catalogue")

	b.Handle(ctx, text(btnCar))
	assert.Contains(t, callbacks(tg.last("sendMessage").markup), encode(actForgetCar), "the car can still be forgotten")
	b.Handle(ctx, press(encode(actForgetCar)))
	assert.Equal(t, msgCarForgotten, tg.last("editMessageText").text)
}

func TestWithoutConsultantTheWholeCatalogueOpens(t *testing.T) {
	b := newTestBot()
	b.Handle(context.Background(), text(btnCatalog))
	assert.Contains(t, b.tg.last("sendMessage").text, "Оберіть розділ", "no consultant, no way to name a car")
}
