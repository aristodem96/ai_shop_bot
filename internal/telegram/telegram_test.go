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
