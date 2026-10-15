// Package telegram is a minimal Telegram Bot API client: long polling, sending
// and editing messages, inline keyboards and callback answers. A handful of
// methods do not justify a dependency, and a hand-written client is trivially
// faked in tests.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultBaseURL = "https://api.telegram.org"

// ErrNotModified is returned when an edit would leave a message as it was.
// Telegram treats that as an error; for a bot that re-renders a cart after
// every tap it is the normal outcome of a double tap, so callers ignore it.
var ErrNotModified = errors.New("telegram: message is not modified")

// Client talks to one bot.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// New returns a client for the bot with the given token.
func New(token string) *Client {
	return NewWithBaseURL(defaultBaseURL, token)
}

// NewWithBaseURL points the client somewhere else; tests use it.
func NewWithBaseURL(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		// Must outlast the long-poll timeout, or every idle poll would be
		// reported as a failure.
		http: &http.Client{Timeout: PollTimeout + 30*time.Second},
	}
}

// User is a Telegram account.
type User struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}

// Chat is where a message was sent.
type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

// ChatPrivate is a one-to-one conversation with the bot.
const ChatPrivate = "private"

// Message is a message. For a callback on a message too old to be read, only
// Chat and MessageID are filled, which is all an edit needs.
type Message struct {
	MessageID int64  `json:"message_id"`
	From      *User  `json:"from"`
	Chat      Chat   `json:"chat"`
	Text      string `json:"text"`
}

// CallbackQuery is a press of an inline button.
type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message"`
	Data    string   `json:"data"`
}

// Update is one event from getUpdates.
type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

// ChatID returns the chat an update belongs to, or 0 if it has none.
func (u Update) ChatID() int64 {
	switch {
	case u.Message != nil:
		return u.Message.Chat.ID
	case u.CallbackQuery != nil && u.CallbackQuery.Message != nil:
		return u.CallbackQuery.Message.Chat.ID
	case u.CallbackQuery != nil:
		return u.CallbackQuery.From.ID
	}
	return 0
}

// ReplyMarkup is a keyboard attached to a message. The unexported method
// limits it to the two kinds below, so a typo cannot send arbitrary JSON.
type ReplyMarkup interface{ replyMarkup() }

// InlineKeyboard is a grid of buttons under a message.
type InlineKeyboard struct {
	Rows [][]InlineButton `json:"inline_keyboard"`
}

func (*InlineKeyboard) replyMarkup() {}

// InlineButton sends CallbackData back to the bot when pressed, or opens URL.
type InlineButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
	URL          string `json:"url,omitempty"`
}

// ReplyKeyboard replaces the phone keyboard with buttons that send their text.
type ReplyKeyboard struct {
	Rows [][]KeyboardButton `json:"keyboard"`
	// Resize fits the keyboard to its buttons instead of half the screen.
	Resize bool `json:"resize_keyboard,omitempty"`
	// Persistent keeps it shown even after the user hides it.
	Persistent bool `json:"is_persistent,omitempty"`
}

func (*ReplyKeyboard) replyMarkup() {}

// KeyboardButton is a reply keyboard button.
type KeyboardButton struct {
	Text string `json:"text"`
}

// PollTimeout is how long a single getUpdates call waits for new events.
// Long polling is what lets the bot run without a public URL of its own.
const PollTimeout = 30 * time.Second

// GetUpdates long-polls for events after offset, waiting up to timeout.
//
// Passing an offset is also what acknowledges everything before it: Telegram
// forgets an update only once a later call asks for a higher offset.
func (c *Client) GetUpdates(ctx context.Context, offset int64, timeout time.Duration) ([]Update, error) {
	var updates []Update
	err := c.call(ctx, "getUpdates", map[string]any{
		"offset":          offset,
		"timeout":         int(timeout.Seconds()),
		"allowed_updates": []string{"message", "callback_query"},
	}, &updates)
	return updates, err
}

// SendMessage sends HTML-formatted text.
func (c *Client) SendMessage(ctx context.Context, chatID int64, html string, markup ReplyMarkup) error {
	params := map[string]any{
		"chat_id":              chatID,
		"text":                 html,
		"parse_mode":           "HTML",
		"link_preview_options": map[string]any{"is_disabled": true},
	}
	if markup != nil {
		params["reply_markup"] = markup
	}
	return c.call(ctx, "sendMessage", params, nil)
}

// SendPhoto sends a photo by URL with an HTML caption. Telegram downloads the
// photo itself, so the bot never holds the bytes.
func (c *Client) SendPhoto(ctx context.Context, chatID int64, photoURL, captionHTML string, markup ReplyMarkup) error {
	params := map[string]any{
		"chat_id":    chatID,
		"photo":      photoURL,
		"caption":    captionHTML,
		"parse_mode": "HTML",
	}
	if markup != nil {
		params["reply_markup"] = markup
	}
	return c.call(ctx, "sendPhoto", params, nil)
}

// EditMessageText replaces the text and keyboard of a text message.
func (c *Client) EditMessageText(ctx context.Context, chatID, messageID int64, html string, markup *InlineKeyboard) error {
	params := map[string]any{
		"chat_id":              chatID,
		"message_id":           messageID,
		"text":                 html,
		"parse_mode":           "HTML",
		"link_preview_options": map[string]any{"is_disabled": true},
	}
	if markup != nil {
		params["reply_markup"] = markup
	}
	return c.call(ctx, "editMessageText", params, nil)
}

// EditMessageReplyMarkup replaces only the keyboard, which works for photo
// messages as well as text ones.
func (c *Client) EditMessageReplyMarkup(ctx context.Context, chatID, messageID int64, markup *InlineKeyboard) error {
	return c.call(ctx, "editMessageReplyMarkup", map[string]any{
		"chat_id":      chatID,
		"message_id":   messageID,
		"reply_markup": markup,
	}, nil)
}

// AnswerCallbackQuery stops the button's loading spinner, optionally with a
// short toast, or a dialog when alert is set. Every callback must be answered,
// or the button keeps spinning on the user's screen.
func (c *Client) AnswerCallbackQuery(ctx context.Context, id, text string, alert bool) error {
	return c.call(ctx, "answerCallbackQuery", map[string]any{
		"callback_query_id": id,
		"text":              text,
		"show_alert":        alert,
	}, nil)
}

// ChatActionTyping shows "typing..." in the chat header.
const ChatActionTyping = "typing"

// SendChatAction shows a status such as "typing..." for about five seconds or
// until the bot's next message, whichever comes first.
func (c *Client) SendChatAction(ctx context.Context, chatID int64, action string) error {
	return c.call(ctx, "sendChatAction", map[string]any{"chat_id": chatID, "action": action}, nil)
}

// Command is one entry of the menu Telegram shows under the message box.
type Command struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// SetCommands registers the command menu.
func (c *Client) SetCommands(ctx context.Context, commands []Command) error {
	return c.call(ctx, "setMyCommands", map[string]any{"commands": commands}, nil)
}

type envelope struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
}

func (c *Client) call(ctx context.Context, method string, params map[string]any, out any) error {
	body, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("telegram: encode %s: %w", method, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/bot"+c.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("telegram: build %s request: %w", method, redact(err))
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("telegram: %s: %w", method, redact(err))
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("telegram: read %s response: %w", method, err)
	}

	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("telegram: %s: unexpected response (HTTP %d)", method, resp.StatusCode)
	}
	if !env.OK {
		if strings.Contains(env.Description, "message is not modified") {
			return ErrNotModified
		}
		return fmt.Errorf("telegram: %s failed (%d): %s", method, env.ErrorCode, env.Description)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(env.Result, out); err != nil {
		return fmt.Errorf("telegram: decode %s result: %w", method, err)
	}
	return nil
}

// redact strips the request URL from a transport error.
//
// The bot token is part of every Bot API URL, and net/http formats errors as
// `Post "https://api.telegram.org/bot<token>/getUpdates": dial tcp: ...`. Logged
// as is, the first network blip would write the token -- full control of the
// bot -- into the logs.
func redact(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return fmt.Errorf("%s request failed: %w", urlErr.Op, urlErr.Err)
	}
	return err
}
