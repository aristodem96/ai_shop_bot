package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const token = "123:secret"

// stub serves one canned Bot API answer and records the request it got.
func stub(t *testing.T, answer string) (*Client, *map[string]any, *string) {
	t.Helper()
	var (
		got  map[string]any
		path string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		_, _ = io.WriteString(w, answer)
	}))
	t.Cleanup(srv.Close)
	return NewWithBaseURL(srv.URL, token), &got, &path
}

func TestGetUpdatesDecodesCallbacks(t *testing.T) {
	c, got, path := stub(t, `{"ok":true,"result":[
		{"update_id":7,"callback_query":{"id":"cb1","from":{"id":5},
			"message":{"message_id":9,"chat":{"id":5,"type":"private"}},"data":"p:12"}}]}`)

	updates, err := c.GetUpdates(context.Background(), 7, 10*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "/bot"+token+"/getUpdates", *path)
	assert.EqualValues(t, 7, (*got)["offset"])
	assert.EqualValues(t, 10, (*got)["timeout"])

	require.Len(t, updates, 1)
	cb := updates[0].CallbackQuery
	require.NotNil(t, cb)
	assert.Equal(t, "p:12", cb.Data)
	assert.Equal(t, int64(9), cb.Message.MessageID)
	assert.Equal(t, int64(5), updates[0].ChatID())
}

func TestSendMessageEncodesInlineKeyboard(t *testing.T) {
	c, got, _ := stub(t, `{"ok":true,"result":{}}`)

	err := c.SendMessage(context.Background(), 5, "<b>hi</b>", &InlineKeyboard{
		Rows: [][]InlineButton{{{Text: "Go", CallbackData: "go"}}},
	})
	require.NoError(t, err)
	assert.Equal(t, "HTML", (*got)["parse_mode"])
	markup := (*got)["reply_markup"].(map[string]any)
	row := markup["inline_keyboard"].([]any)[0].([]any)
	assert.Equal(t, map[string]any{"text": "Go", "callback_data": "go"}, row[0])
}

func TestEmptyInlineKeyboardIsAnArray(t *testing.T) {
	raw, err := json.Marshal(&InlineKeyboard{})
	require.NoError(t, err)
	assert.JSONEq(t, `{"inline_keyboard":[]}`, string(raw), "Telegram rejects null here")
}

func TestNotModifiedIsASentinel(t *testing.T) {
	c, _, _ := stub(t, `{"ok":false,"error_code":400,
		"description":"Bad Request: message is not modified: specified new message content and reply markup are exactly the same"}`)

	err := c.EditMessageReplyMarkup(context.Background(), 5, 9, &InlineKeyboard{})
	require.ErrorIs(t, err, ErrNotModified)
}

func TestTransportErrorsDoNotLeakTheToken(t *testing.T) {
	// Nothing listens on this port, so the request fails in the transport.
	c := NewWithBaseURL("http://127.0.0.1:1", token)

	err := c.SendMessage(context.Background(), 5, "hi", nil)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "secret")
}

func TestDownloadFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bot" + token + "/getFile":
			_, _ = io.WriteString(w, `{"ok":true,"result":{"file_id":"f1","file_size":4,"file_path":"photos/a.jpg"}}`)
		case "/file/bot" + token + "/photos/a.jpg":
			_, _ = io.WriteString(w, "JPEG")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c := NewWithBaseURL(srv.URL, token)
	ctx := context.Background()

	f, err := c.GetFile(ctx, "f1")
	require.NoError(t, err)
	assert.Equal(t, "photos/a.jpg", f.FilePath)

	data, err := c.DownloadFile(ctx, f.FilePath, 10)
	require.NoError(t, err)
	assert.Equal(t, "JPEG", string(data))

	_, err = c.DownloadFile(ctx, f.FilePath, 3)
	require.ErrorIs(t, err, ErrFileTooLarge, "the limit holds while reading")

	_, err = c.DownloadFile(ctx, "photos/missing.jpg", 10)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "secret", "the token is part of the file URL")
}

func TestMessageWithPhotoDecodes(t *testing.T) {
	var u Update
	require.NoError(t, json.Unmarshal([]byte(`{"update_id":1,"message":{"message_id":2,
		"chat":{"id":5,"type":"private"},"caption":"що це?",
		"photo":[{"file_id":"small","width":90,"height":67},{"file_id":"big","width":1280,"height":960}]}}`), &u))
	require.Len(t, u.Message.Photo, 2)
	assert.Equal(t, "big", u.Message.Photo[1].FileID)
	assert.Equal(t, "що це?", u.Message.Caption)
}

func TestSetChatCommandsScopesToTheChat(t *testing.T) {
	c, got, path := stub(t, `{"ok":true,"result":true}`)

	err := c.SetChatCommands(context.Background(), -100500, []Command{{Command: "word", Description: "Слово"}})
	require.NoError(t, err)
	assert.Equal(t, "/bot"+token+"/setMyCommands", *path)
	assert.Equal(t, map[string]any{"type": "chat", "chat_id": float64(-100500)}, (*got)["scope"],
		"only that chat's menu changes; buyers keep theirs")
}

func TestForceReplyEncodes(t *testing.T) {
	c, got, _ := stub(t, `{"ok":true,"result":{}}`)
	require.NoError(t, c.SendMessage(context.Background(), 5, "Назва?", &ForceReply{ForceReply: true, Placeholder: "Назва"}))
	assert.Equal(t, map[string]any{"force_reply": true, "input_field_placeholder": "Назва"}, (*got)["reply_markup"])
}

// A nil keyboard of a concrete type is "no keyboard", not "reply_markup":
// null, which Telegram rejects together with the message.
func TestNilKeyboardIsOmitted(t *testing.T) {
	c, got, _ := stub(t, `{"ok":true,"result":{}}`)
	var none *InlineKeyboard
	require.NoError(t, c.SendMessage(context.Background(), 5, "hi", none))
	_, sent := (*got)["reply_markup"]
	assert.False(t, sent)

	require.NoError(t, c.SendPhoto(context.Background(), 5, "https://x/p.png", "hi", (*ReplyKeyboard)(nil)))
	_, sent = (*got)["reply_markup"]
	assert.False(t, sent)
}

// A flood wait of a few seconds is sat out, not passed on as a lost message.
func TestFloodWaitIsRetried(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			_, _ = io.WriteString(w, `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 1","parameters":{"retry_after":1}}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true,"result":{}}`)
	}))
	t.Cleanup(srv.Close)

	start := time.Now()
	require.NoError(t, NewWithBaseURL(srv.URL, token).SendMessage(context.Background(), 5, "hi", nil))
	assert.Equal(t, 2, calls)
	assert.GreaterOrEqual(t, time.Since(start), time.Second, "after the wait Telegram asked for")
}

func TestLongFloodWaitIsReported(t *testing.T) {
	c, _, _ := stub(t, `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 600","parameters":{"retry_after":600}}`)
	err := c.SendMessage(context.Background(), 5, "hi", nil)
	require.ErrorContains(t, err, "429", "ten minutes is a limit to report, not to sit out")
}

func TestUploadPhotoSendsAFileAndReturnsItsID(t *testing.T) {
	var (
		fields = map[string]string{}
		file   []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseMultipartForm(1<<20))
		for k, v := range r.MultipartForm.Value {
			fields[k] = v[0]
		}
		f, _, err := r.FormFile("photo")
		require.NoError(t, err)
		file, _ = io.ReadAll(f)
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":1,"chat":{"id":5},"photo":[{"file_id":"small"},{"file_id":"big"}]}}`)
	}))
	t.Cleanup(srv.Close)

	id, err := NewWithBaseURL(srv.URL, token).UploadPhoto(context.Background(), 5, "p.jpg", []byte("JPEG"), "<b>Колодки</b>",
		&InlineKeyboard{Rows: [][]InlineButton{{{Text: "+", CallbackData: "a:1"}}}})
	require.NoError(t, err)
	assert.Equal(t, "big", id)
	assert.Equal(t, []byte("JPEG"), file)
	assert.Equal(t, "5", fields["chat_id"])
	assert.Equal(t, "HTML", fields["parse_mode"])
	assert.JSONEq(t, `{"inline_keyboard":[[{"text":"+","callback_data":"a:1"}]]}`, fields["reply_markup"])
}
